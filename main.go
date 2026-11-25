package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	ntSparkline "github.com/NimbleMarkets/ntcharts/sparkline"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

const (
	idleCIInactivityThreshold = 10 * time.Minute
	recentPushWindow          = 3 * 24 * time.Hour
	idlePanelMaxHeight        = 8
	apiRoot                   = "https://api.github.com"
	graphQLURL                = "https://api.github.com/graphql"
	allRefreshInterval        = 60 * time.Second
	refsRefreshInterval       = 180 * time.Second
	activityRefreshInterval   = 5 * time.Minute
	selectedInterval          = 20 * time.Second
	logRefreshInterval        = 15 * time.Second
	maxLogBytes               = 2 << 20
	activityHistoryDays       = 30
	attentionFailureWindow    = 24 * time.Hour
	queuedFollowUpAfter       = 15 * time.Minute
	runningFollowUpAfter      = 45 * time.Minute
	reviewFollowUpAfter       = 72 * time.Hour
)

var version = "dev"

type branch struct {
	Name             string       `json:"name"`
	SHA              string       `json:"sha"`
	Date             string       `json:"date"`
	Msg              string       `json:"message"`
	History          []commitInfo `json:"history"`
	HistoryTruncated bool         `json:"history_truncated,omitempty"`
}

type commitInfo struct {
	SHA         string `json:"sha"`
	Date        string `json:"date"`
	Msg         string `json:"message"`
	Author      string `json:"author"`
	AuthorLogin string `json:"author_login,omitempty"`
}

type pullRequestSummary struct {
	Number                      int      `json:"number"`
	Title                       string   `json:"title"`
	URL                         string   `json:"url"`
	IsDraft                     bool     `json:"is_draft"`
	CreatedAt                   string   `json:"created_at"`
	UpdatedAt                   string   `json:"updated_at"`
	ReviewDecision              string   `json:"review_decision"`
	MergeStateStatus            string   `json:"merge_state_status"`
	HeadRefName                 string   `json:"head_ref_name"`
	AuthorLogin                 string   `json:"author_login,omitempty"`
	RequestedReviewers          []string `json:"requested_reviewers,omitempty"`
	RequestedReviewersTruncated bool     `json:"requested_reviewers_truncated,omitempty"`
}

type run struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Title      string `json:"display_title"`
	Branch     string `json:"head_branch"`
	SHA        string `json:"head_sha"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	Event      string `json:"event"`
	RunNumber  int    `json:"run_number"`
	CreatedAt  string `json:"created_at"`
	StartedAt  string `json:"run_started_at"`
	UpdatedAt  string `json:"updated_at"`
	Actor      struct {
		Login string `json:"login"`
	} `json:"actor"`
	URL string `json:"html_url"`
}

type repository struct {
	FullName          string               `json:"full_name"`
	Name              string               `json:"name"`
	URL               string               `json:"url"`
	Private           bool                 `json:"private"`
	Fork              bool                 `json:"fork"`
	Stars             int                  `json:"stars"`
	Forks             int                  `json:"forks"`
	PushedAt          string               `json:"pushed_at"`
	Default           string               `json:"default_branch"`
	PRs               int                  `json:"open_prs"`
	PullRequests      []pullRequestSummary `json:"pull_requests,omitempty"`
	PRsTruncated      bool                 `json:"pull_requests_truncated,omitempty"`
	Issues            int                  `json:"open_issues"`
	BranchN           int                  `json:"branch_count"`
	Branches          []branch             `json:"branches"`
	Commits           []commitInfo         `json:"commits,omitempty"`
	CommitHistoryDays int                  `json:"commit_history_days,omitempty"`
	BranchHistoryDays int                  `json:"branch_history_days,omitempty"`
	Runs              []run                `json:"runs"`
	Fetched           time.Time            `json:"fetched"`
	BranchesFetched   time.Time            `json:"branches_fetched"`
	CommitsFetched    time.Time            `json:"commits_fetched"`
}

type diskCache struct {
	Login   string            `json:"login"`
	Repos   []repository      `json:"repos"`
	SavedAt time.Time         `json:"saved_at"`
	ETags   map[string]string `json:"etags,omitempty"`
}

type apiClient struct {
	token string
	http  *http.Client
	mu    sync.Mutex
	etags map[string]string
	cache map[string][]run
}

func newAPIClient() (*apiClient, error) {
	cmd := exec.Command("gh", "auth", "token", "--hostname", "github.com")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	data, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("could not read GitHub CLI login: %s", strings.TrimSpace(stderr.String()))
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return nil, errors.New("GitHub CLI returned an empty login token")
	}
	return &apiClient{token: token, http: &http.Client{Timeout: 25 * time.Second}, etags: map[string]string{}, cache: map[string][]run{}}, nil
}

func (c *apiClient) request(ctx context.Context, method, url string, body io.Reader, etagKey string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	if etagKey != "" {
		c.mu.Lock()
		etag := c.etags[etagKey]
		c.mu.Unlock()
		if etag != "" {
			req.Header.Set("If-None-Match", etag)
		}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if etagKey != "" && resp.Header.Get("ETag") != "" {
		c.mu.Lock()
		c.etags[etagKey] = resp.Header.Get("ETag")
		c.mu.Unlock()
	}
	return resp, nil
}

func responseError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	var detail struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &detail)
	if resp.StatusCode == http.StatusForbidden && strings.Contains(strings.ToLower(detail.Message), "rate limit") {
		if reset := resp.Header.Get("X-RateLimit-Reset"); reset != "" {
			if sec, err := strconv.ParseInt(reset, 10, 64); err == nil {
				return fmt.Errorf("GitHub API limit reached; resets at %s", time.Unix(sec, 0).Local().Format("15:04:05"))
			}
		}
	}
	if detail.Message == "" {
		detail.Message = strings.TrimSpace(string(body))
	}
	return fmt.Errorf("GitHub API %s: %s", resp.Status, detail.Message)
}

const repoQuery = `query($after:String) {
  viewer {
    login
    repositories(first:100, after:$after, affiliations:[OWNER,COLLABORATOR,ORGANIZATION_MEMBER]) {
      nodes {
        nameWithOwner name url isPrivate isFork stargazerCount forkCount pushedAt
        defaultBranchRef { name }
        pullRequests(states:OPEN, first:10, orderBy:{field:UPDATED_AT,direction:DESC}) {
          totalCount
          nodes {
            number title url isDraft createdAt updatedAt reviewDecision mergeStateStatus headRefName
            author { login }
            reviewRequests(first:5) {
              nodes {
                requestedReviewer {
                  __typename
                  ... on User { login }
                  ... on Bot { login }
                  ... on Mannequin { login }
                  ... on Team { name }
                  ... on EnterpriseTeam { combinedSlug }
                }
              }
              pageInfo { hasNextPage }
            }
          }
          pageInfo { hasNextPage }
        }
        issues(states:OPEN) { totalCount }
        refs(refPrefix:"refs/heads/", first:100) {
          totalCount
          nodes { name target { ... on Commit { oid committedDate messageHeadline } } }
          pageInfo { hasNextPage endCursor }
        }
      }
      pageInfo { hasNextPage endCursor }
    }
  }
}`

type repoResponse struct {
	Data struct {
		Viewer struct {
			Login        string `json:"login"`
			Repositories struct {
				Nodes []struct {
					NameWithOwner string `json:"nameWithOwner"`
					Name          string `json:"name"`
					URL           string `json:"url"`
					Private       bool   `json:"isPrivate"`
					Fork          bool   `json:"isFork"`
					Stars         int    `json:"stargazerCount"`
					Forks         int    `json:"forkCount"`
					PushedAt      string `json:"pushedAt"`
					Default       *struct {
						Name string `json:"name"`
					} `json:"defaultBranchRef"`
					PRs struct {
						Total int `json:"totalCount"`
						Nodes []struct {
							Number           int    `json:"number"`
							Title            string `json:"title"`
							URL              string `json:"url"`
							IsDraft          bool   `json:"isDraft"`
							CreatedAt        string `json:"createdAt"`
							UpdatedAt        string `json:"updatedAt"`
							ReviewDecision   string `json:"reviewDecision"`
							MergeStateStatus string `json:"mergeStateStatus"`
							HeadRefName      string `json:"headRefName"`
							Author           struct {
								Login string `json:"login"`
							} `json:"author"`
							ReviewRequests struct {
								Nodes []struct {
									RequestedReviewer struct {
										Typename     string `json:"__typename"`
										Login        string `json:"login"`
										Name         string `json:"name"`
										CombinedSlug string `json:"combinedSlug"`
									} `json:"requestedReviewer"`
								} `json:"nodes"`
								Page struct {
									HasNext bool `json:"hasNextPage"`
								} `json:"pageInfo"`
							} `json:"reviewRequests"`
						} `json:"nodes"`
						Page struct {
							HasNext bool `json:"hasNextPage"`
						} `json:"pageInfo"`
					} `json:"pullRequests"`
					Issues struct {
						Total int `json:"totalCount"`
					} `json:"issues"`
					Refs struct {
						Total int `json:"totalCount"`
						Nodes []struct {
							Name   string `json:"name"`
							Target struct {
								OID     string `json:"oid"`
								Date    string `json:"committedDate"`
								Message string `json:"messageHeadline"`
							} `json:"target"`
						} `json:"nodes"`
						Page struct {
							HasNext bool   `json:"hasNextPage"`
							End     string `json:"endCursor"`
						} `json:"pageInfo"`
					} `json:"refs"`
				} `json:"nodes"`
				Page struct {
					HasNext bool   `json:"hasNextPage"`
					End     string `json:"endCursor"`
				} `json:"pageInfo"`
			} `json:"repositories"`
		} `json:"viewer"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

func (c *apiClient) fetchRepositories(ctx context.Context) (string, []repository, error) {
	var all []repository
	var login, after string
	for {
		payload, _ := json.Marshal(map[string]any{"query": repoQuery, "variables": map[string]any{"after": nullable(after)}})
		resp, err := c.request(ctx, http.MethodPost, graphQLURL, bytes.NewReader(payload), "")
		if err != nil {
			return login, nil, err
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			err = responseError(resp)
			resp.Body.Close()
			return login, nil, err
		}
		var result repoResponse
		err = json.NewDecoder(resp.Body).Decode(&result)
		resp.Body.Close()
		if err != nil {
			return login, nil, fmt.Errorf("decode repository snapshot: %w", err)
		}
		if len(result.Errors) > 0 {
			return login, nil, errors.New(result.Errors[0].Message)
		}
		login = result.Data.Viewer.Login
		for _, n := range result.Data.Viewer.Repositories.Nodes {
			r := repository{FullName: n.NameWithOwner, Name: n.Name, URL: n.URL, Private: n.Private, Fork: n.Fork, Stars: n.Stars, Forks: n.Forks, PushedAt: n.PushedAt, PRs: n.PRs.Total, Issues: n.Issues.Total, BranchN: n.Refs.Total, BranchesFetched: time.Now()}
			if n.Default != nil {
				r.Default = n.Default.Name
			}
			r.PRsTruncated = n.PRs.Page.HasNext
			for _, p := range n.PRs.Nodes {
				pr := pullRequestSummary{
					Number: p.Number, Title: p.Title, URL: p.URL, IsDraft: p.IsDraft,
					CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt, ReviewDecision: p.ReviewDecision,
					MergeStateStatus: p.MergeStateStatus, HeadRefName: p.HeadRefName, AuthorLogin: p.Author.Login,
					RequestedReviewersTruncated: p.ReviewRequests.Page.HasNext,
				}
				for _, request := range p.ReviewRequests.Nodes {
					reviewer := request.RequestedReviewer.Login
					if reviewer == "" {
						reviewer = request.RequestedReviewer.Name
					}
					if reviewer == "" {
						reviewer = request.RequestedReviewer.CombinedSlug
					}
					if reviewer == "" {
						reviewer = strings.ToLower(request.RequestedReviewer.Typename)
					}
					if reviewer != "" {
						pr.RequestedReviewers = append(pr.RequestedReviewers, reviewer)
					}
				}
				r.PullRequests = append(r.PullRequests, pr)
			}
			for _, b := range n.Refs.Nodes {
				br := branch{Name: b.Name, SHA: b.Target.OID, Date: b.Target.Date, Msg: b.Target.Message}
				r.Branches = append(r.Branches, br)
			}
			all = append(all, r)
		}
		page := result.Data.Viewer.Repositories.Page
		if !page.HasNext {
			break
		}
		after = page.End
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	return login, all, nil
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (c *apiClient) fetchRuns(ctx context.Context, fullName string) ([]run, error) {
	key := "runs:" + fullName
	url := apiRoot + "/repos/" + fullName + "/actions/runs?per_page=100"
	resp, err := c.request(ctx, http.MethodGet, url, nil, key)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotModified {
		resp.Body.Close()
		c.mu.Lock()
		cached := append([]run(nil), c.cache[fullName]...)
		c.mu.Unlock()
		return cached, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		err = responseError(resp)
		resp.Body.Close()
		return nil, err
	}
	var data struct {
		Runs []run `json:"workflow_runs"`
	}
	err = json.NewDecoder(resp.Body).Decode(&data)
	resp.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("decode Actions runs: %w", err)
	}
	c.mu.Lock()
	c.cache[fullName] = append([]run(nil), data.Runs...)
	c.mu.Unlock()
	return data.Runs, nil
}

func (c *apiClient) fetchBranches(ctx context.Context, fullName, after string) ([]branch, string, error) {
	query := `query($owner:String!,$name:String!,$after:String,$since:GitTimestamp!){repository(owner:$owner,name:$name){refs(refPrefix:"refs/heads/",first:100,after:$after){nodes{name target{...on Commit{oid committedDate messageHeadline history(first:20,since:$since){nodes{oid committedDate messageHeadline author{name user{login}}}pageInfo{hasNextPage}}}}}pageInfo{hasNextPage endCursor}}}}`
	parts := strings.SplitN(fullName, "/", 2)
	since := time.Now().Add(-time.Duration(activityHistoryDays) * 24 * time.Hour).UTC().Format(time.RFC3339)
	payload, _ := json.Marshal(map[string]any{"query": query, "variables": map[string]any{"owner": parts[0], "name": parts[1], "after": nullable(after), "since": since}})
	resp, err := c.request(ctx, http.MethodPost, graphQLURL, bytes.NewReader(payload), "")
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		err = responseError(resp)
		resp.Body.Close()
		return nil, "", err
	}
	var data struct {
		Data struct {
			Repository struct {
				Refs struct {
					Nodes []struct {
						Name   string `json:"name"`
						Target struct {
							OID     string `json:"oid"`
							Date    string `json:"committedDate"`
							Message string `json:"messageHeadline"`
							History struct {
								Nodes []struct {
									OID     string `json:"oid"`
									Date    string `json:"committedDate"`
									Message string `json:"messageHeadline"`
									Author  struct {
										Name string `json:"name"`
										User struct {
											Login string `json:"login"`
										} `json:"user"`
									} `json:"author"`
								} `json:"nodes"`
								Page struct {
									More bool `json:"hasNextPage"`
								} `json:"pageInfo"`
							} `json:"history"`
						} `json:"target"`
					} `json:"nodes"`
					Page struct {
						More bool   `json:"hasNextPage"`
						End  string `json:"endCursor"`
					} `json:"pageInfo"`
				} `json:"refs"`
			} `json:"repository"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	err = json.NewDecoder(resp.Body).Decode(&data)
	resp.Body.Close()
	if err != nil {
		return nil, "", err
	}
	if len(data.Errors) > 0 {
		return nil, "", errors.New(data.Errors[0].Message)
	}
	var result []branch
	for _, n := range data.Data.Repository.Refs.Nodes {
		br := branch{Name: n.Name, SHA: n.Target.OID, Date: n.Target.Date, Msg: n.Target.Message, HistoryTruncated: n.Target.History.Page.More}
		for _, c := range n.Target.History.Nodes {
			br.History = append(br.History, commitInfo{SHA: c.OID, Date: c.Date, Msg: c.Message, Author: c.Author.Name, AuthorLogin: c.Author.User.Login})
		}
		result = append(result, br)
	}
	if data.Data.Repository.Refs.Page.More {
		return result, data.Data.Repository.Refs.Page.End, nil
	}
	return result, "", nil
}

func (c *apiClient) fetchCommitHistory(ctx context.Context, repo repository) ([]commitInfo, error) {
	if repo.Default == "" {
		return nil, nil
	}
	since := time.Now().Add(-time.Duration(activityHistoryDays) * 24 * time.Hour).UTC().Format(time.RFC3339)
	var commits []commitInfo
	for page := 1; ; page++ {
		endpoint := fmt.Sprintf("%s/repos/%s/commits?sha=%s&since=%s&per_page=100&page=%d",
			apiRoot, repo.FullName, url.QueryEscape(repo.Default), url.QueryEscape(since), page)
		resp, err := c.request(ctx, http.MethodGet, endpoint, nil, "")
		if err != nil {
			return nil, err
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			err = responseError(resp)
			resp.Body.Close()
			return nil, err
		}
		var data []struct {
			SHA    string `json:"sha"`
			Commit struct {
				Committer struct {
					Date string `json:"date"`
				} `json:"committer"`
				Message string `json:"message"`
				Author  struct {
					Name string `json:"name"`
				} `json:"author"`
			} `json:"commit"`
			Author struct {
				Login string `json:"login"`
			} `json:"author"`
		}
		err = json.NewDecoder(resp.Body).Decode(&data)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("decode commit history for %s: %w", repo.FullName, err)
		}
		for _, item := range data {
			commits = append(commits, commitInfo{
				SHA: item.SHA, Date: item.Commit.Committer.Date,
				Msg:    strings.SplitN(item.Commit.Message, "\n", 2)[0],
				Author: item.Commit.Author.Name, AuthorLogin: item.Author.Login,
			})
		}
		if len(data) < 100 {
			break
		}
	}
	return commits, nil
}

type commitHistoryMsg struct {
	commits map[string][]commitInfo
	errors  map[string]string
}

func fetchCommitHistoryCmd(c *apiClient, repos []repository) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 55*time.Second)
		defer cancel()
		msg := commitHistoryMsg{commits: make(map[string][]commitInfo), errors: make(map[string]string)}
		sem := make(chan struct{}, 4)
		var wg sync.WaitGroup
		var mu sync.Mutex
		cutoff := time.Now().Add(-30 * 24 * time.Hour)
		var activeRepos []repository
		for _, repo := range repos {
			pushedAt := parseTime(repo.PushedAt)
			if !pushedAt.IsZero() && pushedAt.Before(cutoff) {
				msg.commits[repo.FullName] = nil
				continue
			}
			activeRepos = append(activeRepos, repo)
		}
		for _, repo := range activeRepos {
			name := repo.FullName
			wg.Add(1)
			go func() {
				defer wg.Done()
				select {
				case sem <- struct{}{}:
				case <-ctx.Done():
					mu.Lock()
					msg.errors[name] = ctx.Err().Error()
					mu.Unlock()
					return
				}
				defer func() { <-sem }()
				items, err := c.fetchCommitHistory(ctx, repo)
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					msg.errors[name] = err.Error()
					return
				}
				msg.commits[name] = items
			}()
		}
		wg.Wait()
		return msg
	}
}

func (c *apiClient) fetchLog(ctx context.Context, repo string, id int64) (string, error) {
	cmd := exec.CommandContext(ctx, "gh", "run", "view", strconv.FormatInt(id, 10), "--repo", repo, "--log")
	var stderr bytes.Buffer
	out := &tailBuffer{max: maxLogBytes}
	cmd.Stdout = out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s", strings.TrimSpace(stderr.String()))
	}
	return out.String(), nil
}

type tailBuffer struct {
	data []byte
	max  int
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	written := len(p)
	if len(p) >= b.max {
		b.data = append(b.data[:0], p[len(p)-b.max:]...)
		return written, nil
	}
	if excess := len(b.data) + len(p) - b.max; excess > 0 {
		copy(b.data, b.data[excess:])
		b.data = b.data[:len(b.data)-excess]
	}
	b.data = append(b.data, p...)
	return written, nil
}
func (b *tailBuffer) String() string { return string(b.data) }

type reposMsg struct {
	login string
	repos []repository
	err   error
}
type runsMsg struct {
	repo       string
	runs       []run
	err        error
	generation uint64
}
type branchesMsg struct {
	repo     string
	branches []branch
	err      error
}
type branchHistoryMsg struct {
	branches map[string][]branch
	skipped  map[string]bool
	errors   map[string]string
}

type logMsg struct {
	repo string
	id   int64
	text string
	err  error
}
type tickMsg time.Time
type animationTickMsg time.Time
type keyboardSupportTimeoutMsg struct{}

type model struct {
	api                       *apiClient
	agentEventsPath           string
	agentCursor               agentSpoolCursor
	agentTasks                map[string]agentTask
	agentSeenEventIDs         map[string]struct{}
	agentSeenOrder            []string
	lastAgentPoll             time.Time
	agentLastErr              string
	agentRejected             int
	busyAgentPoll             bool
	busyCommitHistory         bool
	lastCommitHistory         time.Time
	busyBranchHistory         bool
	lastBranchHistory         time.Time
	login                     string
	repos                     []repository
	width, height             int
	cursor, runCursor         int
	copyNotice                string
	copyNoticeAt              time.Time
	keyboardEnhancementsKnown bool
	rightShiftSupported       bool
	altSupported              bool
	clipboardCopy             func(string) error
	copyTemplate              string
	templateEditing           bool
	templateDraft             string
	configPath                string
	refreshGeneration         uint64
	refreshRunsAfterRepos     bool
	attentionCursor           int
	overviewPane              int // 0: repository health, 1: in flight / waiting
	inFlightCursor            int
	prCursor                  int
	commitScroll              int
	workloadScroll            int
	commitPeriod              int
	agentWorkloadMode         bool
	runScroll                 int
	logOffset                 int
	focus                     int // overview, repositories, CI queue
	detail                    bool
	filter                    string
	filtering                 bool
	busyRuns                  map[string]bool
	busyBranches              map[string]bool
	queuedRuns                map[string]bool
	runQueue                  []string
	inFlightRuns              int
	busyRepos                 bool
	retryAt                   map[string]time.Time
	failures                  map[string]int
	lastRepos, lastRuns       time.Time
	lastErr                   string
	cachePath                 string
	logMode, logWatch         bool
	logRepo                   string
	logRun                    int64
	logText, logErr           string
	logUpdated                time.Time
	logBusy                   bool
	lastLog                   time.Time
	showHelp                  bool
	animationFrame            int
	animationEnabled          bool
	animationActive           bool
}

func fetchReposCmd(c *apiClient) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		login, repos, err := c.fetchRepositories(ctx)
		return reposMsg{login, repos, err}
	}
}
func fetchRunsCmd(c *apiClient, repo string, generation uint64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
		defer cancel()
		runs, err := c.fetchRuns(ctx, repo)
		return runsMsg{repo: repo, runs: runs, err: err, generation: generation}
	}
}
func fetchAllBranchesCmd(c *apiClient, repo string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		defer cancel()
		var all []branch
		after := ""
		for {
			page, next, err := c.fetchBranches(ctx, repo, after)
			if err != nil {
				return branchesMsg{repo: repo, err: err}
			}
			all = append(all, page...)
			if next == "" {
				break
			}
			after = next
		}
		return branchesMsg{repo, all, nil}
	}
}
func fetchBranchHistoryCmd(c *apiClient, repos []repository) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
		msg := branchHistoryMsg{branches: make(map[string][]branch), skipped: make(map[string]bool), errors: make(map[string]string)}
		sem := make(chan struct{}, 4)
		var wg sync.WaitGroup
		var mu sync.Mutex
		cutoff := time.Now().Add(-30 * 24 * time.Hour)
		for _, repo := range repos {
			if pushed := parseTime(repo.PushedAt); !pushed.IsZero() && pushed.Before(cutoff) {
				msg.skipped[repo.FullName] = true
				continue
			}
			repo := repo
			wg.Add(1)
			go func() {
				defer wg.Done()
				select {
				case sem <- struct{}{}:
				case <-ctx.Done():
					mu.Lock()
					msg.errors[repo.FullName] = ctx.Err().Error()
					mu.Unlock()
					return
				}
				defer func() { <-sem }()
				var all []branch
				after := ""
				for {
					page, next, err := c.fetchBranches(ctx, repo.FullName, after)
					if err != nil {
						mu.Lock()
						msg.errors[repo.FullName] = err.Error()
						mu.Unlock()
						return
					}
					all = append(all, page...)
					if next == "" {
						break
					}
					after = next
				}
				mu.Lock()
				msg.branches[repo.FullName] = all
				mu.Unlock()
			}()
		}
		wg.Wait()
		return msg
	}
}

func fetchLogCmd(c *apiClient, repo string, id int64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
		defer cancel()
		text, err := c.fetchLog(ctx, repo, id)
		return logMsg{repo, id, text, err}
	}
}
func (m *model) animationCmdIfNeeded() tea.Cmd {
	if !m.animationEnabled || m.animationActive {
		return nil
	}
	m.animationActive = true
	return animationCmd()
}

func keyboardSupportTimeoutCmd() tea.Cmd {
	return tea.Tick(1200*time.Millisecond, func(time.Time) tea.Msg { return keyboardSupportTimeoutMsg{} })
}

func animationCmd() tea.Cmd {
	return tea.Tick(180*time.Millisecond, func(t time.Time) tea.Msg { return animationTickMsg(t) })
}

func tickCmd(agentTelemetryEnabled bool) tea.Cmd {
	interval := 15 * time.Second
	if agentTelemetryEnabled {
		interval = agentPollInterval
	}
	return tea.Tick(interval, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func initialCachePath() string {
	if p := os.Getenv("GH_FLEET_CACHE"); p != "" {
		return p
	}
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(cacheDir, "gh-fleet", "cache.json")
}

type userConfig struct {
	CopyTemplate string `json:"copy_template,omitempty"`
}

func initialConfigPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "gh-fleet", "config.json")
}

func loadUserConfig(path string) userConfig {
	var config userConfig
	if path != "" {
		data, err := os.ReadFile(path)
		if err == nil {
			_ = json.Unmarshal(data, &config)
		}
	}
	return config
}

func saveUserConfig(path string, config userConfig) error {
	if path == "" {
		return errors.New("user configuration directory is unavailable")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func repositoryCopyText(template, repositoryURL string) string {
	if strings.TrimSpace(template) == "" {
		return repositoryURL
	}
	if strings.Contains(template, "@@@") {
		return strings.ReplaceAll(template, "@@@", repositoryURL)
	}
	return strings.TrimRight(template, "\r\n") + "\n" + repositoryURL
}

func loadCache(path string) diskCache {
	var c diskCache
	data, err := os.ReadFile(path)
	if err == nil {
		_ = json.Unmarshal(data, &c)
	}
	return c
}
func saveCache(m model) {
	if m.cachePath == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(m.cachePath), 0700)
	_ = os.Chmod(filepath.Dir(m.cachePath), 0700)
	c := diskCache{Login: m.login, Repos: m.repos, SavedAt: time.Now()}
	m.api.mu.Lock()
	c.ETags = make(map[string]string, len(m.api.etags))
	for k, v := range m.api.etags {
		c.ETags[k] = v
	}
	m.api.mu.Unlock()
	b, err := json.Marshal(c)
	if err != nil {
		return
	}
	tmp := m.cachePath + ".tmp"
	if os.WriteFile(tmp, b, 0600) == nil && os.Chmod(tmp, 0600) == nil {
		_ = os.Rename(tmp, m.cachePath)
	}
}
func newModel(client *apiClient, cachePath string, agentEventPaths ...string) model {
	agentEventsPath := os.Getenv("GH_FLEET_AGENT_EVENTS")
	if len(agentEventPaths) > 0 {
		agentEventsPath = agentEventPaths[0]
	}
	c := loadCache(cachePath)
	configPath := initialConfigPath()
	config := loadUserConfig(configPath)
	client.etags = c.ETags
	if client.etags == nil {
		client.etags = make(map[string]string)
	}
	for _, r := range c.Repos {
		client.cache[r.FullName] = append([]run(nil), r.Runs...)
	}
	var refsAt, runsAt, commitsAt, branchHistoryAt time.Time
	needsActivityHistory := false
	for _, r := range c.Repos {
		if r.CommitHistoryDays < activityHistoryDays {
			needsActivityHistory = true
		}
		if r.CommitsFetched.After(commitsAt) {
			commitsAt = r.CommitsFetched
		}
		if r.BranchesFetched.After(refsAt) {
			refsAt = r.BranchesFetched
		}
		if r.BranchHistoryDays >= activityHistoryDays && r.BranchesFetched.After(branchHistoryAt) {
			branchHistoryAt = r.BranchesFetched
		}
		if r.Fetched.After(runsAt) {
			runsAt = r.Fetched
		}
	}
	if needsActivityHistory {
		commitsAt = time.Time{}
	}
	return model{api: client, login: c.Login, repos: c.Repos, cachePath: cachePath, configPath: configPath, copyTemplate: config.CopyTemplate, busyRepos: true,
		animationEnabled: os.Getenv("GH_FLEET_NO_ANIMATION") != "1" && os.Getenv("TERM") != "dumb",
		animationActive:  os.Getenv("GH_FLEET_NO_ANIMATION") != "1" && os.Getenv("TERM") != "dumb",
		agentEventsPath:  agentEventsPath, agentTasks: make(map[string]agentTask),
		agentSeenEventIDs: make(map[string]struct{}), busyAgentPoll: agentEventsPath != "",
		busyRuns: make(map[string]bool), busyBranches: make(map[string]bool), queuedRuns: make(map[string]bool),
		retryAt: make(map[string]time.Time), failures: make(map[string]int), lastRepos: refsAt, lastRuns: runsAt, lastCommitHistory: commitsAt, lastBranchHistory: branchHistoryAt}
}

func (m model) Init() tea.Cmd {
	cmds := []tea.Cmd{fetchReposCmd(m.api), tickCmd(m.agentEventsPath != ""), keyboardSupportTimeoutCmd()}
	if m.animationEnabled {
		m.animationActive = true
		cmds = append(cmds, animationCmd())
	}
	if m.agentEventsPath != "" {
		cmds = append(cmds, agentPollCmd(m.agentEventsPath, m.agentCursor))
	}
	return tea.Batch(cmds...)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case keyboardSupportTimeoutMsg:
		if !m.keyboardEnhancementsKnown {
			m.keyboardEnhancementsKnown = true
			m.rightShiftSupported = false
			m.altSupported = false
		}
	case tea.KeyboardEnhancementsMsg:
		m.keyboardEnhancementsKnown = true
		m.rightShiftSupported = msg.SupportsAllKeysAsEscapeCodes()
		m.altSupported = msg.SupportsAllKeysAsEscapeCodes()
	case tea.KeyReleaseMsg:
		return m, nil
	case animationTickMsg:
		m.animationFrame = (m.animationFrame + 1) % 8
		if m.animationEnabled {
			return m, animationCmd()
		}
		m.animationActive = false
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.KeyPressMsg:
		if m.showHelp {
			if msg.String() == "?" || msg.String() == "esc" || msg.String() == "q" {
				m.showHelp = false
			}
			return m, nil
		}
		if m.templateEditing {
			switch msg.String() {
			case "esc":
				m.templateEditing = false
				m.templateDraft = ""
				m.lastErr = ""
			case "ctrl+s":
				if err := saveUserConfig(m.configPath, userConfig{CopyTemplate: m.templateDraft}); err != nil {
					m.lastErr = "Template: " + err.Error()
				} else {
					m.copyTemplate = m.templateDraft
					m.templateEditing = false
					m.copyNotice, m.copyNoticeAt = "Copy template saved", time.Now()
					m.lastErr = ""
				}
			case "backspace":
				if m.templateDraft != "" {
					_, size := utf8.DecodeLastRuneInString(m.templateDraft)
					m.templateDraft = m.templateDraft[:len(m.templateDraft)-size]
				}
			case "enter":
				m.templateDraft += "\n"
			default:
				value := msg.Text
				if value == "" && msg.Code <= unicode.MaxRune && unicode.IsPrint(msg.Code) {
					value = string(msg.Code)
				}
				m.templateDraft += value
			}
			return m, nil
		}
		if m.filtering {
			switch msg.String() {
			case "esc":
				m.filtering = false
				m.filter = ""
			case "enter":
				m.filtering = false
			case "backspace":
				if m.filter != "" {
					_, size := utf8.DecodeLastRuneInString(m.filter)
					m.filter = m.filter[:len(m.filter)-size]
				}
			default:
				if msg.Text != "" || (msg.Code <= unicode.MaxRune && unicode.IsPrint(msg.Code)) {
					value := msg.Text
					if value == "" {
						value = string(msg.Code)
					}
					m.filter += value
					m.cursor = 0
				}
			}
			return m, nil
		}
		if msg.Code == tea.KeyRightShift && !msg.IsRepeat {
			if m.focus == 0 && m.overviewPane == 0 && m.rightShiftSupported {
				m.copySelectedRepository()
			}
			return m, nil
		}
		if msg.Code == tea.KeySpace {
			return m, tea.Batch(m.refreshAll(false)...)
		}
		if msg.String() == "R" {
			return m, tea.Batch(m.refreshAll(true)...)
		}
		if msg.String() == "c" && m.focus == 0 && m.overviewPane == 0 {
			m.copySelectedRepository()
			return m, nil
		}
		if msg.String() == "C" {
			return m, tea.Batch(m.clearCacheAndRefresh()...)
		}
		if m.logMode {
			switch msg.String() {
			case "esc", "backspace", "delete":
				m.logMode = false
				m.logWatch = false
			case "u":
				m.logBusy = true
				m.lastLog = time.Now()
				return m, fetchLogCmd(m.api, m.logRepo, m.logRun)
			case "w":
				m.logWatch = !m.logWatch
				m.lastLog = time.Time{}
			case "up", "k":
				m.logScroll(8)
			case "down", "j":
				m.logScroll(-8)
			case "pgup":
				m.logScroll(20)
			case "pgdown":
				m.logScroll(-20)
			}
			return m, nil
		}
		if m.agentWorkloadMode {
			switch msg.String() {
			case "esc", "backspace", "delete", "a":
				m.agentWorkloadMode = false
			case "r":
				var cmds []tea.Cmd
				if !m.busyCommitHistory && len(m.repos) > 0 {
					m.busyCommitHistory = true
					cmds = append(cmds, fetchCommitHistoryCmd(m.api, m.repos))
				}
				if !m.busyBranchHistory && len(m.repos) > 0 {
					m.busyBranchHistory = true
					cmds = append(cmds, fetchBranchHistoryCmd(m.api, m.repos))
				}
				for _, repo := range m.repos {
					m.queueRun(repo.FullName, false)
				}
				cmds = append(cmds, m.drainRunQueue()...)
				if len(cmds) > 0 {
					return m, tea.Batch(cmds...)
				}
			case "up", "k":
				m.workloadScroll = max(0, m.workloadScroll-1)
			case "down", "j":
				m.workloadScroll = min(max(0, len(collectAgentWorkload(m.repos, time.Now()))-1), m.workloadScroll+1)
			case "pgup":
				m.workloadScroll = max(0, m.workloadScroll-3)
			case "pgdown":
				m.workloadScroll = min(max(0, len(collectAgentWorkload(m.repos, time.Now()))-1), m.workloadScroll+3)
			case "home", "g":
				m.workloadScroll = 0
			case "end", "G":
				m.workloadScroll = max(0, len(collectAgentWorkload(m.repos, time.Now()))-1)
			}
			return m, nil
		}
		if msg.String() == "t" {
			if m.focus == 0 && m.overviewPane == 0 {
				m.templateEditing = true
				m.templateDraft = ""
				m.lastErr = ""
				m.copyNotice = ""
			}
			return m, nil
		}
		if msg.String() == "a" {
			m.agentWorkloadMode = true
			m.workloadScroll = 0
			var cmds []tea.Cmd
			if !m.busyCommitHistory && len(m.repos) > 0 && (m.lastCommitHistory.IsZero() || time.Since(m.lastCommitHistory) >= activityRefreshInterval) {
				m.busyCommitHistory = true
				cmds = append(cmds, fetchCommitHistoryCmd(m.api, m.repos))
			}
			if !m.busyBranchHistory && len(m.repos) > 0 && (m.lastBranchHistory.IsZero() || time.Since(m.lastBranchHistory) >= activityRefreshInterval) {
				m.busyBranchHistory = true
				cmds = append(cmds, fetchBranchHistoryCmd(m.api, m.repos))
			}
			if len(cmds) > 0 {
				return m, tea.Batch(cmds...)
			}
			return m, nil
		}
		if msg.String() == "ctrl+c" || msg.String() == "q" {
			return m, tea.Quit
		}
		switch msg.String() {
		case "?":
			m.showHelp = true
		case "1":
			m.focus = 0
		case "2":
			m.focus = 1
		case "3":
			m.focus = 2
		case "left":
			if m.focus == 0 {
				m.overviewPane = (m.overviewPane + 1) % 2
			}
		case "right":
			if m.focus == 0 {
				m.overviewPane = (m.overviewPane + 1) % 2
			}
		case "tab":
			m.focus = (m.focus + 1) % 3
		case "shift+tab":
			m.focus = (m.focus + 2) % 3
		case "esc", "delete":
			if m.filter != "" {
				m.filter = ""
			} else if m.detail {
				m.detail = false
			}
		case "enter":
			switch m.focus {
			case 0:
				if m.overviewPane == 1 {
					if item := m.selectedInFlight(); item != nil && item.URL != "" {
						_ = openURL(item.URL)
					}
				} else if repo := m.selectedRepo(); repo != nil && repo.URL != "" {
					_ = openURL(repo.URL)
				}
			case 1:
				if m.detail {
					if r := m.selectedRepo(); r != nil && m.prCursor >= 0 && m.prCursor < len(r.PullRequests) {
						_ = openURL(r.PullRequests[m.prCursor].URL)
					}
				} else if len(m.visibleRepos()) > 0 {
					m.detail = true
					m.prCursor = 0
					if m.detail {
						r := m.selectedRepo()
						var cmds []tea.Cmd
						if r != nil {
							if m.queuedRuns[r.FullName] {
								filtered := m.runQueue[:0]
								for _, name := range m.runQueue {
									if name != r.FullName {
										filtered = append(filtered, name)
									}
								}
								m.runQueue = filtered
								delete(m.queuedRuns, r.FullName)
							}
							m.queueRun(r.FullName, true)
						}
						cmds = append(cmds, m.drainRunQueue()...)
						if len(cmds) > 0 {
							return m, tea.Batch(cmds...)
						}
					}
				}
			case 2:
				if r := m.selectedRun(); r != nil {
					return m, m.openRunLogs(r.repo, r.item.ID)
				}
			}
		case "r":
			return m, tea.Batch(m.refreshAll(false)...)
		case "/":
			m.focus = 1
			m.filtering = true
			m.filter = ""
		case "o":
			switch m.focus {
			case 0:
				if m.overviewPane == 1 {
					if item := m.selectedInFlight(); item != nil && item.URL != "" {
						_ = openURL(item.URL)
					}
				} else if repo := m.selectedRepo(); repo != nil && repo.URL != "" {
					_ = openURL(repo.URL)
				}
			case 1:
				if r := m.selectedRepo(); r != nil {
					if m.detail && m.prCursor >= 0 && m.prCursor < len(r.PullRequests) {
						_ = openURL(r.PullRequests[m.prCursor].URL)
					} else {
						_ = openURL(r.URL)
					}
				}
			case 2:
				if r := m.selectedRun(); r != nil {
					_ = openURL(r.item.URL)
				}
			}
		case "l":
			if m.focus == 0 {
				if m.overviewPane == 1 {
					if item := m.selectedInFlight(); item != nil && item.RunID != 0 {
						return m, m.openRunLogs(item.Repo, item.RunID)
					}
				} else if repo := m.selectedRepo(); repo != nil {
					if item := latestRun(*repo); item != nil && item.ID != 0 {
						return m, m.openRunLogs(repo.FullName, item.ID)
					}
				}
			}
			if m.focus == 2 {
				if r := m.selectedRun(); r != nil {
					return m, m.openRunLogs(r.repo, r.item.ID)
				}
			}
		case "up", "k":
			m.move(-1)
		case "down", "j":
			m.move(1)
		case "home", "g":
			switch m.focus {
			case 0:
				switch m.overviewPane {
				case 1:
					m.inFlightCursor = 0
				default:
					m.cursor = 0
				}
			case 1:
				if m.detail {
					m.prCursor = 0
				} else {
					m.cursor = 0
				}
			case 2:
				m.runCursor = 0
			}
		case "end", "G":
			switch m.focus {
			case 0:
				switch m.overviewPane {
				case 1:
					m.inFlightCursor = max(0, len(m.inFlightItems())-1)
				default:
					m.cursor = max(0, len(m.visibleRepos())-1)
				}
			case 1:
				if m.detail {
					if r := m.selectedRepo(); r != nil {
						m.prCursor = max(0, len(r.PullRequests)-1)
					}
				} else {
					m.cursor = max(0, len(m.visibleRepos())-1)
				}
			case 2:
				m.runCursor = max(0, len(m.visibleRuns())-1)
			}
		case "backspace":
			// Search input is handled above while filter mode is active.
		}
	case reposMsg:
		m.busyRepos = false
		if msg.err != nil {
			m.lastErr = msg.err.Error()
			m.backoff("repos")
		} else {
			m.clearBackoff("repos")
			old := make(map[string]repository, len(m.repos))
			for _, r := range m.repos {
				old[r.FullName] = r
			}
			for i := range msg.repos {
				if v, ok := old[msg.repos[i].FullName]; ok {
					msg.repos[i].Runs = v.Runs
					msg.repos[i].Fetched = v.Fetched
					msg.repos[i].Branches = v.Branches
					msg.repos[i].BranchesFetched = v.BranchesFetched
					msg.repos[i].Branches = v.Branches
					msg.repos[i].BranchHistoryDays = v.BranchHistoryDays
					msg.repos[i].Commits = v.Commits
					msg.repos[i].CommitHistoryDays = v.CommitHistoryDays
					msg.repos[i].CommitsFetched = v.CommitsFetched
				}
			}
			m.repos = msg.repos
			m.login = msg.login
			m.lastRepos = time.Now()
			m.lastErr = ""
			m.cursor = min(m.cursor, max(0, len(m.visibleRepos())-1))
			saveCache(m)
			var cmds []tea.Cmd
			for _, r := range m.repos {
				if m.refreshRunsAfterRepos || r.Fetched.IsZero() || time.Since(r.Fetched) > allRefreshInterval {
					m.queueRun(r.FullName, false)
				}
			}
			m.refreshRunsAfterRepos = false
			cmds = append(cmds, m.drainRunQueue()...)
			if len(cmds) > 0 {
				return m, tea.Batch(cmds...)
			}
		}
	case runsMsg:
		delete(m.busyRuns, msg.repo)
		m.inFlightRuns = max(0, m.inFlightRuns-1)
		if msg.generation != m.refreshGeneration {
			if !m.refreshRunsAfterRepos {
				m.queueRun(msg.repo, false)
			}
			if cmds := m.drainRunQueue(); len(cmds) > 0 {
				return m, tea.Batch(cmds...)
			}
			return m, nil
		}
		if msg.err != nil {
			m.lastErr = msg.err.Error()
			m.backoff("runs:" + msg.repo)
		} else {
			m.clearBackoff("runs:" + msg.repo)
			if r := m.repoByName(msg.repo); r != nil {
				r.Runs = msg.runs
				r.Fetched = time.Now()
			}
			m.lastRuns = time.Now()
			m.lastErr = ""
			saveCache(m)
		}
		if cmds := m.drainRunQueue(); len(cmds) > 0 {
			return m, tea.Batch(cmds...)
		}
	case branchesMsg:
		delete(m.busyBranches, msg.repo)
		if msg.err != nil {
			m.lastErr = msg.err.Error()
			m.backoff("branches:" + msg.repo)
		} else if r := m.repoByName(msg.repo); r != nil {
			m.clearBackoff("branches:" + msg.repo)
			r.Branches = msg.branches
			r.BranchN = len(msg.branches)
			r.BranchesFetched = time.Now()
			r.BranchHistoryDays = activityHistoryDays
			saveCache(m)
		}
	case branchHistoryMsg:
		now := time.Now()
		for name, message := range msg.errors {
			m.lastErr = name + ": " + message
		}
		if len(msg.errors) > 0 {
			m.backoff("branch-history")
		} else {
			m.clearBackoff("branch-history")
		}
		for name, items := range msg.branches {
			if r := m.repoByName(name); r != nil {
				r.Branches = items
				r.BranchN = max(r.BranchN, len(items))
				r.BranchesFetched = now
				r.BranchHistoryDays = activityHistoryDays
			}
		}
		for name := range msg.skipped {
			if r := m.repoByName(name); r != nil {
				r.BranchesFetched = now
				r.BranchHistoryDays = activityHistoryDays
			}
		}
		m.lastBranchHistory = now
		m.busyBranchHistory = false
		saveCache(m)
	case commitHistoryMsg:
		for name, message := range msg.errors {
			m.lastErr = name + ": " + message
		}
		for name, items := range msg.commits {
			if r := m.repoByName(name); r != nil {
				r.Commits = items
				r.CommitHistoryDays = activityHistoryDays
				r.CommitsFetched = time.Now()
			}
		}
		m.lastCommitHistory = time.Now()
		if len(msg.commits) > 0 {
			saveCache(m)
		}
		m.busyCommitHistory = false
	case logMsg:
		m.logBusy = false
		m.logUpdated = time.Now()
		if msg.err != nil {
			m.logErr = msg.err.Error()
		} else {
			m.logText = msg.text
			m.logErr = ""
		}
	case agentPollMsg:
		m.busyAgentPoll = false
		m.lastAgentPoll = time.Now()
		if msg.Err != nil {
			m.agentLastErr = msg.Err.Error()
			break
		}
		if msg.Batch.Reset {
			m.agentTasks = make(map[string]agentTask)
			m.agentSeenEventIDs = make(map[string]struct{})
			m.agentSeenOrder = nil
			m.agentRejected = 0
		}
		m.agentCursor = msg.Batch.Cursor
		m.agentRejected += msg.Batch.InvalidLines
		for _, event := range msg.Batch.Events {
			err := applyAgentEvent(m.agentTasks, m.agentSeenEventIDs, &m.agentSeenOrder, event, time.Now())
			if err != nil && err.Error() != "duplicate event_id" {
				m.agentRejected++
			}
		}
		pruneAgentTasks(m.agentTasks, time.Now())
		m.agentLastErr = ""
	case tickMsg:
		var cmds []tea.Cmd
		now := time.Now()
		if m.agentEventsPath != "" && !m.busyAgentPoll &&
			(m.lastAgentPoll.IsZero() || now.Sub(m.lastAgentPoll) >= agentPollInterval) {
			m.busyAgentPoll = true
			cmds = append(cmds, agentPollCmd(m.agentEventsPath, m.agentCursor))
		}
		if len(m.repos) == 0 && !m.busyRepos && !now.Before(m.retryAt["repos"]) {
			m.busyRepos = true
			cmds = append(cmds, fetchReposCmd(m.api))
		}
		if !m.detail && now.Sub(m.lastRepos) >= refsRefreshInterval && !m.busyRepos && !now.Before(m.retryAt["repos"]) {
			m.busyRepos = true
			cmds = append(cmds, fetchReposCmd(m.api))
		}
		if m.agentWorkloadMode && !m.detail && len(m.repos) > 0 && !m.busyCommitHistory &&
			(m.lastCommitHistory.IsZero() || now.Sub(m.lastCommitHistory) >= activityRefreshInterval) {
			m.busyCommitHistory = true
			cmds = append(cmds, fetchCommitHistoryCmd(m.api, m.repos))
		}
		needsBranchHistory := false
		for _, repo := range m.repos {
			if repo.BranchHistoryDays < activityHistoryDays {
				needsBranchHistory = true
				break
			}
		}
		if m.agentWorkloadMode && !m.detail && len(m.repos) > 0 && !m.busyBranchHistory &&
			(needsBranchHistory || m.lastBranchHistory.IsZero() || now.Sub(m.lastBranchHistory) >= activityRefreshInterval) &&
			!now.Before(m.retryAt["branch-history"]) {
			m.busyBranchHistory = true
			cmds = append(cmds, fetchBranchHistoryCmd(m.api, m.repos))
		}
		for _, r := range m.repos {
			interval := allRefreshInterval
			selected := m.detail && m.selectedRepo() != nil && r.FullName == m.selectedRepo().FullName
			if m.detail && !selected {
				continue
			}
			if selected {
				interval = selectedInterval
			}
			if (r.Fetched.IsZero() || now.Sub(r.Fetched) >= interval) && !now.Before(m.retryAt["runs:"+r.FullName]) {
				m.queueRun(r.FullName, selected)
			}
		}
		cmds = append(cmds, m.drainRunQueue()...)
		if m.logMode && m.logWatch && !m.logBusy && now.Sub(m.lastLog) >= logRefreshInterval {
			m.logBusy = true
			m.lastLog = now
			cmds = append(cmds, fetchLogCmd(m.api, m.logRepo, m.logRun))
		}
		cmds = append(cmds, tickCmd(m.agentEventsPath != ""))
		if len(cmds) > 1 {
			return m, tea.Batch(cmds...)
		}
	}
	return m, nil
}

// refreshAll bypasses the normal refresh schedule. A forced refresh clears ETags
// so GitHub returns fresh representations instead of responding with HTTP 304.
func (m *model) refreshAll(force bool) []tea.Cmd {
	if m.busyRepos {
		m.lastErr = "Repository refresh already in progress; keeping the current snapshot"
		return nil
	}
	m.refreshGeneration++
	m.refreshRunsAfterRepos = true
	m.runQueue = nil
	m.queuedRuns = make(map[string]bool)
	if force {
		m.api.mu.Lock()
		m.api.etags = make(map[string]string)
		m.api.cache = make(map[string][]run)
		m.api.mu.Unlock()
	}
	m.lastErr = "Refreshing GitHub snapshot…"
	if force {
		m.lastErr = "Force-refreshing GitHub snapshot…"
	}
	delete(m.retryAt, "repos")
	m.busyRepos = true
	for _, repo := range m.repos {
		delete(m.retryAt, "runs:"+repo.FullName)
		delete(m.retryAt, "branches:"+repo.FullName)
	}
	cmds := []tea.Cmd{fetchReposCmd(m.api)}
	if cmd := m.animationCmdIfNeeded(); cmd != nil {
		cmds = append(cmds, cmd)
	}
	return cmds
}

// clearCacheAndRefresh removes the on-disk snapshot and ETags while retaining
// the currently rendered data until a replacement snapshot has been fetched.
func (m *model) clearCacheAndRefresh() []tea.Cmd {
	if m.busyRepos {
		m.lastErr = "Repository refresh already in progress; cache was not cleared"
		return nil
	}
	if m.cachePath != "" {
		if err := os.Remove(m.cachePath); err != nil && !errors.Is(err, os.ErrNotExist) {
			m.lastErr = "Could not clear cache: " + err.Error()
			return nil
		}
	}
	m.api.mu.Lock()
	m.api.etags = make(map[string]string)
	m.api.cache = make(map[string][]run)
	m.api.mu.Unlock()
	m.lastErr = "Local cache cleared; fetching a fresh GitHub snapshot…"
	return m.refreshAll(true)
}

func (m *model) logScroll(n int) {
	m.logOffset = max(0, min(m.logOffset+n, max(0, len(strings.Split(m.logText, "\n"))-1)))
}
func (m *model) move(n int) {
	switch m.focus {
	case 0:
		switch m.overviewPane {
		case 1:
			m.inFlightCursor = max(0, min(len(m.inFlightItems())-1, m.inFlightCursor+n))
		default:
			m.cursor = max(0, min(len(m.visibleRepos())-1, m.cursor+n))
		}
	case 1:
		if m.detail {
			if r := m.selectedRepo(); r != nil {
				m.prCursor = max(0, min(len(r.PullRequests)-1, m.prCursor+n))
			}
		} else {
			m.cursor = max(0, min(len(m.visibleRepos())-1, m.cursor+n))
		}
	case 2:
		m.runCursor = max(0, min(len(m.visibleRuns())-1, m.runCursor+n))
	}
}

func (m model) inFlightItems() []inFlightItem {
	var agentTasks map[string]agentTask
	if m.agentEventsPath != "" {
		agentTasks = m.agentTasks
	}
	return collectInFlightItems(m.repos, time.Now(), agentTasks)
}

func latestCIActivity(repo repository) time.Time {
	var latest time.Time
	for _, item := range repo.Runs {
		value := item.UpdatedAt
		if value == "" {
			value = item.CreatedAt
		}
		if parsed := parseTime(value); !parsed.IsZero() && parsed.After(latest) {
			latest = parsed
		}
	}
	return latest
}

func isRecentPushCIIdle(repo repository, now time.Time) bool {
	pushed := parseTime(repo.PushedAt)
	if pushed.IsZero() || pushed.After(now) || now.Sub(pushed) > recentPushWindow {
		return false
	}
	for _, item := range repo.Runs {
		if isActive(item) {
			return false
		}
	}
	lastCI := latestCIActivity(repo)
	return lastCI.IsZero() || (!lastCI.After(now) && now.Sub(lastCI) >= idleCIInactivityThreshold)
}

func repositoryIsStale(repo repository, now time.Time) bool {
	pushed := parseTime(repo.PushedAt)
	return !pushed.IsZero() && !pushed.After(now) && now.Sub(pushed) > 14*24*time.Hour
}

func repositoryPushAgeStyle(age time.Duration) lipgloss.Style {
	if age > 30*time.Minute {
		return badStyle
	}
	return goodStyle
}

func repoNameStyle(repo *repository, now time.Time) lipgloss.Style {
	if repo != nil && repositoryIsStale(*repo, now) {
		return mutedStyle
	}
	if repo != nil && isRecentPushCIIdle(*repo, now) {
		return activeStyle
	}
	return titleStyle
}

func idleRepositories(repos []repository, now time.Time) []*repository {
	var idle []*repository
	for i := range repos {
		repo := &repos[i]
		if !isRecentPushCIIdle(*repo, now) {
			continue
		}
		idle = append(idle, repo)
	}
	sort.SliceStable(idle, func(i, j int) bool {
		a, b := parseTime(idle[i].PushedAt), parseTime(idle[j].PushedAt)
		if !a.Equal(b) {
			return a.After(b)
		}
		return idle[i].FullName < idle[j].FullName
	})
	return idle
}

func (m *model) copySelectedRepository() {
	repo := m.selectedRepo()
	if repo == nil || repo.URL == "" {
		return
	}
	copyText := repositoryCopyText(m.copyTemplate, repo.URL)
	copyFn := m.clipboardCopy
	if copyFn == nil {
		copyFn = copyToClipboard
	}
	if err := copyFn(copyText); err != nil {
		m.lastErr = "Clipboard: " + err.Error()
		return
	}
	m.copyNotice, m.copyNoticeAt = "Copied "+repo.FullName, time.Now()
}

func (m model) selectedInFlight() *inFlightItem {
	items := m.inFlightItems()
	if len(items) == 0 {
		return nil
	}
	item := items[max(0, min(m.inFlightCursor, len(items)-1))]
	return &item
}

func (m *model) openRunLogs(repo string, id int64) tea.Cmd {
	m.logMode = true
	m.logRepo = repo
	m.logRun = id
	m.logText = ""
	m.logErr = ""
	m.logBusy = true
	m.logWatch = true
	m.logOffset = 0
	m.lastLog = time.Now()
	return fetchLogCmd(m.api, repo, id)
}

func (m *model) queueRun(name string, priority bool) {
	if m.busyRuns[name] || m.queuedRuns[name] {
		return
	}
	m.queuedRuns[name] = true
	if priority {
		m.runQueue = append([]string{name}, m.runQueue...)
	} else {
		m.runQueue = append(m.runQueue, name)
	}
}

func (m *model) backoff(key string) {
	m.failures[key]++
	power := min(6, m.failures[key]-1)
	delay := 5 * time.Second * time.Duration(1<<power)
	if delay > 5*time.Minute {
		delay = 5 * time.Minute
	}
	m.retryAt[key] = time.Now().Add(delay)
}
func (m *model) clearBackoff(key string) { delete(m.failures, key); delete(m.retryAt, key) }

func (m *model) drainRunQueue() []tea.Cmd {
	var cmds []tea.Cmd
	for m.inFlightRuns < 4 && len(m.runQueue) > 0 {
		name := m.runQueue[0]
		m.runQueue = m.runQueue[1:]
		delete(m.queuedRuns, name)
		if m.busyRuns[name] {
			continue
		}
		m.busyRuns[name] = true
		m.inFlightRuns++
		cmds = append(cmds, fetchRunsCmd(m.api, name, m.refreshGeneration))
	}
	return cmds
}
func (m model) repoByName(name string) *repository {
	for i := range m.repos {
		if m.repos[i].FullName == name {
			return &m.repos[i]
		}
	}
	return nil
}
func (m model) visibleRepos() []*repository {
	out := make([]*repository, 0, len(m.repos))
	for i := range m.repos {
		r := &m.repos[i]
		if m.filter == "" || strings.Contains(strings.ToLower(r.Name), strings.ToLower(m.filter)) || strings.Contains(strings.ToLower(r.FullName), strings.ToLower(m.filter)) {
			out = append(out, r)
		}
	}
	now := time.Now()
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := repoGroupRank(*out[i], now), repoGroupRank(*out[j], now)
		if ri != rj {
			return ri < rj
		}
		si, sj := repoPriority(out[i]), repoPriority(out[j])
		if si != sj {
			return si < sj
		}
		return latestRepoDate(*out[i]).After(latestRepoDate(*out[j]))
	})
	return out
}
func (m model) selectedRepo() *repository {
	rows := m.visibleRepos()
	if len(rows) == 0 {
		return nil
	}
	return rows[max(0, min(m.cursor, len(rows)-1))]
}

func shortName(full string) string {
	parts := strings.SplitN(full, "/", 2)
	if len(parts) == 2 {
		return parts[1]
	}
	return full
}

func (m model) repoLabel(r *repository) string {
	duplicates := 0
	for i := range m.repos {
		if m.repos[i].Name == r.Name {
			duplicates++
		}
	}
	if duplicates > 1 {
		return r.FullName
	}
	return r.Name
}

func (m model) repoLabelByName(full string) string {
	if r := m.repoByName(full); r != nil {
		return m.repoLabel(r)
	}
	return shortName(full)
}

type runRef struct {
	repo  string
	short string
	item  run
}

func (m model) visibleRuns() []runRef {
	var out []runRef
	for _, r := range m.repos {
		if m.detail {
			selected := m.selectedRepo()
			if selected == nil || selected.FullName != r.FullName {
				continue
			}
		}
		for _, v := range r.Runs {
			if !isActive(v) && !(v.Status == "completed" && v.Conclusion == "failure" && time.Since(parseTime(v.UpdatedAt)) <= 24*time.Hour) {
				continue
			}
			out = append(out, runRef{r.FullName, m.repoLabel(&r), v})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		pi, pj := runPriority(out[i].item), runPriority(out[j].item)
		if pi != pj {
			return pi < pj
		}
		return out[i].item.UpdatedAt > out[j].item.UpdatedAt
	})
	return out
}
func (m model) selectedRun() *runRef {
	rows := m.visibleRuns()
	if len(rows) == 0 {
		return nil
	}
	r := rows[max(0, min(m.runCursor, len(rows)-1))]
	return &r
}
func repoPriority(r *repository) int {
	now := time.Now()
	if len(collectAttentionItems([]repository{*r}, now)) > 0 {
		return 0
	}
	if runningRuns(*r) > 0 {
		return 1
	}
	if queuedRuns(*r) > 0 {
		return 2
	}
	for _, pr := range r.PullRequests {
		if !pr.IsDraft && pr.ReviewDecision == "REVIEW_REQUIRED" && len(pr.RequestedReviewers) > 0 {
			return 3
		}
	}
	if r.PRs > 0 {
		return 4
	}
	if r.Issues > 0 {
		return 5
	}
	return 6
}
func latestRepoDate(r repository) time.Time {
	return parseTime(r.PushedAt)
}
func latestRun(r repository) *run {
	for i := range r.Runs {
		if r.Runs[i].Status != "completed" {
			return &r.Runs[i]
		}
	}
	if len(r.Runs) > 0 {
		return &r.Runs[0]
	}
	return nil
}
func runPriority(r run) int {
	if r.Status == "in_progress" {
		return 0
	}
	if queuedRun(r) {
		return 1
	}
	if r.Status == "completed" && r.Conclusion == "failure" {
		return 2
	}
	return 3
}
func isActive(r run) bool { return r.Status != "completed" && r.Status != "" }

func runningRuns(r repository) int {
	n := 0
	for _, item := range r.Runs {
		if item.Status == "in_progress" {
			n++
		}
	}
	return n
}
func runState(r run) string {
	if isActive(r) {
		return r.Status
	}
	if r.Conclusion != "" {
		return r.Conclusion
	}
	return r.Status
}
func shortAge(value string) string {
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return "—"
	}
	d := time.Since(t)
	if d < time.Minute {
		return "now"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

type commitActivity struct {
	Repo   string
	Branch string
	Commit commitInfo
}

func repoCommitActivity(repo repository, now time.Time) []commitActivity {
	var out []commitActivity
	seen := make(map[string]bool)
	add := func(item commitInfo, branchName string) {
		t := parseTime(item.Date)
		if t.IsZero() || t.After(now) || now.Sub(t) > activityHistoryDays*24*time.Hour {
			return
		}
		key := item.SHA
		if key == "" {
			key = item.Date + ":" + item.Msg + ":" + commitActor(item)
		}
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, commitActivity{Repo: repo.FullName, Branch: branchName, Commit: item})
	}
	for _, item := range repo.Commits {
		add(item, repo.Default)
	}
	for _, br := range repo.Branches {
		if len(br.History) == 0 {
			add(commitInfo{SHA: br.SHA, Date: br.Date, Msg: br.Msg}, br.Name)
			continue
		}
		for _, item := range br.History {
			add(item, br.Name)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Commit.Date > out[j].Commit.Date })
	return out
}

func commits(repos []repository, scope string) []commitActivity {
	var out []commitActivity
	now := time.Now()
	for _, repo := range repos {
		if scope != "" && repo.FullName != scope {
			continue
		}
		out = append(out, repoCommitActivity(repo, now)...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Commit.Date > out[j].Commit.Date })
	return out
}

var (
	ink             = lipgloss.Color("#e6edf7")
	muted           = lipgloss.Color("#8793a8")
	panelBorder     = lipgloss.Color("#26384f")
	cyan            = lipgloss.Color("#67e8f9")
	green           = lipgloss.Color("#4ade80")
	red             = lipgloss.Color("#fb7185")
	yellow          = lipgloss.Color("#fbbf24")
	purple          = lipgloss.Color("#c4b5fd")
	blue            = lipgloss.Color("#60a5fa")
	magenta         = lipgloss.Color("#f472b6")
	teal            = lipgloss.Color("#2dd4bf")
	selectedBG      = lipgloss.Color("#12364a")
	titleStyle      = lipgloss.NewStyle().Foreground(cyan).Bold(true)
	brandStyle      = lipgloss.NewStyle().Foreground(blue).Bold(true)
	mutedStyle      = lipgloss.NewStyle().Foreground(muted)
	goodStyle       = lipgloss.NewStyle().Foreground(green)
	badStyle        = lipgloss.NewStyle().Foreground(red).Bold(true)
	activeStyle     = lipgloss.NewStyle().Foreground(yellow).Bold(true)
	branchStyle     = lipgloss.NewStyle().Foreground(purple)
	numberStyle     = lipgloss.NewStyle().Foreground(blue)
	trendStyle      = lipgloss.NewStyle().Foreground(purple)
	peopleStyle     = lipgloss.NewStyle().Foreground(magenta)
	branchDataStyle = lipgloss.NewStyle().Foreground(teal)
	bodyStyle       = lipgloss.NewStyle().Foreground(ink)
	timeStyle       = lipgloss.NewStyle().Foreground(blue)
	actionStyle     = lipgloss.NewStyle().Foreground(teal).Bold(true)
	selectStyle     = lipgloss.NewStyle().Foreground(ink).Background(selectedBG).Bold(true)
)

func copyToClipboard(value string) error {
	var name string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		name = "pbcopy"
	case "windows":
		name = "clip"
	default:
		for _, candidate := range []struct {
			name string
			args []string
		}{{"wl-copy", nil}, {"xclip", []string{"-selection", "clipboard"}}, {"xsel", []string{"--clipboard", "--input"}}} {
			if path, err := exec.LookPath(candidate.name); err == nil {
				name, args = path, candidate.args
				break
			}
		}
	}
	if name == "" {
		return errors.New("no clipboard utility found (install wl-clipboard, xclip, or xsel)")
	}
	cmd := exec.Command(name, args...)
	cmd.Stdin = strings.NewReader(value)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s: %w", strings.TrimSpace(string(output)), err)
	}
	return nil
}

func openURL(raw string) error {
	parsed, err := url.ParseRequestURI(raw)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
		return fmt.Errorf("invalid browser URL")
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", raw)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", raw)
	default:
		cmd = exec.Command("xdg-open", raw)
	}
	return cmd.Start()
}

func width(s string) int { return lipgloss.Width(s) }

// selectedRow paints the full terminal row, including trailing blank cells.
func selectedRow(s string, w int) string {
	// Nested ANSI foreground styles can reset an outer background. Strip them
	// for the selected row so the selection background is continuous edge-to-edge.
	return selectStyle.Width(w).Render(fit(ansi.Strip(s), w))
}

func fit(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if width(s) <= n {
		return s + strings.Repeat(" ", n-width(s))
	}
	truncated := ansi.Truncate(s, n, "…")
	return truncated + strings.Repeat(" ", max(0, n-width(truncated)))
}
func box(title string, content []string, w, h int, active bool) string {
	if w < 8 || h < 3 {
		return strings.Join(content, "\n")
	}
	c := panelBorder
	if active {
		c = cyan
	}
	lineStyle := lipgloss.NewStyle().Foreground(c)
	inner := w - 2
	topTitle := ""
	if title != "" {
		topTitle = "─ " + title + " "
		if width(topTitle) > inner-1 {
			topTitle = fit(topTitle, inner-1)
		}
	}
	rows := []string{lineStyle.Render("┌" + topTitle + strings.Repeat("─", max(0, inner-width(topTitle))) + "┐")}
	for i := 0; i < h-2; i++ {
		s := ""
		if i < len(content) {
			s = content[i]
		}
		s = fit(s, inner)
		rows = append(rows, lineStyle.Render("│")+s+lineStyle.Render("│"))
	}
	rows = append(rows, lineStyle.Render("└"+strings.Repeat("─", inner)+"┘"))
	return strings.Join(rows, "\n")
}

func (m model) View() tea.View {
	view := tea.NewView(m.viewContent())
	view.AltScreen = true
	view.KeyboardEnhancements.ReportEventTypes = true
	view.KeyboardEnhancements.ReportAllKeysAsEscapeCodes = true
	view.KeyboardEnhancements.ReportAssociatedText = true
	return view
}

func (m model) viewContent() string {
	if m.width == 0 || m.height == 0 {
		return "Starting GitHub Fleet…"
	}
	if m.templateEditing {
		return m.copyTemplateView()
	}
	if m.width < 40 {
		return fit("Widen terminal to at least 40 columns.", m.width)
	}
	if m.showHelp {
		return m.helpView()
	}
	if m.logMode {
		return m.logView()
	}
	if m.agentWorkloadMode {
		return m.agentWorkloadView()
	}
	if m.height < 10 {
		return fit("Increase terminal height to at least 10 rows.", m.width)
	}
	switch m.focus {
	case 1:
		return m.repositoriesView()
	case 2:
		return m.ciQueueView()
	default:
		return m.overviewView()
	}
}

func (m model) copyTemplateView() string {
	lines := []string{
		"Paste your template into this terminal prompt.",
		"Use @@@ wherever the selected repository URL should appear.",
		"Multiple markers are replaced; without a marker, the URL is appended.",
		"",
		"Template content is hidden while editing.",
		"",
		"Ctrl+S  Save template",
		"Esc     Cancel without changing the saved template",
	}
	return box("PASTE REPOSITORY COPY TEMPLATE", lines, m.width-4, m.height-2, true)
}

func (m model) renderTabs(w int) string {
	labels := []string{"Overview", "Repositories", "CI queue"}
	var parts []string
	for i, label := range labels {
		label = strconv.Itoa(i+1) + " " + label
		if i == m.focus {
			parts = append(parts, selectStyle.Render(" "+label+" "))
		} else {
			parts = append(parts, mutedStyle.Render(" "+label+" "))
		}
	}
	line := strings.Join(parts, mutedStyle.Render("  │  "))
	return fit(line, w)
}

type attentionItem struct {
	Priority int
	Kind     string
	Repo     string
	Subject  string
	Detail   string
	Next     string
	Age      time.Duration
	URL      string
	RunID    int64
}

func collectAttentionItems(repos []repository, now time.Time) []attentionItem {
	var items []attentionItem
	for _, repo := range repos {
		for _, item := range repo.Runs {
			switch {
			case item.Status == "completed" && item.Conclusion == "failure" && item.UpdatedAt != "":
				age := ageSince(now, item.UpdatedAt)
				if age <= attentionFailureWindow {
					subject := item.Name
					if subject == "" {
						subject = item.Title
					}
					items = append(items, attentionItem{Priority: 0, Kind: "CI FAILED", Repo: repo.FullName, Subject: subject, Detail: "Workflow completed with a failure", Next: "Inspect failed run", Age: age, URL: item.URL, RunID: item.ID})
				}
			case queuedRun(item):
				started := item.CreatedAt
				if started == "" {
					started = item.UpdatedAt
				}
				age := ageSince(now, started)
				if started != "" && age >= queuedFollowUpAfter {
					subject := item.Name
					if subject == "" {
						subject = item.Title
					}
					items = append(items, attentionItem{Priority: 4, Kind: "CI QUEUED", Repo: repo.FullName, Subject: subject, Detail: "Workflow has remained queued", Next: "Check runner queue", Age: age, URL: item.URL, RunID: item.ID})
				}
			case item.Status == "in_progress":
				started := item.StartedAt
				if started == "" {
					started = item.CreatedAt
				}
				age := ageSince(now, started)
				if started != "" && age >= runningFollowUpAfter {
					subject := item.Name
					if subject == "" {
						subject = item.Title
					}
					items = append(items, attentionItem{Priority: 5, Kind: "CI RUNNING", Repo: repo.FullName, Subject: subject, Detail: "Long-running workflow; duration alone does not mean it is stuck", Next: "Inspect run if unexpected", Age: age, URL: item.URL, RunID: item.ID})
				}
			}
		}

		for _, pr := range repo.PullRequests {
			if pr.IsDraft {
				continue
			}
			age := ageSince(now, pr.UpdatedAt)
			subject := fmt.Sprintf("PR #%d · %s", pr.Number, pr.Title)
			switch {
			case pr.ReviewDecision == "CHANGES_REQUESTED":
				items = append(items, attentionItem{Priority: 1, Kind: "CHANGES REQUESTED", Repo: repo.FullName, Subject: subject, Detail: "GitHub reports requested changes", Next: "Review feedback", Age: age, URL: pr.URL})
			case pr.MergeStateStatus == "DIRTY":
				items = append(items, attentionItem{Priority: 2, Kind: "CONFLICTS", Repo: repo.FullName, Subject: subject, Detail: "GitHub reports a dirty merge state", Next: "Inspect branch conflicts", Age: age, URL: pr.URL})
			case pr.ReviewDecision == "REVIEW_REQUIRED" && len(pr.RequestedReviewers) == 0:
				items = append(items, attentionItem{Priority: 3, Kind: "REVIEW NEEDED", Repo: repo.FullName, Subject: subject, Detail: "Review is required and no reviewer is requested", Next: "Choose a reviewer", Age: age, URL: pr.URL})
			case pr.ReviewDecision == "REVIEW_REQUIRED" && len(pr.RequestedReviewers) > 0 && age >= reviewFollowUpAfter:
				items = append(items, attentionItem{Priority: 6, Kind: "REVIEW WAIT", Repo: repo.FullName, Subject: subject, Detail: "Requested review has not been updated for 72h", Next: "Follow up with reviewer", Age: age, URL: pr.URL})
			}
		}
		if repo.PRsTruncated {
			items = append(items, attentionItem{
				Priority: 7, Kind: "PR SAMPLE", Repo: repo.FullName,
				Subject: fmt.Sprintf("%d open PRs · latest 10 inspected", repo.PRs),
				Detail:  "Additional open PR review states were not loaded",
				Next:    "Inspect remaining PRs", URL: repo.URL,
			})
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Priority != items[j].Priority {
			return items[i].Priority < items[j].Priority
		}
		return items[i].Age > items[j].Age
	})
	return items
}

func ageSince(now time.Time, value string) time.Duration {
	t := parseTime(value)
	if t.IsZero() || t.After(now) {
		return 0
	}
	return now.Sub(t)
}

func ageLabel(age time.Duration) string {
	if age < time.Minute {
		return "now"
	}
	if age < time.Hour {
		return fmt.Sprintf("%dm", int(age.Minutes()))
	}
	if age < 24*time.Hour {
		return fmt.Sprintf("%dh", int(age.Hours()))
	}
	return fmt.Sprintf("%dd", int(age.Hours()/24))
}

func attentionStyle(kind string) lipgloss.Style {
	switch kind {
	case "CI FAILED", "CHANGES REQUESTED", "CONFLICTS", "AGENT FAILED":
		return badStyle
	case "AGENT BLOCKED", "AGENT STALE":
		return branchStyle
	case "REVIEW NEEDED", "CI QUEUED", "REVIEW WAIT", "CI RUNNING", "PR SAMPLE":
		return activeStyle
	default:
		return mutedStyle
	}
}

func (m model) attentionItems() []attentionItem {
	now := time.Now()
	items := collectAttentionItems(m.repos, now)
	if m.agentEventsPath != "" {
		items = append(items, collectAgentAttentionItems(m.agentTasks, now)...)
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Priority != items[j].Priority {
			return items[i].Priority < items[j].Priority
		}
		return items[i].Age > items[j].Age
	})
	return items
}

func (m model) selectedAttention() *attentionItem {
	items := m.attentionItems()
	if len(items) == 0 {
		return nil
	}
	item := items[max(0, min(m.attentionCursor, len(items)-1))]
	return &item
}

func (m model) renderOverviewSummary() string {
	items := m.attentionItems()
	var openPRs, running, queued int
	for _, repo := range m.repos {
		openPRs += repo.PRs
		running += runningRuns(repo)
		queued += queuedRuns(repo)
	}
	if m.width < 70 {
		return fit(fmt.Sprintf("ATT %d  ·  PR %d  ·  RUN %d  ·  QUEUED %d", len(items), openPRs, running, queued), m.width)
	}
	fleetLabel := fmt.Sprintf("REPOSITORIES %d", len(m.repos))
	if m.agentEventsPath != "" {
		switch {
		case m.agentLastErr != "":
			fleetLabel = "AGENTS ?"
		case m.busyAgentPoll || m.lastAgentPoll.IsZero():
			fleetLabel = "AGENTS …"
		default:
			activeAgents, _ := countAgentTasks(m.agentTasks, time.Now())
			fleetLabel = fmt.Sprintf("AGENTS %d", activeAgents)
		}
	}
	parts := []string{
		badStyle.Render(fmt.Sprintf("ATTENTION %d", len(items))),
		prCountStyle(openPRs).Render(fmt.Sprintf("OPEN PRs %d", openPRs)),
		activeStyle.Render(fmt.Sprintf("CI RUNNING %d", running)),
		branchStyle.Render(fmt.Sprintf("CI QUEUED %d", queued)),
		mutedStyle.Render(fleetLabel),
	}
	return fit(strings.Join(parts, mutedStyle.Render("   ·   ")), m.width)
}

func (m model) renderAttentionRows(w, h int) []string {
	repos := m.visibleRepos()
	if w < 88 {
		rows := make([]string, 0, h)
		if len(repos) == 0 {
			message := "Waiting for the first GitHub repository snapshot…"
			if len(m.repos) > 0 {
				message = "No repositories match the current filter."
			}
			return []string{mutedStyle.Render(fit(message, w))}
		}
		capacity := max(1, h/2)
		start := max(0, min(m.cursor-capacity/2, max(0, len(repos)-capacity)))
		for i := start; i < min(len(repos), start+capacity) && len(rows)+1 < h; i++ {
			repo := repos[i]
			now := time.Now()
			stale := repositoryIsStale(*repo, now)
			push := "NO PUSH DATA"
			pushStyle := mutedStyle
			if t := parseTime(repo.PushedAt); !t.IsZero() {
				age := now.Sub(t)
				push = ageLabel(age) + " ago"
				pushStyle = repositoryPushAgeStyle(age)
			}
			state := repoPortfolioState(*repo, now)
			ci := repoCI(*repo)
			nameStyle, labelStyle, stateStyle := repoNameStyle(repo, now), mutedStyle, repoStateStyle(state)
			ciTextStyle, prTextStyle, issueTextStyle := ciStyle(ci), prCountStyle(repo.PRs), issueCountStyle(repo.Issues)
			if stale {
				nameStyle, labelStyle, stateStyle = mutedStyle, mutedStyle, mutedStyle
				pushStyle, ciTextStyle, prTextStyle, issueTextStyle = mutedStyle, mutedStyle, mutedStyle, mutedStyle
			}
			first := nameStyle.Render(m.repoLabel(repo)) + labelStyle.Render("  ·  PUSH ") + pushStyle.Render(push) + labelStyle.Render("  ·  ") + stateStyle.Render(state)
			second := ciTextStyle.Render("CI "+ci) + labelStyle.Render("  ·  PR ") + prTextStyle.Render(strconv.Itoa(repo.PRs)) + labelStyle.Render("  ·  ISSUES ") + issueTextStyle.Render(strconv.Itoa(repo.Issues))
			first, second = fit(first, w), fit(second, w)
			if i == m.cursor && m.overviewPane == 0 {
				first, second = selectedRow(first, w), selectedRow(second, w)
			}
			rows = append(rows, first, second)
		}
		for len(rows) < h {
			rows = append(rows, "")
		}
		return rows[:min(len(rows), h)]
	}

	rows := []string{mutedStyle.Render(fit("REPOSITORY                 LAST PUSH       CI STATE          PR   ISSUES", w))}
	if len(repos) == 0 {
		message := "Waiting for the first GitHub repository snapshot…"
		if len(m.repos) > 0 {
			message = "No repositories match the current filter."
		}
		rows = append(rows, mutedStyle.Render(fit(message, w)))
	}
	room := max(0, h-1)
	start := max(0, min(m.cursor-max(0, room/2), max(0, len(repos)-room)))
	for i := start; i < min(len(repos), start+room); i++ {
		repo := repos[i]
		now := time.Now()
		stale := repositoryIsStale(*repo, now)
		push, pushStyle := "NO PUSH DATA", mutedStyle
		if t := parseTime(repo.PushedAt); !t.IsZero() {
			age := now.Sub(t)
			push = ageLabel(age) + " ago"
			pushStyle = repositoryPushAgeStyle(age)
		}
		ci := repoCI(*repo)
		nameW, pushW, ciW, countW := max(10, w/4), 12, 15, 5
		nameStyle, ciTextStyle := repoNameStyle(repo, now), ciStyle(ci)
		prTextStyle, issueTextStyle := prCountStyle(repo.PRs), issueCountStyle(repo.Issues)
		if stale {
			nameStyle, pushStyle, ciTextStyle = mutedStyle, mutedStyle, mutedStyle
			prTextStyle, issueTextStyle = mutedStyle, mutedStyle
		}
		line := nameStyle.Render(fit(m.repoLabel(repo), nameW)) + " " +
			pushStyle.Render(fit(push, pushW)) + " " + ciTextStyle.Render(fit(ci, ciW)) + " " +
			prTextStyle.Render(fit(strconv.Itoa(repo.PRs), countW)) + " " +
			issueTextStyle.Render(fit(strconv.Itoa(repo.Issues), countW))
		if i == m.cursor && m.overviewPane == 0 {
			line = selectedRow(line, w)
		}
		rows = append(rows, line)
	}
	for len(rows) < h {
		rows = append(rows, "")
	}
	return rows[:min(len(rows), h)]
}

func signalsFirst(signals []string) string {
	if len(signals) == 0 {
		return ""
	}
	return signals[0]
}

type inFlightItem struct {
	Priority int
	Kind     string
	Repo     string
	Subject  string
	Detail   string
	Actor    string
	Event    string
	Age      time.Duration
	URL      string
	RunID    int64
}

func collectInFlightItems(repos []repository, now time.Time, agentTaskSets ...map[string]agentTask) []inFlightItem {
	var items []inFlightItem
	for _, repo := range repos {
		for _, item := range repo.Runs {
			if !isActive(item) {
				continue
			}
			started := item.StartedAt
			if started == "" {
				started = item.CreatedAt
			}
			subject := item.Name
			if subject == "" {
				subject = item.Title
			}
			kind, priority := "RUNNING", 0
			if queuedRun(item) {
				kind, priority = "QUEUED", 1
				started = item.CreatedAt
				if started == "" {
					started = item.UpdatedAt
				}
			}
			items = append(items, inFlightItem{Priority: priority, Kind: kind, Repo: repo.FullName, Subject: subject, Detail: item.Branch, Actor: item.Actor.Login, Event: item.Event, Age: ageSince(now, started), URL: item.URL, RunID: item.ID})
		}
		for _, pr := range repo.PullRequests {
			if pr.IsDraft || pr.ReviewDecision != "REVIEW_REQUIRED" || len(pr.RequestedReviewers) == 0 {
				continue
			}
			reviewers := strings.Join(pr.RequestedReviewers, ", ")
			if pr.RequestedReviewersTruncated {
				reviewers += ", …"
			}
			items = append(items, inFlightItem{Priority: 2, Kind: "REVIEW", Repo: repo.FullName, Subject: fmt.Sprintf("PR #%d · %s", pr.Number, pr.Title), Detail: "Waiting on " + reviewers, Age: ageSince(now, pr.UpdatedAt), URL: pr.URL})
		}
	}
	if len(agentTaskSets) > 0 {
		items = append(items, collectAgentInFlight(agentTaskSets[0], now)...)
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Priority != items[j].Priority {
			return items[i].Priority < items[j].Priority
		}
		return items[i].Age > items[j].Age
	})
	return items
}

func (m model) renderInFlightRows(w, h int) []string {
	items := m.inFlightItems()
	if w <= 0 || h <= 0 {
		return nil
	}
	selected := max(0, min(m.inFlightCursor, len(items)-1))
	if len(items) == 0 {
		rows := []string{mutedStyle.Render(fit("No active workflows or explicitly requested reviews.", w))}
		for len(rows) < h {
			rows = append(rows, "")
		}
		return rows[:h]
	}
	rows := []string{mutedStyle.Render(fit("STATE       REPOSITORY / WORKFLOW                                      ELAPSED", w))}
	room := max(0, h-1)
	capacity := max(1, room/2)
	start := max(0, min(selected-capacity/2, max(0, len(items)-capacity)))
	for i := start; i < min(len(items), start+capacity); i++ {
		item := items[i]
		state := activeStyle
		if item.Kind == "REVIEW" || item.Kind == "QUEUED" || strings.Contains(item.Kind, "WAITING") || strings.Contains(item.Kind, "STALE") {
			state = branchStyle
		}
		if strings.Contains(item.Kind, "BLOCKED") {
			state = badStyle
		}
		primary := state.Render(fit(item.Kind, 12)) + " " +
			titleStyle.Render(fit(shortName(item.Repo), max(10, w/4))) + " " +
			bodyStyle.Render(fit(item.Subject, max(8, w/2))) + " " + timeStyle.Render(ageLabel(item.Age))
		metadata := branchDataStyle.Render("  BRANCH " + fallback(item.Detail, "—"))
		if item.Kind == "REVIEW" {
			metadata = branchDataStyle.Render("  " + fallback(item.Detail, "review requested"))
		} else if item.RunID != 0 || item.Actor != "" || item.Event != "" {
			metadata += mutedStyle.Render("  ·  EVENT ") + numberStyle.Render(fallback(item.Event, "—")) +
				mutedStyle.Render("  ·  TRIGGER ") + peopleStyle.Render(fallback(item.Actor, "unknown")) +
				mutedStyle.Render(fmt.Sprintf("  ·  RUN #%d", item.RunID))
		}
		primary, metadata = fit(primary, w), fit(metadata, w)
		if i == selected && m.overviewPane == 1 {
			primary, metadata = selectedRow(primary, w), selectedRow(metadata, w)
		}
		rows = append(rows, primary, metadata)
	}
	for len(rows) < h {
		rows = append(rows, "")
	}
	return rows[:min(len(rows), h)]
}

func (m model) overviewView() string {
	w, h := m.width, m.height
	header, tabs := m.renderHeader(w), m.renderTabs(w)
	summary, footer, ciChart := m.renderOverviewSummary(), m.renderFooter(w), m.renderCIHealthChart(w)
	if h >= 22 {
		bodyH := max(5, h-lipgloss.Height(header)-5)
		if w >= 108 {
			leftW := int(float64(w) * 0.61)
			rightW := w - leftW - 1
			left := box("REPOSITORY HEALTH · PUSH AGE + CI", m.renderAttentionRows(leftW-2, bodyH-2), leftW, bodyH, m.overviewPane == 0)
			right := box("IN FLIGHT / WAITING", m.renderInFlightRows(rightW-2, bodyH-2), rightW, bodyH, m.overviewPane == 1)
			return lipgloss.JoinVertical(lipgloss.Left, header, tabs, summary, ciChart, " ", lipgloss.JoinHorizontal(lipgloss.Top, left, " ", right), footer)
		}
		attentionH := max(3, h-lipgloss.Height(header)-11)
		attention := box("REPOSITORY HEALTH · PUSH AGE + CI", m.renderAttentionRows(w-2, attentionH-2), w, attentionH, m.overviewPane == 0)
		waiting := box("IN FLIGHT / WAITING", m.renderInFlightRows(w-2, 3), w, 5, m.overviewPane == 1)
		return lipgloss.JoinVertical(lipgloss.Left, header, tabs, summary, ciChart, " ", attention, waiting, footer)
	}
	bodyH := max(3, h-7)
	attention := box("REPOSITORY HEALTH · PUSH AGE + CI", m.renderAttentionRows(w-2, bodyH-2), w, bodyH, m.overviewPane == 0)
	return lipgloss.JoinVertical(lipgloss.Left, header, tabs, summary, ciChart, " ", attention, footer)
}

func repoGroupRank(repo repository, now time.Time) int {
	switch {
	case runningRuns(repo) > 0:
		return 0
	case queuedRuns(repo) > 0:
		return 1
	}
	pushed := parseTime(repo.PushedAt)
	if pushed.IsZero() {
		return 6
	}
	age := now.Sub(pushed)
	switch {
	case age <= 24*time.Hour:
		return 2
	case age <= 7*24*time.Hour:
		return 3
	case age <= 30*24*time.Hour:
		return 4
	default:
		return 5
	}
}

func repoGroupLabel(rank int) string {
	switch rank {
	case 0:
		return "CI RUNNING"
	case 1:
		return "CI QUEUED"
	case 2:
		return "PUSHED IN THE LAST 24H"
	case 3:
		return "ACTIVE THIS WEEK"
	case 4:
		return "QUIET · 8–30 DAYS"
	case 5:
		return "DORMANT · OVER 30 DAYS"
	default:
		return "NO PUSH DATA"
	}
}

func repoPortfolioState(repo repository, now time.Time) string {
	if len(collectAttentionItems([]repository{repo}, now)) > 0 {
		return "ATTENTION"
	}
	switch repoGroupRank(repo, now) {
	case 0:
		return "CI RUNNING"
	case 1:
		return "CI QUEUED"
	case 2:
		return "ACTIVE <24H"
	case 3:
		return "ACTIVE <7D"
	case 4:
		return "QUIET 8–30D"
	case 5:
		return "DORMANT >30D"
	default:
		return "NO PUSH DATA"
	}
}

func (m model) renderPortfolioRepos(w, h int) []string {
	repos := m.visibleRepos()
	rows := []string{mutedStyle.Render(fit("REPOSITORY                 PUSH AGE       DELIVERY STATE      CI          PRs  ISSUES", w))}
	if len(repos) == 0 {
		message := "No matching repositories. Press / to filter or r to refresh."
		if len(m.repos) == 0 {
			message = "Waiting for GitHub repository snapshot…"
		}
		rows = append(rows, mutedStyle.Render(fit(message, w)))
		return rows[:min(len(rows), h)]
	}
	now := time.Now()
	if w < 70 {
		lastGroup := -1
		selectedLine := 1
		for i, repo := range repos {
			group := repoGroupRank(*repo, now)
			if group != lastGroup {
				count := 0
				for _, candidate := range repos {
					if repoGroupRank(*candidate, now) == group {
						count++
					}
				}
				rows = append(rows, groupStyle(group).Render(fit(fmt.Sprintf("▸ %s · %d", repoGroupLabel(group), count), w)))
				lastGroup = group
			}
			push := "NO PUSH DATA"
			if !parseTime(repo.PushedAt).IsZero() {
				push = shortAge(repo.PushedAt) + " ago"
			}
			state, ci := repoPortfolioState(*repo, now), repoCI(*repo)
			first := titleStyle.Render(m.repoLabel(repo)) + "  " + repoStateStyle(state).Render(state)
			second := timeStyle.Render("PUSH "+push) + mutedStyle.Render("  ·  ") + ciStyle(ci).Render("CI "+ci) +
				mutedStyle.Render("  ·  PR ") + prCountStyle(repo.PRs).Render(strconv.Itoa(repo.PRs)) +
				mutedStyle.Render(" · ISSUES ") + issueCountStyle(repo.Issues).Render(strconv.Itoa(repo.Issues))
			if i == m.cursor {
				first, second = selectedRow(fit(first, w), w), selectedRow(fit(second, w), w)
				selectedLine = len(rows)
				if len(rows) > 0 && strings.Contains(rows[len(rows)-1], "▸ ") {
					selectedLine = len(rows) - 1
				}
			}
			rows = append(rows, fit(first, w), fit(second, w))
		}
		start := max(0, min(selectedLine-max(0, (h-2)/2), max(0, len(rows)-h)))
		visible := rows[start:min(len(rows), start+h)]
		for len(visible) < h {
			visible = append(visible, "")
		}
		return visible[:min(len(visible), h)]
	}
	nameW := max(12, min(28, w/3))
	pushW, stateW, ciW, prW, issueW := 10, 15, 12, 4, 6
	lastGroup := -1
	for i, repo := range repos {
		group := repoGroupRank(*repo, now)
		if group != lastGroup {
			count := 0
			for _, candidate := range repos {
				if repoGroupRank(*candidate, now) == group {
					count++
				}
			}
			rows = append(rows, groupStyle(group).Render(fit(fmt.Sprintf("▸ %s  ·  %d repositories", repoGroupLabel(group), count), w)))
			lastGroup = group
		}
		push := "—"
		if !parseTime(repo.PushedAt).IsZero() {
			push = shortAge(repo.PushedAt)
		}
		state, ci := repoPortfolioState(*repo, now), repoCI(*repo)
		line := titleStyle.Render(fit(m.repoLabel(repo), nameW)) + " " +
			timeStyle.Render(fit(push, pushW)) + " " +
			repoStateStyle(state).Render(fit(state, stateW)) + " " +
			ciStyle(ci).Render(fit(ci, ciW)) + " " +
			prCountStyle(repo.PRs).Render(fit(strconv.Itoa(repo.PRs), prW)) + " " +
			issueCountStyle(repo.Issues).Render(fit(strconv.Itoa(repo.Issues), issueW))
		if i == m.cursor {
			line = selectedRow(line, w)
		}
		rows = append(rows, line)
	}
	// Center the selected repository without splitting its group heading from its row.
	selectedLine := 1
	repoIndex := 0
	for index, line := range rows {
		if index == 0 || strings.Contains(line, "▸ ") {
			continue
		}
		if repoIndex == m.cursor {
			selectedLine = index
			if index > 0 && strings.Contains(rows[index-1], "▸ ") {
				selectedLine = index - 1
			}
			break
		}
		repoIndex++
	}
	start := max(0, min(selectedLine-max(0, (h-2)/2), max(0, len(rows)-h)))
	visible := rows[start:min(len(rows), start+h)]
	for len(visible) < h {
		visible = append(visible, "")
	}
	return visible[:min(len(visible), h)]
}

func prCountStyle(n int) lipgloss.Style {
	switch {
	case n == 0:
		return badStyle
	case n <= 2:
		return activeStyle
	case n <= 5:
		return branchStyle
	default:
		return lipgloss.NewStyle().Foreground(magenta).Bold(true)
	}
}

func issueCountStyle(n int) lipgloss.Style {
	switch {
	case n == 0:
		return goodStyle
	case n <= 2:
		return branchStyle
	case n <= 5:
		return activeStyle
	default:
		return badStyle
	}
}

func groupStyle(group int) lipgloss.Style {
	switch group {
	case 0:
		return activeStyle
	case 1:
		return branchStyle
	case 2:
		return goodStyle
	case 3:
		return titleStyle
	case 4:
		return branchStyle
	case 5:
		return badStyle
	default:
		return mutedStyle
	}
}

func (m model) repositoriesView() string {
	w, h := m.width, m.height
	header, tabs := m.renderHeader(w), m.renderTabs(w)
	if m.detail {
		return m.repoDetailView()
	}
	bodyH := max(5, h-6)
	body := box("REPOSITORY PORTFOLIO · GROUPED BY CI + PUSH RECENCY", m.renderPortfolioRepos(w-2, bodyH-2), w, bodyH, true)
	return lipgloss.JoinVertical(lipgloss.Left, header, tabs, body, m.renderFooter(w))
}

func pullRequestReviewText(pr pullRequestSummary) string {
	switch {
	case pr.IsDraft:
		return "DRAFT"
	case pr.MergeStateStatus == "DIRTY":
		return "CONFLICTS"
	case pr.ReviewDecision == "CHANGES_REQUESTED":
		return "CHANGES REQUESTED"
	case pr.ReviewDecision == "REVIEW_REQUIRED" && len(pr.RequestedReviewers) == 0:
		return "REVIEWER NOT REQUESTED"
	case pr.ReviewDecision == "REVIEW_REQUIRED":
		label := "AWAITING " + strings.Join(pr.RequestedReviewers, ", ")
		if pr.RequestedReviewersTruncated {
			label += ", …"
		}
		return label
	case pr.ReviewDecision == "APPROVED":
		return "APPROVED"
	default:
		return "REVIEW STATUS UNKNOWN"
	}
}

func prReviewStyle(pr pullRequestSummary) lipgloss.Style {
	switch {
	case pr.MergeStateStatus == "DIRTY", pr.ReviewDecision == "CHANGES_REQUESTED":
		return badStyle
	case pr.ReviewDecision == "APPROVED":
		return goodStyle
	case pr.IsDraft:
		return mutedStyle
	default:
		return activeStyle
	}
}

func (m model) repoDetailView() string {
	w, h := m.width, m.height
	header, tabs := m.renderHeader(w), m.renderTabs(w)
	repo := m.selectedRepo()
	if repo == nil {
		return lipgloss.JoinVertical(lipgloss.Left, header, tabs, mutedStyle.Render("No repository selected."), m.renderFooter(w))
	}
	panelHeight := max(6, h-5)
	innerRows := max(1, panelHeight-2)
	prCapacity := max(1, innerRows-9)
	prStart := max(0, min(m.prCursor-prCapacity/2, max(0, len(repo.PullRequests)-prCapacity)))
	prEnd := min(len(repo.PullRequests), prStart+prCapacity)
	content := []string{
		titleStyle.Render(repo.FullName) + mutedStyle.Render("  ·  default branch ") + branchDataStyle.Render(fallback(repo.Default, "unknown")),
		titleStyle.Render("OPEN PRs ") + prCountStyle(repo.PRs).Render(strconv.Itoa(repo.PRs)) +
			mutedStyle.Render("  ·  OPEN ISSUES ") + issueCountStyle(repo.Issues).Render(strconv.Itoa(repo.Issues)) +
			mutedStyle.Render("  ·  BRANCHES ") + branchDataStyle.Render(strconv.Itoa(repo.BranchN)) +
			mutedStyle.Render("  ·  LAST PUSH ") + timeStyle.Render(fallback(shortAge(repo.PushedAt), "—")),
		"",
		titleStyle.Render("OPEN PULL REQUESTS  ·  ↑↓ SELECT  ·  ENTER / o OPEN"),
	}
	if len(repo.PullRequests) == 0 {
		content = append(content, mutedStyle.Render("No open pull requests in the latest snapshot."))
	}
	for i := prStart; i < prEnd; i++ {
		pr := repo.PullRequests[i]
		line := numberStyle.Render(fmt.Sprintf("#%-5d", pr.Number)) + " " +
			bodyStyle.Render(pr.Title) + "  " +
			prReviewStyle(pr).Render(pullRequestReviewText(pr)) + "  " +
			timeStyle.Render(shortAge(pr.UpdatedAt)+" ago")
		if i == m.prCursor {
			content = append(content, selectStyle.Width(w-2).Render(fit(line, w-2)))
		} else {
			content = append(content, fit(line, w-2))
		}
	}
	if repo.PRsTruncated {
		content = append(content, mutedStyle.Render("Showing the 10 most recently updated PRs; more are available on GitHub."))
	}
	content = append(content, "", titleStyle.Render("RECENT CI"))
	ciShown := 0
	for _, item := range repo.Runs {
		if isActive(item) || (item.Status == "completed" && item.Conclusion == "failure" && ageSince(time.Now(), item.UpdatedAt) <= 24*time.Hour) {
			label := item.Name
			if label == "" {
				label = item.Title
			}
			content = append(content, fit(stateStyle(item).Render(stateText(item))+"  "+bodyStyle.Render(label)+"  "+branchDataStyle.Render(fallback(item.Branch, "branch unknown"))+"  "+timeStyle.Render(shortAge(item.UpdatedAt)+" ago"), w-2))
			ciShown++
			if ciShown >= 2 || len(content) >= innerRows {
				break
			}
		}
	}
	if ciShown == 0 && len(content) < innerRows {
		content = append(content, mutedStyle.Render("No active or recent failed workflows."))
	}
	if len(content) > innerRows {
		content = content[:innerRows]
	}
	panel := box("REPOSITORY DETAIL", content, w, panelHeight, true)
	return lipgloss.JoinVertical(lipgloss.Left, header, tabs, panel, m.renderFooter(w))
}

func (m model) renderCIHealthChart(w int) string {
	var passed, failed, running, waiting int
	for _, repo := range m.repos {
		for _, item := range repo.Runs {
			switch runState(item) {
			case "success":
				passed++
			case "failure", "startup_failure", "action_required":
				failed++
			case "in_progress":
				running++
			case "queued", "waiting", "requested", "pending":
				waiting++
			}
		}
	}
	total := passed + failed + running + waiting
	if total == 0 {
		return mutedStyle.Render(fit("CI SIGNAL // waiting for workflow samples", w))
	}
	counts := []int{passed, failed, running, waiting}
	styles := []lipgloss.Style{goodStyle, badStyle, activeStyle, branchStyle}
	if w < 70 {
		label := fmt.Sprintf("CI P%d F%d R%d W%d ", passed, failed, running, waiting)
		barWidth := max(4, min(12, w-width(label)-1))
		var bar strings.Builder
		for i, count := range counts {
			segments := 0
			if count > 0 {
				segments = max(1, count*barWidth/total)
			}
			bar.WriteString(styles[i].Render(strings.Repeat("█", segments)))
		}
		return fit(label+bar.String(), w)
	}
	legend := goodStyle.Render(fmt.Sprintf(" PASS %d ", passed)) +
		badStyle.Render(fmt.Sprintf(" FAIL %d ", failed)) +
		activeStyle.Render(fmt.Sprintf(" RUN %d ", running)) +
		branchStyle.Render(fmt.Sprintf(" WAIT %d ", waiting))
	prefix := mutedStyle.Render("CI SIGNAL // loaded sample  ")
	barWidth := max(4, min(36, w-width(prefix)-width(legend)-2))
	var bar strings.Builder
	for i, count := range counts {
		segments := 0
		if count > 0 {
			segments = max(1, count*barWidth/total)
		}
		bar.WriteString(styles[i].Render(strings.Repeat("█", segments)))
	}
	return fit(prefix+bar.String()+"  "+legend, w)
}

func (m model) ciQueueView() string {
	w, h := m.width, m.height
	header, tabs := m.renderHeader(w), m.renderTabs(w)
	bodyH := max(5, h-7)
	body := box(fmt.Sprintf("CI QUEUE · %d RUNNING · %d QUEUED", m.runningCI(), m.queuedCI()), m.renderActions(w-2, bodyH-2), w, bodyH, true)
	return lipgloss.JoinVertical(lipgloss.Left, header, tabs, m.renderCIHealthChart(w), body, m.renderFooter(w))
}

func fallback(value, otherwise string) string {
	if value == "" {
		return otherwise
	}
	return value
}

func (m model) renderHeader(w int) string {
	state := "SYNCING"
	if m.busyRepos && len(m.repos) > 0 {
		state = "CACHE · REFRESHING"
	} else if m.busyRepos {
		state = "FETCHING ACCOUNT"
	} else if m.lastErr != "" {
		state = "STALE · CACHED DATA"
	} else if !m.lastRepos.IsZero() {
		state = "CONNECTED · " + shortAge(m.lastRepos.Format(time.RFC3339)) + " ago"
	}
	spinnerFrames := []string{"◐", "◓", "◑", "◒"}
	pulseFrames := []string{"●", "◉", "●", "◉", "●", "◉", "●", "◉"}
	spinnerColors := []lipgloss.Color{cyan, blue, purple, teal}
	indicator := goodStyle.Render("●")
	busy := m.busyRepos || m.inFlightRuns > 0 || m.busyCommitHistory || m.busyBranchHistory || m.logBusy || m.busyAgentPoll
	if m.animationEnabled {
		frame := m.animationFrame % len(spinnerColors)
		glyph := pulseFrames[m.animationFrame%len(pulseFrames)]
		if busy {
			glyph = spinnerFrames[frame]
		}
		indicator = lipgloss.NewStyle().Foreground(spinnerColors[frame]).Bold(true).Render(glyph)
	}
	status := goodStyle.Render(indicator + " " + state)
	if m.lastErr != "" {
		status = badStyle.Render("● " + state)
	}
	agentStyle := mutedStyle
	if m.agentLastErr != "" {
		agentStyle = badStyle
	}
	agentLine := ""
	if m.agentEventsPath != "" {
		agentLine = "\n" + fit(agentStyle.Render(m.agentRuntimeNotice()), w)
	}
	if w < 70 {
		smallState := "● SYNCING"
		if m.animationEnabled {
			smallState = spinnerFrames[m.animationFrame] + " SYNCING"
		}
		smallStyle := lipgloss.NewStyle().Foreground(spinnerColors[m.animationFrame]).Bold(true)
		if !m.lastRepos.IsZero() {
			smallState = "● CONNECTED"
			smallStyle = goodStyle
			if m.animationEnabled {
				smallState = pulseFrames[m.animationFrame%len(pulseFrames)] + " CONNECTED"
				smallStyle = lipgloss.NewStyle().Foreground(spinnerColors[m.animationFrame%len(spinnerColors)]).Bold(true)
			}
		}
		if m.lastErr != "" {
			smallState = "● STALE"
			smallStyle = badStyle
		}
		return fit(titleStyle.Render("GH FLEET")+"  "+smallStyle.Render(smallState), w) + agentLine
	}
	brand := brandStyle.Render("GH FLEET") + mutedStyle.Render("  /  ") + titleStyle.Render("DELIVERY OPERATIONS")
	login := mutedStyle.Render("  " + m.login)
	left := fit(brand+login, max(1, w-width(status)-1))
	gap := max(1, w-width(left)-width(status))
	return left + strings.Repeat(" ", gap) + status + agentLine
}

func metric(label, value, detail string, color lipgloss.Color, w int) string {
	cw := max(14, w-2)
	top := lipgloss.NewStyle().Foreground(color).Bold(true).Render(fit(value, cw))
	mid := lipgloss.NewStyle().Foreground(ink).Bold(true).Render(fit(label, cw))
	bot := mutedStyle.Render(fit(detail, cw))
	return box("", []string{top, mid, bot}, w, 5, false)
}
func (m model) renderMetrics(w int) string {
	var active, running, waiting, failed, commits30d int
	contributors := map[string]bool{}
	partialActivity := false
	now := time.Now()
	for _, repo := range m.repos {
		if m.detail {
			selected := m.selectedRepo()
			if selected == nil || selected.FullName != repo.FullName {
				continue
			}
		}
		work := repoWorkload(repo, now)
		commits30d += work.Commits30d
		partialActivity = partialActivity || work.ActivitySampled
		for _, actor := range work.Actors {
			contributors[actor] = true
		}
		if work.Commits7d > 0 || runningRuns(repo) > 0 || queuedRuns(repo) > 0 {
			active++
		}
		for _, item := range repo.Runs {
			if item.Status == "in_progress" {
				running++
			}
			if queuedRun(item) {
				waiting++
			}
			if item.Status == "completed" && item.Conclusion == "failure" && now.Sub(parseTime(item.UpdatedAt)) <= 24*time.Hour {
				failed++
			}
		}
	}
	repoCount := len(m.repos)
	if m.detail {
		repoCount = 0
		if m.selectedRepo() != nil {
			repoCount = 1
		}
	}
	cw := (w - 4) / 5
	ciColor := green
	if failed > 0 {
		ciColor = red
	} else if running > 0 || waiting > 0 {
		ciColor = yellow
	}
	coverage := 0
	for _, repo := range m.repos {
		if repo.CommitHistoryDays >= activityHistoryDays && repo.BranchHistoryDays >= activityHistoryDays {
			coverage++
		}
	}
	if m.detail {
		if selected := m.selectedRepo(); selected != nil {
			coverage = 0
			if selected.CommitHistoryDays >= activityHistoryDays && selected.BranchHistoryDays >= activityHistoryDays {
				coverage = 1
			}
		}
	}
	activityPrefix := ""
	if partialActivity {
		activityPrefix = "~"
	}
	items := []string{
		metric("REPOSITORIES", strconv.Itoa(repoCount), "monitored", cyan, cw),
		metric("ACTIVE REPOS", strconv.Itoa(active), "activity or CI · 7d", purple, cw),
		metric("COMMITS · 30D", activityPrefix+countFmt(commits30d), fmt.Sprintf("%d/%d repos covered", coverage, repoCount), blue, cw),
		metric("CONTRIBUTORS", activityPrefix+countFmt(len(contributors)), "unique commit authors · 30d", cyan, cw),
		metric("CI · RUN / WAIT", fmt.Sprintf("%d / %d", running, waiting), fmt.Sprintf("%d failures · 24h", failed), ciColor, cw),
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, joinWithSpaces(items, 1)...)
}
func joinWithSpaces(items []string, gap int) []string {
	out := make([]string, 0, len(items)*2-1)
	for i, s := range items {
		if i > 0 {
			out = append(out, strings.Repeat(" ", gap))
		}
		out = append(out, s)
	}
	return out
}
func parseTime(s string) time.Time { t, _ := time.Parse(time.RFC3339, s); return t }

func (m model) repoTitle() string {
	if m.filtering {
		return "SEARCH REPOSITORIES · " + m.filter
	}
	if m.detail {
		if r := m.selectedRepo(); r != nil {
			return "REPOSITORY · " + m.repoLabel(r)
		}
	}
	if m.filter != "" {
		return "REPOSITORIES · filter: " + m.filter
	}
	return fmt.Sprintf("REPOSITORY WORKLOAD · %d", len(m.repos))
}
func (m model) activityTitle() string {
	if m.detail {
		if r := m.selectedRepo(); r != nil {
			return "RECENT COMMITS · " + m.repoLabel(r) + " · 30D"
		}
	}
	return "RECENT REPOSITORY ACTIVITY · LAST 30 DAYS"
}
func (m model) actionsTitle() string {
	if m.detail {
		if r := m.selectedRepo(); r != nil {
			return "ACTIONS · " + m.repoLabel(r)
		}
	}
	return fmt.Sprintf("CI QUEUE · %d RUNNING · %d WAITING", m.runningCI(), m.queuedCI())
}
func (m model) activeRuns() int {
	n := 0
	for _, r := range m.repos {
		for _, x := range r.Runs {
			if isActive(x) {
				n++
			}
		}
	}
	return n
}
func queuedRun(r run) bool {
	switch r.Status {
	case "queued", "waiting", "requested", "pending":
		return true
	default:
		return false
	}
}
func (m model) runningCI() int {
	n := 0
	for _, repo := range m.repos {
		for _, item := range repo.Runs {
			if item.Status == "in_progress" {
				n++
			}
		}
	}
	return n
}
func (m model) queuedCI() int {
	n := 0
	for _, repo := range m.repos {
		for _, item := range repo.Runs {
			if queuedRun(item) {
				n++
			}
		}
	}
	return n
}
func countFmt(n int) string {
	if n > 999 {
		return "999+"
	}
	return strconv.Itoa(n)
}
func (m model) renderRepos(w, h int) []string {
	rows := m.visibleRepos()
	content := make([]string, 0, h)
	nameW := max(10, w/5)
	workW, trendW, peopleW, branchW, ciW, prW := 14, 7, 5, 3, 10, 3
	header := fmt.Sprintf("%-*s %*s %*s %*s %*s %*s %*s", nameW, "REPOSITORY", workW, "24H/7D/30D", trendW, "TREND", peopleW, "PPL", branchW, "BR", ciW, "CI", prW, "PR")
	content = append(content, mutedStyle.Render(fit(header, w)))
	room := max(0, h-1)
	if len(rows) == 0 {
		message := "No matching repositories"
		if len(m.repos) == 0 {
			message = "Waiting for GitHub repository snapshot…"
		}
		content = append(content, mutedStyle.Render(message))
	}
	start := max(0, min(m.cursor-room/2, max(0, len(rows)-room)))
	for i := start; i < min(len(rows), start+room); i++ {
		r := rows[i]
		work := repoWorkload(*r, time.Now())
		approx := ""
		if work.ActivitySampled {
			approx = "~"
		}
		ci := repoCI(*r)
		line := titleStyle.Render(fit(m.repoLabel(r), nameW)) + " " +
			numberStyle.Render(fit(fmt.Sprintf("%s%d/%d/%d", approx, work.Commits24h, work.Commits7d, work.Commits30d), workW)) + " " +
			trendStyle.Render(fit(sparkline(work.Daily), trendW)) + " " +
			peopleStyle.Render(fit(approx+countFmt(len(work.Actors)), peopleW)) + " " +
			branchDataStyle.Render(fit(countFmt(work.ActiveBranches7d), branchW)) + " " +
			ciStyle(ci).Render(fit(ci, ciW)) + " " +
			activeStyle.Render(fit(countFmt(r.PRs), prW))
		if i == m.cursor {
			line = selectedRow(line, w)
		}
		content = append(content, fit(line, w))
	}
	for len(content) < h {
		content = append(content, "")
	}
	return content
}
func ciStyle(state string) lipgloss.Style {
	switch {
	case strings.Contains(state, "RUN"):
		return activeStyle
	case strings.Contains(state, "WAIT"):
		return branchStyle
	case strings.Contains(state, "FAIL"):
		return badStyle
	case strings.Contains(state, "OK"):
		return goodStyle
	default:
		return mutedStyle
	}
}

func repoStateStyle(state string) lipgloss.Style {
	switch {
	case state == "ATTENTION" || strings.HasPrefix(state, "DORMANT"):
		return badStyle
	case state == "CI RUNNING" || strings.HasPrefix(state, "ACTIVE"):
		return activeStyle
	case state == "CI QUEUED" || strings.HasPrefix(state, "QUIET") || state == "IN REVIEW":
		return branchStyle
	case state == "NO PUSH DATA":
		return mutedStyle
	default:
		return mutedStyle
	}
}
func repoCI(repo repository) string {
	running, waiting := 0, 0
	for _, item := range repo.Runs {
		if item.Status == "in_progress" {
			running++
		}
		if queuedRun(item) {
			waiting++
		}
	}
	switch {
	case running > 0 && waiting > 0:
		return fmt.Sprintf("%d RUN · %d WAIT", running, waiting)
	case running > 0:
		return fmt.Sprintf("%d RUNNING", running)
	case waiting > 0:
		return fmt.Sprintf("%d WAITING", waiting)
	}
	if latest := latestRun(repo); latest != nil {
		switch latest.Conclusion {
		case "success":
			return "✓ OK"
		case "failure":
			if time.Since(parseTime(latest.UpdatedAt)) <= 24*time.Hour {
				return "FAILED"
			}
			return "LAST FAIL"
		case "cancelled":
			return "CANCELLED"
		}
	}
	if len(repo.Runs) == 0 {
		return "NO RECENT CI"
	}
	return "NO ACTIVE CI"
}

type repoWork struct {
	Commits24h, Commits7d, Commits30d int
	ActiveBranches7d                  int
	Daily                             []int
	Actors                            []string
	LastCommit                        time.Time
	ActivitySampled                   bool
}

func commitActor(item commitInfo) string {
	if item.AuthorLogin != "" {
		return item.AuthorLogin
	}
	return item.Author
}

func repoWorkload(repo repository, now time.Time) repoWork {
	out := repoWork{Daily: make([]int, 7), ActivitySampled: repo.CommitHistoryDays < activityHistoryDays || repo.BranchHistoryDays < activityHistoryDays}
	actors := map[string]bool{}
	activeBranches := map[string]bool{}
	for _, br := range repo.Branches {
		if br.HistoryTruncated {
			out.ActivitySampled = true
		}
	}
	for _, activity := range repoCommitActivity(repo, now) {
		item := activity.Commit
		t := parseTime(item.Date)
		out.Commits30d++
		age := now.Sub(t)
		if age <= 7*24*time.Hour {
			out.Commits7d++
			activeBranches[activity.Branch] = true
		}
		if age < 24*time.Hour {
			out.Commits24h++
		}
		day := int(now.UTC().Truncate(24*time.Hour).Sub(t.UTC().Truncate(24*time.Hour)) / (24 * time.Hour))
		if day >= 0 && day < 7 {
			out.Daily[6-day]++
		}
		if t.After(out.LastCommit) {
			out.LastCommit = t
		}
		if actor := commitActor(item); actor != "" {
			actors[actor] = true
		}
	}
	out.ActiveBranches7d = len(activeBranches)
	for actor := range actors {
		out.Actors = append(out.Actors, actor)
	}
	sort.Strings(out.Actors)
	return out
}
func queuedRuns(repo repository) int {
	n := 0
	for _, item := range repo.Runs {
		if queuedRun(item) {
			n++
		}
	}
	return n
}

func commitCounts(repos []repository, now time.Time) ([]int, []int) {
	hourly, daily := make([]int, 24), make([]int, 7)
	seen := make(map[string]bool)
	for _, repo := range repos {
		for _, activity := range repoCommitActivity(repo, now) {
			item := activity.Commit
			t, err := time.Parse(time.RFC3339, item.Date)
			if err != nil || t.After(now) || now.Sub(t) >= 7*24*time.Hour {
				continue
			}
			key := repo.FullName + ":" + item.SHA
			if item.SHA == "" {
				key = repo.FullName + ":" + item.Date + ":" + item.Msg
			}
			if seen[key] {
				continue
			}
			seen[key] = true
			day := int(now.Truncate(24*time.Hour).Sub(t.Truncate(24*time.Hour)) / (24 * time.Hour))
			if day >= 0 && day < 7 {
				daily[6-day]++
			}
			if now.Sub(t) < 24*time.Hour {
				hour := int(now.Truncate(time.Hour).Sub(t.Truncate(time.Hour)) / time.Hour)
				if hour >= 0 && hour < 24 {
					hourly[23-hour]++
				}
			}
		}
	}
	return hourly, daily
}

func sparkline(values []int) string {
	levels := []rune("▁▂▃▄▅▆▇█")
	maxValue := 0
	for _, value := range values {
		if value > maxValue {
			maxValue = value
		}
	}
	if maxValue == 0 {
		return strings.Repeat("·", len(values))
	}
	var out strings.Builder
	for _, value := range values {
		if value == 0 {
			out.WriteRune('·')
			continue
		}
		level := value * (len(levels) - 1) / maxValue
		if level == 0 {
			level = 1
		}
		out.WriteRune(levels[level])
	}
	return out.String()
}

func sumInts(values []int) int {
	total := 0
	for _, value := range values {
		total += value
	}
	return total
}

func scaledBarUnits(values []int, height, scaleMax int) []int {
	out := make([]int, len(values))
	maximum := max(1, scaleMax)
	if height <= 0 {
		return out
	}
	for i, value := range values {
		if value <= 0 {
			continue
		}
		units := value * height * 8 / maximum
		out[i] = max(1, units)
	}
	return out
}

func renderBarRows(values []int, chartWidth, chartHeight, period, scaleMax int, now time.Time) []string {
	if len(values) == 0 || chartWidth < len(values) || chartHeight < 1 {
		return []string{mutedStyle.Render("No activity in this range")}
	}
	heights := scaledBarUnits(values, chartHeight, scaleMax)
	gap := 1
	if chartWidth < len(values)*2-1 {
		gap = 0
	}
	slot := max(1, (chartWidth-gap*(len(values)-1))/len(values))
	rows := make([]string, 0, chartHeight+2)
	partialBlocks := []rune("▏▎▍▌▋▊▉")
	for level := chartHeight; level >= 1; level-- {
		barColor := blue
		if level == chartHeight {
			barColor = purple
		} else if level == chartHeight-1 {
			barColor = cyan
		}
		barStyle := lipgloss.NewStyle().Foreground(barColor).Bold(true)
		var line strings.Builder
		for i, barUnits := range heights {
			if i > 0 {
				line.WriteString(strings.Repeat(" ", gap))
			}
			remaining := barUnits - (level-1)*8
			switch {
			case remaining >= 8:
				line.WriteString(barStyle.Render(strings.Repeat("█", slot)))
			case remaining > 0:
				glyph := string(partialBlocks[remaining-1])
				line.WriteString(barStyle.Render(strings.Repeat(glyph, slot)))
			case level == 1:
				line.WriteString(mutedStyle.Render(strings.Repeat("·", slot)))
			default:
				line.WriteString(strings.Repeat(" ", slot))
			}
		}
		rows = append(rows, line.String())
	}
	baseline := mutedStyle.Render(strings.Repeat("─", min(chartWidth, slot*len(values)+gap*(len(values)-1))))
	rows = append(rows, baseline)

	var labels strings.Builder
	if period == 7 {
		start := now.UTC().Truncate(24 * time.Hour).Add(-6 * 24 * time.Hour)
		for i := range values {
			if i > 0 {
				labels.WriteString(strings.Repeat(" ", gap))
			}
			label := start.Add(time.Duration(i) * 24 * time.Hour).Format("Mon 02")
			labels.WriteString(fit(label, slot))
		}
	} else {
		axisWidth := slot*len(values) + gap*(len(values)-1)
		axis := []rune(strings.Repeat(" ", axisWidth))
		for _, tick := range []struct {
			index int
			label string
		}{{0, "-24h"}, {6, "-18h"}, {12, "-12h"}, {18, "-6h"}, {23, "now"}} {
			position := tick.index * (slot + gap)
			if position+len(tick.label) > len(axis) {
				position = max(0, len(axis)-len(tick.label))
			}
			for offset, char := range tick.label {
				axis[position+offset] = char
			}
		}
		labels.WriteString(string(axis))
	}
	rows = append(rows, mutedStyle.Render(strings.TrimRight(labels.String(), " ")))
	return rows
}

func (m model) renderCommitChart(w, h int) []string {
	if h <= 0 {
		return nil
	}
	now := time.Now().UTC()
	repos := m.chartRepos()
	if len(repos) == 0 {
		message := "Waiting for repository history…"
		if len(m.repos) == 0 {
			message = "Waiting for GitHub repositories…"
		}
		return []string{mutedStyle.Render(message)}
	}

	// One shared scale makes activity levels comparable across repositories.
	scaleMax := 1
	for _, repo := range repos {
		scaleMax = max(scaleMax, maxInt(repoCommitBuckets(*repo, now, 24)))
	}

	chartHeight := 3
	if h < 12 {
		chartHeight = 2
	}
	cardHeight := chartHeight + 3 // heading, bars, baseline, and time axis
	visible := min(len(repos), max(1, h/cardHeight))
	content := make([]string, 0, h)
	chartWidth := max(24, w-2)

	for _, repo := range repos[:visible] {
		values := repoCommitBuckets(*repo, now, 24)
		total, peak, active := sumInts(values), maxInt(values), 0
		for _, value := range values {
			if value > 0 {
				active++
			}
		}
		name := m.repoLabel(repo)
		meta := fmt.Sprintf("%s  %d commits  ·  peak %d  ·  %d/24 active", name, total, peak, active)
		if repo.CommitsFetched.IsZero() {
			meta = fmt.Sprintf("%s  ·  loading history", name)
		}
		if w >= 65 {
			trend := ntSparkline.New(
				max(12, min(20, w/4)), 1,
				ntSparkline.WithMaxValue(float64(scaleMax)),
				ntSparkline.WithNoAutoMaxValue(),
				ntSparkline.WithStyle(lipgloss.NewStyle().Foreground(purple)),
			)
			trend.PushAll(intsToFloats(values))
			trend.DrawColumnsOnly()
			line := titleStyle.Render(fit(name, max(8, w-62))) +
				mutedStyle.Render(fmt.Sprintf("  %d commits · peak %d · %d/24 active  ", total, peak, active)) +
				strings.TrimSpace(trend.View())
			content = append(content, fit(line, w))
		} else {
			content = append(content, titleStyle.Render(fit(meta, w)))
		}
		content = append(content, renderBarRows(values, chartWidth, chartHeight, 24, scaleMax, now)...)
	}
	for len(content) < h {
		content = append(content, "")
	}
	return content[:min(len(content), h)]
}

func (m model) chartRepos() []*repository {
	var repos []*repository
	for i := range m.repos {
		if m.detail {
			selected := m.selectedRepo()
			if selected == nil || selected.FullName != m.repos[i].FullName {
				continue
			}
		}
		repos = append(repos, &m.repos[i])
	}
	now := time.Now().UTC()
	period := m.commitPeriod
	if period != 7 && period != 365 {
		period = 24
	}
	if period == 365 {
		active := repos[:0]
		for _, repo := range repos {
			if repoYearTotal(*repo, now) > 0 {
				active = append(active, repo)
			}
		}
		repos = active
	}
	sort.SliceStable(repos, func(i, j int) bool {
		a, b := 0, 0
		if period == 365 {
			a = repoYearTotal(*repos[i], now)
			b = repoYearTotal(*repos[j], now)
		} else {
			a = sumInts(repoCommitBuckets(*repos[i], now, period))
			b = sumInts(repoCommitBuckets(*repos[j], now, period))
		}
		if a != b {
			return a > b
		}
		return repos[i].FullName < repos[j].FullName
	})
	return repos
}

func repoCommitBuckets(repo repository, now time.Time, period int) []int {
	if period == 7 {
		values := make([]int, 7)
		start := now.Truncate(24 * time.Hour).Add(-6 * 24 * time.Hour)
		for _, activity := range repoCommitActivity(repo, now) {
			item := activity.Commit
			t, err := time.Parse(time.RFC3339, item.Date)
			if err != nil || t.Before(start) || t.After(now) {
				continue
			}
			index := int(t.UTC().Truncate(24*time.Hour).Sub(start) / (24 * time.Hour))
			if index >= 0 && index < len(values) {
				values[index]++
			}
		}
		return values
	}
	values := make([]int, 24)
	start := now.Add(-24 * time.Hour)
	for _, activity := range repoCommitActivity(repo, now) {
		item := activity.Commit
		t, err := time.Parse(time.RFC3339, item.Date)
		if err != nil || t.Before(start) || t.After(now) {
			continue
		}
		index := int(t.Sub(start) / time.Hour)
		if index >= 0 && index < len(values) {
			values[index]++
		}
	}
	return values
}

func repoYearDailyCounts(repo repository, now time.Time) map[string]int {
	counts := make(map[string]int)
	start := now.UTC().Truncate(24*time.Hour).AddDate(0, 0, -364)
	for _, item := range repo.Commits {
		t, err := time.Parse(time.RFC3339, item.Date)
		if err == nil && !t.Before(start) && !t.After(now) {
			counts[t.UTC().Format("2006-01-02")]++
		}
	}
	return counts
}

func repoYearTotal(repo repository, now time.Time) int {
	total := 0
	for _, count := range repoYearDailyCounts(repo, now) {
		total += count
	}
	return total
}

// calendarHeatmapRows renders a Sunday-first, 53-week calendar. Only dates in the trailing year contribute.
func calendarHeatmapRows(repo repository, now time.Time, widthLimit int) []string {
	const weeks = 53
	now = now.UTC()
	counts := repoYearDailyCounts(repo, now)
	maxCount := 0
	for _, count := range counts {
		maxCount = max(maxCount, count)
	}
	colors := []lipgloss.Color{
		lipgloss.Color("#45475a"),
		lipgloss.Color("#315b46"),
		lipgloss.Color("#3d8058"),
		lipgloss.Color("#55a96b"),
		lipgloss.Color("#8bd49c"),
	}
	glyphs := []string{"·", "░", "▒", "▓", "█"}
	gridStart := now.Truncate(24*time.Hour).AddDate(0, 0, -364)
	gridStart = gridStart.Add(-time.Duration(gridStart.Weekday()) * 24 * time.Hour)
	monthLine := []rune(strings.Repeat(" ", weeks))
	for dayOffset := 0; dayOffset < weeks*7; dayOffset++ {
		date := gridStart.Add(time.Duration(dayOffset) * 24 * time.Hour)
		if dayOffset == 0 || date.Day() == 1 {
			week := dayOffset / 7
			for offset, ch := range date.Format("Jan") {
				if week+offset < len(monthLine) {
					monthLine[week+offset] = ch
				}
			}
		}
	}
	rows := []string{mutedStyle.Render(strings.TrimRight(string(monthLine), " "))}
	for weekday := 0; weekday < 7; weekday++ {
		var line strings.Builder
		for week := 0; week < weeks; week++ {
			date := gridStart.Add(time.Duration(week*7+weekday) * 24 * time.Hour)
			count := counts[date.Format("2006-01-02")]
			level := 0
			if count > 0 {
				level = 1
				if maxCount > 1 {
					level = min(4, 1+(count-1)*4/maxCount)
				}
			}
			line.WriteString(lipgloss.NewStyle().Foreground(colors[level]).Render(glyphs[level]))
		}
		rows = append(rows, line.String())
	}
	if widthLimit > 0 && widthLimit < weeks {
		for i := range rows {
			rows[i] = fit(rows[i], widthLimit)
		}
	}
	return rows
}

func calendarLegend() string {
	return mutedStyle.Render("LESS ·") +
		lipgloss.NewStyle().Foreground(lipgloss.Color("#315b46")).Render(" ░") +
		lipgloss.NewStyle().Foreground(lipgloss.Color("#3d8058")).Render(" ▒") +
		lipgloss.NewStyle().Foreground(lipgloss.Color("#55a96b")).Render(" ▓") +
		lipgloss.NewStyle().Foreground(lipgloss.Color("#8bd49c")).Render(" █") +
		mutedStyle.Render(" MORE")
}

func maxInt(values []int) int {
	maximum := 0
	for _, value := range values {
		if value > maximum {
			maximum = value
		}
	}
	return maximum
}

func intsToFloats(values []int) []float64 {
	out := make([]float64, len(values))
	for i, value := range values {
		out[i] = float64(value)
	}
	return out
}

type agentWorkload struct {
	Name                                    string
	Commits24h, Commits7d, Running, Waiting int
	Repos                                   map[string]bool
	Last                                    time.Time
}

func collectAgentWorkload(repos []repository, now time.Time) []agentWorkload {
	byActor := map[string]*agentWorkload{}
	get := func(name string) *agentWorkload {
		if name == "" {
			name = "unknown"
		}
		if byActor[name] == nil {
			byActor[name] = &agentWorkload{Name: name, Repos: map[string]bool{}}
		}
		return byActor[name]
	}
	for _, repo := range repos {
		seen := map[string]bool{}
		for _, activity := range repoCommitActivity(repo, now) {
			item := activity.Commit
			t := parseTime(item.Date)
			if t.IsZero() || now.Sub(t) > 7*24*time.Hour {
				continue
			}
			key := item.SHA
			if key == "" {
				key = item.Date + ":" + item.Msg + ":" + commitActor(item)
			}
			if seen[key] {
				continue
			}
			seen[key] = true
			a := get(commitActor(item))
			a.Commits7d++
			if now.Sub(t) <= 24*time.Hour {
				a.Commits24h++
			}
			a.Repos[repo.FullName] = true
			if t.After(a.Last) {
				a.Last = t
			}
		}
		for _, item := range repo.Runs {
			updated := parseTime(item.UpdatedAt)
			if item.Actor.Login == "" || updated.IsZero() || now.Sub(updated) > 7*24*time.Hour {
				continue
			}
			actor := get(item.Actor.Login)
			if item.Status == "in_progress" {
				actor.Running++
			}
			if queuedRun(item) {
				actor.Waiting++
			}
			actor.Repos[repo.FullName] = true
			if updated.After(actor.Last) {
				actor.Last = updated
			}
		}
	}
	out := make([]agentWorkload, 0, len(byActor))
	for _, a := range byActor {
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		as := a.Commits7d + a.Running*10 + a.Waiting*5
		bs := b.Commits7d + b.Running*10 + b.Waiting*5
		if as != bs {
			return as > bs
		}
		return a.Name < b.Name
	})
	return out
}
func (m model) agentWorkloadView() string {
	w, h := m.width, m.height
	if w < 70 || h < 12 {
		return "GitHub identity diagnostics need at least 70 columns and 12 rows.\nPress Esc to return."
	}
	head := m.renderHeader(w)
	title := titleStyle.Render("GITHUB IDENTITIES · BY REPOSITORY") + mutedStyle.Render("   Observed commit authors + workflow trigger actors · last 7 days")
	rows := collectAgentWorkload(m.repos, time.Now())
	if m.detail {
		if r := m.selectedRepo(); r != nil {
			filtered := make([]agentWorkload, 0, len(rows))
			for _, a := range rows {
				if a.Repos[r.FullName] {
					filtered = append(filtered, a)
				}
			}
			rows = filtered
		}
	}
	content := []string{mutedStyle.Render(fit("OBSERVED IDENTITY   COMMITS 24H / 7D     CI TRIGGERED RUN / WAIT     REPOSITORIES      LAST SEEN", w))}
	room := max(1, h-7)
	if len(rows) == 0 {
		content = append(content, mutedStyle.Render("No actor activity found in the last 7 days."))
	}
	start := min(m.workloadScroll, max(0, len(rows)-room))
	for i := start; i < min(len(rows), start+room); i++ {
		a := rows[i]
		names := make([]string, 0, len(a.Repos))
		for name := range a.Repos {
			names = append(names, shortName(name))
		}
		sort.Strings(names)
		line := titleStyle.Render(fit(a.Name, 18)) + " " + numberStyle.Render(fmt.Sprintf("%5d / %-5d", a.Commits24h, a.Commits7d)) + " " + activeStyle.Render(fmt.Sprintf("%3d / %-3d", a.Running, a.Waiting)) + " " + branchDataStyle.Render(fit(strings.Join(names, ","), max(12, w-75))) + " " + timeStyle.Render(shortAge(a.Last.Format(time.RFC3339)))
		if i == start {
			line = selectedRow(line, w)
		}
		content = append(content, fit(line, w))
	}
	footer := mutedStyle.Render(fmt.Sprintf("IDENTITIES %d  ·  not verified agent identities  ·  ↑↓ scroll  r refresh  Esc back", len(rows)))
	return lipgloss.JoinVertical(lipgloss.Left, head, title, strings.Join(content, "\n"), footer)
}

func (m model) renderActivity(w, h int) []string {
	var items []commitActivity
	scope := ""
	if m.detail {
		if r := m.selectedRepo(); r != nil {
			scope = r.FullName
		}
	}
	now := time.Now()
	cut := now.Add(-time.Duration(activityHistoryDays) * 24 * time.Hour)
	for _, repo := range m.repos {
		if scope != "" && repo.FullName != scope {
			continue
		}
		for _, item := range repoCommitActivity(repo, now) {
			if parseTime(item.Commit.Date).Before(cut) {
				continue
			}
			items = append(items, item)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Commit.Date > items[j].Commit.Date })
	content := []string{mutedStyle.Render(fit("RECENT COMMITS · REPO / ACTOR", w))}
	room := max(0, (h-1)/2)
	if len(items) == 0 {
		message := "No recent commits in the last 30 days"
		if m.busyCommitHistory {
			message = "Syncing recent commit activity…"
		}
		content = append(content, mutedStyle.Render(message))
	}
	start := max(0, min(m.commitScroll, max(0, len(items)-room)))
	for _, item := range items[start:min(len(items), start+room)] {
		actor := commitActor(item.Commit)
		if actor == "" {
			actor = "unknown"
		}
		first := titleStyle.Render(fit(m.repoLabelByName(item.Repo), max(8, w/4))) + " " + branchStyle.Render(fit(item.Branch, max(8, w/4))) + " " + peopleStyle.Render(fit(actor, max(8, w/4))) + " " + timeStyle.Render(shortAge(item.Commit.Date))
		second := mutedStyle.Render("  " + item.Commit.Msg)
		content = append(content, fit(first, w), fit(second, w))
	}
	for len(content) < h {
		content = append(content, "")
	}
	return content[:min(len(content), h)]
}
func (m model) renderActions(w, h int) []string {
	items := m.visibleRuns()
	content := []string{mutedStyle.Render(fit("STATE   REPOSITORY · WORKFLOW", w))}
	rows := max(0, (h-1)/2)
	if len(items) == 0 {
		message := "No running or queued CI pipelines"
		if len(m.repos) == 0 {
			message = "Waiting for repository snapshot…"
		}
		content = append(content, mutedStyle.Render(message))
	}
	start := max(0, min(m.runCursor-rows/2, max(0, len(items)-rows)))
	for i := start; i < min(len(items), start+rows); i++ {
		ref := items[i]
		workflow := ref.item.Name
		if workflow == "" {
			workflow = ref.item.Title
		}
		actor := ref.item.Actor.Login
		if actor == "" {
			actor = "unknown"
		}
		branch := ref.item.Branch
		if branch == "" {
			branch = "—"
		}
		first := stateStyle(ref.item).Render(fit(stateText(ref.item), 8)) + " " + titleStyle.Render(fit(ref.short, 14)) + " " + bodyStyle.Render(fit(workflow, max(8, w-25)))
		second := branchDataStyle.Render("  "+fit(branch, max(8, w-36))) + " " +
			peopleStyle.Render("· trigger: "+fit(actor, 10)) + " " +
			timeStyle.Render("· "+shortAge(ref.item.UpdatedAt))
		if i == m.runCursor {
			first = selectedRow(first, w)
			second = selectedRow(second, w)
		}
		content = append(content, fit(first, w), fit(second, w))
	}
	for len(content) < h {
		content = append(content, "")
	}
	return content[:min(len(content), h)]
}
func stateText(r run) string {
	switch runState(r) {
	case "success":
		return "✓ pass"
	case "failure", "startup_failure", "action_required":
		return "✗ fail"
	case "in_progress":
		return "● RUNNING"
	case "queued", "waiting", "requested", "pending":
		return "◷ WAITING"
	case "cancelled":
		return "⊘ stop"
	case "skipped":
		return "– skip"
	default:
		return "— n/a"
	}
}
func compactCI(r run) string {
	switch runState(r) {
	case "success":
		return "✓ ok"
	case "failure", "startup_failure", "action_required":
		return "✗ bad"
	case "in_progress", "queued", "waiting", "requested", "pending":
		return "● run"
	case "cancelled":
		return "⊘ stop"
	case "skipped":
		return "– skip"
	default:
		return "—"
	}
}
func stateStyle(r run) lipgloss.Style {
	switch runState(r) {
	case "success":
		return goodStyle
	case "failure", "startup_failure", "action_required":
		return badStyle
	case "in_progress":
		return activeStyle
	case "queued", "waiting", "requested", "pending":
		return branchStyle
	default:
		return mutedStyle
	}
}
func (m model) renderFooter(w int) string {
	copyHint := "c copy · t paste template"
	left := "↑↓ select · ←/→ queues · Enter open · l logs · r refresh · R force · C clear cache · ? help"
	switch m.focus {
	case 0:
		left = "↑↓ select · ←/→ panels · " + copyHint + " · Space refresh · Delete back · r/R/C controls"
	case 1:
		left = "↑↓ repos · Enter detail · / filter · Space refresh · Delete back · R force · C cache · o browser"
		if m.detail {
			left = "↑↓ PRs · Enter/o open · Space refresh · Delete back · R force · C cache"
		}
	case 2:
		left = "↑↓ runs · Enter/l logs · o browser · Space refresh · Delete back · R force · C cache · q quit"
	}
	if w < 70 {
		switch m.focus {
		case 0:
			left = "↑↓ select · ←/→ panels · " + copyHint + " · Space refresh · Delete back"
		case 1:
			left = "↑↓ select · Enter detail · Space refresh · Delete back"
			if m.detail {
				left = "↑↓ PRs · Enter open · Space refresh · Delete back"
			}
		case 2:
			left = "↑↓ runs · Enter logs · Space refresh · Delete back"
		}
	}
	if m.templateEditing {
		left = "COPY TEMPLATE · Ctrl+S save · Enter newline · Esc cancel"
	} else if m.filtering {
		left = "FILTER: " + m.filter + "_   type to search  Enter accept  Esc clear"
	}
	right := ""
	if m.lastErr != "" {
		right = badStyle.Render("⚠ " + m.lastErr)
	} else if m.copyNotice != "" && time.Since(m.copyNoticeAt) < 4*time.Second {
		right = goodStyle.Render("✓ " + m.copyNotice)
	} else if !m.lastRepos.IsZero() {
		right = mutedStyle.Render("GitHub " + shortAge(m.lastRepos.Format(time.RFC3339)) + " ago")
	}
	if width(left)+width(right)+1 > w {
		right = ""
	}
	left = fit(left, w)
	if right != "" {
		left = fit(left, max(1, w-width(right)-1))
	}
	return left + strings.Repeat(" ", max(0, w-width(left)-width(right))) + right
}

func (m model) logView() string {
	w, h := m.width, m.height
	if w < 30 || h < 8 {
		return "Terminal too small for log view."
	}
	head := titleStyle.Render("LIVE ACTION LOG") + mutedStyle.Render(fmt.Sprintf("  %s · #%d", shortName(m.logRepo), m.logRun))
	if m.logWatch {
		head += activeStyle.Render("  ● watching")
	}
	if !m.logUpdated.IsZero() {
		head += mutedStyle.Render("  updated " + shortAge(m.logUpdated.Format(time.RFC3339)) + " ago")
	}
	lines := strings.Split(strings.TrimSuffix(m.logText, "\n"), "\n")
	maxLines := h - 3
	start := max(0, len(lines)-maxLines)
	if m.logOffset > 0 {
		start = max(0, start-m.logOffset)
	}
	body := make([]string, 0, maxLines)
	for _, line := range lines[start:min(len(lines), start+maxLines)] {
		body = append(body, fit(line, w))
	}
	for len(body) < maxLines {
		body = append(body, "")
	}
	footer := "u refresh logs  w live polling  R force data  C clear cache  ↑↓/PgUp/PgDn scroll  Esc back"
	if m.logBusy {
		footer = activeStyle.Render("Fetching latest logs…  " + footer)
	} else if m.logErr != "" {
		footer = badStyle.Render(m.logErr + "  ·  " + footer)
	}
	return head + "\n" + strings.Join(body, "\n") + "\n" + footer
}
func (m model) helpView() string {
	lines := []string{
		"A human-facing console for delivery state across GitHub repositories.", "",
		"NAVIGATION",
		"  Tab / Shift+Tab     Next / previous view",
		"  1 / 2 / 3           Jump directly to Overview, Repositories, CI queue",
		"  ↑ / ↓ or j / k     Select an item in the focused list",
		"  ← / →              Switch between Overview attention and in-flight queues",
		"  Enter               Open selected item, enter repository detail, or open CI logs",
		"  l                   Open logs for a selected workflow run",
		"  o                   Open selected GitHub destination in browser",
		"  c                   Copy selected repository URL or template output",
		"  t                   Paste a hidden copy template (Ctrl+S save, Esc cancel)",
		"  a                   Optional activity diagnostic (not a people ranking)",
		"  Esc                 Return from detail or filter",
		"  /                   Filter repositories", "",
		"CI OPERATIONS",
		"  RUNNING              GitHub Actions workflow is in progress",
		"  QUEUED               Workflow is waiting for a runner",
		"  FAILED               Workflow completed with a failure in the last 24h",
		"  r                    Refresh repository and Actions data (conditional)",
		"  R                    Force refresh; bypass cached ETags",
		"  C                    Clear local cache and force a fresh snapshot",
		"  l / Enter on run     Open its logs; w toggles live log polling",
		"  u                    Refresh the current log immediately", "",
		"SOURCES + FRESHNESS",
		m.agentRuntimeNotice(),
		"  Commit authors and Actions trigger actors are observed identities, not agent owners.",
		"  Activity history is loaded only when the optional identity diagnostic is opened.",
		"  Repository and PR summaries refresh every 3 minutes; Actions refresh every 60 seconds.",
		"  Idle list: last push within 3 days, no active CI, and no CI run update in the last 10 minutes.",
		"  Cached data remains visible during API outages; stale data is labelled.",
		"  r refreshes; R bypasses ETags; C clears local cache and refreshes.",
		"  Agent events are read-only from --agent-events or GH_FLEET_AGENT_EVENTS.",
		"  Agent telemetry is not written to the GitHub metadata cache.",
	}
	return box("HELP", lines, m.width-4, m.height-2, true)
}

func main() {
	cacheFlag := flag.String("cache", "", "cache file path (default: user cache directory)")
	agentEventsFlag := flag.String("agent-events", os.Getenv("GH_FLEET_AGENT_EVENTS"), "read-only agent JSONL event spool (or GH_FLEET_AGENT_EVENTS)")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Printf("gh-fleet %s\n", version)
		return
	}
	client, err := newAPIClient()
	if err != nil {
		fmt.Fprintln(os.Stderr, "GitHub Fleet:", err, "\nRun gh auth login, then start this dashboard again.")
		os.Exit(1)
	}
	cachePath := initialCachePath()
	if *cacheFlag != "" {
		cachePath = *cacheFlag
	}
	if cachePath == "" {
		fmt.Fprintln(os.Stderr, "GitHub Fleet: cache directory is unavailable; cached startup will be disabled")
	}
	program := tea.NewProgram(newModel(client, cachePath, *agentEventsFlag))
	if _, err := program.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "GitHub Fleet:", err)
		os.Exit(1)
	}
}

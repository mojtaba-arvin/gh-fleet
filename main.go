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
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	ntSparkline "github.com/NimbleMarkets/ntcharts/sparkline"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

const (
	apiRoot                 = "https://api.github.com"
	graphQLURL              = "https://api.github.com/graphql"
	allRefreshInterval      = 60 * time.Second
	refsRefreshInterval     = 180 * time.Second
	activityRefreshInterval = 5 * time.Minute
	selectedInterval        = 20 * time.Second
	logRefreshInterval      = 15 * time.Second
	maxLogBytes             = 2 << 20
	activityHistoryDays     = 30
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
	FullName          string       `json:"full_name"`
	Name              string       `json:"name"`
	URL               string       `json:"url"`
	Private           bool         `json:"private"`
	Fork              bool         `json:"fork"`
	Stars             int          `json:"stars"`
	Forks             int          `json:"forks"`
	PushedAt          string       `json:"pushed_at"`
	Default           string       `json:"default_branch"`
	PRs               int          `json:"open_prs"`
	Issues            int          `json:"open_issues"`
	BranchN           int          `json:"branch_count"`
	Branches          []branch     `json:"branches"`
	Commits           []commitInfo `json:"commits,omitempty"`
	CommitHistoryDays int          `json:"commit_history_days,omitempty"`
	BranchHistoryDays int          `json:"branch_history_days,omitempty"`
	Runs              []run        `json:"runs"`
	Fetched           time.Time    `json:"fetched"`
	BranchesFetched   time.Time    `json:"branches_fetched"`
	CommitsFetched    time.Time    `json:"commits_fetched"`
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
        pullRequests(states:OPEN) { totalCount }
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
	repo string
	runs []run
	err  error
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

type model struct {
	api                 *apiClient
	busyCommitHistory   bool
	lastCommitHistory   time.Time
	busyBranchHistory   bool
	lastBranchHistory   time.Time
	login               string
	repos               []repository
	width, height       int
	cursor, runCursor   int
	commitScroll        int
	workloadScroll      int
	commitPeriod        int
	agentWorkloadMode   bool
	runScroll           int
	logOffset           int
	focus               int // repositories, activity, Actions
	detail              bool
	filter              string
	filtering           bool
	busyRuns            map[string]bool
	busyBranches        map[string]bool
	queuedRuns          map[string]bool
	runQueue            []string
	inFlightRuns        int
	busyRepos           bool
	retryAt             map[string]time.Time
	failures            map[string]int
	lastRepos, lastRuns time.Time
	lastErr             string
	cachePath           string
	logMode, logWatch   bool
	logRepo             string
	logRun              int64
	logText, logErr     string
	logUpdated          time.Time
	logBusy             bool
	lastLog             time.Time
	showHelp            bool
}

func fetchReposCmd(c *apiClient) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		login, repos, err := c.fetchRepositories(ctx)
		return reposMsg{login, repos, err}
	}
}
func fetchRunsCmd(c *apiClient, repo string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
		defer cancel()
		runs, err := c.fetchRuns(ctx, repo)
		return runsMsg{repo, runs, err}
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
func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
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
func newModel(client *apiClient, cachePath string) model {
	c := loadCache(cachePath)
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
	return model{api: client, login: c.Login, repos: c.Repos, cachePath: cachePath, busyRepos: true,
		busyRuns: make(map[string]bool), busyBranches: make(map[string]bool), queuedRuns: make(map[string]bool),
		retryAt: make(map[string]time.Time), failures: make(map[string]int), lastRepos: refsAt, lastRuns: runsAt, lastCommitHistory: commitsAt, lastBranchHistory: branchHistoryAt}
}

func (m model) Init() tea.Cmd { return tea.Batch(fetchReposCmd(m.api), tickCmd()) }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.KeyMsg:
		if m.showHelp {
			if msg.String() == "?" || msg.String() == "esc" || msg.String() == "q" {
				m.showHelp = false
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
					m.filter = m.filter[:len(m.filter)-1]
				}
			default:
				if msg.Type == tea.KeyRunes {
					m.filter += msg.String()
					m.cursor = 0
				}
			}
			return m, nil
		}
		if m.logMode {
			switch msg.String() {
			case "esc", "backspace":
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
			case "esc", "backspace", "c":
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
		if msg.String() == "c" {
			m.agentWorkloadMode = true
			m.workloadScroll = 0

			return m, nil
		}
		if msg.String() == "ctrl+c" || msg.String() == "q" {
			return m, tea.Quit
		}
		switch msg.String() {
		case "?":
			m.showHelp = true
		case "tab":
			m.focus = (m.focus + 1) % 3
		case "shift+tab":
			m.focus = (m.focus + 2) % 3
		case "esc":
			if m.filter != "" {
				m.filter = ""
			} else if m.detail {
				m.detail = false
			}
		case "enter":
			if m.focus == 0 && len(m.visibleRepos()) > 0 {
				m.detail = !m.detail
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
						if (len(r.Branches) < r.BranchN || time.Since(r.BranchesFetched) >= activityRefreshInterval) && !m.busyBranches[r.FullName] {
							m.busyBranches[r.FullName] = true
							cmds = append(cmds, fetchAllBranchesCmd(m.api, r.FullName))
						}
					}
					cmds = append(cmds, m.drainRunQueue()...)
					if len(cmds) > 0 {
						return m, tea.Batch(cmds...)
					}
				}
			}
			if m.focus == 2 {
				if r := m.selectedRun(); r != nil {
					m.logMode = true
					m.logRepo = r.repo
					m.logRun = r.item.ID
					m.logText = ""
					m.logErr = ""
					m.logBusy = true
					m.logWatch = true
					m.lastLog = time.Now()
					return m, fetchLogCmd(m.api, m.logRepo, m.logRun)
				}
			}
		case "r":
			if m.detail {
				if r := m.selectedRepo(); r != nil {
					m.lastErr = "Refreshing " + m.repoLabel(r) + "…"
					delete(m.retryAt, "runs:"+r.FullName)
					delete(m.retryAt, "branches:"+r.FullName)
					m.queueRun(r.FullName, true)
					cmds := m.drainRunQueue()
					if !m.busyBranches[r.FullName] {
						m.busyBranches[r.FullName] = true
						cmds = append(cmds, fetchAllBranchesCmd(m.api, r.FullName))
					}
					if len(cmds) > 0 {
						return m, tea.Batch(cmds...)
					}
					return m, nil
				}
			}
			m.lastErr = "Refreshing GitHub…"
			delete(m.retryAt, "repos")
			m.busyRepos = true
			for _, r := range m.repos {
				delete(m.retryAt, "runs:"+r.FullName)
				delete(m.retryAt, "branches:"+r.FullName)
				m.queueRun(r.FullName, false)
			}
			return m, tea.Batch(append([]tea.Cmd{fetchReposCmd(m.api)}, m.drainRunQueue()...)...)
		case "/":
			m.focus = 0
			m.filtering = true
			m.filter = ""
		case "o":
			if m.focus == 0 {
				if r := m.selectedRepo(); r != nil {
					_ = exec.Command("open", r.URL).Start()
				}
			}
			if m.focus == 2 {
				if r := m.selectedRun(); r != nil {
					_ = exec.Command("open", r.item.URL).Start()
				}
			}
		case "l":
			if m.focus == 2 {
				if r := m.selectedRun(); r != nil {
					m.logMode = true
					m.logRepo = r.repo
					m.logRun = r.item.ID
					m.logText = ""
					m.logErr = ""
					m.logBusy = true
					m.logWatch = true
					m.lastLog = time.Now()
					return m, fetchLogCmd(m.api, m.logRepo, m.logRun)
				}
			}
		case "up", "k":
			m.move(-1)
		case "down", "j":
			m.move(1)
		case "home", "g":
			if m.focus == 0 {
				m.cursor = 0
			} else if m.focus == 2 {
				m.runCursor = 0
			}
		case "end", "G":
			if m.focus == 0 {
				m.cursor = max(0, len(m.visibleRepos())-1)
			} else if m.focus == 2 {
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
				if r.Fetched.IsZero() || time.Since(r.Fetched) > allRefreshInterval {
					m.queueRun(r.FullName, false)
				}
			}
			cmds = append(cmds, m.drainRunQueue()...)
			needsBranchHistory := false
			for _, repo := range m.repos {
				if repo.BranchHistoryDays < activityHistoryDays || repo.BranchesFetched.IsZero() || time.Since(repo.BranchesFetched) >= activityRefreshInterval {
					needsBranchHistory = true
					break
				}
			}
			if needsBranchHistory && !m.busyBranchHistory && !time.Now().Before(m.retryAt["branch-history"]) {
				m.busyBranchHistory = true
				cmds = append(cmds, fetchBranchHistoryCmd(m.api, m.repos))
			}
			if len(cmds) > 0 {
				return m, tea.Batch(cmds...)
			}
		}
	case runsMsg:
		delete(m.busyRuns, msg.repo)
		m.inFlightRuns = max(0, m.inFlightRuns-1)
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
	case tickMsg:
		var cmds []tea.Cmd
		now := time.Now()
		if len(m.repos) == 0 && !m.busyRepos && !now.Before(m.retryAt["repos"]) {
			m.busyRepos = true
			cmds = append(cmds, fetchReposCmd(m.api))
		}
		if !m.detail && now.Sub(m.lastRepos) >= refsRefreshInterval && !m.busyRepos && !now.Before(m.retryAt["repos"]) {
			m.busyRepos = true
			cmds = append(cmds, fetchReposCmd(m.api))
		}
		if !m.detail && len(m.repos) > 0 && !m.busyCommitHistory &&
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
		if !m.detail && len(m.repos) > 0 && !m.busyBranchHistory &&
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
			branchKey := "branches:" + r.FullName
			if selected && !m.busyBranches[r.FullName] && (r.BranchesFetched.IsZero() || now.Sub(r.BranchesFetched) >= activityRefreshInterval) && !now.Before(m.retryAt[branchKey]) {
				m.busyBranches[r.FullName] = true
				cmds = append(cmds, fetchAllBranchesCmd(m.api, r.FullName))
			}
		}
		cmds = append(cmds, m.drainRunQueue()...)
		if m.logMode && m.logWatch && !m.logBusy && now.Sub(m.lastLog) >= logRefreshInterval {
			m.logBusy = true
			m.lastLog = now
			cmds = append(cmds, fetchLogCmd(m.api, m.logRepo, m.logRun))
		}
		cmds = append(cmds, tickCmd())
		if len(cmds) > 1 {
			return m, tea.Batch(cmds...)
		}
	}
	return m, nil
}

func (m *model) logScroll(n int) {
	m.logOffset = max(0, min(m.logOffset+n, max(0, len(strings.Split(m.logText, "\n"))-1)))
}
func (m *model) move(n int) {
	switch m.focus {
	case 0:
		m.cursor = max(0, min(len(m.visibleRepos())-1, m.cursor+n))
	case 2:
		m.runCursor = max(0, min(len(m.visibleRuns())-1, m.runCursor+n))
	case 1:
		m.commitScroll = max(0, m.commitScroll+n)
	}
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
		cmds = append(cmds, fetchRunsCmd(m.api, name))
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
		if m.filter == "" || strings.Contains(strings.ToLower(r.Name), strings.ToLower(m.filter)) {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
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
	for _, x := range r.Runs {
		if x.Status == "in_progress" {
			return 0
		}
	}
	for _, x := range r.Runs {
		if queuedRun(x) {
			return 1
		}
	}
	for _, x := range r.Runs {
		if x.Status == "completed" && x.Conclusion == "failure" && time.Since(parseTime(x.UpdatedAt)) < 24*time.Hour {
			return 2
		}
	}
	if repoWorkload(*r, time.Now()).Commits24h > 0 {
		return 3
	}
	return 4
}
func latestRepoDate(r repository) time.Time {
	var best time.Time
	for _, b := range r.Branches {
		if t, err := time.Parse(time.RFC3339, b.Date); err == nil && t.After(best) {
			best = t
		}
	}
	return best
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
	ink         = lipgloss.Color("#cdd6f4")
	muted       = lipgloss.Color("#7f849c")
	panelBorder = lipgloss.Color("#45475a")
	cyan        = lipgloss.Color("#89dceb")
	green       = lipgloss.Color("#a6e3a1")
	red         = lipgloss.Color("#f38ba8")
	yellow      = lipgloss.Color("#f9e2af")
	purple      = lipgloss.Color("#cba6f7")
	blue        = lipgloss.Color("#89b4fa")
	selectedBG  = lipgloss.Color("#313244")
	titleStyle  = lipgloss.NewStyle().Foreground(cyan).Bold(true)
	mutedStyle  = lipgloss.NewStyle().Foreground(muted)
	goodStyle   = lipgloss.NewStyle().Foreground(green)
	badStyle    = lipgloss.NewStyle().Foreground(red).Bold(true)
	activeStyle = lipgloss.NewStyle().Foreground(yellow).Bold(true)
	branchStyle = lipgloss.NewStyle().Foreground(purple)
	selectStyle = lipgloss.NewStyle().Foreground(ink).Background(selectedBG).Bold(true)
)

func width(s string) int { return lipgloss.Width(s) }
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

func (m model) View() string {
	if m.width == 0 || m.height == 0 {
		return "Starting GitHub Fleet…"
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
	if m.width < 110 {
		return m.narrowView()
	}
	w, h := m.width, m.height
	headerH, metricsH, footerH, gap := 2, 5, 1, 1
	mainH := max(7, h-headerH-metricsH-footerH-2*gap)
	leftW := int(float64(w) * 0.57)
	if leftW < 58 {
		leftW = 58
	}
	if leftW > w-45 {
		leftW = w - 45
	}
	rightW := w - leftW - gap
	ciH := max(5, mainH*62/100)
	activityH := mainH - ciH - 1
	if activityH < 4 {
		activityH = 4
		ciH = mainH - activityH - 1
	}
	head := m.renderHeader(w)
	metrics := m.renderMetrics(w)
	repoPanel := box(m.repoTitle(), m.renderRepos(leftW-2, mainH-2), leftW, mainH, m.focus == 0)
	ciPanel := box(m.actionsTitle(), m.renderActions(rightW-2, ciH-2), rightW, ciH, m.focus == 2)
	activityPanel := box(m.activityTitle(), m.renderActivity(rightW-2, activityH-2), rightW, activityH, m.focus == 1)
	right := lipgloss.JoinVertical(lipgloss.Left, ciPanel, " ", activityPanel)
	body := lipgloss.JoinHorizontal(lipgloss.Top, repoPanel, strings.Repeat(" ", gap), right)
	return lipgloss.JoinVertical(lipgloss.Left, head, metrics, " ", body, m.renderFooter(w))
}

func (m model) renderHeader(w int) string {
	brand := titleStyle.Render("GH FLEET  /  AGENT OPERATIONS")
	login := mutedStyle.Render("  " + m.login)
	state := "SYNCING"
	if m.busyRepos && len(m.repos) > 0 {
		state = "CACHE · REFRESHING"
	} else if m.busyRepos {
		state = "FETCHING ACCOUNT"
	} else if m.lastErr != "" {
		state = "OFFLINE · CACHED DATA"
	} else if !m.lastRepos.IsZero() {
		state = "CONNECTED · " + shortAge(m.lastRepos.Format(time.RFC3339)) + " ago"
	}
	status := goodStyle.Render("● " + state)
	if m.lastErr != "" {
		status = badStyle.Render("● " + state)
	}
	left := brand + login
	return left + strings.Repeat(" ", max(1, w-width(left)-width(status))) + status + "\n" + mutedStyle.Render(fit("Fleet workload  /  agent attribution  /  CI pipeline control", w))
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
	items := []string{
		metric("REPOSITORIES", strconv.Itoa(repoCount), "monitored", cyan, cw),
		metric("ACTIVE REPOS", strconv.Itoa(active), "activity or CI · 7d", purple, cw),
		metric("COMMITS · 30D", countFmt(commits30d), fmt.Sprintf("%d/%d repos covered", coverage, repoCount), blue, cw),
		metric("CONTRIBUTORS", countFmt(len(contributors)), "unique commit authors · 30d", cyan, cw),
		metric("CI · RUN / WAIT", fmt.Sprintf("%d / %d", running, waiting), fmt.Sprintf("%d failures · 24h", failed), func() lipgloss.Color {
			if failed > 0 {
				return red
			}
			if running > 0 || waiting > 0 {
				return yellow
			}
			return green
		}(), cw),
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
			mutedStyle.Render(fit(fmt.Sprintf("%s%d/%d/%d", approx, work.Commits24h, work.Commits7d, work.Commits30d), workW)) + " " +
			branchStyle.Render(fit(sparkline(work.Daily), trendW)) + " " +
			mutedStyle.Render(fit(approx+countFmt(len(work.Actors)), peopleW)) + " " +
			mutedStyle.Render(fit(countFmt(work.ActiveBranches7d), branchW)) + " " +
			ciStyle(ci).Render(fit(ci, ciW)) + " " +
			mutedStyle.Render(fit(countFmt(r.PRs), prW))
		if i == m.cursor {
			line = selectStyle.Width(w).Render(fit(line, w))
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
		}
	}
	return "—"
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
		return "Agent workload view needs a terminal at least 70 columns wide and 12 rows high.\nPress Esc to return."
	}
	head := m.renderHeader(w)
	title := titleStyle.Render("AGENT WORKLOAD · BY REPOSITORY") + mutedStyle.Render("   Commit authors + GitHub Actions actors · last 7 days")
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
	content := []string{mutedStyle.Render(fit("ACTOR / AGENT       COMMITS 24H / 7D     CI RUN / WAIT     REPOSITORIES                 LAST SEEN", w))}
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
		line := titleStyle.Render(fit(a.Name, 18)) + " " + mutedStyle.Render(fmt.Sprintf("%5d / %-5d", a.Commits24h, a.Commits7d)) + " " + activeStyle.Render(fmt.Sprintf("%3d / %-3d", a.Running, a.Waiting)) + " " + fit(strings.Join(names, ","), max(12, w-75)) + " " + mutedStyle.Render(shortAge(a.Last.Format(time.RFC3339)))
		content = append(content, fit(line, w))
	}
	footer := mutedStyle.Render(fmt.Sprintf("ACTORS %d  ·  ↑↓ / j k scroll  PgUp/PgDn page  r refresh  Esc back", len(rows)))
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
		first := titleStyle.Render(fit(m.repoLabelByName(item.Repo), max(8, w/4))) + " " + branchStyle.Render(fit(item.Branch, max(8, w/4))) + " " + mutedStyle.Render(fit(actor, max(8, w/4))) + " " + mutedStyle.Render(shortAge(item.Commit.Date))
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
		first := stateStyle(ref.item).Render(fit(stateText(ref.item), 8)) + " " + titleStyle.Render(fit(ref.short, 14)) + " " + fit(workflow, max(8, w-25))
		second := mutedStyle.Render("  " + fit(branch, max(8, w-26)) + " · " + fit(actor, 10) + " · " + shortAge(ref.item.UpdatedAt))
		if i == m.runCursor {
			first = selectStyle.Width(w).Render(fit(first, w))
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
	left := "↑↓ move  Tab section  c agent workload  Enter detail/log  / filter  r refresh  ? help  q quit"
	if m.filtering {
		left = "FILTER: " + m.filter + "_   type to search  Enter accept  Esc clear"
	}
	right := ""
	if m.lastErr != "" {
		right = badStyle.Render("⚠ " + m.lastErr)
	} else if !m.lastRuns.IsZero() {
		right = mutedStyle.Render("Actions " + shortAge(m.lastRuns.Format(time.RFC3339)) + " ago")
	}
	if width(left)+width(right)+1 > w {
		right = ""
	}
	return left + strings.Repeat(" ", max(1, w-width(left)-width(right))) + right
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
	footer := "u refresh now  w toggle live polling  ↑↓/PgUp/PgDn scroll  Esc back"
	if m.logBusy {
		footer = activeStyle.Render("Fetching latest logs…  " + footer)
	} else if m.logErr != "" {
		footer = badStyle.Render(m.logErr + "  ·  " + footer)
	}
	return head + "\n" + strings.Join(body, "\n") + "\n" + footer
}
func (m model) narrowView() string {
	w, h := m.width, m.height
	if w < 40 {
		return "Enlarge terminal to at least 90 columns for the dashboard."
	}
	head := m.renderHeader(w)
	tabs := []string{"1 Repositories", "2 Agent work", "3 CI queue"}
	var tabLine string
	for i, t := range tabs {
		if i == m.focus {
			tabLine += selectStyle.Render(" "+t+" ") + "  "
		} else {
			tabLine += mutedStyle.Render(" "+t+" ") + "  "
		}
	}
	contentH := max(5, h-7)
	var content []string
	panelTitle := []string{m.repoTitle(), m.activityTitle(), m.actionsTitle()}[m.focus]
	switch m.focus {
	case 0:
		content = m.renderRepos(w-2, contentH)
	case 1:
		content = m.renderActivity(w-2, contentH)
	case 2:
		content = m.renderActions(w-2, contentH)
	}
	return lipgloss.JoinVertical(lipgloss.Left, head, tabLine, box(panelTitle, content, w, contentH+2, true), m.renderFooter(w))
}
func (m model) helpView() string {
	lines := []string{
		"An operations cockpit for agent work across every accessible repository.", "",
		"NAVIGATION",
		"  Tab / Shift+Tab     Focus repository workload, agent work, or CI queue",
		"  ↑ / ↓ or j / k     Move through repositories, commits, and pipelines",
		"  c                   Open the agent-by-repository workload view",
		"  Enter               Scope to a repository / open selected CI logs",
		"  Esc                 Return from repository or log detail",
		"  /                   Filter repository names", "",
		"CI OPERATIONS",
		"  RUNNING              GitHub Actions workflow is in progress",
		"  WAITING              Workflow is queued, waiting, requested, or pending",
		"  FAIL                 Workflow completed with a failure in the last 24h",
		"  r                    Refresh repository and Actions data",
		"  o                    Open selected repository or workflow in browser",
		"  l / Enter on run     Open its logs; w toggles live log polling",
		"  u                    Refresh the current log immediately", "",
		"ATTRIBUTION + DATA",
		"  Commit authors use GitHub login where available; human and automation accounts are counted uniformly.",
		"  Actions actors identify who triggered a workflow; this is not an agent heartbeat.",
		"  Activity counts show unique commits over 24h / 7d / 30d, including active feature branches.",
		"  People counts are distinct commit authors over 30 days; BR counts branches active in the last 7 days.",
		"  A ~ prefix means history is incomplete or a branch reached the 20-commit sample limit.",
		"  Repository metadata and commit history refresh every 3 minutes; Actions refresh every 60 seconds.",
		"  Cached data remains visible during API outages.",
	}
	return box("HELP", lines, m.width-4, m.height-2, true)
}

func main() {
	cacheFlag := flag.String("cache", "", "cache file path (default: user cache directory)")
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
	program := tea.NewProgram(newModel(client, cachePath), tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := program.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "GitHub Fleet:", err)
		os.Exit(1)
	}
}

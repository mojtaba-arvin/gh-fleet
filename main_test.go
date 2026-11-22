package main

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestDefaultDashboardRendersHumanAttentionQueue(t *testing.T) {
	now := time.Now().UTC()
	m := model{
		width: 120, height: 30, lastRepos: now,
		repos: []repository{{
			FullName: "acme/checkout", Name: "checkout", PRs: 1,
			Runs: []run{{
				ID: 7, Name: "unit-tests", Status: "completed", Conclusion: "failure",
				UpdatedAt: now.Add(-time.Hour).Format(time.RFC3339),
				URL:       "https://github.com/acme/checkout/actions/runs/7",
			}},
			PullRequests: []pullRequestSummary{{
				Number: 12, Title: "Adjust checkout", ReviewDecision: "CHANGES_REQUESTED",
				UpdatedAt: now.Add(-2 * time.Hour).Format(time.RFC3339),
				URL:       "https://github.com/acme/checkout/pull/12",
			}},
		}},
	}
	view := m.View()
	for _, want := range []string{"DELIVERY OPERATIONS", "NEEDS ATTENTION", "CI FAILED", "CHANGES REQUESTED", "IN FLIGHT / WAITING"} {
		if !strings.Contains(view, want) {
			t.Errorf("operator overview does not contain %q:\n%s", want, view)
		}
	}
	for _, unwanted := range []string{"COMMITS · 30D", "CONTRIBUTORS", "ACTOR / AGENT", "2 commits"} {
		if strings.Contains(view, unwanted) {
			t.Errorf("operator overview still foregrounds %q:\n%s", unwanted, view)
		}
	}
}

func TestNarrowDashboardShowsOperatorAttentionAndReviewQueue(t *testing.T) {
	now := time.Now().UTC()
	m := model{
		width: 80, height: 24, lastRepos: now,
		repos: []repository{{
			FullName: "acme/active", Name: "active", PRs: 1,
			PullRequests: []pullRequestSummary{{
				Number: 27, Title: "Review access change", ReviewDecision: "REVIEW_REQUIRED",
				RequestedReviewers: []string{"reviewer-a"},
				UpdatedAt:          now.Add(-time.Hour).Format(time.RFC3339),
				URL:                "https://github.com/acme/active/pull/27",
			}},
		}},
	}
	view := m.View()
	for _, want := range []string{"Overview", "Repositories", "CI queue", "NEEDS ATTENTION", "IN FLIGHT / WAITING", "REVIEW", "reviewer-a"} {
		if !strings.Contains(view, want) {
			t.Errorf("narrow operator dashboard does not contain %q:\n%s", want, view)
		}
	}
}


func TestOverviewPanelsCanBeFocusedAndNavigated(t *testing.T) {
	now := time.Now().UTC()
	m := model{
		width: 120, height: 30,
		repos: []repository{{
			FullName: "acme/ops",
			Runs: []run{{
				ID: 41, Name: "unit-tests", Status: "in_progress",
				StartedAt: now.Add(-time.Minute).Format(time.RFC3339),
				URL: "https://github.com/acme/ops/actions/runs/41",
			}},
			PullRequests: []pullRequestSummary{{
				Number: 22, Title: "Tighten permissions", ReviewDecision: "REVIEW_REQUIRED",
				RequestedReviewers: []string{"reviewer-a"},
				UpdatedAt: now.Add(-time.Hour).Format(time.RFC3339),
				URL: "https://github.com/acme/ops/pull/22",
			}},
		}},
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRight})
	m = updated.(model)
	if m.overviewPane != 1 {
		t.Fatalf("right arrow selected overview pane %d, want in-flight pane 1", m.overviewPane)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(model)
	if m.inFlightCursor != 1 {
		t.Fatalf("down arrow moved in-flight cursor to %d, want 1", m.inFlightCursor)
	}
	selected := m.selectedInFlight()
	if selected == nil || selected.Kind != "REVIEW" {
		t.Fatalf("selected in-flight item = %+v, want requested-review item", selected)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	m = updated.(model)
	if m.overviewPane != 0 {
		t.Fatalf("left arrow selected overview pane %d, want attention pane 0", m.overviewPane)
	}
}

func TestInFlightWorkflowLogsHaveAnExplicitShortcut(t *testing.T) {
	m := model{
		width: 120, height: 30, focus: 0, overviewPane: 1,
		repos: []repository{{FullName: "acme/ops", Runs: []run{{
			ID: 41, Name: "unit-tests", Status: "in_progress",
			URL: "https://github.com/acme/ops/actions/runs/41",
		}}}},
	}
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")})
	got := updated.(model)
	if !got.logMode || got.logRepo != "acme/ops" || got.logRun != 41 {
		t.Fatalf("l did not open logs for the selected workflow: %+v", got)
	}
	if cmd == nil {
		t.Fatal("opening workflow logs did not request log data")
	}
}

func TestNumberKeysJumpToPrimaryViews(t *testing.T) {
	m := model{width: 120, height: 30}
	for _, tc := range []struct {
		key  rune
		view int
	}{{'1', 0}, {'2', 1}, {'3', 2}} {
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{tc.key}})
		m = updated.(model)
		if m.focus != tc.view {
			t.Errorf("key %q selected view %d, want %d", tc.key, m.focus, tc.view)
		}
	}
}

func TestFilterBackspaceRemovesOneUnicodeRune(t *testing.T) {
	m := model{width: 80, height: 24, focus: 1, filtering: true, filter: "café"}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	got := updated.(model).filter
	if got != "caf" {
		t.Fatalf("filter after backspace = %q, want %q", got, "caf")
	}
}

func TestConfiguredAgentTelemetryStillShowsItsActualConnectionState(t *testing.T) {
	m := model{width: 80, height: 24, agentEventsPath: "/tmp/agent-events.jsonl", busyAgentPoll: true}
	header := m.renderHeader(80)
	if !strings.Contains(header, "CONNECTING") {
		t.Fatalf("configured agent source status was hidden:\n%s", header)
	}
}

func TestRunningRunsCountsOnlyInProgress(t *testing.T) {
	repo := repository{Runs: []run{
		{Status: "in_progress"},
		{Status: "queued"},
		{Status: "waiting"},
		{Status: "completed"},
		{Status: "in_progress"},
	}}
	if got := runningRuns(repo); got != 2 {
		t.Fatalf("runningRuns() = %d, want 2", got)
	}
}

func TestCommitCountsBucketsAndDeduplicates(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 30, 0, 0, time.UTC)
	repos := []repository{
		{FullName: "acme/one", Commits: []commitInfo{
			{SHA: "today", Date: "2026-11-22T12:05:00Z"},
			{SHA: "today", Date: "2026-11-22T12:05:00Z"},
			{SHA: "hour-ago", Date: "2026-11-22T11:05:00Z"},
			{SHA: "three-days", Date: "2026-11-19T08:00:00Z"},
			{SHA: "old", Date: "2026-11-14T08:00:00Z"},
			{SHA: "future", Date: "2026-11-23T08:00:00Z"},
		}},
		{FullName: "acme/two", Commits: []commitInfo{
			{SHA: "today", Date: "2026-11-22T12:10:00Z"},
		}},
	}
	hourly, daily := commitCounts(repos, now)
	if got := sumInts(hourly); got != 3 {
		t.Fatalf("24h total = %d, want 3", got)
	}
	if hourly[23] != 2 || hourly[22] != 1 {
		t.Fatalf("hourly buckets current/previous = %d/%d, want 2/1", hourly[23], hourly[22])
	}
	if got := sumInts(daily); got != 4 {
		t.Fatalf("7d total = %d, want 4", got)
	}
	if daily[6] != 3 || daily[3] != 1 {
		t.Fatalf("daily buckets today/three-days-ago = %d/%d, want 2/1", daily[6], daily[3])
	}
}

func TestSparklineKeepsZeroBucketsAtBaseline(t *testing.T) {
	if got, want := sparkline([]int{0, 2, 0}), "·█·"; got != want {
		t.Fatalf("sparkline() = %q, want %q", got, want)
	}
}

func TestRepoCommitBucketsAreIndependent(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 30, 0, 0, time.UTC)
	one := repository{FullName: "acme/one", Commits: []commitInfo{
		{SHA: "a", Date: "2026-11-22T12:05:00Z"},
		{SHA: "b", Date: "2026-11-22T11:05:00Z"},
		{SHA: "old", Date: "2026-11-14T08:00:00Z"},
	}}
	two := repository{FullName: "acme/two", Commits: []commitInfo{
		{SHA: "c", Date: "2026-11-22T12:10:00Z"},
	}}
	one24 := repoCommitBuckets(one, now, 24)
	two24 := repoCommitBuckets(two, now, 24)
	if sumInts(one24) != 2 || sumInts(two24) != 1 {
		t.Fatalf("independent 24h totals = %d and %d, want 2 and 1", sumInts(one24), sumInts(two24))
	}
	one7 := repoCommitBuckets(one, now, 7)
	two7 := repoCommitBuckets(two, now, 7)
	if sumInts(one7) != 2 || sumInts(two7) != 1 {
		t.Fatalf("independent 7d totals = %d and %d, want 2 and 1", sumInts(one7), sumInts(two7))
	}
	if one24[23] != 1 || one24[22] != 1 || two24[23] != 1 {
		t.Fatalf("hourly buckets were not kept separate: one=%v two=%v", one24, two24)
	}
}

func TestScaledBarUnitsUseSharedScale(t *testing.T) {
	got := scaledBarUnits([]int{0, 2, 5}, 4, 10)
	want := []int{0, 6, 16}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("scaledBarUnits()[%d] = %d, want %d (all: %v)", i, got[i], want[i], got)
		}
	}
}

func TestScaledBarUnitsPreserveSmallNonZeroActivity(t *testing.T) {
	got := scaledBarUnits([]int{1, 0, 100}, 5, 100)
	if got[0] != 1 || got[1] != 0 || got[2] != 40 {
		t.Fatalf("scaledBarUnits() = %v, want [1 0 40]", got)
	}
}

func TestRepoWorkloadCountsUniqueCommitsAndAttributesActors(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	repo := repository{
		FullName: "acme/ops",
		Commits: []commitInfo{
			{SHA: "a", Date: "2026-11-22T11:00:00Z", AuthorLogin: "agent-a"},
			{SHA: "a", Date: "2026-11-22T11:00:00Z", AuthorLogin: "agent-a"},
			{SHA: "b", Date: "2026-11-20T11:00:00Z", AuthorLogin: "agent-b"},
			{SHA: "old", Date: "2026-11-10T11:00:00Z", AuthorLogin: "old-agent"},
		},
		Runs: []run{{Status: "in_progress", UpdatedAt: now.Format(time.RFC3339), Actor: struct {
			Login string `json:"login"`
		}{Login: "ci-actor"}}},
	}
	got := repoWorkload(repo, now)
	if got.Commits24h != 1 || got.Commits7d != 2 || got.Commits30d != 3 {
		t.Fatalf("commit totals = %d/%d/%d, want 1/2/3", got.Commits24h, got.Commits7d, got.Commits30d)
	}
	if strings.Join(got.Actors, ",") != "agent-a,agent-b,old-agent" {
		t.Fatalf("actors = %v", got.Actors)
	}
}

func TestRepoCISeparatesRunningWaitingAndFailure(t *testing.T) {
	repo := repository{Runs: []run{
		{Status: "in_progress"},
		{Status: "queued"},
		{Status: "completed", Conclusion: "failure", UpdatedAt: time.Now().Add(-time.Hour).Format(time.RFC3339)},
	}}
	if got := repoCI(repo); got != "1 RUN · 1 WAIT" {
		t.Fatalf("repoCI() = %q, want running and waiting counts", got)
	}
	if !queuedRun(run{Status: "waiting"}) || queuedRun(run{Status: "completed"}) {
		t.Fatal("queuedRun does not distinguish waiting from completed")
	}
}

func TestVisibleRunsShowsOperationalQueueAndRecentFailuresOnly(t *testing.T) {
	now := time.Now()
	m := model{repos: []repository{{FullName: "acme/ops", Name: "ops", Runs: []run{
		{ID: 1, Status: "completed", Conclusion: "success", UpdatedAt: now.Format(time.RFC3339)},
		{ID: 2, Status: "queued", UpdatedAt: now.Format(time.RFC3339)},
		{ID: 3, Status: "in_progress", UpdatedAt: now.Format(time.RFC3339)},
		{ID: 4, Status: "completed", Conclusion: "failure", UpdatedAt: now.Add(-time.Hour).Format(time.RFC3339)},
	}}}}
	got := m.visibleRuns()
	if len(got) != 3 {
		t.Fatalf("visible runs = %d, want 3", len(got))
	}
	if got[0].item.ID != 3 || got[1].item.ID != 2 || got[2].item.ID != 4 {
		t.Fatalf("operational queue order = %d, %d, %d", got[0].item.ID, got[1].item.ID, got[2].item.ID)
	}
}

func TestAgentWorkloadCombinesCommitAndCIAttribution(t *testing.T) {
	now := time.Now()
	repos := []repository{{FullName: "acme/one", Name: "one",
		Commits: []commitInfo{{SHA: "a", Date: now.Add(-time.Hour).Format(time.RFC3339), AuthorLogin: "agent-a"}},
		Runs: []run{{Status: "in_progress", UpdatedAt: now.Format(time.RFC3339), Actor: struct {
			Login string `json:"login"`
		}{Login: "agent-b"}}},
	}}
	got := collectAgentWorkload(repos, now)
	if len(got) != 2 {
		t.Fatalf("actors = %d, want 2", len(got))
	}
	found := map[string]agentWorkload{}
	for _, item := range got {
		found[item.Name] = item
	}
	if found["agent-a"].Commits7d != 1 || found["agent-b"].Running != 1 {
		t.Fatalf("agent workload attribution incorrect: %+v", got)
	}
}

func TestRepoWorkloadIncludesFeatureBranchCommitsAndDeduplicates(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	repo := repository{
		FullName: "acme/ops",
		Default:  "main",
		Commits: []commitInfo{
			{SHA: "shared", Date: "2026-11-22T10:00:00Z", AuthorLogin: "agent-main"},
		},
		Branches: []branch{
			{
				Name: "feature/search",
				History: []commitInfo{
					{SHA: "shared", Date: "2026-11-22T10:00:00Z", AuthorLogin: "agent-main"},
					{SHA: "feature-1", Date: "2026-11-22T11:00:00Z", AuthorLogin: "agent-feature"},
				},
			},
			{
				Name: "agent/worker",
				History: []commitInfo{
					{SHA: "worker-1", Date: "2026-11-21T11:00:00Z", AuthorLogin: "agent-worker"},
				},
			},
		},
	}
	got := repoWorkload(repo, now)
	if got.Commits24h != 2 || got.Commits7d != 3 {
		t.Fatalf("branch-aware commit totals = %d/%d, want 2/3", got.Commits24h, got.Commits7d)
	}
	if strings.Join(got.Actors, ",") != "agent-feature,agent-main,agent-worker" {
		t.Fatalf("branch-aware actors = %v", got.Actors)
	}
	activities := repoCommitActivity(repo, now)
	if len(activities) != 3 {
		t.Fatalf("unique branch commit activities = %d, want 3", len(activities))
	}
	foundFeatureBranch := false
	for _, item := range activities {
		if item.Commit.SHA == "feature-1" && item.Branch == "feature/search" {
			foundFeatureBranch = true
		}
	}
	if !foundFeatureBranch {
		t.Fatal("feature branch commit was not attributed to its branch")
	}
}

func TestRepoWorkloadCountsThirtyDayCommitsAndActiveBranches(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	repo := repository{
		FullName: "acme/ops",
		Commits: []commitInfo{
			{SHA: "today", Date: "2026-11-22T11:00:00Z", AuthorLogin: "human-a"},
		},
		Branches: []branch{
			{Name: "feature/one", History: []commitInfo{
				{SHA: "week", Date: "2026-11-17T11:00:00Z", AuthorLogin: "agent-a"},
				{SHA: "month", Date: "2026-11-01T11:00:00Z", AuthorLogin: "human-b"},
			}},
			{Name: "feature/two", History: []commitInfo{
				{SHA: "week-2", Date: "2026-11-18T11:00:00Z", AuthorLogin: "agent-b"},
			}},
		},
	}
	got := repoWorkload(repo, now)
	if got.Commits24h != 1 || got.Commits7d != 3 || got.Commits30d != 4 {
		t.Fatalf("activity = %d/%d/%d, want 1/3/4", got.Commits24h, got.Commits7d, got.Commits30d)
	}
	if got.ActiveBranches7d != 3 {
		t.Fatalf("active branches = %d, want 3", got.ActiveBranches7d)
	}
	if len(got.Actors) != 4 {
		t.Fatalf("contributors = %v, want four distinct commit authors", got.Actors)
	}
}

func TestRepoWorkloadMarksSampledBranchHistory(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	repo := repository{Branches: []branch{{Name: "busy", HistoryTruncated: true, History: []commitInfo{
		{SHA: "recent", Date: "2026-11-22T11:00:00Z", AuthorLogin: "agent"},
	}}}}
	got := repoWorkload(repo, now)
	if !got.ActivitySampled {
		t.Fatal("expected sampled activity to be marked")
	}
}

func TestAttentionItemsPrioritizeExplicitHumanActions(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	repos := []repository{{
		FullName: "acme/checkout",
		Runs: []run{
			{ID: 1, Name: "test", Status: "completed", Conclusion: "failure", UpdatedAt: now.Add(-time.Hour).Format(time.RFC3339), URL: "https://github.com/acme/checkout/actions/runs/1"},
			{ID: 2, Name: "deploy-preview", Status: "queued", CreatedAt: now.Add(-20 * time.Minute).Format(time.RFC3339)},
			{ID: 3, Name: "integration", Status: "in_progress", StartedAt: now.Add(-90 * time.Minute).Format(time.RFC3339)},
			{ID: 4, Name: "fast", Status: "in_progress", StartedAt: now.Add(-5 * time.Minute).Format(time.RFC3339)},
		},
		PullRequests: []pullRequestSummary{
			{Number: 10, Title: "Adjust checkout flow", ReviewDecision: "CHANGES_REQUESTED", UpdatedAt: now.Add(-2 * time.Hour).Format(time.RFC3339)},
			{Number: 11, Title: "Add payment retry", ReviewDecision: "REVIEW_REQUIRED", UpdatedAt: now.Add(-3 * time.Hour).Format(time.RFC3339)},
			{Number: 12, Title: "Fix migration", MergeStateStatus: "DIRTY", UpdatedAt: now.Add(-4 * time.Hour).Format(time.RFC3339)},
			{Number: 13, Title: "Improve docs", ReviewDecision: "REVIEW_REQUIRED", RequestedReviewers: []string{"reviewer-a"}, UpdatedAt: now.Add(-96 * time.Hour).Format(time.RFC3339)},
			{Number: 14, Title: "Draft change", IsDraft: true, ReviewDecision: "CHANGES_REQUESTED", UpdatedAt: now.Add(-time.Hour).Format(time.RFC3339)},
			{Number: 15, Title: "Approved change", ReviewDecision: "APPROVED", UpdatedAt: now.Add(-time.Hour).Format(time.RFC3339)},
		},
	}}
	got := collectAttentionItems(repos, now)
	wantKinds := []string{"CI FAILED", "CHANGES REQUESTED", "CONFLICTS", "REVIEW NEEDED", "CI QUEUED", "CI RUNNING", "REVIEW WAIT"}
	if len(got) != len(wantKinds) {
		t.Fatalf("attention items = %d, want %d: %+v", len(got), len(wantKinds), got)
	}
	for i, want := range wantKinds {
		if got[i].Kind != want {
			t.Errorf("attention[%d].Kind = %q, want %q", i, got[i].Kind, want)
		}
	}
	if got[0].URL != "https://github.com/acme/checkout/actions/runs/1" || got[0].RunID != 1 {
		t.Fatalf("failed workflow lost its actionable link: %+v", got[0])
	}
}

func TestAttentionThresholdsDoNotLabelRecentWorkAsStuck(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	repo := repository{
		Runs: []run{
			{Status: "queued", CreatedAt: now.Add(-5 * time.Minute).Format(time.RFC3339)},
			{Status: "in_progress", StartedAt: now.Add(-10 * time.Minute).Format(time.RFC3339)},
			{Status: "completed", Conclusion: "failure", UpdatedAt: now.Add(-48 * time.Hour).Format(time.RFC3339)},
		},
		PullRequests: []pullRequestSummary{
			{Number: 4, IsDraft: true, ReviewDecision: "REVIEW_REQUIRED", UpdatedAt: now.Add(-96 * time.Hour).Format(time.RFC3339)},
			{Number: 5, ReviewDecision: "APPROVED", UpdatedAt: now.Add(-96 * time.Hour).Format(time.RFC3339)},
		},
	}
	if got := collectAttentionItems([]repository{repo}, now); len(got) != 0 {
		t.Fatalf("recent, draft, approved, or old events should not create attention: %+v", got)
	}
}

func TestOverviewDefaultsToHumanActionQueues(t *testing.T) {
	now := time.Now().UTC()
	m := model{
		width: 120, height: 30, login: "operator", lastRepos: now,
		repos: []repository{{
			FullName: "acme/checkout", Name: "checkout", PRs: 1,
			PullRequests: []pullRequestSummary{{
				Number: 42, Title: "Payment retry", ReviewDecision: "REVIEW_REQUIRED",
				URL: "https://github.com/acme/checkout/pull/42",
			}},
		}},
	}
	view := m.View()
	for _, want := range []string{"DELIVERY OPERATIONS", "Overview", "NEEDS ATTENTION", "IN FLIGHT / WAITING", "REVIEW NEEDED"} {
		if !strings.Contains(view, want) {
			t.Errorf("operator overview does not contain %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "Agent runtime: NOT CONNECTED") {
		t.Errorf("GitHub-only dashboard should not spend a permanent line on an unconfigured optional source:\n%s", view)
	}
	for _, unwanted := range []string{"COMMITS · 30D", "CONTRIBUTORS", "ACTOR / AGENT"} {
		if strings.Contains(view, unwanted) {
			t.Errorf("operator overview still foregrounds %q:\n%s", unwanted, view)
		}
	}
}

func TestRepositoryPortfolioDoesNotTreatCommitVolumeAsHealth(t *testing.T) {
	now := time.Now().UTC()
	repo := repository{
		FullName: "acme/quiet", PushedAt: now.Add(-40 * 24 * time.Hour).Format(time.RFC3339),
		Commits: []commitInfo{{SHA: "many", Date: now.Format(time.RFC3339)}},
	}
	if got := repoPortfolioState(repo, now); got != "QUIET" {
		t.Fatalf("portfolio state = %q, want QUIET; commit volume must not override repository delivery state", got)
	}
}

func TestPullRequestReviewTextUsesExplicitReviewEvidence(t *testing.T) {
	cases := []struct {
		name string
		pr   pullRequestSummary
		want string
	}{
		{"draft", pullRequestSummary{IsDraft: true, ReviewDecision: "REVIEW_REQUIRED"}, "DRAFT"},
		{"changes requested", pullRequestSummary{ReviewDecision: "CHANGES_REQUESTED"}, "CHANGES REQUESTED"},
		{"reviewer missing", pullRequestSummary{ReviewDecision: "REVIEW_REQUIRED"}, "REVIEWER NOT REQUESTED"},
		{"waiting", pullRequestSummary{ReviewDecision: "REVIEW_REQUIRED", RequestedReviewers: []string{"alice"}}, "AWAITING alice"},
		{"waiting reviewer list truncated", pullRequestSummary{ReviewDecision: "REVIEW_REQUIRED", RequestedReviewers: []string{"alice"}, RequestedReviewersTruncated: true}, "AWAITING alice, …"},
		{"approved", pullRequestSummary{ReviewDecision: "APPROVED"}, "APPROVED"},
		{"unknown", pullRequestSummary{}, "REVIEW STATUS UNKNOWN"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pullRequestReviewText(tc.pr); got != tc.want {
				t.Fatalf("review text = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestOverviewResponsiveLayoutAtEightyColumns(t *testing.T) {
	now := time.Now().UTC()
	m := model{width: 80, height: 24, lastRepos: now, repos: []repository{{FullName: "acme/api", Name: "api"}}}
	view := m.View()
	for _, want := range []string{"Overview", "Repositories", "CI queue", "NEEDS ATTENTION"} {
		if !strings.Contains(view, want) {
			t.Errorf("80-column overview missing %q:\n%s", want, view)
		}
	}
}

func TestMoveSelectsPullRequestsInRepositoryDetail(t *testing.T) {
	m := model{
		focus: 1, detail: true,
		repos: []repository{{
			FullName: "acme/api",
			PullRequests: []pullRequestSummary{
				{Number: 10, Title: "first"},
				{Number: 11, Title: "second"},
				{Number: 12, Title: "third"},
			},
		}},
	}
	m.move(1)
	if m.prCursor != 1 {
		t.Fatalf("PR cursor = %d, want 1", m.prCursor)
	}
	m.move(1)
	m.move(1)
	if m.prCursor != 2 {
		t.Fatalf("PR cursor = %d, want 2", m.prCursor)
	}
	m.move(1)
	if m.prCursor != 2 {
		t.Fatalf("PR cursor moved past final item: %d", m.prCursor)
	}
	m.move(-10)
	if m.prCursor != 0 {
		t.Fatalf("PR cursor did not clamp to first item: %d", m.prCursor)
	}
}

func TestRepositoryDetailShowsSelectablePullRequests(t *testing.T) {
	now := time.Now().UTC()
	m := model{
		width: 100, height: 24, focus: 1, detail: true,
		repos: []repository{{
			FullName: "acme/api", Name: "api", PRs: 2, PushedAt: now.Format(time.RFC3339),
			PullRequests: []pullRequestSummary{
				{Number: 42, Title: "Add checkout retry", ReviewDecision: "REVIEW_REQUIRED", RequestedReviewers: []string{"reviewer-a"}, UpdatedAt: now.Add(-time.Hour).Format(time.RFC3339), URL: "https://github.com/acme/api/pull/42"},
				{Number: 41, Title: "Fix payment retry", ReviewDecision: "CHANGES_REQUESTED", UpdatedAt: now.Add(-2 * time.Hour).Format(time.RFC3339), URL: "https://github.com/acme/api/pull/41"},
			},
		}},
	}
	view := m.repoDetailView()
	for _, want := range []string{"OPEN PULL REQUESTS", "ENTER / o OPEN", "#42", "AWAITING reviewer-a", "#41", "CHANGES REQUESTED"} {
		if !strings.Contains(view, want) {
			t.Errorf("repository detail missing %q:\n%s", want, view)
		}
	}
}

func TestCompactOverviewKeepsGitHubStateWithoutExternalSourceNag(t *testing.T) {
	m := model{width: 40, height: 14, focus: 0}
	view := m.View()
	for _, want := range []string{"ATT 0", "NEEDS ATTENTION"} {
		if !strings.Contains(view, want) {
			t.Errorf("40-column overview missing %q:\n%s", want, view)
		}
	}
}

func TestShortTerminalOverviewDoesNotOverflowHeight(t *testing.T) {
	for _, height := range []int{10, 11, 12, 13, 16, 17} {
		m := model{width: 80, height: height, focus: 0}
		lines := strings.Split(m.View(), "\n")
		if len(lines) > height {
			t.Errorf("overview rendered %d lines at terminal height %d", len(lines), height)
		}
	}
}

func TestIncompletePullRequestSampleIsVisibleAsAttention(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	repo := repository{
		FullName: "acme/busy", Name: "busy", URL: "https://github.com/acme/busy",
		PRs: 14, PRsTruncated: true,
		PullRequests: []pullRequestSummary{{Number: 14, Title: "Recent PR", ReviewDecision: "APPROVED"}},
	}
	items := collectAttentionItems([]repository{repo}, now)
	if len(items) != 1 {
		t.Fatalf("attention items = %d, want sample warning: %+v", len(items), items)
	}
	if items[0].Kind != "PR SAMPLE" || items[0].URL != repo.URL {
		t.Fatalf("incomplete PR sample is not actionable: %+v", items[0])
	}
	if got := repoPortfolioState(repo, now); got != "ATTENTION" {
		t.Fatalf("portfolio state = %q, want ATTENTION while PR sample is incomplete", got)
	}
}

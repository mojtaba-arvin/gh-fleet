package main

import (
	"strings"
	"testing"
	"time"
)

func TestDefaultDashboardRendersWideCommitHistogram(t *testing.T) {
	now := time.Now().UTC()
	m := model{
		width:  100,
		height: 30,
		repos: []repository{{
			FullName:       "acme/active",
			Name:           "active",
			Default:        "main",
			CommitsFetched: now,
			Commits: []commitInfo{
				{SHA: "one", Date: now.Add(-time.Minute).Format(time.RFC3339)},
				{SHA: "two", Date: now.Add(-time.Hour).Format(time.RFC3339)},
			},
		}},
	}
	view := strings.Join(m.renderCommitChart(90, 12), "\n")
	for _, want := range []string{"active", "2 commits", "peak 1", "now", "█"} {
		if !strings.Contains(view, want) {
			t.Errorf("default dashboard chart does not contain %q:\n%s", want, view)
		}
	}
}

func TestNarrowDashboardShowsOperationalRepositoryWorkload(t *testing.T) {
	now := time.Now().UTC()
	m := model{
		width: 80, height: 30,
		repos: []repository{{
			FullName: "acme/active", Name: "active", Default: "main",
			CommitsFetched: now,
			Commits: []commitInfo{
				{SHA: "one", Date: now.Add(-time.Minute).Format(time.RFC3339), AuthorLogin: "agent-one"},
				{SHA: "two", Date: now.Add(-time.Hour).Format(time.RFC3339), AuthorLogin: "agent-one"},
			},
			Runs: []run{{Status: "in_progress", Actor: struct {
				Login string `json:"login"`
			}{Login: "agent-two"}}},
		}},
	}
	view := m.narrowView()
	for _, want := range []string{"Repositories", "active", "agent-one", "2/2"} {
		if !strings.Contains(view, want) {
			t.Errorf("narrow operations dashboard does not contain %q:\n%s", want, view)
		}
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

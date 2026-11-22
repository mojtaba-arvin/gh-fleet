package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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

func TestDefaultDashboardStartsOnActivityAndShowsRepositoryHistograms(t *testing.T) {
	cachePath := filepath.Join(t.TempDir(), "cache.json")
	client := &apiClient{cache: make(map[string][]run), etags: make(map[string]string)}
	m := newModel(client, cachePath)
	if m.focus != 1 {
		t.Fatalf("default focus = %d, want Activity section (1)", m.focus)
	}

	now := time.Now().UTC()
	m.repos = []repository{
		{
			FullName: "acme/alpha",
			Name: "alpha",
			CommitsFetched: now,
			Commits: []commitInfo{
				{SHA: "a1", Date: now.Add(-20 * time.Minute).Format(time.RFC3339)},
				{SHA: "a2", Date: now.Add(-40 * time.Minute).Format(time.RFC3339)},
			},
		},
		{
			FullName: "acme/beta",
			Name: "beta",
			CommitsFetched: now,
			Commits: []commitInfo{
				{SHA: "b1", Date: now.Add(-90 * time.Minute).Format(time.RFC3339)},
			},
		},
	}
	rendered := strings.Join(m.renderCommitChart(90, 10), "\n")
	for _, want := range []string{"alpha", "2 commits", "beta", "1 commits"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("default dashboard chart is missing %q:\n%s", want, rendered)
		}
	}
}

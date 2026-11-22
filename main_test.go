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

func TestNarrowDefaultDashboardShowsCommitChartsImmediately(t *testing.T) {
	now := time.Now().UTC()
	m := model{
		width:  80,
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
	view := m.narrowView()
	for _, want := range []string{"OVERVIEW", "2 commits", "REPOSITORIES", "█"} {
		if !strings.Contains(view, want) {
			t.Errorf("narrow default dashboard does not contain %q:\n%s", want, view)
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

func TestCalendarHeatmapRowsRendersFullYearAndMonthLabels(t *testing.T) {
	now:=time.Date(2026,11,22,12,0,0,0,time.UTC)
	repo:=repository{FullName:"acme/calendar",Commits:[]commitInfo{
		{SHA:"a",Date:"2026-11-22T10:00:00Z"},{SHA:"b",Date:"2026-11-22T11:00:00Z"},
		{SHA:"c",Date:"2026-03-10T11:00:00Z"},{SHA:"old",Date:"2025-10-01T11:00:00Z"},
	}}
	rows:=calendarHeatmapRows(repo,now,53)
	if len(rows)!=8 { t.Fatalf("calendar rows=%d, want 8",len(rows)) }
	if !strings.Contains(rows[0],"Jan") || !strings.Contains(rows[0],"Nov") { t.Fatalf("month labels missing: %q",rows[0]) }
	for i,row:=range rows[1:] { if width(row)!=53 { t.Errorf("row %d width=%d, want 53",i,width(row)) } }
}

func TestChartReposRanksEveryActiveYearRepository(t *testing.T) {
	now:=time.Now().UTC()
	m:=model{commitPeriod:365,repos:[]repository{
		{FullName:"acme/quiet",Name:"quiet",Commits:[]commitInfo{{SHA:"q",Date:now.Add(-time.Hour).Format(time.RFC3339)}}},
		{FullName:"acme/busy",Name:"busy",Commits:[]commitInfo{{SHA:"b1",Date:now.Add(-time.Hour).Format(time.RFC3339)},{SHA:"b2",Date:now.Add(-30*time.Minute).Format(time.RFC3339)}}},
		{FullName:"acme/inactive",Name:"inactive",Commits:[]commitInfo{{SHA:"old",Date:now.AddDate(-2,0,0).Format(time.RFC3339)}}},
	}}
	got:=m.chartRepos()
	if len(got)!=2 { t.Fatalf("active yearly repositories=%d, want 2",len(got)) }
	if got[0].FullName!="acme/busy" { t.Fatalf("first ranked repo=%s, want acme/busy",got[0].FullName) }
}

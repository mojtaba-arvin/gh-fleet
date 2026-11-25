package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func TestSelectedRowFillsEntireTerminalWidth(t *testing.T) {
	const width = 96
	got := selectedRow("CI FAILED  api  unit-tests", width)
	if actual := lipglossWidth(got); actual != width {
		t.Fatalf("selected row width = %d, want %d", actual, width)
	}
}

func lipglossWidth(value string) int {
	return width(value)
}

func TestCIHealthChartShowsCopyableStatusBreakdown(t *testing.T) {
	m := model{width: 100, repos: []repository{{Runs: []run{
		{Status: "completed", Conclusion: "success"},
		{Status: "completed", Conclusion: "failure"},
		{Status: "in_progress"},
		{Status: "queued"},
	}}}}
	chart := m.renderCIHealthChart(100)
	for _, want := range []string{"CI SIGNAL", "PASS 1", "FAIL 1", "RUN 1", "WAIT 1"} {
		if !strings.Contains(chart, want) {
			t.Errorf("CI chart missing %q: %q", want, chart)
		}
	}
}

func TestForceRefreshAndCacheClearKeys(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "cache.json")
	if err := os.WriteFile(cachePath, []byte("cached"), 0600); err != nil {
		t.Fatal(err)
	}
	client := &apiClient{
		etags: map[string]string{"repos": "etag-1"},
		cache: map[string][]run{"acme/api": {{ID: 7}}},
	}
	m := model{
		api: client, cachePath: cachePath,
		busyRuns: map[string]bool{}, busyBranches: map[string]bool{},
		queuedRuns: map[string]bool{}, retryAt: map[string]time.Time{},
		failures: map[string]int{}, animationEnabled: false,
	}
	updated, cmd := m.Update(tea.KeyPressMsg{Code: 'R', Text: "R"})
	got := updated.(model)
	if cmd == nil || !got.busyRepos {
		t.Fatal("R did not start a force refresh")
	}
	if len(client.etags) != 0 || len(client.cache) != 0 {
		t.Fatalf("force refresh retained ETags/cache: etags=%v cache=%v", client.etags, client.cache)
	}

	got.busyRepos = false
	updated, cmd = got.Update(tea.KeyPressMsg{Code: 'C', Text: "C"})
	got = updated.(model)
	if cmd == nil || !got.busyRepos {
		t.Fatal("C did not start a cache-clearing refresh")
	}
	if _, err := os.Stat(cachePath); !os.IsNotExist(err) {
		t.Fatalf("cache file still exists after C: err=%v", err)
	}
}

func TestSelectedRowStripsNestedANSIAndFillsFullWidth(t *testing.T) {
	const width = 96
	colored := titleStyle.Render("repo") + " " + badStyle.Render("FAILED")
	got := selectedRow(colored, width)
	if actual := lipglossWidth(got); actual != width {
		t.Fatalf("selected row width = %d, want %d", actual, width)
	}
	if strings.Contains(got, "\x1b[") {
		t.Fatal("selected row retained nested ANSI styles that can interrupt its full-width background")
	}
}

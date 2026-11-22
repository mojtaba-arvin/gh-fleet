package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func agentTestEvent(now time.Time, id, eventType, state string, sequence int64) agentEvent {
	return agentEvent{
		SchemaVersion:            "1",
		EventID:                  id,
		Sequence:                 sequence,
		EventType:                eventType,
		OccurredAt:               now.UTC().Format(time.RFC3339),
		AgentID:                  "runner-local-01",
		TaskID:                   "task-42",
		Repository:               "example/service",
		Branch:                   "agent/fix-timeout",
		State:                    state,
		Source:                   "example-runner/1.0",
		HeartbeatIntervalSeconds: 10,
	}
}

func applyTestAgentEvent(t *testing.T, tasks map[string]agentTask, seen map[string]struct{}, order *[]string, event agentEvent, now time.Time) {
	t.Helper()
	if err := applyAgentEvent(tasks, seen, order, event, now); err != nil {
		t.Fatalf("applyAgentEvent(%s): %v", event.EventType, err)
	}
}

func TestAgentReducerTracksLifecycleAndHeartbeatWithoutChangingState(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	tasks := map[string]agentTask{}
	seen := map[string]struct{}{}
	var order []string

	start := agentTestEvent(now, "evt-start", "agent.started", "starting", 1)
	start.Summary = "Preparing workspace"
	start.WorkflowRunID = json.RawMessage("987654")
	applyTestAgentEvent(t, tasks, seen, &order, start, now)

	progress := agentTestEvent(now.Add(10*time.Second), "evt-progress", "agent.progress", "working", 2)
	progress.Summary = "\x1b[31mEditing files\x1b[0m\nsafe summary"
	applyTestAgentEvent(t, tasks, seen, &order, progress, now.Add(10*time.Second))

	heartbeat := agentTestEvent(now.Add(20*time.Second), "evt-heartbeat", "agent.heartbeat", "", 3)
	applyTestAgentEvent(t, tasks, seen, &order, heartbeat, now.Add(20*time.Second))

	task := tasks[agentTaskKey(start.AgentID, start.TaskID)]
	if task.State != "working" {
		t.Fatalf("heartbeat changed lifecycle state to %q", task.State)
	}
	if task.WorkflowRunID != "987654" {
		t.Fatalf("workflow run ID = %q, want 987654", task.WorkflowRunID)
	}
	if strings.ContainsAny(task.Summary, "\x1b\n\r") || !strings.Contains(task.Summary, "Editing files") {
		t.Fatalf("summary was not safely normalized: %q", task.Summary)
	}
	if task.LastSignalAt != now.Add(20*time.Second) {
		t.Fatalf("last signal = %s, want heartbeat timestamp", task.LastSignalAt)
	}
	if task.HeartbeatInterval != 10*time.Second {
		t.Fatalf("heartbeat interval = %s, want 10s", task.HeartbeatInterval)
	}
}

func TestAgentReducerDeduplicatesAndRejectsOutOfOrderSequence(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	tasks := map[string]agentTask{}
	seen := map[string]struct{}{}
	var order []string
	start := agentTestEvent(now, "evt-start", "agent.started", "starting", 10)
	applyTestAgentEvent(t, tasks, seen, &order, start, now)

	progress := agentTestEvent(now.Add(time.Second), "evt-progress", "agent.progress", "working", 11)
	applyTestAgentEvent(t, tasks, seen, &order, progress, now.Add(time.Second))

	if err := applyAgentEvent(tasks, seen, &order, progress, now.Add(2*time.Second)); err == nil || err.Error() != "duplicate event_id" {
		t.Fatalf("duplicate event should be ignored, got %v", err)
	}
	old := agentTestEvent(now.Add(2*time.Second), "evt-old", "agent.waiting", "waiting", 9)
	old.Reason = "review"
	if err := applyAgentEvent(tasks, seen, &order, old, now.Add(2*time.Second)); err == nil || !strings.Contains(err.Error(), "sequence") {
		t.Fatalf("out-of-order sequence should be rejected, got %v", err)
	}
	task := tasks[agentTaskKey(start.AgentID, start.TaskID)]
	if task.State != "working" || task.Sequence != 11 {
		t.Fatalf("rejected events changed task state: %+v", task)
	}
}

func TestAgentTerminalStateCannotBeResurrectedByHeartbeatOrProgress(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	tasks := map[string]agentTask{}
	seen := map[string]struct{}{}
	var order []string
	start := agentTestEvent(now, "evt-start", "agent.started", "starting", 1)
	applyTestAgentEvent(t, tasks, seen, &order, start, now)
	completed := agentTestEvent(now.Add(time.Second), "evt-done", "agent.completed", "completed", 2)
	applyTestAgentEvent(t, tasks, seen, &order, completed, now.Add(time.Second))

	heartbeat := agentTestEvent(now.Add(2*time.Second), "evt-late-heartbeat", "agent.heartbeat", "", 3)
	if err := applyAgentEvent(tasks, seen, &order, heartbeat, now.Add(2*time.Second)); err == nil {
		t.Fatal("late heartbeat must not be accepted after a terminal event")
	}
	progress := agentTestEvent(now.Add(3*time.Second), "evt-late-progress", "agent.progress", "working", 4)
	if err := applyAgentEvent(tasks, seen, &order, progress, now.Add(3*time.Second)); err == nil {
		t.Fatal("progress must not resurrect a terminal task")
	}
	task := tasks[agentTaskKey(start.AgentID, start.TaskID)]
	if task.State != "completed" || task.LastSignalAt != now.Add(time.Second) {
		t.Fatalf("terminal task changed after late events: %+v", task)
	}
}

func TestAgentReducerRequiresStartAndControlledWaitReason(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	tasks := map[string]agentTask{}
	seen := map[string]struct{}{}
	var order []string
	orphan := agentTestEvent(now, "evt-orphan", "agent.progress", "working", 1)
	if err := applyAgentEvent(tasks, seen, &order, orphan, now); err == nil {
		t.Fatal("orphan progress must require agent.started")
	}
	start := agentTestEvent(now, "evt-start", "agent.started", "starting", 1)
	applyTestAgentEvent(t, tasks, seen, &order, start, now)

	waiting := agentTestEvent(now.Add(time.Second), "evt-wait", "agent.waiting", "waiting", 2)
	if err := applyAgentEvent(tasks, seen, &order, waiting, now.Add(time.Second)); err == nil {
		t.Fatal("waiting without a reason must be rejected")
	}
	waiting.Reason = "approval"
	waiting.Summary = "Needs environment approval"
	applyTestAgentEvent(t, tasks, seen, &order, waiting, now.Add(time.Second))
	task := tasks[agentTaskKey(start.AgentID, start.TaskID)]
	if task.State != "waiting" || task.Reason != "approval" {
		t.Fatalf("waiting state was not retained: %+v", task)
	}
}

func TestAgentValidationRejectsUnsupportedOrUnsafeEvents(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	base := agentTestEvent(now, "evt-valid", "agent.started", "starting", 1)
	cases := []struct {
		name string
		edit func(*agentEvent)
	}{
		{"schema", func(e *agentEvent) { e.SchemaVersion = "2" }},
		{"repository", func(e *agentEvent) { e.Repository = "example/../service" }},
		{"branch", func(e *agentEvent) { e.Branch = "feature/\x1b[31munsafe" }},
		{"commit sha", func(e *agentEvent) { e.CommitSHA = "not-a-sha" }},
		{"unknown event", func(e *agentEvent) { e.EventType = "agent.tool_output" }},
		{"wrong state", func(e *agentEvent) { e.State = "working" }},
		{"heartbeat interval", func(e *agentEvent) { e.HeartbeatIntervalSeconds = 2 }},
		{"reason", func(e *agentEvent) { e.Reason = "unbounded-free-text" }},
		{"workflow ID", func(e *agentEvent) { e.WorkflowRunID = json.RawMessage("-3") }},
		{"source", func(e *agentEvent) { e.Source = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			event := base
			tc.edit(&event)
			if _, _, err := validateAgentEvent(event, now); err == nil {
				t.Fatal("expected event to be rejected")
			}
		})
	}
}

func TestAgentStalenessUsesThreeHeartbeatIntervalsWithMinimumGrace(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	task := agentTask{State: "working", LastSignalAt: now.Add(-59 * time.Second), HeartbeatInterval: 10 * time.Second}
	if isAgentTaskStale(task, now) {
		t.Fatal("task should remain fresh within the 60-second minimum grace")
	}
	task.LastSignalAt = now.Add(-61 * time.Second)
	if !isAgentTaskStale(task, now) {
		t.Fatal("task should be stale after the 60-second minimum grace")
	}
	task.HeartbeatInterval = 30 * time.Second
	task.LastSignalAt = now.Add(-89 * time.Second)
	if isAgentTaskStale(task, now) {
		t.Fatal("30-second heartbeat should have a 90-second stale threshold")
	}
	task.LastSignalAt = now.Add(-91 * time.Second)
	if !isAgentTaskStale(task, now) {
		t.Fatal("30-second heartbeat should be stale after 90 seconds")
	}
	task.State = "completed"
	if isAgentTaskStale(task, now) {
		t.Fatal("terminal state must not be marked stale")
	}
}

func TestReadAgentSpoolWaitsForCompleteLineAndReadsIncrementally(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "agent-events.jsonl")
	event := agentTestEvent(time.Now().UTC().Truncate(time.Second), "evt-start", "agent.started", "starting", 1)
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(raw[:len(raw)-2]); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	first, err := readAgentSpool(path, agentSpoolCursor{})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Events) != 0 || len(first.Cursor.Pending) == 0 {
		t.Fatalf("incomplete JSON line was not retained: %+v", first)
	}
	file, err = os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(append(raw[len(raw)-2:], '\n')); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := readAgentSpool(path, first.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Events) != 1 || second.Events[0].EventID != "evt-start" {
		t.Fatalf("completed JSON line was not delivered: %+v", second)
	}
	if second.Cursor.Offset <= first.Cursor.Offset || len(second.Cursor.Pending) != 0 {
		t.Fatalf("cursor did not advance cleanly: first=%+v second=%+v", first.Cursor, second.Cursor)
	}
}

func TestReadAgentSpoolResetsAfterTruncation(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "agent-events.jsonl")
	now := time.Now().UTC().Truncate(time.Second)
	start := agentTestEvent(now, "evt-start", "agent.started", "starting", 1)
	progress := agentTestEvent(now.Add(time.Second), "evt-progress", "agent.progress", "working", 2)
	one, _ := json.Marshal(start)
	two, _ := json.Marshal(progress)
	if err := os.WriteFile(path, append(append(append([]byte{}, one...), '\n'), append(two, '\n')...), 0600); err != nil {
		t.Fatal(err)
	}
	first, err := readAgentSpool(path, agentSpoolCursor{})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Events) != 2 {
		t.Fatalf("initial spool events = %d, want 2", len(first.Events))
	}
	replacement := agentTestEvent(now.Add(2*time.Second), "evt-new-start", "agent.started", "starting", 1)
	replacement.AgentID = "runner-next"
	raw, _ := json.Marshal(replacement)
	if len(raw)+1 >= int(first.Cursor.Offset) {
		t.Fatal("test setup did not create a shorter replacement spool")
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	second, err := readAgentSpool(path, first.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Reset || len(second.Events) != 1 || second.Events[0].AgentID != "runner-next" {
		t.Fatalf("spool truncation was not detected: %+v", second)
	}
}

func TestReadAgentSpoolRejectsSymlinksAndPermissiveFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission and symlink semantics are tested on Unix")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "target.jsonl")
	if err := os.WriteFile(target, []byte{}, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.jsonl")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := readAgentSpool(link, agentSpoolCursor{}); err == nil {
		t.Fatal("symlink spool must be rejected")
	}
	if err := os.Chmod(target, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readAgentSpool(target, agentSpoolCursor{}); err == nil {
		t.Fatal("group/world-readable spool must be rejected")
	}
}

func TestReadAgentSpoolRejectsOversizedInput(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "agent-events.jsonl")
	data := make([]byte, maxAgentSpoolBytes+1)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readAgentSpool(path, agentSpoolCursor{}); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized spool error = %v", err)
	}
}

func TestAgentTasksAppearInOperatorAttentionAndInFlightQueues(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	tasks := map[string]agentTask{
		"blocked": {
			AgentID: "agent-a", TaskID: "task-blocked", Repository: "acme/api", State: "blocked",
			Reason: "approval", Summary: "Needs deployment approval", LastSignalAt: now, LastEventAt: now,
		},
		"failed": {
			AgentID: "agent-b", TaskID: "task-failed", Repository: "acme/web", State: "failed",
			Summary: "Dependency installation failed", LastSignalAt: now, LastEventAt: now,
		},
		"waiting": {
			AgentID: "agent-c", TaskID: "task-waiting", Repository: "acme/worker", State: "waiting",
			Reason: "ci", Summary: "Waiting for checks", LastSignalAt: now, HeartbeatInterval: 30 * time.Second,
		},
		"stale": {
			AgentID: "agent-d", TaskID: "task-stale", Repository: "acme/worker", State: "working",
			LastSignalAt: now.Add(-2 * time.Minute), HeartbeatInterval: 10 * time.Second,
		},
	}
	attention := collectAgentAttentionItems(tasks, now)
	kinds := map[string]bool{}
	for _, item := range attention {
		kinds[item.Kind] = true
	}
	for _, want := range []string{"AGENT BLOCKED", "AGENT FAILED", "AGENT STALE"} {
		if !kinds[want] {
			t.Errorf("attention queue missing %s: %+v", want, attention)
		}
	}
	inFlight := collectAgentInFlight(tasks, now)
	foundWaiting := false
	foundStale := false
	for _, item := range inFlight {
		foundWaiting = foundWaiting || item.Kind == "AGENT WAITING"
		foundStale = foundStale || item.Kind == "AGENT STALE"
	}
	if !foundWaiting {
		t.Fatalf("waiting task missing from in-flight queue: %+v", inFlight)
	}
	if foundStale {
		t.Fatalf("stale task should appear only in attention, not duplicated in in-flight: %+v", inFlight)
	}
}

func TestAgentRuntimeNoticeDistinguishesDisconnectedUnavailableAndConnected(t *testing.T) {
	now := time.Now().UTC()
	if got := (model{}).agentRuntimeNotice(); got != "Agent runtime: NOT CONNECTED" {
		t.Fatalf("unconfigured source notice = %q", got)
	}
	m := model{agentEventsPath: "/private/events.jsonl", agentLastErr: "permission denied"}
	if got := m.agentRuntimeNotice(); !strings.Contains(got, "SOURCE UNAVAILABLE") {
		t.Fatalf("unavailable source notice = %q", got)
	}
	m.agentLastErr = ""
	m.lastAgentPoll = now
	m.agentTasks = map[string]agentTask{
		"active": {State: "working", LastSignalAt: now, HeartbeatInterval: 30 * time.Second},
		"stale":  {State: "waiting", LastSignalAt: now.Add(-2 * time.Minute), HeartbeatInterval: 10 * time.Second},
	}
	if got := m.agentRuntimeNotice(); !strings.Contains(got, "2 active") || !strings.Contains(got, "1 stale") {
		t.Fatalf("connected source notice = %q", got)
	}
}

func TestGitHubOnlyOverviewDoesNotShowAgentTaskCounts(t *testing.T) {
	m := model{width: 120, height: 30, lastRepos: time.Now(), repos: []repository{{FullName: "acme/api", Name: "api"}}}
	m.agentTasks = map[string]agentTask{
		"agent": {AgentID: "runner", TaskID: "task-1", Repository: "acme/api", State: "working", LastSignalAt: time.Now()},
	}
	view := m.View()
	if !strings.Contains(view, "Agent runtime: NOT CONNECTED") {
		t.Fatalf("GitHub-only view does not disclose missing runtime source:\n%s", view)
	}
	if strings.Contains(view, "AGENTS 1") {
		t.Fatalf("GitHub-only view unexpectedly displays agent task count:\n%s", view)
	}
}

func TestReadAgentSpoolRejectsPermissiveParentDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix directory permission semantics are tested on Unix")
	}
	parent := filepath.Join(t.TempDir(), "shared")
	if err := os.Mkdir(parent, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parent, "agent-events.jsonl")
	if err := os.WriteFile(path, []byte{}, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readAgentSpool(path, agentSpoolCursor{}); err == nil || !strings.Contains(err.Error(), "parent directory") {
		t.Fatalf("permissive parent directory error = %v", err)
	}
}

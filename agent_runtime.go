package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

const (
	maxAgentSpoolBytes        = 4 << 20
	maxAgentEventLineBytes    = 16 << 10
	maxAgentSummaryBytes      = 240
	maxAgentTasks             = 1000
	maxAgentSeenEventIDs      = 20000
	maxAgentEventAge          = 30 * 24 * time.Hour
	agentTerminalRetention    = 24 * time.Hour
	agentDefaultHeartbeat     = 30 * time.Second
	agentMinimumStaleGrace    = 60 * time.Second
	agentMinimumHeartbeatSec  = 5
	agentMaximumHeartbeatSec  = 3600
	agentPollInterval         = 5 * time.Second
)

var (
	agentTokenPattern  = regexp.MustCompile("^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$")
	agentRepoPattern   = regexp.MustCompile("^[A-Za-z0-9][A-Za-z0-9_.-]{0,99}/[A-Za-z0-9][A-Za-z0-9_.-]{0,99}$")
	agentBranchPattern = regexp.MustCompile("^[A-Za-z0-9][A-Za-z0-9._/-]{0,254}$")
	agentSHAPattern    = regexp.MustCompile("^[A-Fa-f0-9]{7,64}$")
)

type agentEvent struct {
	SchemaVersion            string          `json:"schema_version"`
	EventID                  string          `json:"event_id"`
	Sequence                 int64           `json:"sequence,omitempty"`
	EventType                string          `json:"event_type"`
	OccurredAt               string          `json:"occurred_at"`
	AgentID                  string          `json:"agent_id"`
	TaskID                   string          `json:"task_id"`
	Repository               string          `json:"repository"`
	Branch                   string          `json:"branch,omitempty"`
	CommitSHA                string          `json:"commit_sha,omitempty"`
	PullRequest              int             `json:"pull_request,omitempty"`
	WorkflowRunID            json.RawMessage `json:"workflow_run_id,omitempty"`
	State                    string          `json:"state,omitempty"`
	Reason                   string          `json:"reason,omitempty"`
	Summary                  string          `json:"summary,omitempty"`
	Source                   string          `json:"source"`
	HeartbeatIntervalSeconds int             `json:"heartbeat_interval_seconds,omitempty"`
}

type agentTask struct {
	AgentID           string
	TaskID            string
	Repository        string
	Branch            string
	CommitSHA         string
	PullRequest       int
	WorkflowRunID     string
	State             string
	Reason            string
	Summary           string
	Source            string
	LastEventAt       time.Time
	LastSignalAt      time.Time
	HeartbeatInterval time.Duration
	Sequence          int64
	HasSequence       bool
}

type agentSpoolCursor struct {
	Offset     int64
	Pending    []byte
	Info       os.FileInfo
	Discarding bool
}

type agentSpoolBatch struct {
	Events       []agentEvent
	Cursor       agentSpoolCursor
	InvalidLines int
	Reset        bool
}

type agentPollMsg struct {
	Batch agentSpoolBatch
	Err   error
}

func agentTaskKey(agentID, taskID string) string {
	return agentID + "\x00" + taskID
}

func isAgentTerminal(state string) bool {
	switch state {
	case "completed", "failed", "cancelled":
		return true
	default:
		return false
	}
}

func isAgentActive(state string) bool {
	switch state {
	case "starting", "working", "waiting", "blocked":
		return true
	default:
		return false
	}
}

func agentStaleAfter(interval time.Duration) time.Duration {
	if interval <= 0 {
		interval = agentDefaultHeartbeat
	}
	grace := 3 * interval
	if grace < agentMinimumStaleGrace {
		return agentMinimumStaleGrace
	}
	return grace
}

func isAgentTaskStale(task agentTask, now time.Time) bool {
	if !isAgentActive(task.State) || task.LastSignalAt.IsZero() || task.LastSignalAt.After(now) {
		return false
	}
	return now.Sub(task.LastSignalAt) > agentStaleAfter(task.HeartbeatInterval)
}

func sanitizeAgentSummary(value string) string {
	value = strings.Join(strings.Fields(ansi.Strip(value)), " ")
	var out strings.Builder
	for _, r := range value {
		if unicode.IsControl(r) {
			r = ' '
		}
		if out.Len()+utf8.RuneLen(r) > maxAgentSummaryBytes {
			break
		}
		out.WriteRune(r)
	}
	return strings.Join(strings.Fields(out.String()), " ")
}

func normalizeWorkflowRunID(raw json.RawMessage) (string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	if raw[0] == '"' {
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return "", errors.New("workflow_run_id must be a string or positive integer")
		}
		if value == "" || len(value) > 64 {
			return "", errors.New("workflow_run_id has an invalid length")
		}
		for _, r := range value {
			if !(r >= '0' && r <= '9') && !(r >= 'A' && r <= 'Z') && !(r >= 'a' && r <= 'z') && !strings.ContainsRune("._:-", r) {
				return "", errors.New("workflow_run_id contains unsupported characters")
			}
		}
		return value, nil
	}
	var value int64
	if err := json.Unmarshal(raw, &value); err != nil || value <= 0 {
		return "", errors.New("workflow_run_id must be a positive integer or safe string")
	}
	return fmt.Sprintf("%d", value), nil
}

func validateAgentEvent(event agentEvent, now time.Time) (time.Time, string, error) {
	if event.SchemaVersion != "1" {
		return time.Time{}, "", errors.New("unsupported schema_version")
	}
	if !agentTokenPattern.MatchString(event.EventID) {
		return time.Time{}, "", errors.New("event_id is missing or invalid")
	}
	if !agentTokenPattern.MatchString(event.AgentID) || !agentTokenPattern.MatchString(event.TaskID) {
		return time.Time{}, "", errors.New("agent_id or task_id is missing or invalid")
	}
	if !agentRepoPattern.MatchString(event.Repository) {
		return time.Time{}, "", errors.New("repository must use owner/name format")
	}
	if event.Branch != "" && (!agentBranchPattern.MatchString(event.Branch) || strings.Contains(event.Branch, "..") || strings.Contains(event.Branch, "//")) {
		return time.Time{}, "", errors.New("branch contains unsupported characters")
	}
	if event.CommitSHA != "" && !agentSHAPattern.MatchString(event.CommitSHA) {
		return time.Time{}, "", errors.New("commit_sha must be 7–64 hexadecimal characters")
	}
	if event.PullRequest < 0 || event.PullRequest > 100000000 {
		return time.Time{}, "", errors.New("pull_request is outside the supported range")
	}
	if event.Sequence < 0 {
		return time.Time{}, "", errors.New("sequence cannot be negative")
	}
	sourceToken := strings.ReplaceAll(event.Source, "/", ":")
	if len(event.Source) == 0 || len(event.Source) > 128 || !agentTokenPattern.MatchString(sourceToken) {
		return time.Time{}, "", errors.New("source is missing or invalid")
	}
	if event.HeartbeatIntervalSeconds != 0 &&
		(event.HeartbeatIntervalSeconds < agentMinimumHeartbeatSec || event.HeartbeatIntervalSeconds > agentMaximumHeartbeatSec) {
		return time.Time{}, "", errors.New("heartbeat_interval_seconds must be between 5 and 3600")
	}
	occurredAt, err := time.Parse(time.RFC3339, event.OccurredAt)
	if err != nil {
		return time.Time{}, "", errors.New("occurred_at must be an RFC3339 timestamp")
	}
	occurredAt = occurredAt.UTC()
	if occurredAt.After(now.Add(2 * time.Minute)) {
		return time.Time{}, "", errors.New("occurred_at is too far in the future")
	}
	if now.Sub(occurredAt) > maxAgentEventAge {
		return time.Time{}, "", errors.New("event is older than the accepted retention window")
	}
	if len(event.Summary) > 4096 {
		return time.Time{}, "", errors.New("summary exceeds the input size limit")
	}

	expectedState := ""
	switch event.EventType {
	case "agent.started":
		expectedState = "starting"
	case "agent.heartbeat":
		if event.State != "" {
			return time.Time{}, "", errors.New("agent.heartbeat must not change lifecycle state")
		}
	case "agent.progress", "agent.resumed":
		expectedState = "working"
	case "agent.waiting":
		expectedState = "waiting"
	case "agent.blocked":
		expectedState = "blocked"
	case "agent.completed":
		expectedState = "completed"
	case "agent.failed":
		expectedState = "failed"
	case "agent.cancelled":
		expectedState = "cancelled"
	default:
		return time.Time{}, "", errors.New("unknown event_type")
	}
	if expectedState != "" && event.State != expectedState {
		return time.Time{}, "", fmt.Errorf("%s requires state=%q", event.EventType, expectedState)
	}
	if (event.State == "waiting" || event.State == "blocked") && event.Reason == "" {
		return time.Time{}, "", errors.New("waiting and blocked states require a reason")
	}
	switch event.Reason {
	case "", "ci", "review", "approval", "user", "dependency", "rate_limit", "external_tool":
	default:
		return time.Time{}, "", errors.New("reason is not an allowed reason code")
	}
	if event.Reason != "" && event.State != "waiting" && event.State != "blocked" {
		return time.Time{}, "", errors.New("reason is only valid for waiting or blocked states")
	}
	workflowID, err := normalizeWorkflowRunID(event.WorkflowRunID)
	if err != nil {
		return time.Time{}, "", err
	}
	return occurredAt, workflowID, nil
}

type oldAgentTask struct {
	key string
	at  time.Time
}

func pruneAgentTasks(tasks map[string]agentTask, now time.Time) {
	for key, task := range tasks {
		if isAgentTerminal(task.State) && !task.LastEventAt.IsZero() && now.Sub(task.LastEventAt) > agentTerminalRetention {
			delete(tasks, key)
		}
	}
	if len(tasks) <= maxAgentTasks {
		return
	}
	var terminal []oldAgentTask
	for key, task := range tasks {
		if isAgentTerminal(task.State) {
			terminal = append(terminal, oldAgentTask{key: key, at: task.LastEventAt})
		}
	}
	sort.Slice(terminal, func(i, j int) bool { return terminal[i].at.Before(terminal[j].at) })
	for _, item := range terminal {
		if len(tasks) <= maxAgentTasks {
			break
		}
		delete(tasks, item.key)
	}
}

func validateAgentTransition(task agentTask, eventType, newState string) error {
	if eventType == "agent.heartbeat" {
		return nil
	}
	if eventType == "agent.started" {
		return errors.New("agent.started is only valid for a new task_id")
	}
	switch task.State {
	case "starting", "working", "waiting", "blocked":
		switch newState {
		case "working", "waiting", "blocked", "completed", "failed", "cancelled":
			return nil
		}
	}
	return fmt.Errorf("invalid lifecycle transition from %s to %s", task.State, newState)
}

func applyAgentEvent(tasks map[string]agentTask, seen map[string]struct{}, seenOrder *[]string, event agentEvent, now time.Time) error {
	occurredAt, workflowID, err := validateAgentEvent(event, now)
	if err != nil {
		return err
	}
	if _, exists := seen[event.EventID]; exists {
		return errors.New("duplicate event_id")
	}

	key := agentTaskKey(event.AgentID, event.TaskID)
	task, exists := tasks[key]
	if !exists && event.EventType != "agent.started" {
		return errors.New("task must begin with agent.started")
	}
	if exists {
		if err := validateAgentTransition(task, event.EventType, event.State); err != nil {
			return err
		}
		if event.Sequence > 0 && task.HasSequence && event.Sequence <= task.Sequence {
			return errors.New("sequence is not newer than the last accepted event")
		}
		if event.Sequence == 0 && !occurredAt.After(task.LastEventAt) {
			return errors.New("event timestamp is not newer than the last accepted event")
		}
		if task.Repository != event.Repository {
			return errors.New("repository cannot change within a task")
		}
	}

	interval := time.Duration(event.HeartbeatIntervalSeconds) * time.Second
	if interval == 0 && exists {
		interval = task.HeartbeatInterval
	}
	if interval == 0 {
		interval = agentDefaultHeartbeat
	}
	if !exists {
		if len(tasks) >= maxAgentTasks {
			pruneAgentTasks(tasks, now)
		}
		if len(tasks) >= maxAgentTasks {
			return errors.New("active task limit reached")
		}
		task = agentTask{AgentID: event.AgentID, TaskID: event.TaskID, Repository: event.Repository}
	}
	if event.EventType != "agent.heartbeat" {
		task.State = event.State
		task.Reason = event.Reason
	}
	if event.Branch != "" {
		task.Branch = event.Branch
	}
	if event.CommitSHA != "" {
		task.CommitSHA = event.CommitSHA
	}
	if event.PullRequest != 0 {
		task.PullRequest = event.PullRequest
	}
	if workflowID != "" {
		task.WorkflowRunID = workflowID
	}
	if event.Summary != "" {
		task.Summary = sanitizeAgentSummary(event.Summary)
	}
	task.Source = event.Source
	task.HeartbeatInterval = interval
	if occurredAt.After(task.LastEventAt) || task.LastEventAt.IsZero() {
		task.LastEventAt = occurredAt
	}
	if occurredAt.After(task.LastSignalAt) || task.LastSignalAt.IsZero() {
		task.LastSignalAt = occurredAt
	}
	if event.Sequence > 0 {
		task.Sequence = event.Sequence
		task.HasSequence = true
	}
	tasks[key] = task

	seen[event.EventID] = struct{}{}
	*seenOrder = append(*seenOrder, event.EventID)
	if len(*seenOrder) > maxAgentSeenEventIDs {
		oldest := (*seenOrder)[0]
		*seenOrder = (*seenOrder)[1:]
		delete(seen, oldest)
	}
	return nil
}

func readAgentSpool(path string, cursor agentSpoolCursor) (agentSpoolBatch, error) {
	if strings.TrimSpace(path) == "" {
		return agentSpoolBatch{}, errors.New("agent event path is empty")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return agentSpoolBatch{}, fmt.Errorf("agent event source: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return agentSpoolBatch{}, errors.New("agent event source must be a regular, non-symlink file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return agentSpoolBatch{}, errors.New("agent event source permissions must be 0600 or stricter")
	}
	if info.Size() > maxAgentSpoolBytes {
		return agentSpoolBatch{}, fmt.Errorf("agent event source exceeds %d MiB; rotate it and re-emit agent.started snapshots for active tasks", maxAgentSpoolBytes>>20)
	}

	reset := cursor.Info != nil && (!os.SameFile(cursor.Info, info) || info.Size() < cursor.Offset)
	offset := cursor.Offset
	pending := append([]byte(nil), cursor.Pending...)
	discarding := cursor.Discarding
	if reset {
		offset, pending, discarding = 0, nil, false
	}
	if offset < 0 || offset > info.Size() {
		offset, pending, discarding, reset = 0, nil, false, true
	}

	file, err := os.Open(path)
	if err != nil {
		return agentSpoolBatch{}, fmt.Errorf("open agent event source: %w", err)
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return agentSpoolBatch{}, fmt.Errorf("stat opened agent event source: %w", err)
	}
	if !os.SameFile(info, openedInfo) {
		return agentSpoolBatch{}, errors.New("agent event source changed while opening; retrying on next poll")
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return agentSpoolBatch{}, fmt.Errorf("seek agent event source: %w", err)
	}
	data, err := io.ReadAll(io.LimitReader(file, info.Size()-offset))
	if err != nil {
		return agentSpoolBatch{}, fmt.Errorf("read agent event source: %w", err)
	}
	combined := append(pending, data...)
	batch := agentSpoolBatch{Cursor: agentSpoolCursor{Offset: offset + int64(len(data)), Info: info}, Reset: reset}
	if discarding {
		newline := bytes.IndexByte(combined, '\n')
		if newline < 0 {
			batch.Cursor.Discarding = true
			return batch, nil
		}
		combined = combined[newline+1:]
	}
	for {
		newline := bytes.IndexByte(combined, '\n')
		if newline < 0 {
			break
		}
		line := bytes.TrimSuffix(combined[:newline], []byte("\r"))
		if len(line) > maxAgentEventLineBytes {
			batch.InvalidLines++
		} else if len(bytes.TrimSpace(line)) > 0 {
			var event agentEvent
			if err := json.Unmarshal(line, &event); err != nil {
				batch.InvalidLines++
			} else {
				batch.Events = append(batch.Events, event)
			}
		}
		combined = combined[newline+1:]
	}
	if len(combined) > maxAgentEventLineBytes {
		batch.InvalidLines++
		batch.Cursor.Discarding = true
	} else {
		batch.Cursor.Pending = append([]byte(nil), combined...)
	}
	return batch, nil
}

func agentPollCmd(path string, cursor agentSpoolCursor) tea.Cmd {
	return func() tea.Msg {
		batch, err := readAgentSpool(path, cursor)
		return agentPollMsg{Batch: batch, Err: err}
	}
}

func collectAgentAttentionItems(tasks map[string]agentTask, now time.Time) []attentionItem {
	var items []attentionItem
	for _, task := range tasks {
		age := time.Duration(0)
		if !task.LastSignalAt.IsZero() && now.After(task.LastSignalAt) {
			age = now.Sub(task.LastSignalAt)
		}
		item := attentionItem{
			Repo: task.Repository,
			Subject: fmt.Sprintf("Agent %s · task %s", task.AgentID, task.TaskID),
			Age: age,
		}
		switch {
		case task.State == "failed":
			item.Priority, item.Kind, item.Detail, item.Next = 1, "AGENT FAILED", task.Summary, "Inspect runner failure"
		case task.State == "blocked":
			item.Priority, item.Kind, item.Detail = 2, "AGENT BLOCKED", task.Reason+": "+task.Summary
			item.Next = agentReasonAction(task.Reason)
		case isAgentTaskStale(task, now):
			item.Priority, item.Kind, item.Detail, item.Next = 6, "AGENT STALE", "No signal within the expected heartbeat window", "Check runner heartbeat"
		default:
			continue
		}
		items = append(items, item)
	}
	return items
}

func agentReasonAction(reason string) string {
	switch reason {
	case "ci":
		return "Inspect linked CI run"
	case "review":
		return "Inspect review status"
	case "approval":
		return "Provide required approval"
	case "user":
		return "Respond to agent request"
	case "dependency":
		return "Unblock dependency"
	case "rate_limit":
		return "Check provider rate limit"
	case "external_tool":
		return "Inspect external tool"
	default:
		return "Inspect reported blocker"
	}
}

func collectAgentInFlight(tasks map[string]agentTask, now time.Time) []inFlightItem {
	var items []inFlightItem
	for _, task := range tasks {
		if !isAgentActive(task.State) {
			continue
		}
		kind, priority := "AGENT WORKING", 0
		switch task.State {
		case "starting":
			kind = "AGENT STARTING"
		case "waiting":
			kind, priority = "AGENT WAITING", 1
		case "blocked":
			continue
		}
		detail := "agent " + task.AgentID + " · task " + task.TaskID
		if task.Reason != "" {
			detail += " · " + task.Reason
		}
		if task.Summary != "" {
			detail += " · " + task.Summary
		}
		if task.Source != "" {
			detail += " · " + task.Source
		}
		if task.WorkflowRunID != "" {
			detail += " · workflow #" + task.WorkflowRunID
		}
		if isAgentTaskStale(task, now) {
			kind, priority = "AGENT STALE", 3
		}
		age := time.Duration(0)
		if !task.LastSignalAt.IsZero() && now.After(task.LastSignalAt) {
			age = now.Sub(task.LastSignalAt)
		}
		items = append(items, inFlightItem{
			Priority: priority, Kind: kind, Repo: task.Repository,
			Subject: task.TaskID, Detail: detail, Age: age,
		})
	}
	return items
}

func countAgentTasks(tasks map[string]agentTask, now time.Time) (active, stale int) {
	for _, task := range tasks {
		if !isAgentActive(task.State) {
			continue
		}
		active++
		if isAgentTaskStale(task, now) {
			stale++
		}
	}
	return active, stale
}

func (m model) agentRuntimeNotice() string {
	if m.agentEventsPath == "" {
		return "Agent runtime: NOT CONNECTED"
	}
	if m.agentLastErr != "" {
		return "Agent runtime: SOURCE UNAVAILABLE · check spool path and permissions"
	}
	if m.lastAgentPoll.IsZero() || m.busyAgentPoll {
		return "Agent runtime: CONNECTING"
	}
	active, stale := countAgentTasks(m.agentTasks, time.Now())
	notice := fmt.Sprintf("Agent runtime: CONNECTED · %d active · %d stale", active, stale)
	if m.agentRejected > 0 {
		notice += fmt.Sprintf(" · %d rejected events", m.agentRejected)
	}
	return notice
}

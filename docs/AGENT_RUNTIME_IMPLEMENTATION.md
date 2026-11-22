# Agent runtime integration — implementation design

## Goal

Turn the already documented provider-neutral event contract into a usable, low-cost, local observation path. The operator should see actual runner-reported task state beside GitHub facts without treating GitHub usernames or Actions actors as agent telemetry.

This phase is read-only. gh-fleet does not start, stop, approve, or send commands to agents.

## Scope and decisions

### Transport: local JSONL spool only

- Configure one existing JSONL file with `--agent-events PATH` or `GH_FLEET_AGENT_EVENTS`.
- The runner/orchestrator owns appending events. gh-fleet opens the file read-only.
- No HTTP listener, socket server, remote relay, database, daemon, or new dependency.
- A missing/unreadable/untrusted file is an explicit source error; it is never interpreted as an empty/healthy agent fleet.
- On Unix-like systems the spool must be a regular, non-symlink file with mode 0600 or stricter, inside a real parent directory with mode 0700 or stricter. Windows ACLs remain the operator's responsibility.

### Bounded incremental reader

- Poll every five seconds only when a path is configured; GitHub-only mode retains its 15-second UI tick.
- Read appended bytes from an in-memory offset; preserve an incomplete final line until its newline arrives.
- Detect truncation/replacement and restart the reducer from the new file. A producer rotating a file must write a fresh `agent.started` snapshot for each task that is still active before subsequent events for those tasks.
- Maximum spool size: 4 MiB. Maximum JSONL event line: 16 KiB. Oversized/malformed lines are rejected and counted; a file above the size limit is reported as an error until the producer rotates it.
- Polling and file reads happen in Bubble Tea commands, never in `Update` or `View`.

### Event validation and reducer

- Accept schema version `"1"` only; validate required IDs, repository, timestamps, event type, state, reason, source, SHA, PR/run IDs, and size limits.
- Sanitize summary text before presentation; strip ANSI/control characters and cap it at 240 characters.
- Deduplicate `event_id`; retain a bounded 20,000-ID window.
- Enforce strictly increasing per-task sequence when present. Without sequence, reject events whose timestamp is not newer than the last accepted event.
- Require `agent.started` before any other event for a task. Heartbeat updates liveness only and never changes lifecycle state.
- Terminal states (`completed`, `failed`, `cancelled`) cannot be resurrected by late heartbeats or later events for the same task ID. A retry uses a new task ID.
- Keep at most 1,000 task records; expire terminal records after 24 hours. Active records remain until terminal or process exit.
- Heartbeat interval defaults to 30 seconds; valid declaration is 5–3600 seconds. Mark a task stale after max(three intervals, 60 seconds) without a new accepted signal. Stale overlays the reported state; it is not a terminal state.

### Human-facing behavior

- Header reports whether the source is configured, connected, or unavailable; it shows active and stale task counts.
- The existing in-flight queue adds agent-starting/working/waiting tasks, with agent ID, task ID, repository, branch, and sanitized summary.
- Blocked, failed, and stale tasks enter Needs attention with an observable reason and a suggested next inspection. Completed/cancelled tasks leave the active queue.
- GitHub Actions state remains a separate item. Correlation is shown only when an explicit workflow run ID matches.
- No per-agent throughput/ranking, utilization score, inferred owner, model prompt, reasoning, tool arguments, file contents, environment variables, or secrets.

### CLI and compatibility

- GitHub-only operation remains the default and requires no telemetry file.
- `--agent-events` takes precedence over `GH_FLEET_AGENT_EVENTS`.
- `--version` remains usable without GitHub authentication.
- Telemetry is not persisted in the GitHub cache. It is reconstructed from the event spool on startup.

## Acceptance criteria

1. GitHub-only mode shows `Agent runtime: NOT CONNECTED` and makes no local event-file reads.
2. A configured valid spool is read incrementally and updates task state without blocking the UI.
3. Duplicate event IDs, out-of-order sequences, invalid states/reasons, malformed JSON, unsafe file permissions, symlinks, and oversized input are rejected without losing the last valid task state.
4. A heartbeat changes freshness, not lifecycle state.
5. Terminal tasks cannot be resurrected by a late heartbeat.
6. Stale is computed from the declared heartbeat interval and shown as a freshness warning, not a new lifecycle state.
7. A file replacement/truncation resets the cursor and reducer; the producer must re-emit a start snapshot for active tasks.
8. Overview and in-flight list show source attribution and task context, with no activity-based agent leaderboard.
9. Unit tests cover validation, deduplication, ordering, terminal protection, heartbeat freshness, rotation, and UI rendering.
10. Existing Go stable and Go 1.24 CI remains green.

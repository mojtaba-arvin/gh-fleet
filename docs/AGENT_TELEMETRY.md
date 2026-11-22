# Agent telemetry contract

## Purpose

GitHub APIs describe repository and CI activity. They cannot reliably tell gh-fleet whether an external coding agent is actively working, executing a tool, waiting for CI, blocked on approval, or finished.

This contract defines the minimal opt-in signal from an agent runner/orchestrator. It is provider-neutral; adapters can be written for individual agent frameworks without coupling the TUI to those frameworks.

## Transport recommendation

Start with a **local JSON Lines event spool**:

- Each event is one JSON object per line; independent runners can append events and gh-fleet can tail the file.
- Keep the first version local and read-only: observe agents, do not control them.
- Restrict file permissions to the current user, cap event size, and rotate by size/age.
- A Unix-domain socket is a good later option if lower latency or stronger delivery semantics are needed.
- If an HTTP receiver is later needed, bind to loopback by default, require a high-entropy local secret, cap request/body size, and never expose it publicly by default.
- For multi-host operation, prefer a small authenticated relay with TLS and per-runner credentials over exposing a workstation listener.

The first implementation should support one transport only. JSONL is the simplest durable baseline.

## Event envelope

| Field | Type | Requirement | Meaning |
| --- | --- | --- | --- |
| schema_version | string | required | Contract version, initially 1 |
| event_id | string | required | Unique ID for idempotent ingestion |
| sequence | integer | recommended | Monotonic sequence within a task/run |
| event_type | string | required | Stable lifecycle event name |
| occurred_at | RFC3339 timestamp | required | When the source says the event happened |
| agent_id | string | required | Stable runner/agent identity, not a GitHub username |
| task_id | string | required | Stable task/run identity |
| repository | owner/name | required | Target GitHub repository |
| branch | string | optional | Working branch |
| commit_sha | string | optional | Current head commit |
| pull_request | integer | optional | Associated PR number |
| workflow_run_id | integer/string | optional | Explicit CI correlation |
| state | string | required for state events | Lifecycle state |
| reason | string | required for waiting / blocked | Controlled reason code |
| summary | string | optional, bounded | Safe short human-readable status |
| source | string | required | Adapter/runner identity and version |
| heartbeat_interval_seconds | integer | optional | Expected heartbeat interval; 5–3600 seconds, default 30 |

Example (illustrative only):

    {
      "schema_version": "1",
      "event_id": "01JEXAMPLE0000000000000000",
      "sequence": 12,
      "event_type": "agent.waiting",
      "occurred_at": "2026-11-22T12:30:00Z",
      "agent_id": "runner-local-01",
      "task_id": "task-42",
      "repository": "example/service",
      "branch": "agent/fix-timeout",
      "commit_sha": "0123456789abcdef0123456789abcdef01234567",
      "pull_request": 123,
      "workflow_run_id": 987654,
      "state": "waiting",
      "reason": "ci",
      "summary": "Waiting for required checks",
      "source": "example-runner/1.0",
      "heartbeat_interval_seconds": 30
    }

## Lifecycle events

Use stable, low-cardinality event names:

- agent.started
- agent.heartbeat
- agent.progress
- agent.waiting
- agent.resumed
- agent.blocked
- agent.completed
- agent.failed
- agent.cancelled

Progress is optional and should be emitted sparingly, on meaningful step changes rather than every token or tool call. Heartbeats should be periodic and configurable; an initial target of 20–30 seconds is reasonable for a local runner, but the source must declare its expected interval.

Allowed states: starting, working, waiting, blocked, completed, failed, cancelled.

Allowed waiting reasons: ci, review, approval, user, dependency, rate_limit, external_tool.

A heartbeat updates liveness but does not imply progress. Stale is a derived freshness overlay, not a lifecycle state. Initially mark stale only after three expected heartbeat intervals, with a minimum grace period; retain the last reported lifecycle state and label it stale. A missing event source means not connected, not zero agents.

## Validation and reduction

The ingestion/reducer must:

1. Reject unsupported schema versions, unknown event types, malformed timestamps, invalid repository identifiers, oversized strings, and out-of-range numeric fields.
2. Deduplicate by event_id.
3. Prefer per-task sequence ordering; tolerate bounded out-of-order delivery and reject events older than the retained cursor.
4. Never let a late heartbeat resurrect a task after completed, failed, or cancelled.
5. Keep terminal states terminal unless a new task ID is created; retries should use an explicit attempt ID or incremented attempt.
6. Validate every state transition and preserve the last valid state when an event is rejected.
7. Expire or compact old event history with a bounded retention policy.
8. Treat payloads as untrusted data; escape terminal control sequences and strip unsafe control characters before rendering.

## Correlation rules

Use explicit task_id, agent_id, workflow_run_id, pull_request, and commit SHA first. Branch and repository are supporting context, not unique identifiers.

- A workflow run is linked to a task only when the event explicitly includes its run ID, or a trusted adapter event confirms the association.
- Matching repository + branch + time window may be shown as a possible match, never silently as a confirmed link.
- Multiple tasks on one repository are valid. Do not collapse them into one repository-level agent status.
- GitHub Actions actor.login remains the workflow trigger identity, not the task owner.

## Privacy and security

Never collect by default:

- system prompts, user prompts, model reasoning, or full model outputs
- raw tool arguments/results, shell command contents, or file contents
- environment variables, access tokens, cookies, credentials, or private keys
- full local filesystem paths or host identifiers that expose personal information

Collect only operational metadata needed to coordinate work. Bound and sanitize summary; make it opt-in if a runner might include sensitive content. Do not write secrets to logs. Keep local spool/socket permissions restricted to the current user. If a remote relay is implemented, use TLS, authentication, authorization by repository/task scope, replay protection, and bounded payload/rate limits.

## OpenTelemetry alignment

Where a runner already emits OpenTelemetry, adapters should reuse its trace/span context and established GenAI semantic attributes instead of inventing a parallel tracing vocabulary. Agent invocation and tool execution are duration-bearing spans; lifecycle changes such as waiting, resumed, completed, and failed are timestamped events. Do not ingest prompt/output content by default. The contract above is the small operational projection consumed by gh-fleet, not a replacement for distributed tracing.

## Compatibility

GitHub-only operation remains supported. When no agent event source is configured, show observed GitHub actors and CI states with an explicit Agent telemetry: not connected indicator. Never synthesize agent identities from commit or workflow usernames.


## gh-fleet local spool setup

The first gh-fleet adapter consumes one existing local JSONL file. It does not run a server or control the agent process.

Create a private spool on macOS/Linux:

    mkdir -p "$HOME/.cache/gh-fleet"
    chmod 700 "$HOME/.cache/gh-fleet"
    touch "$HOME/.cache/gh-fleet/agent-events.jsonl"
    chmod 600 "$HOME/.cache/gh-fleet/agent-events.jsonl"

Run the dashboard with:

    gh-fleet --agent-events "$HOME/.cache/gh-fleet/agent-events.jsonl"

Or set `GH_FLEET_AGENT_EVENTS` to the same path. The command-line flag takes precedence. The reader rejects symlinks, non-regular files, Unix files with group/other permissions, files over 4 MiB, and event lines over 16 KiB. It reads incrementally and does not write telemetry into the GitHub metadata cache.

Each record must be one complete JSON object followed by a newline. Producers should append a whole serialized event in one write where possible. If the spool is replaced or truncated, the reducer resets and the producer must re-emit an `agent.started` snapshot for every task that remains active before appending later events. This allows the new spool to rebuild state without treating a heartbeat as a start event.

The dashboard polls the configured file every five seconds. GitHub-only mode does not open or poll an agent event file.

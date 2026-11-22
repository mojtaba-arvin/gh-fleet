# Operations control-plane design

## Product objective

gh-fleet is an **operator console for coordinating AI coding agents across repositories**, not a contribution-graph viewer. Its primary job is to help an operator decide:

1. What is actively progressing?
2. What is waiting, blocked, stale, or failing?
3. Which agent, repository, branch, pull request, and CI run are connected?
4. What should be inspected or acted on next?

The overview must be understandable in a few seconds. Historical analytics are supporting context, not the primary screen.

## Evidence model: never confuse signals with state

Every displayed status must carry an evidence class:

| Evidence class | Examples | What it proves |
| --- | --- | --- |
| GitHub-observed | commit, branch head, PR, Actions run, review | GitHub recorded this event/state |
| Orchestrator-reported | agent started, heartbeat, waiting for CI, completed | The connected runner reported this lifecycle state |
| Derived | stale heartbeat, likely idle, risk/priority score | gh-fleet calculated an indicator from explicit inputs |

Do not infer a live agent from a commit author, workflow actor, branch name, or open PR. Do not infer that an agent is waiting for CI merely because a workflow is running. Render unknown as **Unknown / Not connected**, not Idle.

## Information architecture

### 1. Overview (default)

The first screen is designed for a human operator, not as an agent activity chart. Its questions are:

- What needs a human decision now?
- What is progressing or waiting?
- Which repository, PR, or workflow is involved?
- What is the next action?

The screen contains:
- a source/freshness header, including an explicit "Agent runtime: not connected" state until runner telemetry exists;
- a small summary strip for actionable items, open PRs, running CI, queued CI, and repository count;
- a prioritized **Needs attention** list with a concrete GitHub fact, age, next action, and destination;
- an **In flight / waiting** list for current CI and explicitly requested PR reviews.

The initial attention rules are recent CI failure, changes requested, review required without a requested reviewer, reported dirty merge state, queued CI older than 15 minutes, running CI older than 45 minutes, and requested review without an update for 72 hours. These are inspection cues, not diagnoses that a process is stuck or an SLA violation. Draft PRs are excluded from review-ready attention.

### 2. Repositories

The portfolio view has one row per repository, sorted by operational need rather than commit count. Each row shows a plain-language state, open PR and issue counts, last push age, and latest CI signal.

Repository detail shows the latest ten open PRs and their review decision / requested reviewers, current CI context, and links to GitHub. Additional PRs remain included in the total count; the detail view marks when its ten-item sample is truncated.

### 3. CI queue

The CI queue separates running, queued, and recent failed workflows. It shows workflow, repository, branch, trigger actor, update age, and log access. A trigger actor is never treated as an agent owner. Successful historical runs remain out of the urgent queue.

### 4. GitHub identities

An optional diagnostic view groups commit authors and Actions trigger actors. It is not a workload score and does not infer human-versus-agent identity. It is explicitly labelled as observed GitHub identities.

### 5. Timeline / event feed

A future timeline may show meaningful GitHub and orchestrator events, but only when event ordering, correlation, and source freshness can be represented accurately. Do not stream every commit or tool event into the operator's primary view.

### 2. Agents

An agent list and detail view should show stable agent ID and display name; runner/orchestrator; current task and repository/branch/PR; explicit lifecycle state and reason; start time and elapsed duration; heartbeat age; bounded progress text; and recent lifecycle events with related CI runs.

Do not expose prompts, model chain-of-thought, raw tool inputs, environment variables, or secrets.

### 3. Repositories

Show active tasks and agents, branch/PR associations, required/recent CI state, failing checks, review requests, unresolved review conversations, and recent commits. Commit-volume charts are secondary. A small sparkline is acceptable only when it helps compare recent activity.

### 4. CI queue

Separate queued, running, successful, failed, cancelled, and skipped. Show workflow, job/check, repository, branch, PR, trigger actor, duration, queue age, last update, and a direct route to logs. A run's trigger actor is not automatically the agent that owns the task; correlate through an explicit task/run association when available.

### 5. Timeline / event feed

Use a chronological, filterable feed for meaningful changes: task started, waiting, resumed, PR opened, CI queued/running/failed/passed, review requested/received, completed, and cancelled. Group repeated low-value progress updates. Never stream every token or raw tool argument.

## Status lifecycle

Use explicit states with documented transitions:

- starting
- working
- waiting (required reason: ci, review, approval, user, dependency, rate_limit, external_tool)
- blocked
- completed
- failed
- cancelled
- unknown (no trustworthy current signal)

A heartbeat is liveness evidence, not progress. The UI should calculate stale as a freshness overlay, not as a lifecycle state. For example, an agent may be working with a stale heartbeat or waiting for CI with a stale heartbeat.

Use a configurable heartbeat interval. Initial recommendation: mark stale after three missed intervals, with a minimum grace period. Do not convert stale directly to failed.

## Visual language and motion

The terminal should feel polished and alive without looking noisy.

- Use a restrained adaptive palette, consistent status colors, strong typography hierarchy, aligned columns, subtle separators, and compact labels.
- Reserve saturated colors for actionable states: red for failure, amber for waiting/stale, green for success, blue/cyan for active work. Never rely on color alone; include text/icon labels.
- Prefer a compact state badge, elapsed time, and progress indicator over large decorative charts.
- Use short transition animations when an item changes state or a refresh completes: subtle spinner, brief highlight, or a small progress sweep.
- Never continuously animate every row. Animate only visible, active components and only while an operation is actually in progress.
- Provide a no-animation/reduced-motion mode and automatically disable motion for non-interactive output, TERM=dumb, NO_COLOR, and constrained environments when detection is reliable.
- Do not blink critical errors. Avoid rapid full-screen redraws, scrolling marquees, and decorative ASCII art that consumes vertical space.
- Keep all essential information visible in a static frame.

Suggested motion policy: one low-frequency activity indicator while a request/task is active; one short (roughly 300–700 ms) transition accent on meaningful state changes; no idle animation. Animation ticks should stop when no animated component is visible or active. Coalesce multiple events into one render update.

## Resource and API budgets

### Rendering and CPU

- Keep Bubble Tea's model/update/view flow; network and filesystem work must stay in commands, never block Update or View.
- Make View deterministic and cheap. Precompute expensive aggregates when data changes, not on every animation tick.
- Reuse styles and cached derived values; render only visible rows for long lists.
- Use event-driven UI changes. Animation ticks exist only during an active animation and stop immediately afterward.
- Add a reduced-motion/static mode. A static screen must not require a periodic tick.
- Avoid migrating Bubble Tea major versions solely for visual effects. Benchmark and test terminal compatibility before a major upgrade.

### GitHub API

GitHub recommends webhooks rather than continuous polling when a reachable service can receive them. For a standalone CLI without a daemon, polling remains a fallback:

- Poll on fixed, purposeful schedules; never create one timer per row/repository.
- Use conditional requests (ETag / If-None-Match) and preserve cached data on 304 and transient errors.
- Prioritize visible and urgent entities; coalesce duplicate refreshes.
- Use a single request scheduler per API origin with a small bounded in-flight limit; avoid request fan-out and respect Retry-After, rate-limit reset, and exponential backoff.
- Fetch summaries first; load jobs, logs, reviews, and detailed branch history only on demand.
- Stop background refresh when suspended or when the relevant detail view is inactive.
- Expose last successful refresh, next retry, and API rate-limit status so stale data is not mistaken for current data.

### Local storage

Keep the cache bounded and atomic, with owner-only permissions. Store only metadata needed to restore the last useful view. Do not persist credentials or sensitive agent content. Bound event history by age and size; rotate or compact append-only event data.

## Architecture recommendation

Keep the TUI as a thin operator client with explicit data-source boundaries:

- GitHubSource: repositories, branches, PRs, reviews, Actions, logs.
- AgentEventSource: orchestrator lifecycle events and heartbeats.
- Correlation: link agent task IDs to repository, branch, PR, and workflow run IDs.
- StateReducer: validate, deduplicate, order, and reduce lifecycle events into current task state.
- Scheduler: prioritize/coalesce refreshes, apply budgets, retry/backoff.
- Presentation: pure rendering of view models; no network or filesystem calls.

Do not make the terminal itself a public webhook server. The safest first integration is a local event source (JSONL spool or Unix-domain socket) written by a local runner/adapter. Bind any optional HTTP receiver to loopback only by default, authenticate it, and make remote exposure an explicitly documented deployment. A hosted multi-machine relay can be added later if justified.

## Delivery sequence

1. **Telemetry contract and adapter:** stable event schema, lifecycle reducer, stale detection, correlation IDs, validation, privacy controls.
2. **Agent/task view:** render real agent tasks separately from observed GitHub actors; preserve GitHub-only mode when no source is configured.
3. **CI and PR correlation:** associate runs/PRs to tasks using explicit IDs and head SHA/branch as secondary matching evidence; ambiguous matches remain unlinked.
4. **Operator actions:** open logs/PRs, retry failed jobs, cancel runs, and request agent pause/resume only when the provider supports it and authorization is clear.
5. **Webhook/relay option:** add only if multiple hosts or near-real-time updates require a persistent receiver.
6. **Visual polish and profiling:** bounded state-transition animation, reduced motion, rendering benchmarks, large-fleet API/load tests.

## Acceptance criteria

- Actual orchestrator state is distinguishable from GitHub-observed activity at a glance.
- A task can be linked to an agent, repository, branch, PR, and CI run without usernames alone.
- Stale telemetry is visible and never silently treated as failure or idle.
- No network/filesystem operation blocks the UI event loop.
- Idle mode performs no animation-driven redraws.
- Animation can be disabled and never hides state text.
- API requests are bounded, deduplicated, conditional where supported, and back off on rate limits.
- The overview remains useful with telemetry disconnected and with a narrow terminal.
- Prompts, outputs, tool arguments, credentials, and environment data are not collected by default.

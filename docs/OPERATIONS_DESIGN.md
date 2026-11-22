# Operations dashboard design

## Product objective

gh-fleet is a human-facing delivery operations console across GitHub repositories, pull requests, and CI pipelines. Its first screen should let an operator answer, within seconds:

1. What needs a human decision or inspection now?
2. What is progressing or waiting?
3. Which repository, pull request, or workflow is involved?
4. What is the next useful action?
5. How fresh and trustworthy is the displayed evidence?

The overview is an operations queue, not a contribution graph or productivity leaderboard. Historical activity is secondary.

## Evidence model

Every displayed status belongs to one of three evidence classes:

| Evidence class | Examples | What it proves |
| --- | --- | --- |
| GitHub-observed | Repository push, open PR, review decision, requested reviewer, Actions run | GitHub recorded the fact or state |
| Orchestrator-reported | Agent started, heartbeat, waiting for CI, completed | A configured runner reported the lifecycle event |
| Derived hint | Queued for 18m, running for 52m, stale heartbeat | gh-fleet calculated a cue from explicit inputs |

Never infer that an agent is live from a commit author, branch name, open PR, or Actions trigger actor. A running workflow does not prove an external agent is waiting for CI. Render a missing agent source as **Agent runtime: NOT CONNECTED**, not as zero agents, idle, or healthy.

Derived thresholds are inspection prompts rather than diagnoses. For example, "queued for 18m" is supported; "CI is stuck" is not. Thresholds are not SLAs unless an operator explicitly configures them.

## Information architecture

### 1. Overview (default)

The default page is designed around human action and includes:

- A source/freshness header with GitHub connection state and an explicit agent-runtime-not-connected notice until runner telemetry exists.
- A compact summary: actionable items, open PRs, running CI, queued CI, and monitored repositories.
- **Needs attention**, ordered by severity and then age. Each row has a signal, repository and item, age, and next action.
- **In flight / waiting**, showing running or queued workflows and PRs whose review is explicitly requested.

Do not put commit counts, contributor totals, actor matrices, or charts in the headline strip. Do not rank people, agents, or repositories by raw activity.

### 2. Attention classification

The current implementation uses these initial rules:

| Priority | Signal | Rule | Operator cue |
| --- | --- | --- | --- |
| 1 | CI failed | Failed workflow updated within 24h | Inspect failed run/logs |
| 2 | Changes requested | Non-draft PR decision is CHANGES_REQUESTED | Review feedback |
| 3 | Merge conflict | Non-draft PR merge state is DIRTY | Inspect branch conflicts |
| 4 | Reviewer not requested | Non-draft PR decision is REVIEW_REQUIRED, no requested reviewer | Choose a reviewer |
| 5 | CI queued | Queue age >= 15m | Check runner queue |
| 6 | CI running | Run age >= 45m | Inspect if unexpected |
| 7 | Review follow-up | Review requested, no update for >= 72h | Follow up with reviewer |

A draft PR is not treated as review-ready. A requested reviewer is not treated as a completed review. When more than the latest ten open PRs exist, the attention queue includes an explicit sample-coverage warning; review states for additional PRs remain unknown until inspected on GitHub. An approved PR is not an attention item. Old failures outside the 24h window are not in the urgent list.

The duration thresholds do not prove work is stuck. The UI should describe the observable fact and the suggested next check. If data is missing, do not synthesize a healthy value or zero age.

### 3. Repository portfolio and detail

The portfolio has one row per repository and is sorted by operational need, not popularity or commit volume. It shows:
- a plain-language state (attention, CI running, CI queued, in review, open PRs, open issues, quiet, or no open work);
- repository name;
- open PR and issue counts;
- last push age;
- latest CI signal.

Selecting a repository opens detail with the ten most recently updated open PRs, review decisions, requested reviewers, branch context, and current/recent failed CI runs. The total open PR count remains complete; the UI marks when the ten-item detail sample is truncated.

### 4. CI queue

Show running, queued, and recent failed workflow runs. Include workflow, repository, branch, trigger actor, state, update/queue age, and direct log access. The trigger actor is labelled as such; it is not treated as task owner or agent identity. Successful historical runs are not mixed into the urgent queue.

### 5. GitHub identity diagnostic

The optional diagnostic view groups observed commit authors and Actions trigger actors. It is not an agent list, team performance report, or productivity measure. It must keep the source distinction visible and must not imply that GitHub usernames represent people or autonomous agents.

The default overview does not load 30-day commit histories or branch activity samples. The diagnostic loads this data only when explicitly opened.

## Interaction model

- `Tab` / `Shift+Tab`: Overview, Repositories, CI queue.
- `↑` / `↓` or `j` / `k`: navigate the selected queue/list.
- `Enter`: open selected attention destination, repository detail, or CI logs depending on the current view.
- `o`: open the selected GitHub destination in a browser.
- `l`: open selected workflow logs.
- `a`: open optional observed-identity diagnostic.
- `/`: filter repositories.
- `r`: refresh GitHub data.
- `Esc`: leave detail/filter.
- `?`: help; `q`: quit.

The footer should show shortcuts relevant to the current view. No GitHub-only control should imply that gh-fleet can pause/resume or otherwise control an external agent.

## Visual and responsive behavior

- Prefer a clear type hierarchy, aligned data, concise status labels, and restrained separators.
- Red indicates explicit failure or conflict; amber indicates waiting or follow-up; cyan/blue indicates current activity; green indicates a confirmed clear state.
- Color reinforces text labels; it is never the only representation of state.
- At wide widths, the overview places attention and in-flight queues side by side. At narrower widths it stacks them while preserving the same priority order.
- At least 80 columns is recommended; the application supports a focused layout down to 40 columns.
- The first redesign intentionally has no decorative animation. Essential state is static, always readable, and not hidden behind motion. Refresh scheduling is not an animation loop.

## Resource and API budget

- Network, filesystem, and process work stay outside Bubble Tea `Update` and `View`.
- Repository summaries (including recent PR/review metadata) are fetched with repository metadata every three minutes.
- Actions summaries refresh every 60 seconds with a maximum of four concurrent repository requests; a selected repository's Actions view refreshes every 20 seconds.
- CI logs are fetched only after explicit selection and poll every 15 seconds while live log mode is enabled.
- Commit/branch activity history is not fetched for the overview. It is loaded on demand by the identity diagnostic, with at most four concurrent repositories and a five-minute refresh cadence.
- The dashboard checks refresh schedules every 15 seconds; no one-second idle animation tick is required.
- Preserve cached content during transient failures, expose stale/error state, use conditional requests where available, and back off after errors.
- Fetch summary data first; logs and expanded detail remain on demand.

## Agent lifecycle integration (future)

GitHub is not an agent runtime. To display actual agent state, gh-fleet needs an opt-in runner/orchestrator event source. Its target schema, local JSONL transport, lifecycle reducer, heartbeat freshness, correlation rules, and privacy constraints are defined in [Agent telemetry](AGENT_TELEMETRY.md).

When that source is connected, the UI should display explicit task state separately from GitHub state. Stable `agent_id` and `task_id` identify work; workflow runs and PRs are correlated only through explicit IDs or trusted adapter evidence. Repository and branch alone may suggest a possible match but must not silently confirm it.

Never collect prompts, model reasoning, full model outputs, raw tool inputs/results, secrets, environment variables, or credentials. A heartbeat means liveness, not progress. Stale is a freshness overlay, not a lifecycle state, and must not become failed automatically.

The TUI must not become a public webhook server. Start with a local, owner-only JSONL spool. Add a loopback-authenticated receiver or remote relay only when a real multi-host requirement justifies its operational and security cost.

## Metrics policy

Commit counts, PR counts, branch counts, and workflow trigger activity are diagnostic signals. They are not measures of effort, complexity, quality, or individual productivity. A large merge commit and a one-line fix are each a commit; raw activity is easy to misinterpret and game.

Do not build per-person utilization or activity rankings from GitHub metadata. The overview prioritizes delivery flow, review handoffs, CI failures, and concrete operator decisions.

DORA measures require production deployment and outcome events. GitHub commit and CI data alone cannot establish deployment frequency, change lead time to production, change failure rate, failed-deployment recovery time, or deployment rework rate. Do not label GitHub-only proxies as DORA metrics.

## Acceptance criteria

- A first-time operator can find attention items and their next actions without opening an actor matrix or reading activity charts.
- The overview contains no contribution calendar, commit leaderboard, or per-person productivity ranking.
- Agent state is explicitly not connected until backed by runner telemetry.
- PR and CI items link to the GitHub object that supports their displayed state.
- Threshold cues state observable facts and do not diagnose stalled work.
- 80-column layout stacks cleanly and keeps the same priorities; 40-column minimum remains usable.
- Cached data and freshness/error state remain visible during API failures.
- No network or filesystem operation blocks the UI event loop.
- Idle mode has no animation-driven redraw loop.
- History and logs are loaded only when the operator requests them.
- Sensitive agent content and credentials are excluded by default.

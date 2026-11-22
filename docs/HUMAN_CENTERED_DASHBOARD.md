# Human-centered operations dashboard

## Design brief

gh-fleet is used by a human operator who coordinates delivery across many repositories and coding-agent runs. The default screen must reduce the time between opening the dashboard and knowing what deserves attention. It is not a contribution chart, productivity scoreboard, or a terminal replica of GitHub's repository list.

The primary operator questions are:

1. What needs a human decision or intervention now?
2. What is progressing, and what is waiting on CI or review?
3. Which repository, pull request, and workflow are involved?
4. What is the next useful action, and where can I perform it?
5. How fresh and trustworthy is the information?

## Design principles

### 1. Action before activity

The first screen is an exception-oriented command center. It places concrete intervention items before historical activity. Every attention item must include:
- a specific object (repository, PR, or workflow run);
- an observable reason (not an inferred diagnosis);
- age / last-update context;
- a suggested next action;
- a direct GitHub destination where available.

Do not elevate an item merely because it has many commits, contributors, branches, or lines changed.

### 2. Evidence and uncertainty are visible

Every status belongs to one of three evidence classes:
- **GitHub fact:** PR review decision, requested reviewer, workflow state/conclusion, timestamps, branch and repository metadata.
- **Agent-reported fact:** only a future event received from a configured runner/orchestrator.
- **Derived hint:** a threshold-based cue such as "queued for 18m" or "no activity for 4d".

Derived hints must be phrased as observations, not diagnoses. "Running for 52m" is valid; "agent is stuck" is not. A workflow trigger actor is not the task owner. A commit author is not proof of a human or agent identity.

When no agent event source is configured, show **Agent runtime: not connected** and explicitly state that the dashboard is showing GitHub signals only. Do not show zero agents or invent agent states.

### 3. Optimize for human decisions, not individual surveillance

Do not rank people or agent-like GitHub accounts by commits, PR counts, activity, or utilization. Those measures are easy to game and do not represent productivity. Actor attribution is diagnostic context only and is never labelled as agent workload without explicit telemetry.

The overview uses delivery state: open work, review state, CI state, failures, and data freshness. Historical commit volume remains secondary and may be inspected in repository context.

### 4. Progressive disclosure

The information architecture is deliberately small:

1. **Overview** — needs attention, in-flight work, review/CI waiting, freshness.
2. **Repositories** — portfolio-level status and current work per repository.
3. **CI queue** — queued, running, and recent failed workflow runs with log access.
4. **Repository detail** — open PRs, recent CI, branches, and links, loaded/scoped on demand.
5. **GitHub identities** — optional diagnostic view; labels explicitly say "observed commit authors" and "workflow trigger actors", never "agents".

A screen should not show all metrics and all underlying events simultaneously.

## Default overview layout

### Header

Show product name, authenticated GitHub login, connection state, and last successful GitHub sync. Errors must not hide cached content; show cached/stale state and the last error separately.

Display a persistent source notice when agent telemetry is absent:

> Agent runtime not connected — showing GitHub repository, PR, and Actions signals only.

### Summary strip

Use a maximum of five compact counts:
- **Needs attention:** distinct actionable items, not repositories.
- **Open PRs:** current open pull-request total.
- **CI running:** current in-progress workflows.
- **CI queued:** current queued/waiting workflows.
- **Repositories:** monitored repository count.

These are workload context, not a performance score. Do not put commit or contributor totals in the headline strip.

### Main content

The main panel is split into two queues when the terminal is wide enough and stacks vertically on smaller terminals.

**Needs attention** is sorted by severity, then age:
1. Recent CI failure.
2. PR with changes requested.
3. PR with no requested reviewer while review is required.
4. Reported merge conflict.
5. CI queued beyond the queue-age threshold.
6. Long-running CI beyond the observation threshold.
7. PR waiting on a requested review beyond the follow-up threshold.

Each row must identify the repository and the actual PR/workflow, explain the observed signal, show age, and expose the next human action. Thresholds are prompts for inspection, not claims that work is stuck.

**In flight / waiting** shows current running and queued CI, plus PRs explicitly waiting for a requested review. It distinguishes:
- running;
- queued;
- review requested / awaiting response;
- changes requested;
- ready / approved where known.

Do not claim production deployment or customer impact from a GitHub Actions result.

### Repositories view

Use one row per repository. Lead with a plain-language state (needs attention, CI running, review waiting, open work, quiet), then repository name, open PR / issue counts, last push age, and latest CI state. Sort by operational urgency, not by commits or popularity.

Repository detail shows PR titles and review decisions, workflow links, branches, and recent commit context. Keep verbose history and branch diagnostics out of the overview.

### CI queue

Include queued, running, and recent failed runs. Show workflow, repository, branch, event/PR context when available, trigger actor (as trigger actor only), queue/run age, and a direct log action. Keep successful historical runs out of the urgent queue unless the operator explicitly asks for history.

## State and attention rules

- A failed workflow is an attention item only while it is recent (initially 24 hours); show the run URL and logs.
- A queued workflow becomes a follow-up item after 15 minutes. This is a queue-age threshold, not a stuck diagnosis.
- A running workflow becomes a follow-up item after 45 minutes. Phrase it as "running for …; inspect if unexpected".
- A PR with `CHANGES_REQUESTED` is an attention item.
- A PR with `REVIEW_REQUIRED` and no requested reviewer is labelled "reviewer not requested"; it is not silently assigned to a person.
- A PR with a requested reviewer is shown as awaiting review. After 72 hours without an update, show a follow-up cue; do not call it overdue unless an SLA is explicitly configured.
- A PR with a reported dirty/conflicting merge state is surfaced for inspection.
- Draft PRs are not treated as review-ready work.
- Missing, partial, stale, or unavailable data is shown as unknown/incomplete, not as zero or healthy.

Thresholds are centralized constants and have unit tests. They can become user-configurable later.

## Interaction model

- `Tab` / `Shift+Tab`: Overview, Repositories, CI queue.
- `↑` / `↓` or `j` / `k`: move within the current list.
- `Enter`: open selected PR/repository/run context; on a run, open logs.
- `l`: open selected workflow logs.
- `o`: open the selected GitHub page.
- `/`: filter repositories.
- `r`: refresh.
- `Esc`: leave detail/filter.
- `?`: help; `q`: quit.

The selected row must be visually obvious. The footer shows only actions relevant to the current view. No keyboard shortcut may imply agent control in a GitHub-only mode.

## Responsive and visual behavior

- A useful one-column layout must work at 80 columns; below that, preserve labels and status while progressively hiding secondary metadata.
- Use concise plain-language labels, aligned columns, whitespace, and restrained separators.
- Color reinforces text labels; color is never the only status channel.
- Red is reserved for explicit failure/conflict, amber for waiting/follow-up, blue/cyan for active work, green for confirmed success.
- No large decorative charts or ASCII art on the overview.
- Animations are not part of the first human-centered redesign. Essential content is static and immediately readable. If transition feedback is added later, it must be brief, visible-only, and disabled by `NO_COLOR` / non-interactive output.
- Do not run a one-second animation/render timer while idle. Refresh scheduling should follow data-source cadence.

## Data, performance, and security

- Keep network, file I/O, and process execution out of Bubble Tea `Update` and `View`.
- Keep rendering deterministic and cheap; derive attention summaries when source data changes or in small pure helpers.
- Fetch repository/PR summaries first. Load logs and expanded details only on demand.
- Bound concurrent GitHub requests and retain cached data during errors.
- Distinguish last successful refresh for repository metadata and Actions; never imply a failed refresh is fresh.
- Cache only operational metadata, with owner-only permissions. Never cache credentials, prompts, model outputs, raw tool arguments, environment variables, or secrets.
- Bound summary text and sanitize terminal control characters.

## Metrics policy

DORA metrics require deployment and production-outcome data. GitHub commit and Actions activity alone is insufficient to calculate deployment frequency, change lead time to production, change failure rate, failed-deployment recovery time, or deployment rework rate. Do not label proxies as DORA metrics.

The SPACE research warns against reducing developer productivity to a single activity measure. gh-fleet therefore does not present per-person commit/PR counts as productivity measures. It focuses on flow, handoffs, delivery state, and actionable blockers.

## Acceptance criteria

- A first-time operator can identify attention items and next actions without opening the actor matrix or reading a chart.
- The first page has no contribution histogram, commit leaderboard, or per-person activity ranking.
- Agent state is explicitly "not connected" until backed by runner telemetry.
- CI and PR statuses identify their GitHub evidence and link to the underlying object.
- Threshold-based waiting/long-running cues are described as hints, not failures.
- A narrow terminal shows the same priorities in a stacked layout.
- Cached data and freshness are visible during API errors.
- Idle rendering does not require a one-second tick or continuous animation.
- Automated tests cover attention classification, prioritization, thresholds, and responsive view content.

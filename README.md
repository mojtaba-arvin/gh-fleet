# gh-fleet

A full-screen terminal **agent operations control room** for GitHub repositories and AI coding-agent workloads.

The goal is to answer operational questions quickly: which agents are working, what they are working on, which tasks are blocked, which CI pipelines are running or queued, and what needs attention next.

## Operations dashboard

- **Repository workload:** repositories ranked by operational urgency, with 24h / 7d / 30d unique commits, seven-day trend, distinct commit contributors, active branches, open PRs, and CI state.
- **CI queue:** running and waiting GitHub Actions workflows plus recent failures, with repository, workflow, branch, triggering actor, and update age.
- **Observed actor workload:** press **c** for the cross-repository actor matrix. Commit authors and Actions actors are GitHub activity signals, not verified agent process identities.
- **Repository focus:** select a repository to inspect its activity and pipelines without continuously refreshing unrelated detail.
- **Workflow logs:** inspect and follow a selected run's logs.
- **Resilient monitoring:** cached startup, conditional Actions requests, bounded concurrency, retry backoff, and cache retention during API outages.

## Repository activity metrics

The repository table reports unique commit counts over 24 hours, 7 days, and 30 days; a seven-day trend; distinct commit authors over 30 days; and branches active in the last 7 days. Commits reachable from multiple branches are counted once per repository. Commit authors are counted uniformly: gh-fleet does not infer whether an account is a person, an automation bot, or an AI agent.

Default-branch history is paginated over 30 days. For active repositories, recent history is sampled from every branch (up to 20 commits per branch). A `~` prefix marks incomplete history or a branch that reached the sample limit; fleet-wide activity totals are also prefixed when any included repository is incomplete. Definitions and caveats are in [Repository activity metrics](docs/ACTIVITY_METRICS.md).

## What the dashboard can and cannot know

GitHub is the source of truth for repository, pull-request, and Actions state. A commit author or Actions actor is an **observed GitHub actor**; it is not proof that an autonomous agent is currently running. A running workflow is a CI state; it does not prove an external agent is blocked waiting for CI. Work performed without a pushed commit or GitHub event is invisible to GitHub-based monitoring.

To show true agent lifecycle state, gh-fleet needs explicit events from the agent runner/orchestrator. The target event contract and security boundaries are documented in [Agent telemetry](docs/AGENT_TELEMETRY.md). The recommended operator experience and performance budgets are in the [Operations control-plane design](docs/OPERATIONS_DESIGN.md).

## Design principles

- **Truth before inference:** distinguish observed GitHub facts, orchestrator-reported agent state, and gh-fleet-derived indicators.
- **Action-oriented hierarchy:** surface blocked, failed, stale, and waiting work before historical activity charts.
- **Progressive disclosure:** overview first; details, logs, and timelines on demand.
- **Quiet by default:** animations are short state-transition feedback, never a permanent source of CPU or terminal output.
- **Bounded resource use:** coalesce refreshes, prioritize visible/urgent data, use conditional requests, preserve cache, and back off on errors.
- **Local-first security:** no telemetry content or credentials in logs/cache; any future event receiver must be opt-in and local-only by default.

## Requirements

- GitHub CLI installed and authenticated with gh auth login.
- Go 1.24 or newer to build or install from source.
- A terminal with at least 110 columns for the full dashboard. Smaller terminals use focused tabs.

The authenticated GitHub CLI account needs access to the repositories and Actions data you want to monitor.

## Install

    go install github.com/mojtaba-arvin/gh-fleet@latest
    gh-fleet

Or build from a checkout:

    git clone https://github.com/mojtaba-arvin/gh-fleet.git
    cd gh-fleet
    go build -o gh-fleet .
    ./gh-fleet

Prebuilt archives for macOS, Linux, and Windows are published on the [Releases](https://github.com/mojtaba-arvin/gh-fleet/releases) page when a tagged release is available.

## Controls

| Key | Action |
| --- | --- |
| Tab / Shift+Tab | Focus repositories, agent activity, or CI queue |
| ↑ / ↓, j / k | Move through the focused list |
| Enter on a repository | Scope the dashboard to that repository |
| c | Open the observed actor-by-repository workload matrix |
| Esc | Return from repository or log detail |
| / | Filter repository names |
| Enter on a CI run or l | Open that run's logs |
| w | Pause or resume log refresh |
| u | Refresh the selected run's logs now |
| r | Refresh the dashboard; in repository detail, refresh that repository |
| o | Open the selected repository or workflow in a browser |
| ? | Show help |
| q | Quit |

## Refresh and network behavior

- Repository metadata, branch heads, PR counts, and issue counts refresh every 3 minutes.
- GitHub Actions runs refresh every 60 seconds, with at most four concurrent repository requests.
- Default-branch commit history is paginated over 30 days; active-branch history samples up to 20 commits per branch. Activity history refreshes every 5 minutes with at most four concurrent repository requests.
- In repository detail, Actions data refreshes every 20 seconds; full branch history refreshes every 5 minutes.
- A selected run's log view refreshes every 15 seconds while live polling is enabled.
- Actions requests use ETags when GitHub provides them. Failed requests use exponential backoff and cached data remains visible.
- The account overview fetches up to 100 branch heads per repository and paginates when needed. Activity counts deduplicate commits by SHA across branches.

Press r for an immediate refresh. These are GitHub data refresh intervals, not real-time agent-state guarantees. GitHub recommends webhooks over polling when a reachable integration service is available; see the design document for a future relay path.

## Data and privacy

gh-fleet calls the GitHub API directly. It obtains the active github.com token by running gh auth token; it does not write or print that token. The token is not included in the cache.

The cache contains repository names and metadata, branch names and commit summaries, Actions run metadata, and ETags. It may therefore contain information about private repositories. The cache is stored under the operating system's user cache directory with owner-only file permissions. On macOS this is typically ~/Library/Caches/gh-fleet/cache.json.

Agent telemetry is a separate, opt-in data source. Its default contract deliberately excludes prompts, model responses, environment variables, credentials, and raw tool arguments. See [the telemetry privacy model](docs/AGENT_TELEMETRY.md#privacy-and-security).

## Development

    gofmt -w main.go
    go vet ./...
    go test ./...
    go build ./...

Pull requests should explain the user-visible change and include manual reproduction steps for UI changes. Please do not include credentials, cache files, or private repository data in issues or pull requests.

## License

MIT. See [LICENSE](LICENSE).

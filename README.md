# gh-fleet

A full-screen terminal **agent operations control room** for GitHub repositories and AI coding-agent workloads.

The goal is to answer operational questions quickly: which agents are working, what they are working on, which tasks are blocked, which CI pipelines are running or queued, and what needs attention next.

## Operations dashboard

The default screen is a human-oriented command center. It puts work that may need a decision ahead of historical activity:

- **Needs attention:** recent CI failures, requested changes, PRs requiring review without a requested reviewer, reported merge conflicts, and CI/review waits that have crossed an inspection threshold. Each item shows why it surfaced and the next useful action.
- **In flight / waiting:** running or queued workflows and PRs with an explicitly requested reviewer.
- **Repository portfolio:** a plain-language state for each repository, open PR / issue counts, last push age, and latest CI result.
- **CI queue:** active and recent failed workflows with direct log access.
- **Repository detail:** the ten most recently updated open PRs with review state and requested reviewers, plus current CI context.

The first screen does not rank repositories or people by commit volume. The optional `a` diagnostic view groups GitHub-observed identities; it does not establish whether an identity is a person, bot, or coding agent.

See [Human-centered dashboard design](docs/HUMAN_CENTERED_DASHBOARD.md) for the information architecture, state rules, layout, keyboard model, accessibility, and acceptance criteria.

## What the dashboard can and cannot know

The current implementation is GitHub-first. The header explicitly reports **Agent runtime: NOT CONNECTED**. GitHub provides repository, pull-request, review, and Actions facts, but it does not reveal whether an external coding agent is actively thinking, editing files, blocked on a user, or finished. A workflow trigger actor is not necessarily the agent that created the code. A commit author is not proof of a human or agent identity.

True agent lifecycle state requires explicit events from the agent runner/orchestrator. The target event contract and privacy boundaries are documented in [Agent telemetry](docs/AGENT_TELEMETRY.md). gh-fleet does not synthesize agent states from GitHub usernames or workflow activity.

## Design principles

- **Human decisions first:** show a reason and next action, not just a count or chart.
- **Evidence before inference:** distinguish GitHub facts, future runner-reported facts, and threshold-based hints.
- **No productivity leaderboard:** commit, PR, and workflow actor counts are not measures of individual productivity.
- **Progressive disclosure:** overview first; detailed PRs, CI runs, and logs on demand.
- **Quiet by default:** no persistent animation or one-second redraw timer.
- **Bounded resource use:** bounded concurrency, explicit refresh cadence, cached data during errors, and detailed history loaded only on demand.
- **Local-first security:** credentials never enter the cache; private repository metadata remains in an owner-only local cache.

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

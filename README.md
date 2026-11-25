# gh-fleet

A terminal **delivery operations console** for coordinating work across GitHub repositories, pull requests, and CI pipelines. It is designed for a human operator who needs to know what requires attention, what is waiting, and what to do next.

The GitHub-only mode does not claim to know whether an external coding agent is running. Connect an agent-runner event source before treating agent lifecycle status as known.

## Operations dashboard

The default screen is a human-oriented command center. It puts work that may need a decision ahead of historical activity:

- **Needs attention:** recent CI failures, requested changes, PRs requiring review without a requested reviewer, reported merge conflicts, and CI/review waits that have crossed an inspection threshold. Each item shows why it surfaced and the next useful action.
- **In flight / waiting:** running or queued workflows and PRs with an explicitly requested reviewer.
- **Repository portfolio:** a plain-language state for each repository, open PR / issue counts, last push age, and latest CI result.
- **CI queue:** active and recent failed workflows with direct log access.
- **Repository detail:** the ten most recently updated open PRs with review state and requested reviewers, plus current CI context. Repositories with more than ten open PRs also produce a visible sample-coverage warning; uninspected PR states are not treated as healthy.

The first screen does not rank repositories or people by commit volume. The optional `a` diagnostic view groups GitHub-observed identities; it does not establish whether an identity is a person, bot, or coding agent.

See [Human-centered dashboard design](docs/HUMAN_CENTERED_DASHBOARD.md) for the information architecture, state rules, layout, keyboard model, accessibility, and acceptance criteria. See [UX research and interaction decisions](docs/UX_RESEARCH.md) for the comparative review, keyboard model, semantic colors, and restrained motion policy.

## What the dashboard can and cannot know

The dashboard can run in GitHub-only mode or with an optional local agent event source. Without a configured event spool, the standard GitHub-only dashboard stays focused on GitHub work and does not display a permanent warning for an unused optional source. When a source is configured, its actual connection state is shown; only runner-reported lifecycle events establish task state. GitHub provides repository, pull-request, review, and Actions facts, but it does not reveal whether an external coding agent is actively working, blocked on a user, or finished. A workflow trigger actor is not necessarily the agent that created the code. A commit author is not proof of a human or agent identity.

True agent lifecycle state requires explicit events from the agent runner/orchestrator. The target event contract and privacy boundaries are documented in [Agent telemetry](docs/AGENT_TELEMETRY.md). gh-fleet does not synthesize agent states from GitHub usernames or workflow activity.

## Design principles

- **Human decisions first:** show a reason and next action, not just a count or chart.
- **Evidence before inference:** distinguish GitHub facts, future runner-reported facts, and threshold-based hints.
- **No productivity leaderboard:** commit, PR, and workflow actor counts are not measures of individual productivity.
- **Progressive disclosure:** overview first; detailed PRs, CI runs, and logs on demand.
- **Quiet by default:** motion appears only during active work; no idle animation or background redraw timer.
- **Bounded resource use:** bounded concurrency, explicit refresh cadence, cached data during errors, and detailed history loaded only on demand.
- **Local-first security:** credentials never enter the cache; private repository metadata remains in an owner-only local cache.

## Requirements

- GitHub CLI installed and authenticated with gh auth login.
- Go 1.25 or newer to build or install from source.
- A terminal with at least 80 columns is recommended for the side-by-side overview. The layout stacks vertically at narrower widths and supports terminals down to 40 columns.

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

## Color, motion, and accessibility

The interface uses a dark, high-contrast operator palette with distinct colors for repository names, workload numbers, trends, contributors, branch counts, PR counts, and CI states. Repositories pushed within three days with no active CI and at least ten minutes since their latest CI activity are highlighted yellow in the Repository Health list. Green means success, rose means failure, amber means waiting, cyan means primary context, blue means numeric workload, violet means trends/branches, teal means active branch counts, and magenta means contributors. Selected rows use a full-width background across every visible cell and continuation line. Every chart is rendered as terminal text (sparklines, bars, and heatmaps), not as an image, so labels and values remain selectable and copyable in terminal emulators. Critical states always include text or symbols; color is not the only signal. A compact spinner appears only while an operation is active and stops when the UI is idle. Set `GH_FLEET_NO_ANIMATION=1` to disable motion. `TERM=dumb` also disables it, and terminal color preferences including `NO_COLOR` are respected.

## Controls

| Key | Action |
| --- | --- |
| Tab / Shift+Tab | Switch between Overview, Repositories, and CI queue |
| 1 / 2 / 3 | Jump directly to Overview, Repositories, or CI queue |
| ↑ / ↓, j / k | Move through the focused list |
| ← / → on Overview | Switch between repository health and in-flight / waiting panels |
| Enter on an Overview item | Open its GitHub PR or workflow page |
| l on a workflow item | Open live workflow logs |
| Enter on a repository | Open repository detail; use ↑ / ↓ to select a PR |
| Enter on a selected PR in repository detail | Open the PR; Esc returns to the repository list |
| Enter on a CI run | Open live workflow logs |
| a | Open the optional GitHub identity diagnostic (not agent identity) |
| Esc | Return from repository or log detail |
| / | Filter repositories |
| w | Pause or resume live log refresh |
| u | Refresh selected workflow logs now |
| r | Refresh repository and Actions data using normal freshness/ETag behavior |
| R | Force refresh and bypass cached ETags |
| C | Clear the local disk cache and fetch a fresh snapshot |
| o | Open the selected GitHub page in a browser |
| Right Shift alone | Copy the selected repository URL or template output in the repository health panel (Kitty keyboard protocol required) |
| t | Open a hidden paste prompt for the repository copy template; paste any length, use `@@@` for the URL, Ctrl+S to save, Esc to cancel |
| ? | Show help |
| q | Quit |

## Repository copy template

In the Overview's **Repository Health** panel, press **t** to open a hidden paste prompt for the copy template. Put `@@@` wherever the selected repository URL should be inserted; every marker is replaced. If the template is empty, the plain repository URL is copied. If no marker is present, the URL is appended on a new line. Press Ctrl+S to save or Esc to cancel. The template is stored in the user configuration directory, separately from the removable repository cache.

## Refresh and network behavior

- Repository metadata, branch heads, open PR/review summaries, and issue counts refresh every 3 minutes.
- GitHub Actions runs refresh every 60 seconds, with at most four concurrent repository requests. In repository detail, the selected repository's Actions data refreshes every 20 seconds.
- The overview does not fetch commit history or expanded branch history. The optional `a` identity diagnostic loads 30-day commit history and sampled feature-branch history on demand, at most four repositories concurrently; that history refreshes every 5 minutes while the diagnostic is open.
- A selected run's log view refreshes every 15 seconds while live polling is enabled.
- The UI checks refresh schedules every 15 seconds; this is not a real-time agent-state guarantee.
- Actions requests use ETags when GitHub provides them. Failed requests use exponential backoff and cached data remains visible.
- The account overview reads up to 100 branch heads per repository and paginates when needed. Diagnostic activity counts deduplicate commits by SHA across branches.

Press `r` for an immediate refresh, `R` to bypass cached ETags, or `C` to remove the persisted local snapshot and fetch a fresh one. A manual refresh fetches the repository snapshot first, then refreshes Actions for that snapshot; older in-flight Actions responses are discarded so they cannot overwrite newer list state. Clearing the cache keeps the currently rendered snapshot visible until the replacement fetch succeeds. For multi-machine, near-real-time agent status, use an authenticated event relay; a GitHub-only terminal client cannot observe work that has not produced a GitHub event.

## Agent runtime telemetry (optional)

The default GitHub-only mode remains unchanged. To display real runner-reported task state, configure the local JSONL event spool described in [Agent telemetry](docs/AGENT_TELEMETRY.md):

    mkdir -p "$HOME/.cache/gh-fleet"
    chmod 700 "$HOME/.cache/gh-fleet"
    touch "$HOME/.cache/gh-fleet/agent-events.jsonl"
    chmod 600 "$HOME/.cache/gh-fleet/agent-events.jsonl"
    gh-fleet --agent-events "$HOME/.cache/gh-fleet/agent-events.jsonl"

Alternatively set `GH_FLEET_AGENT_EVENTS`. The CLI flag takes precedence. The runner/orchestrator appends the documented lifecycle events; gh-fleet reads them every five seconds and never sends commands to agents. The file is local, read-only to gh-fleet, capped at 4 MiB, and is not copied into the GitHub metadata cache. Keep prompts, model reasoning, tool arguments/results, file contents, environment variables, and secrets out of the event stream.

For Python-based runners, the repository includes a dependency-free producer SDK. Install it with `python -m pip install ./integrations/python`, then use `AgentTelemetry` to emit lifecycle callbacks or wrap a task in `with telemetry.task():` for automatic start, heartbeat, and terminal events. See [the Python SDK guide](integrations/python/README.md) for examples and security behavior.

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

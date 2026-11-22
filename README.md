# gh-fleet

A full-screen terminal **agent operations and CI control room** for every GitHub repository accessible to the authenticated account.

The dashboard is designed to answer operational questions quickly: which repositories are receiving work, which GitHub actors are producing commits or triggering workflows, which CI pipelines are running or queued, and where recent CI failures need attention.

## Operations dashboard

- **Repository workload:** every repository is ranked by operational urgency: running CI, queued CI, recent CI failures, recent commit activity, then idle. Each row shows commits in the last 24 hours and 7 days, a seven-day activity sparkline, observed commit/Actions actors, and current CI state.
- **CI queue:** a live queue of running and waiting GitHub Actions workflows, plus failures from the last 24 hours. Each entry identifies the repository, workflow, branch, triggering actor, and time since its latest update.
- **Agent workload:** press `c` to see a cross-repository actor matrix with 24-hour / 7-day commit counts, running / waiting CI counts, repositories touched, and last observed activity.
- **Operational summary:** monitored repositories, repositories with recent work or active CI, running workflows, queued workflows, and recent CI failures.
- **Repository scope:** select a repository with `Enter` to focus its pipeline and activity; press `Esc` to return to the fleet.
- **Logs:** open a selected workflow's logs and follow them with 15-second refresh.
- **Resilient monitoring:** cached startup, ETag-aware Actions requests, bounded concurrency, retry backoff, and cache retention during API outages.
- No telemetry. GitHub credentials are read from GitHub CLI and held in memory for API requests.

## Attribution and interpretation

GitHub Actions exposes the actor who triggered each workflow. Commit history exposes a GitHub author login when GitHub can associate the commit with an account. gh-fleet combines these signals to show observed actors per repository; it does **not** claim that a GitHub actor is necessarily an autonomous agent or that a commit is a live agent heartbeat.

Commit counts combine the full seven-day default-branch history with up to ten recent commits per branch for repositories pushed within the last 30 days. This brings unmerged agent branches into the workload view; very high-volume branches can exceed that per-branch sample. Actions actors remain the identity of the workflow trigger, not proof of an autonomous agent heartbeat. GitHub Actions statuses are shown as GitHub reports them: `in_progress` is RUNNING; `queued`, `waiting`, `requested`, and `pending` are WAITING. A queued or running workflow is a CI state, not proof that an external agent process is blocked waiting for it.

## Requirements

- GitHub CLI (`gh`) installed and authenticated with `gh auth login`.
- Go 1.24 or newer to build or install from source.
- A terminal with at least 90 columns for the full dashboard. Smaller terminals use focused tabs.

The authenticated GitHub CLI account needs access to the repositories and Actions data you want to monitor.

## Install

```sh
go install github.com/mojtaba-arvin/gh-fleet@latest
gh-fleet
```

Or build from a checkout:

```sh
git clone https://github.com/mojtaba-arvin/gh-fleet.git
cd gh-fleet
go build -o gh-fleet .
./gh-fleet
```

Prebuilt archives for macOS, Linux, and Windows are published on the [Releases](https://github.com/mojtaba-arvin/gh-fleet/releases) page when a tagged release is available.

## Controls

| Key | Action |
| --- | --- |
| `Tab` / `Shift+Tab` | Focus repositories, agent activity, or CI queue |
| `↑` / `↓`, `j` / `k` | Move through the focused list |
| `Enter` on a repository | Scope the dashboard to that repository |
| `c` | Open the agent-by-repository workload matrix |
| `↑` / `↓` in workload | Scroll actor rows |
| `Esc` | Return from repository or log detail |
| `/` | Filter repository names |
| `Enter` on a CI run or `l` | Open that run's logs |
| `w` | Pause or resume log refresh |
| `u` | Refresh the selected run's logs now |
| `r` | Refresh the dashboard; in repository detail, refresh that repository |
| `o` | Open the selected repository or workflow in a browser |
| `?` | Show help |
| `q` | Quit |

## Refresh and network behavior

- Repository metadata, branch heads, PR counts, and issue counts refresh every 3 minutes.
- GitHub Actions runs refresh every 60 seconds, with at most four concurrent repository requests.
- Default-branch commit activity covers the previous 7 days and refreshes every 60 seconds, with at most four concurrent requests.
- Recent commit histories from all branches are fetched for repositories pushed within the last 30 days, with at most four concurrent repository requests; branch history refreshes every 3 minutes.
- In repository detail, only the selected repository's branch and Actions data refresh every 20 seconds. Other repositories remain on their last cached snapshot.
- A selected run's log view refreshes every 15 seconds while live polling is enabled.
- Actions requests use ETags when GitHub provides them. Failed requests use exponential backoff and cached data remains visible.
- The account overview fetches up to 100 branch heads per repository. Opening a repository loads additional branch pages when needed.

Press `r` for an immediate refresh.

## Data and privacy

`gh-fleet` calls the GitHub API directly. It obtains the active `github.com` token by running `gh auth token`; it does not write or print that token. The token is not included in the cache.

The cache contains repository names and metadata, branch names and commit summaries, Actions run metadata, and ETags. It may therefore contain information about private repositories. The cache is stored under the operating system's user cache directory with owner-only file permissions. On macOS this is typically `~/Library/Caches/gh-fleet/cache.json`.

Override the cache path with either:

```sh
GH_FLEET_CACHE=/path/to/cache.json gh-fleet
gh-fleet --cache /path/to/cache.json
```

## Development

```sh
gofmt -w main.go
go vet ./...
go test ./...
go build ./...
```

Pull requests should explain the user-visible change and include manual reproduction steps for UI changes. Please do not include credentials, cache files, or private repository data in issues or pull requests.

## License

MIT. See [LICENSE](LICENSE).

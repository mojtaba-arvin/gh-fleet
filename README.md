# gh-fleet

A fast, full-screen terminal dashboard for GitHub repositories, branch activity, pull requests, issues, and GitHub Actions.

The dashboard is designed for large terminal windows. It shows an account overview on one screen, then scopes to a selected repository so unrelated repositories do not keep refreshing while you work.

## Features

- Account-wide repository table with branch, open pull request, open issue, and latest Actions status counts.
- Aggregate cards for repositories, branches, open pull requests, open issues, running workflows, recent failures, and recently updated branch heads.
- Recent branch-head activity across repositories; repository detail includes recent commits and authors for its branches.
- Actions runs prioritized by activity and failure state, with logs for the selected run.
- Repository names are shown without repeating the account owner; the owner is included when repository names collide.
- Cached startup, conditional Actions requests, bounded concurrency, and retry backoff for unreliable connections.
- No telemetry. GitHub credentials are read from GitHub CLI and held in memory for API requests.

## Requirements

- GitHub CLI (`gh`) installed and authenticated with `gh auth login`.
- Go 1.24 or newer to build or install from source.
- A terminal with at least 90 columns for the full dashboard. Smaller windows switch to a focused single-panel view.

The current release targets `github.com`. The authenticated GitHub CLI account needs access to the repositories and Actions data you want to view.

## Install

Install the latest tagged source with Go:

```sh
go install github.com/mojtaba-arvin/gh-fleet@latest
```

Then run:

```sh
gh-fleet
```

Or build from a checkout:

```sh
git clone https://github.com/mojtaba-arvin/gh-fleet.git
cd gh-fleet
go build -o gh-fleet .
./gh-fleet
```

After a tagged release is published, prebuilt archives for macOS, Linux, and Windows are available on the [Releases](https://github.com/mojtaba-arvin/gh-fleet/releases) page.

## Controls

| Key | Action |
| --- | --- |
| `Tab` / `Shift+Tab` | Focus repositories, activity, or Actions |
| `↑` / `↓`, `j` / `k` | Move through the focused section |
| `Enter` on a repository | Scope the dashboard to that repository |
| `Esc` | Return to the account overview |
| `/` | Filter repository names |
| `Enter` on a run or `l` | Open that run's logs |
| `w` | Pause or resume log refresh |
| `u` | Refresh the selected run's logs now |
| `r` | Refresh the dashboard; in repository detail, refresh only that repository |
| `o` | Open the selected repository or run in a browser |
| `?` | Show help |
| `q` | Quit |

## Refresh and network behavior

- The account snapshot refreshes every 3 minutes.
- In the account overview, Actions runs refresh every 60 seconds, with at most four concurrent requests.
- In repository detail, only the selected repository's branch and Actions data refresh every 20 seconds. Other repositories remain on their last cached snapshot.
- A selected run's log view refreshes every 15 seconds while live polling is enabled.
- Actions requests use ETags when GitHub provides them. Failed requests use exponential backoff and cached data remains visible.
- The account overview fetches up to 100 branch heads per repository. Opening a repository loads additional branch pages when needed.

These intervals keep the overview responsive without continuously polling every endpoint at a high rate. Press `r` for an immediate refresh.

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
go build ./...
```

Pull requests should explain the user-visible change and include manual reproduction steps for UI changes. Please do not include credentials, cache files, or private repository data in issues or pull requests.

## License

MIT. See [LICENSE](LICENSE).

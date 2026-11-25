# gh-fleet UX research and interaction model

Reviewed: 2026-11-22

## Product boundary

gh-fleet is a GitHub delivery console. Its default experience should help an operator answer three questions quickly:

1. What needs a human decision?
2. What is currently moving or waiting?
3. What GitHub object should I inspect next?

This review does not introduce another service, provider integration, daemon, database, or polling loop. It improves how existing GitHub repository, pull-request, review, and Actions facts are selected and acted on. Optional telemetry remains opt-in and must not occupy default dashboard space when it is not configured.

## External UX review

The following maintained projects and component references were reviewed:

| Reference | Useful interaction pattern | Application to gh-fleet |
| --- | --- | --- |
| [lazygit guide](https://lazygit.dev/docs/guide/) and [keybindings](https://lazygit.dev/keybindings/) | Panels are navigable; Tab and direct-number shortcuts coexist; arrows and j/k move within a list; Enter drills in; Esc returns; ? explains the current interaction model. | Keep the three top-level views, add direct 1/2/3 access, and make both Overview queues genuinely selectable. |
| [gh-dash usage](https://www.gh-dash.dev/getting-started/usage/) and [keybindings](https://www.gh-dash.dev/getting-started/keybindings/) | A GitHub-specific dashboard exposes keyboard help and lets users operate selected PRs/issues without leaving a terminal-first workflow. | Keep actions attached to explicit GitHub objects; Enter opens the canonical PR/workflow page and o remains the browser action. |
| [Charm Bubbles](https://github.com/charmbracelet/bubbles) | Reusable lists, filtering, pagination, viewport scrolling, and generated help are established primitives for Bubble Tea applications. | Prefer predictable list focus and bounded rendering over custom animation or new infrastructure. |

These references are interaction examples, not a mandate to copy their entire feature sets. gh-fleet remains an opinionated delivery dashboard rather than a general-purpose Git client or a clone of another TUI.

## Decisions implemented

### 1. Every visible work queue is actionable

The Overview already showed both Needs attention and In flight / waiting, but only the first queue had a keyboard selection. That made the second half of the screen informational rather than operational.

The two queues now have independent focus and selection state:

- Left / Right switch the focused Overview queue.
- Up / Down and j / k move within the focused queue.
- The focused panel receives the active border and only its selected row is highlighted.
- Home / g and End / G move to the first and last item in the focused queue.
- Enter opens the selected GitHub PR or workflow page.
- l opens workflow logs when the selected item has an explicit Actions run ID.
- o remains the explicit browser-open action.

A queued workflow opens its GitHub run page by default; it is not sent to log retrieval before logs are available. The l action is explicit.

### 2. Top-level navigation is both discoverable and fast

Tab / Shift+Tab remain available. 1, 2, and 3 now jump directly to Overview, Repositories, and CI queue. The tab labels include those numbers so the shortcuts are visible rather than hidden in documentation.

### 3. GitHub-only mode stays quiet

An unconfigured optional Agent event source is not a problem in GitHub-only mode. Its permanent NOT CONNECTED banner used a full line without helping the operator act on GitHub work. The banner is now shown only when the optional source is explicitly configured; actual source errors and connected state remain visible when relevant.

No GitHub facts are inferred from an external Agent identity. The default screen stays centered on repositories, PRs, reviews, and Actions.

### 4. Search editing respects Unicode

Backspace now removes the final UTF-8 rune rather than deleting one byte. This prevents malformed query strings when repository names or search text contain non-ASCII characters.

### 5. Terminal constraints are explicit

- Below 40 columns, the dashboard asks for a wider terminal rather than rendering broken panels.
- Below 10 rows, the standard dashboard asks for more height.
- Compact layouts retain the same queue focus and keyboard actions; only the arrangement changes.
- Selection windows are centered around the active item when the list exceeds the available panel height.

## Acceptance criteria

The Go test suite covers:

- keyboard focus moving between both Overview queues;
- selection moving within the in-flight queue;
- workflow log retrieval being reachable through the explicit l action;
- direct navigation to all three primary views;
- Unicode-safe filter backspace;
- no unused Agent-runtime warning in default GitHub-only mode;
- configured optional telemetry still displaying its real connection state;
- compact layouts and short terminal heights remaining bounded.

CI continues to run gofmt, go vet, the Go test suite, Python SDK tests, and release-target builds. This UX change adds no runtime dependency, endpoint, or scheduled GitHub request.


## Color and motion system (implemented)

The dashboard uses a semantic, high-contrast palette rather than color as decoration:

- **Green** means confirmed healthy or completed successfully.
- **Red / rose** means a failure or an actionable problem.
- **Amber** means queued, waiting, or requiring caution.
- **Cyan / blue** identifies the active workspace, navigation, and current focus.
- **Violet** is reserved for branch and secondary context so it does not compete with attention states.
- Muted text is used for metadata, timestamps, and explanatory detail, never for the only indication of a critical state.

Motion combines an ambient connection pulse with a distinct four-frame activity spinner. The header pulse remains subtle while connected; when GitHub or telemetry work is active it switches to the rotating spinner. It is a visual cue only and does not add network polling. Set `GH_FLEET_NO_ANIMATION=1` to disable motion; `TERM=dumb` also disables it. Color output continues to follow terminal capabilities and `NO_COLOR` conventions.

The animation cadence is 180 ms. The pulse keeps the console visibly alive while the textual state remains authoritative; the busy spinner takes precedence during real work. No flashing alerts or animated list rows are used.

## Copyable cyber-operator presentation (implemented)

The visual direction is a dark operations console with restrained neon accents, not decorative terminal noise. Text remains ordinary terminal text; charts are built from selectable glyphs and labels rather than raster images. This preserves terminal selection/copy workflows and keeps the interface usable over SSH.

Data columns use separate, stable visual channels. PR and issue counts use severity bands rather than one fixed color: zero open PRs is red, zero open issues is green, and higher counts move through distinct caution / high-volume colors. The selected row is highlighted across the full available width, including metadata/continuation lines, so the current focus is unambiguous.

The Overview and CI queue include a compact, text-based status distribution bar with explicit PASS / FAIL / RUN / WAIT counts. Showing it in both places makes the chart useful without forcing a drill-down. It is intentionally labelled as a loaded sample because the UI only has the workflow runs currently present in its repository snapshots; it must not imply complete historical CI coverage.

### Manual cache controls

- `Space` or `r`: refresh repository and Actions data using the normal ETag/freshness behavior.
- `R`: force refresh by clearing in-memory ETags and the Actions response cache before requesting fresh data.
- `C`: remove the persisted cache file, clear ETags, and fetch a fresh snapshot. Existing on-screen data remains visible until the replacement fetch succeeds.
- `Delete`: navigate back from a detail view (or clear the active repository filter).

The refresh path retains bounded concurrency and the existing retry/backoff behavior. It refuses to start a second repository snapshot fetch while one is already in progress. A manual refresh now fetches the repository snapshot before queueing Actions refreshes. Each Actions request carries a refresh generation; responses from older generations are discarded and requeued, preventing an in-flight pre-refresh response from repopulating the refreshed list with stale CI state.



## Repository health, grouping, and terminal selection

The Overview health panel is an inventory rather than an exception-only queue. Every loaded repository is shown with its last push age, current Actions state, and open PR / issue counts. The redundant signal column is intentionally omitted. CI is deliberately independent from push recency: a dormant repository can still show a running or queued workflow.

The Repositories view groups projects in this order:

1. CI running
2. CI queued / waiting for a runner
3. Pushed in the last 24 hours
4. Active this week
5. Quiet for 8–30 days
6. Dormant for more than 30 days
7. No push timestamp available

Each group retains keyboard navigation through the same repository cursor. A selected row uses a continuous full-width selection background; nested ANSI foreground styles are stripped on that selected row because they otherwise interrupt the background fill.

Bubble Tea mouse-cell reporting is intentionally disabled. It intercepted native terminal mouse selection and made visible text difficult to copy. The interface remains keyboard-first; users can select and copy rendered text with their terminal's normal selection and copy shortcuts. The application does not claim to provide a cross-terminal clipboard API.

## Recent pushes with idle CI

The Overview contains a separate **Recent Pushes · CI Idle** panel. It is intentionally based on two independent clocks:

- **Repository activity:** the latest push (`pushed_at`) must be within the last 72 hours. This is why a repository pushed 2 minutes ago can still appear.
- **CI activity:** no workflow may be active (including queued, waiting, requested, or pending states), and the most recent Actions run update must be older than 10 minutes. Repositories with no Actions run sample are eligible if their push is recent.

Pull-request update timestamps and completed workflow timestamps do not replace the repository's push timestamp. The panel is sorted newest push first. At a 30-row terminal height, it has room to display at least five repositories; the panel height grows with available terminal space.

Use Left / Right to cycle Overview panels and Up / Down to select a row. Enter or o opens the repository. Press **c** to copy the selected repository URL (or configured template output); a ✓ confirmation replaces the footer hint after a successful copy, including in narrow terminals. Press **t** to open a hidden paste prompt from Repository Health. Paste a complete (possibly multiline) template using the terminal's bracketed-paste event without seeing its contents; Ctrl+S saves and Esc cancels. The template uses `@@@` as the URL placeholder and is stored separately from the removable cache. The standalone Right Shift copy shortcut remains available on terminals that report the distinct key event, but `c` is the portable shortcut. The separate Recent Pushes · CI Idle panel is removed; repositories matching its former criteria (pushed within three days, no active CI, latest CI activity at least ten minutes ago or no CI history) are highlighted yellow directly in Repository Health. macOS uses `pbcopy`; Linux supports `wl-copy`, `xclip`, or `xsel`; Windows uses `clip`.

Bubble Tea v2 is used for the richer terminal keyboard events; the minimum Go version is now 1.25.


## Repository freshness presentation

- Push age is green through 30 minutes and red only after 30 minutes.
- Repositories whose last push is older than 14 days use gray styling across the entire row, including the push age and CI/PR/issue fields. This stale state takes precedence over the push-age red color.
- The 14-day and 30-minute boundaries are strict: exactly 14 days is not stale; exactly 30 minutes is not red.

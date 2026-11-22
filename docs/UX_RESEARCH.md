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

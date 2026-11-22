# Repository activity diagnostics

## Purpose

This document describes the optional GitHub identity diagnostic opened with `a`. These metrics are not part of the default operator overview and are not used to score repository health, team performance, or individual productivity.

The operational dashboard instead prioritizes pull-request review handoffs, CI failures and waits, actionable exceptions, and data freshness.

## Diagnostic fields

| Field | Definition |
| --- | --- |
| 24h / 7d / 30d commits | Unique observed commits in the trailing time windows |
| Trend | Seven daily commit buckets across default and sampled active feature branches |
| Observed identities | Distinct commit author logins/names; not verified people or agents |
| Active branches | Branches with an observed commit in the trailing seven days |
| CI attribution | Workflow trigger actors, kept separate from commit authors |

A commit present on multiple branches is counted once per repository. GitHub commit author identity is not classified as human or agent. An Actions actor identifies who triggered a workflow, not necessarily who authored the code or owns the task.

## Collection window and limits

The diagnostic loads activity history only after the operator opens it. The default Overview, Repositories, and CI queue views do not fetch 30-day commit history or expanded feature-branch history.

- Default-branch commit history is fetched through the REST commits endpoint for the trailing 30 days, paginated in pages of 100.
- Branch refs are enumerated through GraphQL pagination.
- For recently pushed repositories, GraphQL retrieves up to 20 recent commits per branch in the trailing 30 days.
- If GitHub reports more than 20 matching commits on a branch, or either history source has not completed successfully, counts are marked with a `~` prefix to signal incomplete coverage and lower-bound totals.
- Repositories with no push in the last 30 days are treated as having no recent commit activity; branch history is skipped to avoid unnecessary requests.
- Commits are deduplicated by SHA within each repository, so the same commit reachable from both default and feature branches is not double-counted.

## Interpretation and safeguards

Commit volume measures recorded source-control events, not effort, complexity, quality, or time spent. A large merge commit and a one-line fix each count as one commit. Raw activity can be misleading and easy to game.

These figures are diagnostic context only. They are not displayed as the primary health score, are not used to rank people or teams, and must not be used as a productivity leaderboard. The SPACE research warns against reducing developer productivity to one activity measure. DORA metrics require production deployment and outcome signals; GitHub commit/Actions history alone cannot establish them.

## Resource policy

- Maximum four concurrent repository-history fetches.
- Branch history uses one GraphQL query per repository page of up to 100 refs, with at most 20 commit nodes per branch.
- History refresh interval is five minutes while the optional diagnostic is open; repository metadata refreshes every three minutes and Actions every 60 seconds.
- The overview does not fetch per-commit diffs or file statistics.
- Logs and expanded details are loaded only after operator selection.
- Cached activity history remains available across views and refresh errors, with incomplete coverage marked explicitly.

Fleet-wide commit and observed-identity totals receive a `~` prefix whenever any included repository is incomplete or sampled. This is bounded sampling, not an assertion that every commit on every branch has been retrieved.

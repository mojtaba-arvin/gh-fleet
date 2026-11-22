# Repository activity metrics

## Purpose

The fleet overview should answer how much observable work each repository received, regardless of whether the commit author is a person or an automation account. gh-fleet treats GitHub commit authors uniformly and keeps CI workflow actors as a separate signal.

## Current overview fields

| Field | Definition |
| --- | --- |
| 24h commits | Unique commits whose GitHub committer timestamp is within the trailing 24 hours |
| 7d commits | Unique commits within the trailing seven days |
| 30d commits | Unique commits within the trailing 30 days |
| Trend | Seven daily commit buckets, across default and active feature branches |
| People | Distinct commit author logins/names observed in the trailing 30 days |
| Active branches | Distinct branches with at least one observed commit in the trailing seven days |
| CI | Current running/waiting state or most recent relevant result |
| PR | Current open pull-request count |

A commit present on multiple branches is counted once per repository. GitHub commit author identity is not classified as human or agent. An Actions actor identifies the workflow trigger, not necessarily the author or owner of the code change.

## Collection window and limits

- Default-branch commit history is fetched through the REST commits endpoint for the trailing 30 days, paginated in pages of 100.
- All branch refs are enumerated through GraphQL pagination.
- For recently pushed repositories, GraphQL retrieves up to 20 recent commits per branch in the trailing 30 days.
- If GitHub reports more than 20 matching commits on a branch, or either the default/branch history has not yet completed successfully, the row uses a ~ prefix to signal incomplete coverage and lower-bound counts.
- Repositories with no push in the last 30 days are treated as having no recent commit activity; branch history is skipped to avoid unnecessary requests.
- Commits are deduplicated by SHA within each repository, so a commit reachable from both the default and feature branches is not double-counted.

## Interpretation

Commit volume measures recorded source-control activity, not effort, complexity, quality, or time spent. A large merge commit and a one-line fix each count as one commit. GitHub author identity is not proof that an account represents a human, bot, or AI agent.

These figures are diagnostic context only. They are not displayed as the primary health score, are not used to rank people or teams, and must not be used as a productivity leaderboard. The human-centered overview prioritizes delivery state, review handoffs, CI failures, and concrete next actions.

## Resource policy

- Maximum four concurrent repository history fetches.
- Branch history uses one GraphQL query per repository page of up to 100 refs, with at most 20 commit nodes per branch.
- Activity-history refresh interval is five minutes; repository metadata refreshes every three minutes, CI every 60 seconds, and cached data remains visible during API failures.
- The dashboard does not fetch per-commit diffs or file statistics for the overview.
- Detailed commit/PR information should be loaded only after the operator selects a repository or item.

Fleet-wide commit and contributor totals also receive a ~ prefix whenever any included repository is incomplete or sampled. This is a bounded sampling strategy, not an assertion that every commit on every branch has been retrieved.
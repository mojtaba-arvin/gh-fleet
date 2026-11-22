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
- If GitHub reports more than 20 matching commits on a branch, the row uses a ~ prefix to signal that activity and contributor counts are sampled lower bounds.
- Repositories with no push in the last 30 days are treated as having no recent commit activity; branch history is skipped to avoid unnecessary requests.
- Commits are deduplicated by SHA within each repository, so a commit reachable from both the default and feature branches is not double-counted.

## Interpretation

Commit volume measures recorded source-control activity, not effort, complexity, quality, or time spent. A large merge commit and a one-line fix each count as one commit. For better managerial context, use the combination of commit volume, distinct contributors, active branches, open PRs, CI outcomes, and last observed activity rather than ranking repositories by commit count alone.

GitHub may not associate every commit with a user account. When no login is available, gh-fleet falls back to the commit's author name. It does not infer whether that name represents a human, an AI agent, or a shared bot account.

## Resource policy

- Maximum four concurrent repository history fetches.
- Branch history uses one GraphQL query per repository page of up to 100 refs, with at most 20 commit nodes per branch.
- Refresh interval is three minutes and cached data remains visible during API failures.
- The dashboard does not fetch per-commit diffs or file statistics for the overview.
- Detailed commit/PR information should be loaded only after the operator selects a repository or item.

This is a bounded sampling strategy, not an assertion that every commit on every branch has been retrieved.
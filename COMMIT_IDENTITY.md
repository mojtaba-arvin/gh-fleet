# Commit identity and attribution policy

For maintainer-directed AI-assisted implementation, use Git author `Mojtaba Arvin <m.arvin@live.com>` only after confirming this exact email is verified on the maintainer's GitHub account. Do not use model, assistant, AI-agent, Copilot, Claude, Cursor, Codex, Gemini, generic agent, or bot identities as the author. Do not add bot/AI `Co-authored-by` trailers.

Include `<!-- maintainer-directed-ai-work -->` in the PR body for owner-authorized AI-assisted work. This machine-readable marker requires at least one commit whose Git author maps to `mojtaba-arvin` and whose exact name/email match the maintainer identity. It does not claim the maintainer personally typed every line. Disclose AI assistance in the PR description.

Preserve every real human contributor's original author name/email and GitHub attribution. Never relabel another person as the maintainer and never rewrite historical commits.

Genuine GitHub Actions/Dependabot commits are allowed only with the explicit `allow-automation-commits` label and only when both the Git author and Git committer map to the same allowlisted GitHub Bot account and use that automation account's canonical no-reply email. The exception does not cover agent-written implementation. Bot/agent co-author trailers remain prohibited. GitHub's `web-flow` committer is treated as a platform committer, distinct from the commit author; it does not grant an author exception.

Convenience defaults (not enforcement):

```sh
git config --global user.name "Mojtaba Arvin"
git config --global user.email "m.arvin@live.com"
```

`scripts/check_commit_identity.py` checks all PR commits, including Git author name/email, GitHub author association, Git committer name/email, GitHub committer association, and co-author trailers. It rejects prohibited bot/agent authors and committers, enforces the maintainer identity when the marker is present, and permits only the narrowly defined matching automation identities with the explicit label. Its fixture self-tests run in the ordinary CI matrix.

The `Commit identity` workflow uses `pull_request_target`, read-only GitHub permissions, and checks out only the trusted base-branch revision before running the validator against GitHub's PR commit metadata. Since a newly introduced `pull_request_target` workflow is not executed from the PR head that adds it, the ordinary `CI` workflow runs the validator's self-tests on this PR; full metadata validation becomes active for subsequent PRs after this workflow is merged. Configure `Commit identity` as a required check in branch protection/rulesets where repository governance permits. A workflow alone does not block merges.

Git metadata and GitHub account associations provide attribution evidence, not proof of who physically typed code. The validator cannot detect every deliberately disguised identity. Verify the maintainer email association and disclose AI assistance transparently.

Never amend, rebase, or cherry-pick a contributor's work under the maintainer identity. Preserve the contributor's original author fields and add a separate maintainer-authored commit for AI-assisted changes.

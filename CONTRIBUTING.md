# Contributing

Thanks for helping improve `gh-fleet`.

## Before opening a pull request

- For a larger behavior or interface change, open an issue first to agree on the direction.
- Keep changes focused and explain their user impact.
- Do not add credentials, private repository names, cached data, or log output from private workflows.

## Local workflow

Use Go 1.24 or newer:

```sh
gofmt -w main.go
go vet ./...
go build ./...
```

Pull requests should describe the change, how it was checked, and any terminal or GitHub API behavior that reviewers should verify manually.

## Project conventions

- Keep GitHub authentication delegated to `gh`; do not add token storage or request tokens in issues.
- Keep the overview inexpensive; repository-specific detail should refresh only the selected repository.
- Preserve useful cached data when a request fails.
- Keep user-visible labels concise and avoid repeating the account owner unnecessarily.

## GitHub Issue title convention

Use `<type>(<scope>): <short, clear description>` for every Issue title. The type must be one of `feat`, `fix`, `docs`, `refactor`, `test`, `build`, `ci`, `chore`, `perf`, or `revert`; scope must be concise lowercase kebab-case. Keep priorities, milestones, feature identifiers, agent identity, and run metadata in labels or the Issue body. Do not use bracketed prefixes, standalone priority codes, `Recover:`, or supervisor identity prefixes. Preserve Issue metadata and relationships when renaming. This repository keeps the complete title convention here so contributors do not depend on external/private policy repositories.

## Commit identity and attribution

Read `COMMIT_IDENTITY.md` before creating or reviewing commits. Maintainer-directed AI-assisted commits must use the verified maintainer Git author identity. Preserve other human contributors' Git author identity and GitHub attribution. Do not use AI/bot authors or prohibited bot/agent committers or co-author trailers. Genuine automation is allowed only under the documented, narrowly scoped exception. Disclose AI assistance in the PR body and never rewrite historical commits.

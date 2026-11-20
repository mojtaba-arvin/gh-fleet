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

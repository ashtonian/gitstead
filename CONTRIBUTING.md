# Contributing to git-mgr

Thanks for your interest in improving git-mgr.

## Development setup

Requires Go (see the version in [`go.mod`](go.mod)) and `git` on `PATH`.

```bash
git clone https://github.com/ashtonian/git-mgr
cd git-mgr
go build ./...
```

## Before opening a PR

The CI pipeline runs build, vet, gofmt, race-enabled tests, golangci-lint,
govulncheck, and the e2e suite. Run the equivalents locally:

```bash
go build ./...
go vet ./...
gofmt -l .                 # must print nothing
go test -race ./...

# Linting (matches CI)
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2
golangci-lint run

# E2E (requires Docker — spins up a real Forgejo via testcontainers)
go test -tags=e2e ./e2e/
```

## Guidelines

- Keep changes focused; one logical change per PR.
- Match the surrounding code's style, naming, and comment density.
- Wrap errors with `%w` and context (`fmt.Errorf("doing X: %w", err)`); error
  strings are lowercase with no trailing punctuation.
- Thread `context.Context` through anything doing git or network I/O.
- Add or update tests for behavior changes. Provider/discovery behavior that
  touches a real server belongs in the e2e suite.
- Conventional-style commit prefixes (`feat:`, `fix:`, `docs:`, `ci:`, …) feed
  the release changelog.

## Reporting bugs / requesting features

Open a GitHub issue with reproduction steps and your config (redact tokens).
For security issues, see [SECURITY.md](SECURITY.md).

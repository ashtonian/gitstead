# Contributing to gitten

Thanks for your interest in improving gitten.

## Development setup

Requires Go (see the version in [`go.mod`](go.mod)) and `git` on `PATH`.

```bash
git clone https://github.com/ashtonian/gitten
cd gitten
go build ./...
```

[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) explains how the parts fit
together -- components, data flow, locks and failure modes -- and the
README's code map lists every package.

## Before opening a PR

The CI pipeline runs build, vet, gofmt, race-enabled tests, golangci-lint,
govulncheck, and the e2e suite. Run the equivalents locally:

```bash
go build ./...
go vet ./...
gofmt -l .                 # must print nothing
go test -race ./...

# Linting (matches CI)
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0
golangci-lint run

# E2E: a module of its own. Most tests need only git; the provider tests
# need Docker (they start a Forgejo container via testcontainers).
cd e2e && go test ./...
```

## Guidelines

- Keep changes focused; one logical change per PR.
- Match the surrounding code's style, naming, and comment density.
- Wrap errors with `%w` and context (`fmt.Errorf("doing X: %w", err)`); error
  strings are lowercase with no trailing punctuation.
- Thread `context.Context` through anything doing git or network I/O.
- Add or update tests for behavior changes. Provider/discovery behavior that
  touches a real server belongs in the e2e suite. Tests never touch your home
  directory or git config: each package's `TestMain` calls `testenv.Main`.
- Update the README, SPEC and `config.example.yaml` in the same change as the
  behavior they describe; every config key belongs in the README's tables.
  The configs in `examples/` are loaded by `TestExamplesLoad`, so a change
  that breaks one fails the tests.
- Conventional-style commit prefixes (`feat:`, `fix:`, `docs:`, `ci:`, …) feed
  the release changelog.

## Reporting bugs / requesting features

Open a GitHub issue with reproduction steps and your config (redact tokens).
For security issues, see [SECURITY.md](SECURITY.md).

## Releasing

See [docs/RELEASING.md](docs/RELEASING.md).

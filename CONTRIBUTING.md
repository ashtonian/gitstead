# Contributing to gitstead

Thanks for your interest in improving gitstead.

## Development setup

Requires Go (see the version in [`go.mod`](go.mod)) and `git` on `PATH`.

```bash
git clone https://github.com/ashtonian/gitstead
cd gitstead
go build ./...
```

[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) explains how the parts fit
together -- components, data flow, locks and failure modes -- and the
[architecture code map](docs/ARCHITECTURE.md#code-map) lists every package.

## Before opening a PR

The CI pipeline runs build, vet, gofmt, race-enabled tests, golangci-lint,
govulncheck, and the e2e suite. Run the equivalents locally:

```bash
go build ./...
go vet ./...
gofmt -l .                 # must print nothing
go test ./...                 # unit tests; each package's run is isolated from your home and git config
go test -race ./...

# Linting (matches CI)
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0
golangci-lint run

# Vulnerability scan (matches CI)
go install golang.org/x/vuln/cmd/govulncheck@latest
govulncheck ./...

# E2E: a module of its own. Most tests need only git; the provider tests
# need Docker (they start a Forgejo container via testcontainers).
cd e2e && go test ./...
```

## What CI runs

CI runs only the jobs a change needs, over the packages it affects (SPEC.md,
Build and release). When a test starts reading a file outside its own
package directory, add the file to `go-owners` in `ci.yml`'s scope step.
Otherwise a change to that file alone runs every package or, when the file is
in `go-ignore`, skips the test that reads it. Label a pull request `full-ci`
to run everything.

## Container image

The image packages a binary built on the host. Build the binaries into the
per-platform layout the `Dockerfile` copies from, then the image:

```bash
scripts/build-binaries.sh build/image dev "$(git rev-parse HEAD)" linux/amd64
docker buildx build --platform linux/amd64 -f Dockerfile -t gitstead:dev build/image
```

## Build version information

```bash
go build -ldflags="-X main.version=1.0.0 -X main.commit=$(git rev-parse HEAD)" .
```

[SPEC.md](SPEC.md) is the detailed command and design reference; the
[architecture guide](docs/ARCHITECTURE.md) covers components, locks and
failure modes.

## Guidelines

- Keep changes focused; one logical change per PR.
- Match the surrounding code's style, naming, and comment density.
- Wrap errors with `%w` and context (`fmt.Errorf("doing X: %w", err)`); error
  strings are lowercase with no trailing punctuation.
- Thread `context.Context` through anything doing git or network I/O.
- Add or update tests for behavior changes. Provider/discovery behavior that
  touches a real server belongs in the e2e suite. Tests never touch your home
  directory or git config: each package's `TestMain` calls `testenv.Main`.
- Update the relevant [user guides](docs/README.md), README, SPEC and
  `config.example.yaml` in the same change as the behavior they describe;
  every config key belongs in the user guides' reference tables.
  The configs in `examples/` are loaded by `TestExamplesLoad`, so a change
  that breaks one fails the tests.
- Conventional-style commit prefixes (`feat:`, `fix:`, `docs:`, `ci:`, …) feed
  the release changelog.

## Reporting bugs / requesting features

Open a GitHub issue with reproduction steps and your config (redact tokens).
For security issues, see [SECURITY.md](SECURITY.md).

## Releasing

See [docs/RELEASING.md](docs/RELEASING.md).

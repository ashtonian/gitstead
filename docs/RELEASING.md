# Releasing gitten

A release is a pushed tag: `.github/workflows/release.yml` runs the tests,
then GoReleaser (`.goreleaser.yaml`) builds and publishes everything.

## What a release publishes

| Artifact | Where | Made by |
| --- | --- | --- |
| Archives for macOS and Linux, amd64 and arm64, with `checksums.txt` | the GitHub release for the tag | `builds`, `archives` |
| Release notes from the commits since the last tag (`docs:`, `test:` and `ci:` left out) | the GitHub release | `changelog` |
| Homebrew cask `gitten` | `ashtonian/homebrew-tap`, `Casks/gitten.rb` | `homebrew_casks` |
| Docker image `ashtonian/gitten:<version>` and `:latest`, amd64 and arm64 | Docker Hub | `dockers_v2`, `Dockerfile.goreleaser` |
| `go install github.com/ashtonian/gitten@<version>` | the Go module proxy, from the tag | nothing to do |

A tag with a pre-release suffix (`v1.0.0-rc.1`) is published as a pre-release.

## Before the first release

- **pkg.** `go.mod` takes pkg's config package from `../pkg`; no
  build outside this machine works until it is published as a module and
  pinned (`go get github.com/ashtonian/pkg/config@vX.Y.Z`, then drop the
  `replace`). `go install` refuses a module with a `replace` directive, and
  CI, Docker and GoReleaser all build from a clean checkout. Then remove the
  build-from-source note from the README's Install section.
- **Secrets** in the repository's Actions settings:
  - `DOCKERHUB_USERNAME`, `DOCKERHUB_TOKEN`: a Docker Hub access token with
    read and write. The `ashtonian/gitten` repository is made on the first
    push.
  - `HOMEBREW_TAP_GITHUB_TOKEN`: a fine-grained token with contents read and
    write on `ashtonian/homebrew-tap`; the workflow's own token can't push to
    another repository.
- **CI green on `master`**: build, vet, gofmt, race tests, e2e, lint,
  govulncheck and the Docker build.

## Cutting a release

1. Merge to `master` and wait for CI.
2. Check the config: `goreleaser check`, and optionally a local dry run with
   `goreleaser release --snapshot --clean --skip=docker,homebrew,publish`
   (archives land in `dist/`, which git ignores).
3. Tag and push. Versions follow semver; the first release is `v0.1.0`.

   ```bash
   git tag -a v0.1.0 -m "gitten v0.1.0"
   git push origin v0.1.0
   ```

4. Watch the Release workflow. If it fails before publishing, fix, delete
   the tag (`git push origin :v0.1.0`, `git tag -d v0.1.0`) and tag again; once
   anything is published, release a new patch version instead of retagging.

## After a release

```bash
brew install --cask ashtonian/tap/gitten && gitten version
go install github.com/ashtonian/gitten@v0.1.0 && gitten version
docker run --rm ashtonian/gitten:0.1.0 version
```

Each should print the tag's version and commit. The cask removes macOS's
quarantine bit after install, since the binaries aren't notarized; a
"cannot be opened" dialog means that hook didn't run.

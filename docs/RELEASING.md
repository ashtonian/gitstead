# Releasing gitstead

A release is a pushed tag: `.github/workflows/release.yml` runs the tests,
then GoReleaser (`.goreleaser.yaml`) builds and publishes everything.

## What a release publishes

| Artifact | Where | Made by |
| --- | --- | --- |
| Archives for macOS and Linux, amd64 and arm64, with `checksums.txt` | the GitHub release for the tag | `builds`, `archives` |
| Release notes from the commits since the last tag (`docs:`, `test:` and `ci:` left out) | the GitHub release | `changelog` |
| Homebrew cask `gitstead` | `ashtonian/homebrew-tap`, `Casks/gitstead.rb` | `homebrew_casks` |
| Docker image `ashtonian/gitstead:<version>` and `:latest`, amd64 and arm64 | Docker Hub | `dockers_v2`, `Dockerfile.goreleaser` |
| `go install github.com/ashtonian/gitstead@<version>` | the Go module proxy, from the tag | nothing to do |

A tag with a pre-release suffix (`v1.0.0-rc.1`) is published as a pre-release.

## Before the first release

- **No `replace` in `go.mod`.** `go install` refuses a module with one, and
  CI, Docker and GoReleaser build from a clean checkout: every dependency,
  `github.com/ashtonian/pkg/config` included, is a pinned, published version.
- **Public repository.** `go install`, the Go module proxy and the release
  downloads need `ashtonian/gitstead` public.
- **Secrets** in the repository's Actions settings:
  - `DOCKERHUB_USERNAME`, `DOCKERHUB_TOKEN`: a Docker Hub access token with
    read and write. The `ashtonian/gitstead` repository is made on the first
    push.
  - `HOMEBREW_TAP_SSH_KEY`: the private half of an SSH key without a
    passphrase, whose public half is a deploy key with write access on
    `ashtonian/homebrew-tap` (Settings > Deploy keys). GoReleaser pushes the
    cask with it; the workflow's own token can't push to another repository,
    and a deploy key can write to that one repository only. To replace it:

    ```bash
    ssh-keygen -t ed25519 -N '' -C 'gitstead release -> homebrew-tap' -f tapkey
    gh repo deploy-key add tapkey.pub -R ashtonian/homebrew-tap --allow-write --title "gitstead release (GoReleaser)"
    gh secret set HOMEBREW_TAP_SSH_KEY -R ashtonian/gitstead < tapkey
    rm tapkey tapkey.pub
    ```
- **CI green on `master`**: build, vet, gofmt, race tests, e2e, lint,
  govulncheck and the Docker build.

## Cutting a release

1. Merge to `master` and wait for CI.
2. Check the config: `goreleaser check`, and optionally a local dry run with
   `goreleaser release --snapshot --clean --skip=docker,homebrew,publish`
   (archives land in `dist/`, which git ignores).
3. Tag and push. Versions follow semver; the first release is `v0.1.0`.

   ```bash
   git tag -a v0.1.0 -m "gitstead v0.1.0"
   git push origin v0.1.0
   ```

4. Watch the Release workflow. If it fails before publishing, fix, delete
   the tag (`git push origin :v0.1.0`, `git tag -d v0.1.0`) and tag again; once
   anything is published, release a new patch version instead of retagging.

## After a release

```bash
brew install --cask ashtonian/tap/gitstead && gitstead version
go install github.com/ashtonian/gitstead@v0.1.0 && gitstead version
docker run --rm ashtonian/gitstead:0.1.0 version
```

Each should print the tag's version and commit. Homebrew quarantines what
it downloads and the binaries aren't notarized, so the cask clears the
quarantine after install (`postflight_steps`, from `custom_block` in
`.goreleaser.yaml`); a "cannot be opened" dialog means that step didn't run.
Signing and notarizing the macOS binaries (GoReleaser's `notarize`, with an
Apple Developer ID certificate and an App Store Connect API key as secrets)
would make the step unnecessary, and would let Gatekeeper trust a binary
downloaded any other way too.
Install by the full name, `ashtonian/tap/gitstead`: Homebrew 7 requires trust
for third-party taps, and naming the cask in full trusts it.

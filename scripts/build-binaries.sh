#!/usr/bin/env bash
# Cross-compiles gitstead on the build host into OUT_DIR/<os>/<arch>/gitstead,
# the per-platform layout the Dockerfile copies from (GoReleaser's dockers_v2
# lays out a release the same way). The flags match .goreleaser.yaml's build.
#
# Usage: scripts/build-binaries.sh OUT_DIR VERSION COMMIT PLATFORM...
#   scripts/build-binaries.sh build/image dev "$(git rev-parse HEAD)" linux/amd64
#   docker buildx build --platform linux/amd64 -f Dockerfile -t gitstead:dev build/image
set -euo pipefail

usage="usage: build-binaries.sh OUT_DIR VERSION COMMIT PLATFORM..."
out=${1:?$usage}
version=${2:?$usage}
commit=${3:?$usage}
shift 3
if (($# == 0)); then
  echo "$usage" >&2
  exit 2
fi
cd "$(dirname "$0")/.."
date=$(date -u +%Y-%m-%dT%H:%M:%SZ)
for platform in "$@"; do
  if [[ ! $platform =~ ^[a-z0-9]+/[a-z0-9]+$ ]]; then
    echo "error: platform must be os/arch, got '$platform'" >&2
    exit 2
  fi
  CGO_ENABLED=0 GOOS=${platform%/*} GOARCH=${platform#*/} go build \
    -ldflags="-s -w -X main.version=$version -X main.commit=$commit -X main.date=$date" \
    -o "$out/$platform/gitstead" .
done

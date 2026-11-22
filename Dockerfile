# Packages a gitstead binary built on the host: the build context holds one
# per platform at <os>/<arch>/gitstead, laid out by GoReleaser's dockers_v2 for
# a release and by scripts/build-binaries.sh for CI and local builds.
FROM alpine:3.24
RUN apk add --no-cache git openssh-client
ARG TARGETPLATFORM
COPY $TARGETPLATFORM/gitstead /usr/local/bin/gitstead
ENTRYPOINT ["gitstead"]

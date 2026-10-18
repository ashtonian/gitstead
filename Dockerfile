# syntax=docker/dockerfile:1.4
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .

ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=none
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH go build \
    -ldflags="-s -w -X main.version=$VERSION -X main.commit=$COMMIT -X main.date=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    -o gitstead . && \
    find /root/.cache/go-build -type f -mmin +120 -delete

FROM alpine:3.24
RUN apk add --no-cache git openssh-client
COPY --from=builder /app/gitstead /usr/local/bin/gitstead
ENTRYPOINT ["gitstead"]

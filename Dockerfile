# syntax=docker/dockerfile:1.4
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .

ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=none
RUN CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH go build \
    -ldflags="-s -w -X main.version=$VERSION -X main.commit=$COMMIT -X main.date=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    -o git-mgr .

FROM alpine:3.21
RUN apk add --no-cache git openssh-client
COPY --from=builder /app/git-mgr /usr/local/bin/git-mgr
ENTRYPOINT ["git-mgr"]

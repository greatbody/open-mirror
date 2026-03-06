#!/bin/bash
# Build open-mirror for macOS (both Intel and Apple Silicon).

set -e

VERSION="${VERSION:-dev}"
BUILD_TIME=$(date -u '+%Y-%m-%dT%H:%M:%SZ')
LDFLAGS="-s -w -X main.version=${VERSION} -X main.buildTime=${BUILD_TIME}"

mkdir -p dist

echo "Building macOS amd64 (Intel) ..."
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -ldflags="${LDFLAGS}" -o dist/open-mirror-darwin-amd64 .

echo "Building macOS arm64 (Apple Silicon) ..."
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -ldflags="${LDFLAGS}" -o dist/open-mirror-darwin-arm64 .

echo "Done:"
ls -lh dist/open-mirror-darwin-*

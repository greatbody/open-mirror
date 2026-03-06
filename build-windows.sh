#!/bin/bash
# Build open-mirror for Windows (amd64 and arm64).

set -e

VERSION="${VERSION:-dev}"
BUILD_TIME=$(date -u '+%Y-%m-%dT%H:%M:%SZ')
LDFLAGS="-s -w -X main.version=${VERSION} -X main.buildTime=${BUILD_TIME}"

mkdir -p dist

echo "Building Windows amd64 ..."
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags="${LDFLAGS}" -o dist/open-mirror-windows-amd64.exe .

echo "Building Windows arm64 ..."
CGO_ENABLED=0 GOOS=windows GOARCH=arm64 go build -ldflags="${LDFLAGS}" -o dist/open-mirror-windows-arm64.exe .

echo "Done:"
ls -lh dist/open-mirror-windows-*

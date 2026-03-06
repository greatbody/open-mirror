#!/bin/bash
# Build open-mirror for Linux arm64 (Raspberry Pi, AWS Graviton, etc).

set -e

VERSION="${VERSION:-dev}"
BUILD_TIME=$(date -u '+%Y-%m-%dT%H:%M:%SZ')
LDFLAGS="-s -w -X main.version=${VERSION} -X main.buildTime=${BUILD_TIME}"
OUTPUT="dist/open-mirror-linux-arm64"

mkdir -p dist
echo "Building ${OUTPUT} ..."
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="${LDFLAGS}" -o "${OUTPUT}" .
echo "Done: $(ls -lh "${OUTPUT}" | awk '{print $5}') ${OUTPUT}"

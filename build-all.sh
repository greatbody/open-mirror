#!/bin/bash
# Build open-mirror for all supported platforms.
# Output binaries are placed in the dist/ directory.

set -e

VERSION="${VERSION:-dev}"
BUILD_TIME=$(date -u '+%Y-%m-%dT%H:%M:%SZ')
LDFLAGS="-s -w -X main.version=${VERSION} -X main.buildTime=${BUILD_TIME}"
OUTPUT_DIR="dist"

rm -rf "${OUTPUT_DIR}"
mkdir -p "${OUTPUT_DIR}"

PLATFORMS=(
    "linux/amd64"
    "linux/arm64"
    "linux/arm"
    "darwin/amd64"
    "darwin/arm64"
    "windows/amd64"
    "windows/arm64"
)

echo "Building open-mirror ${VERSION} ..."
echo ""

for platform in "${PLATFORMS[@]}"; do
    GOOS="${platform%/*}"
    GOARCH="${platform#*/}"

    output="${OUTPUT_DIR}/open-mirror-${GOOS}-${GOARCH}"
    if [ "${GOOS}" = "windows" ]; then
        output="${output}.exe"
    fi

    echo "  ${GOOS}/${GOARCH} -> ${output}"
    CGO_ENABLED=0 GOOS="${GOOS}" GOARCH="${GOARCH}" go build -ldflags="${LDFLAGS}" -o "${output}" .
done

echo ""
echo "Done. Binaries in ${OUTPUT_DIR}/:"
ls -lh "${OUTPUT_DIR}/"

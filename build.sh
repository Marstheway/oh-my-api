#!/bin/bash
set -e

APP_NAME="oh-my-api"
OUTPUT_DIR="bin"
MAIN_PKG="./cmd/oh-my-api"

OS="${1:-linux}"
ARCH="${2:-amd64}"

mkdir -p "$OUTPUT_DIR"

echo "Building ${APP_NAME} for ${OS}-${ARCH}..."

CGO_ENABLED=0 GOOS="$OS" GOARCH="$ARCH" go build \
  -trimpath \
  -ldflags="-s -w" \
  -o "${OUTPUT_DIR}/${APP_NAME}-${OS}-${ARCH}" \
  "$MAIN_PKG"

echo "Done: ${OUTPUT_DIR}/${APP_NAME}-${OS}-${ARCH}"

#!/bin/bash
set -euo pipefail

# oh-my-api-bridge runner (standalone xAI OAuth local bridge)
# Usage:
#   BRIDGE_TOKEN=... ./scripts/run-xai-bridge.sh
# or put token in ~/.oh-my-api/bridge/token.txt (0600)
# or export BRIDGE_TOKEN before running.
#
# NOTE: before starting the service, complete xAI OAuth login once:
#   oh-my-api-bridge auth login
# OAuth state is stored at ~/.oh-my-api/bridge/auth.json and is managed
# entirely by the bridge. No Hermes install or auth file is required.

BRIDGE_TOKEN="${BRIDGE_TOKEN:-}"
LISTEN="${LISTEN:-:8081}"
# 默认按脚本自身位置定位仓库根，避免 systemd 以 home 为 CWD 时相对路径失败。
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
BINARY="${BINARY:-$REPO_ROOT/bin/oh-my-api-bridge-linux-amd64}"
STATE_DIR="${STATE_DIR:-$HOME/.oh-my-api/bridge}"

if [ -z "$BRIDGE_TOKEN" ]; then
  if [ -f "$STATE_DIR/token.txt" ]; then
    BRIDGE_TOKEN=$(cat "$STATE_DIR/token.txt")
  fi
fi

if [ -z "$BRIDGE_TOKEN" ]; then
  echo "ERROR: BRIDGE_TOKEN not set and $STATE_DIR/token.txt not found."
  echo "Generate one: openssl rand -hex 24 > $STATE_DIR/token.txt && chmod 600 $STATE_DIR/token.txt"
  exit 1
fi

if [ ! -x "$BINARY" ]; then
  echo "ERROR: binary not found or not executable: $BINARY"
  echo "Run from repo root: ./build.sh"
  exit 1
fi

if [ ! -f "$STATE_DIR/auth.json" ]; then
  echo "WARN: OAuth state not found at $STATE_DIR/auth.json"
  echo "Run 'oh-my-api-bridge auth login' first to complete xAI OAuth authorization."
fi

mkdir -p "$STATE_DIR"
chmod 700 "$STATE_DIR" || true

echo "Starting xAI OAuth bridge..."
echo "  listen:   $LISTEN"
echo "  binary:   $BINARY"
echo "  state:    $STATE_DIR/auth.json"
echo "  (token hidden)"

exec "$BINARY" \
  -listen="$LISTEN" \
  -bridge-token="$BRIDGE_TOKEN"

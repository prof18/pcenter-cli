#!/usr/bin/env bash
#
# Build pcenter and put it on PATH as a symlink, for working against the live
# Store from any directory.
#
#     script/dev-install.sh
#
# The binary is built to the repo root (already gitignored) and linked from
# ~/.local/bin. Because it is a symlink rather than a copy, re-running just the
# build is enough to pick up code changes — the link never goes stale.
#
# The version is stamped from `git describe` rather than left as "dev", so
# `pcenter version` identifies the exact build a manual test result came from.
# See plan/09-manual-live-testing.md.
set -euo pipefail

cd "$(dirname "$0")/.."

BIN_DIR="${PCENTER_DEV_BIN_DIR:-$HOME/.local/bin}"

version="$(git describe --tags --always --dirty)"
commit="$(git rev-parse --short HEAD)"
built_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

go build -ldflags "-X main.version=${version} -X main.commit=${commit} -X main.buildDate=${built_at}" \
  -o pcenter ./cmd/pcenter

mkdir -p "$BIN_DIR"
ln -sf "$PWD/pcenter" "$BIN_DIR/pcenter"

echo "built  $PWD/pcenter  ($version)"
echo "linked $BIN_DIR/pcenter"

case ":$PATH:" in
  *":$BIN_DIR:"*) ;;
  *) echo; echo "NOTE: $BIN_DIR is not on your PATH — add it, or the link will not resolve." ;;
esac

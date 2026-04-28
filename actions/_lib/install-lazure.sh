#!/usr/bin/env bash
set -euo pipefail

ACTION_PATH="${ACTION_PATH:-${GITHUB_ACTION_PATH:-}}"
if [[ -z "$ACTION_PATH" ]]; then
  printf 'lazure action: ACTION_PATH or GITHUB_ACTION_PATH is required\n' >&2
  exit 1
fi

REPO_ROOT="$(cd -- "$ACTION_PATH/../.." && pwd)"
BIN_DIR="${RUNNER_TEMP:-/tmp}/lazure-action/bin"
BIN="$BIN_DIR/lazure"

mkdir -p "$BIN_DIR"

printf 'Building lazure from %s\n' "$REPO_ROOT"
go build -o "$BIN" "$REPO_ROOT"

{
  printf '%s\n' "$BIN_DIR"
} >> "$GITHUB_PATH"

printf 'LAZURE_BIN=%s\n' "$BIN" >> "$GITHUB_ENV"
printf 'lazure-bin=%s\n' "$BIN" >> "$GITHUB_OUTPUT"

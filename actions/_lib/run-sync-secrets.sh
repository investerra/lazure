#!/usr/bin/env bash
set -euo pipefail

fail() {
  printf 'lazure sync_secrets action: %s\n' "$*" >&2
  exit 1
}

require_bool() {
  local name="$1"
  local value="$2"

  case "$value" in
    true | false) ;;
    *) fail "$name must be true or false" ;;
  esac
}

env="${LAZURE_ENV:-}"
dir="${LAZURE_DIR:-deploy}"
verbose="${LAZURE_VERBOSE:-false}"
concurrency="${LAZURE_CONCURRENCY:-10}"
extra_args="${LAZURE_EXTRA_ARGS:-}"

[[ -n "$env" ]] || fail "env is required"
[[ -n "$dir" ]] || fail "dir is required"
[[ -n "$concurrency" ]] || fail "concurrency is required"

require_bool "verbose" "$verbose"

args=(--dir "$dir")
[[ "$verbose" == "true" ]] && args+=(-v)

args+=(secrets sync "$env" -y)
args+=(--concurrency "$concurrency")

if [[ -n "$extra_args" ]]; then
  # shellcheck disable=SC2206
  extra=($extra_args)
  args+=("${extra[@]}")
fi

lazure "${args[@]}"

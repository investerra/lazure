#!/usr/bin/env bash
set -euo pipefail

fail() {
  printf 'lazure wait_for_deploy action: %s\n' "$*" >&2
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
expected_sha="${LAZURE_EXPECTED_SHA:-${EXPECTED_SHA:-${GITHUB_SHA:-}}}"
path="${LAZURE_PATH:-/version}"
field="${LAZURE_FIELD:-commit}"
timeout="${LAZURE_TIMEOUT:-5m}"
interval="${LAZURE_INTERVAL:-10s}"
extra_args="${LAZURE_EXTRA_ARGS:-}"

[[ -n "$env" ]] || fail "env is required"
[[ -n "$dir" ]] || fail "dir is required"
[[ -n "$expected_sha" ]] || fail "expected-sha is required"
[[ -n "$path" ]] || fail "path is required"
[[ -n "$field" ]] || fail "field is required"
[[ -n "$timeout" ]] || fail "timeout is required"
[[ -n "$interval" ]] || fail "interval is required"

require_bool "verbose" "$verbose"

args=(--dir "$dir")
[[ "$verbose" == "true" ]] && args+=(-v)

args+=(wait-for-deploy "$env" --expected-sha "$expected_sha" --path "$path" --field "$field" --timeout "$timeout" --interval "$interval")

if [[ -n "$extra_args" ]]; then
  # shellcheck disable=SC2206
  extra=($extra_args)
  args+=("${extra[@]}")
fi

lazure "${args[@]}"

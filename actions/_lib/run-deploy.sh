#!/usr/bin/env bash
set -euo pipefail

fail() {
  printf 'lazure deploy action: %s\n' "$*" >&2
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
wait="${LAZURE_WAIT:-true}"
logs="${LAZURE_LOGS:-true}"
wait_timeout="${LAZURE_WAIT_TIMEOUT:-5m}"
force="${LAZURE_FORCE:-false}"
color="${LAZURE_COLOR:-false}"
vars="${LAZURE_VARS:-}"
extra_args="${LAZURE_EXTRA_ARGS:-}"

[[ -n "$env" ]] || fail "env is required"
[[ -n "$dir" ]] || fail "dir is required"
[[ -n "$wait_timeout" ]] || fail "wait-timeout is required"

require_bool "verbose" "$verbose"
require_bool "wait" "$wait"
require_bool "logs" "$logs"
require_bool "force" "$force"
require_bool "color" "$color"

args=(--dir "$dir")
[[ "$verbose" == "true" ]] && args+=(-v)

args+=(deploy "$env" -y "--wait=$wait" "--logs=$logs" --wait-timeout "$wait_timeout")
[[ "$force" == "true" ]] && args+=(--force)
[[ "$color" == "false" ]] && args+=(--no-color)

while IFS= read -r line || [[ -n "$line" ]]; do
  [[ -z "$line" ]] && continue
  args+=(--var "$line")
done <<< "$vars"

if [[ -n "$extra_args" ]]; then
  # shellcheck disable=SC2206
  extra=($extra_args)
  args+=("${extra[@]}")
fi

lazure "${args[@]}"

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

diagnostic_color_args=()
[[ "$color" == "false" ]] && diagnostic_color_args+=(--no-color)

started_at="$(date +%s)"

diagnostic_since() {
  local now elapsed

  now="$(date +%s)"
  elapsed=$((now - started_at))
  if [[ "$elapsed" -lt 1 ]]; then
    elapsed=1
  fi
  printf '%ss' "$elapsed"
}

dump_failure_diagnostics() {
  local since

  printf 'lazure deploy action: deploy failed, dumping recent container logs\n' >&2
  if ! lazure --dir "$dir" logs "$env" --tail 20 --follow=false "${diagnostic_color_args[@]}"; then
    printf 'lazure deploy action: failed to dump container logs\n' >&2
  fi

  since="$(diagnostic_since)"
  printf 'lazure deploy action: dumping recent Azure events\n' >&2
  if ! lazure --dir "$dir" events "$env" --since "$since" --limit 20 --expand "${diagnostic_color_args[@]}"; then
    printf 'lazure deploy action: failed to dump Azure events\n' >&2
  fi
}

set +e
lazure "${args[@]}"
status=$?
set -e

if [[ "$status" -ne 0 ]]; then
  dump_failure_diagnostics
  exit "$status"
fi

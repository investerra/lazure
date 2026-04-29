#!/usr/bin/env bash
set -euo pipefail

fail() {
  printf 'lazure validate action: %s\n' "$*" >&2
  exit 1
}

env="${LAZURE_ENV:-}"
dir="${LAZURE_DIR:-deploy}"
verbose="${LAZURE_VERBOSE:-false}"
extra_args="${LAZURE_EXTRA_ARGS:-}"

[[ -n "$env" ]] || fail "env is required"
[[ -n "$dir" ]] || fail "dir is required"

case "$verbose" in
  true | false) ;;
  *) fail "verbose must be true or false" ;;
esac

args=(--dir "$dir")
[[ "$verbose" == "true" ]] && args+=(-v)

args+=(validate "$env")

if [[ -n "$extra_args" ]]; then
  # shellcheck disable=SC2206
  extra=($extra_args)
  args+=("${extra[@]}")
fi

lazure "${args[@]}"

#!/usr/bin/env bash
set -euo pipefail

ACTION_PATH="${ACTION_PATH:-${GITHUB_ACTION_PATH:-}}"
if [[ -z "$ACTION_PATH" ]]; then
  printf 'lazure action: ACTION_PATH or GITHUB_ACTION_PATH is required\n' >&2
  exit 1
fi

# shellcheck source=lazure-action.sh
source "$ACTION_PATH/../_lib/lazure-action.sh"

lazure_build_deploy_args \
  "${LAZURE_ENV:-}" \
  "${LAZURE_DIR:-deploy}" \
  "${LAZURE_VERBOSE:-false}" \
  "${LAZURE_WAIT:-true}" \
  "${LAZURE_LOGS:-true}" \
  "${LAZURE_WAIT_TIMEOUT:-5m}" \
  "${LAZURE_VARS:-}" \
  "${LAZURE_EXTRA_ARGS:-}" \
  "${LAZURE_COLOR:-false}"

"${LAZURE_BIN:-lazure}" "${LAZURE_ARGS[@]}"

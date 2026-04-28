#!/usr/bin/env bash
set -euo pipefail

ACTION_PATH="${ACTION_PATH:-${GITHUB_ACTION_PATH:-}}"
if [[ -z "$ACTION_PATH" ]]; then
  printf 'lazure action: ACTION_PATH or GITHUB_ACTION_PATH is required\n' >&2
  exit 1
fi

# shellcheck source=lazure-action.sh
source "$ACTION_PATH/../_lib/lazure-action.sh"

lazure_build_sync_secrets_args \
  "${LAZURE_ENV:-}" \
  "${LAZURE_DIR:-deploy}" \
  "${LAZURE_VERBOSE:-false}" \
  "${LAZURE_DRY_RUN:-false}" \
  "${LAZURE_CONCURRENCY:-10}" \
  "${LAZURE_EXTRA_ARGS:-}"

"${LAZURE_BIN:-lazure}" "${LAZURE_ARGS[@]}"

#!/usr/bin/env bash
set -euo pipefail

fail() {
  printf 'lazure env_guess action: %s\n' "$*" >&2
  exit 1
}

input_env="${LAZURE_INPUT_ENV:-}"
ref_type="${LAZURE_REF_TYPE:-${GITHUB_REF_TYPE:-}}"
ref_name="${LAZURE_REF_NAME:-${GITHUB_REF_NAME:-}}"
extra_args="${LAZURE_EXTRA_ARGS:-}"

[[ -n "${GITHUB_OUTPUT:-}" ]] || fail "GITHUB_OUTPUT is required"

args=(env-guess)
[[ -n "$input_env" ]] && args+=(--env "$input_env")
[[ -n "$ref_type" ]] && args+=(--ref-type "$ref_type")
[[ -n "$ref_name" ]] && args+=(--ref-name "$ref_name")
args+=(--github-output)

if [[ -n "$extra_args" ]]; then
  # shellcheck disable=SC2206
  extra=($extra_args)
  args+=("${extra[@]}")
fi

lazure "${args[@]}"

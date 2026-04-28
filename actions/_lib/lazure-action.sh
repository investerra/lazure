#!/usr/bin/env bash

set -euo pipefail

LAZURE_ARGS=()

lazure_fail() {
  printf 'lazure action: %s\n' "$*" >&2
  return 1
}

lazure_require() {
  local name="$1"
  local value="$2"

  if [[ -z "$value" ]]; then
    lazure_fail "$name is required"
  fi
}

lazure_bool() {
  local name="$1"
  local value="$2"

  case "$value" in
    true | false)
      return 0
      ;;
    *)
      lazure_fail "$name must be true or false"
      ;;
  esac
}

lazure_add_common_args() {
  local dir="$1"
  local verbose="$2"

  lazure_require "dir" "$dir" || return $?
  lazure_bool "verbose" "$verbose" || return $?

  LAZURE_ARGS=(--dir "$dir")
  if [[ "$verbose" == "true" ]]; then
    LAZURE_ARGS+=(-v)
  fi
}

lazure_add_extra_args() {
  local extra_args="$1"

  if [[ -z "$extra_args" ]]; then
    return 0
  fi

  local extra=()
  # shellcheck disable=SC2206
  extra=($extra_args)
  LAZURE_ARGS+=("${extra[@]}")
}

lazure_add_deploy_vars() {
  local vars="$1"
  local line

  if [[ -z "$vars" ]]; then
    return 0
  fi

  while IFS= read -r line || [[ -n "$line" ]]; do
    [[ -z "$line" ]] && continue
    LAZURE_ARGS+=(--var "$line")
  done <<< "$vars"
}

lazure_build_deploy_args() {
  local env="$1"
  local dir="$2"
  local verbose="$3"
  local wait="$4"
  local logs="$5"
  local wait_timeout="$6"
  local vars="$7"
  local extra_args="$8"
  local color="$9"
  local force="${10}"

  lazure_require "env" "$env" || return $?
  lazure_require "wait-timeout" "$wait_timeout" || return $?
  lazure_bool "wait" "$wait" || return $?
  lazure_bool "logs" "$logs" || return $?
  lazure_bool "color" "$color" || return $?
  lazure_bool "force" "$force" || return $?

  lazure_add_common_args "$dir" "$verbose" || return $?
  LAZURE_ARGS+=(deploy "$env" -y "--wait=$wait" "--logs=$logs" --wait-timeout "$wait_timeout")
  if [[ "$force" == "true" ]]; then
    LAZURE_ARGS+=(--force)
  fi
  if [[ "$color" == "false" ]]; then
    LAZURE_ARGS+=(--no-color)
  fi
  lazure_add_deploy_vars "$vars"
  lazure_add_extra_args "$extra_args"
}

lazure_build_sync_secrets_args() {
  local env="$1"
  local dir="$2"
  local verbose="$3"
  local dry_run="$4"
  local concurrency="$5"
  local extra_args="$6"

  lazure_require "env" "$env" || return $?
  lazure_require "concurrency" "$concurrency" || return $?
  lazure_bool "dry-run" "$dry_run" || return $?

  lazure_add_common_args "$dir" "$verbose" || return $?
  LAZURE_ARGS+=(secrets sync "$env" -y)
  if [[ "$dry_run" == "true" ]]; then
    LAZURE_ARGS+=(--dry-run)
  fi
  LAZURE_ARGS+=(--concurrency "$concurrency")
  lazure_add_extra_args "$extra_args"
}

lazure_build_validate_args() {
  local env="$1"
  local dir="$2"
  local verbose="$3"
  local extra_args="$4"

  lazure_require "env" "$env" || return $?

  lazure_add_common_args "$dir" "$verbose" || return $?
  LAZURE_ARGS+=(validate "$env")
  lazure_add_extra_args "$extra_args"
}

#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
LIB_DIR="$(cd -- "$SCRIPT_DIR/.." && pwd)"

# shellcheck source=../lazure-action.sh
source "$LIB_DIR/lazure-action.sh"

fail() {
  printf 'FAIL: %s\n' "$*" >&2
  exit 1
}

assert_args() {
  local name="$1"
  local expected="$2"
  shift 2

  local actual
  actual="$(printf '<%s>' "$@")"
  if [[ "$actual" != "$expected" ]]; then
    fail "$name: got $actual, want $expected"
  fi
}

test_deploy_defaults() {
  lazure_build_deploy_args "dev" "deploy" "false" "true" "true" "5m" "" "" "false" "false"

  assert_args "deploy defaults" \
    "<--dir><deploy><deploy><dev><-y><--wait=true><--logs=true><--wait-timeout><5m><--no-color>" \
    "${LAZURE_ARGS[@]}"
}

test_deploy_vars_and_extra_args() {
  lazure_build_deploy_args "uat" "infra" "true" "false" "false" "10m" $'image=repo/app:sha\nversion=42' "--print" "true" "true"

  assert_args "deploy vars and extra args" \
    "<--dir><infra><-v><deploy><uat><-y><--wait=false><--logs=false><--wait-timeout><10m><--force><--var><image=repo/app:sha><--var><version=42><--print>" \
    "${LAZURE_ARGS[@]}"
}

test_sync_secrets() {
  lazure_build_sync_secrets_args "dev" "deploy" "false" "true" "3" ""

  assert_args "sync secrets" \
    "<--dir><deploy><secrets><sync><dev><-y><--dry-run><--concurrency><3>" \
    "${LAZURE_ARGS[@]}"
}

test_validate() {
  lazure_build_validate_args "prd" "deploy" "true" ""

  assert_args "validate" \
    "<--dir><deploy><-v><validate><prd>" \
    "${LAZURE_ARGS[@]}"
}

test_invalid_bool_fails() {
  if lazure_build_validate_args "dev" "deploy" "maybe" "" 2>/tmp/lazure-action-test.err; then
    fail "invalid bool unexpectedly succeeded"
  fi
  rg -q "verbose must be true or false" /tmp/lazure-action-test.err || fail "invalid bool error was not useful"
}

test_deploy_defaults
test_deploy_vars_and_extra_args
test_sync_secrets
test_validate
test_invalid_bool_fails

printf 'ok\n'

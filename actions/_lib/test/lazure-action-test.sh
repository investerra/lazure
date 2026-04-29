#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
LIB_DIR="$(cd -- "$SCRIPT_DIR/.." && pwd)"

fail() {
  printf 'FAIL: %s\n' "$*" >&2
  exit 1
}

assert_capture() {
  local name="$1"
  local expected="$2"
  local actual

  actual="$(cat "$LAZURE_CAPTURE")"
  if [[ "$actual" != "$expected" ]]; then
    fail "$name: got $actual, want $expected"
  fi
}

with_fake_lazure() {
  local tmp="$1"

  mkdir -p "$tmp/bin"
  cat > "$tmp/bin/lazure" <<'EOF'
#!/usr/bin/env bash
printf '<%s>' "$@" > "$LAZURE_CAPTURE"
EOF
  chmod +x "$tmp/bin/lazure"
  export PATH="$tmp/bin:$PATH"
}

test_deploy_defaults() {
  local tmp
  tmp="$(mktemp -d)"
  LAZURE_CAPTURE="$tmp/capture"
  export LAZURE_CAPTURE
  with_fake_lazure "$tmp"

  LAZURE_ENV=dev \
    bash "$LIB_DIR/run-deploy.sh"

  assert_capture "deploy defaults" \
    "<--dir><deploy><deploy><dev><-y><--wait=true><--logs=true><--wait-timeout><5m><--no-color>"
}

test_deploy_vars_and_extra_args() {
  local tmp
  tmp="$(mktemp -d)"
  LAZURE_CAPTURE="$tmp/capture"
  export LAZURE_CAPTURE
  with_fake_lazure "$tmp"

  LAZURE_ENV=uat \
    LAZURE_DIR=infra \
    LAZURE_VERBOSE=true \
    LAZURE_WAIT=false \
    LAZURE_LOGS=false \
    LAZURE_WAIT_TIMEOUT=10m \
    LAZURE_FORCE=true \
    LAZURE_COLOR=true \
    LAZURE_VARS=$'image=repo/app:sha\nversion=42' \
    LAZURE_EXTRA_ARGS=--print \
    bash "$LIB_DIR/run-deploy.sh"

  assert_capture "deploy vars and extra args" \
    "<--dir><infra><-v><deploy><uat><-y><--wait=false><--logs=false><--wait-timeout><10m><--force><--var><image=repo/app:sha><--var><version=42><--print>"
}

test_sync_secrets() {
  local tmp
  tmp="$(mktemp -d)"
  LAZURE_CAPTURE="$tmp/capture"
  export LAZURE_CAPTURE
  with_fake_lazure "$tmp"

  LAZURE_ENV=dev \
    LAZURE_CONCURRENCY=3 \
    bash "$LIB_DIR/run-sync-secrets.sh"

  assert_capture "sync secrets" \
    "<--dir><deploy><secrets><sync><dev><-y><--concurrency><3>"
}

test_validate() {
  local tmp
  tmp="$(mktemp -d)"
  LAZURE_CAPTURE="$tmp/capture"
  export LAZURE_CAPTURE
  with_fake_lazure "$tmp"

  LAZURE_ENV=prd \
    LAZURE_VERBOSE=true \
    bash "$LIB_DIR/run-validate.sh"

  assert_capture "validate" \
    "<--dir><deploy><-v><validate><prd>"
}

test_invalid_bool_fails() {
  if LAZURE_ENV=dev LAZURE_VERBOSE=maybe bash "$LIB_DIR/run-validate.sh" 2>/tmp/lazure-action-test.err; then
    fail "invalid bool unexpectedly succeeded"
  fi
  grep -q "verbose must be true or false" /tmp/lazure-action-test.err || fail "invalid bool error was not useful"
}

test_install_from_cached_binary() {
  local tmp path bin_dir
  tmp="$(mktemp -d)"
  path="$tmp/path"
  bin_dir="$tmp/toolcache/lazure/0.7.0/Linux-X64"
  mkdir -p "$bin_dir"
  printf '#!/usr/bin/env bash\nprintf "lazure version 0.7.0\\n"\n' > "$bin_dir/lazure"
  chmod +x "$bin_dir/lazure"

  GITHUB_PATH="$path" \
    RUNNER_TEMP="$tmp" \
    RUNNER_OS=Linux \
    RUNNER_ARCH=X64 \
    RUNNER_TOOL_CACHE="$tmp/toolcache" \
    LAZURE_VERSION=0.7.0 \
    bash "$LIB_DIR/install-lazure.sh"

  grep -q "$bin_dir" "$path" || fail "install did not add cached binary dir to PATH"
}

test_install_requires_token_on_cache_miss() {
  local tmp
  tmp="$(mktemp -d)"

  if GITHUB_PATH="$tmp/path" \
    RUNNER_TEMP="$tmp" \
    RUNNER_OS=Linux \
    RUNNER_ARCH=X64 \
    RUNNER_TOOL_CACHE="$tmp/toolcache" \
    GITHUB_TOKEN= \
    LAZURE_VERSION=0.7.0 \
    bash "$LIB_DIR/install-lazure.sh" 2>/tmp/lazure-action-test.err; then
    fail "install without token unexpectedly succeeded on cache miss"
  fi
  grep -q "GITHUB_TOKEN is required" /tmp/lazure-action-test.err || fail "missing token error was not useful"
}

test_actions_do_not_build_from_source() {
  local forbidden
  forbidden="go[ ]build|actions/setup""-go"
  if grep -R -E "$forbidden" "$LIB_DIR/.." --include 'action.yml' --include '*.sh'; then
    fail "actions must download release binaries, not build from source"
  fi
}

test_dependent_actions_assume_lazure_on_path() {
  for action in deploy sync_secrets validate; do
    if grep -q 'investerra/lazure/actions/install\|version:\|github-token\|lazure-bin\|LAZURE_BIN' "$LIB_DIR/../$action/action.yml"; then
      fail "$action must assume lazure is already available in PATH"
    fi
  done
}

test_install_action_surface() {
  local action_yml="$LIB_DIR/../install/action.yml"
  grep -q '^  version:$' "$action_yml" || fail "install action must expose version input"
  grep -q '^  github-token:$' "$action_yml" || fail "install action must expose github-token input"
  if grep -q 'repository:\|outputs:\|lazure-bin\|LAZURE_BIN' "$action_yml"; then
    fail "install action exposes unsupported install details"
  fi
}

test_deploy_defaults
test_deploy_vars_and_extra_args
test_sync_secrets
test_validate
test_invalid_bool_fails
test_install_from_cached_binary
test_install_requires_token_on_cache_miss
test_actions_do_not_build_from_source
test_dependent_actions_assume_lazure_on_path
test_install_action_surface

printf 'ok\n'

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

with_fake_lazure_failed_deploy() {
  local tmp="$1"

  mkdir -p "$tmp/bin"
  cat > "$tmp/bin/lazure" <<'EOF'
#!/usr/bin/env bash
printf '<%s>' "$@" >> "$LAZURE_CAPTURE"
printf '\n' >> "$LAZURE_CAPTURE"

for arg in "$@"; do
  if [[ "$arg" == "deploy" ]]; then
    exit 42
  fi
done
EOF
  chmod +x "$tmp/bin/lazure"

  cat > "$tmp/bin/date" <<'EOF'
#!/usr/bin/env bash
state="${LAZURE_FAKE_DATE_STATE:-/tmp/lazure-fake-date-state}"
if [[ ! -f "$state" ]]; then
  printf '100'
  : > "$state"
else
  printf '105'
fi
EOF
  chmod +x "$tmp/bin/date"
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

test_deploy_failure_dumps_diagnostics() {
  local tmp
  tmp="$(mktemp -d)"
  LAZURE_CAPTURE="$tmp/capture"
  LAZURE_FAKE_DATE_STATE="$tmp/date-state"
  export LAZURE_CAPTURE
  export LAZURE_FAKE_DATE_STATE
  with_fake_lazure_failed_deploy "$tmp"

  if LAZURE_ENV=uat LAZURE_DIR=infra bash "$LIB_DIR/run-deploy.sh" 2>/tmp/lazure-action-test.err; then
    fail "failed deploy unexpectedly succeeded"
  fi

  assert_capture "deploy failure diagnostics" \
    $'<--dir><infra><deploy><uat><-y><--wait=true><--logs=true><--wait-timeout><5m><--no-color>\n<--dir><infra><logs><uat><--tail><20><--follow=false><--no-color>\n<--dir><infra><events><uat><--since><5s><--limit><20><--expand><--no-color>'
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

test_env_guess() {
  local tmp output
  tmp="$(mktemp -d)"
  output="$tmp/github-output"
  LAZURE_CAPTURE="$tmp/capture"
  export LAZURE_CAPTURE
  with_fake_lazure "$tmp"

  GITHUB_OUTPUT="$output" \
    LAZURE_INPUT_ENV= \
    LAZURE_REF_TYPE=branch \
    LAZURE_REF_NAME=main \
    bash "$LIB_DIR/run-env-guess.sh"

  assert_capture "env guess" \
    "<env-guess><--ref-type><branch><--ref-name><main><--github-output>"
}

test_env_guess_input_override() {
  local tmp output
  tmp="$(mktemp -d)"
  output="$tmp/github-output"
  LAZURE_CAPTURE="$tmp/capture"
  export LAZURE_CAPTURE
  with_fake_lazure "$tmp"

  GITHUB_OUTPUT="$output" \
    LAZURE_INPUT_ENV=prd \
    LAZURE_REF_TYPE=branch \
    LAZURE_REF_NAME=main \
    bash "$LIB_DIR/run-env-guess.sh"

  assert_capture "env guess input override" \
    "<env-guess><--env><prd><--ref-type><branch><--ref-name><main><--github-output>"
}

test_env_guess_github_env_fallback() {
  local tmp output
  tmp="$(mktemp -d)"
  output="$tmp/github-output"
  LAZURE_CAPTURE="$tmp/capture"
  export LAZURE_CAPTURE
  with_fake_lazure "$tmp"

  GITHUB_OUTPUT="$output" \
    GITHUB_REF_TYPE=tag \
    GITHUB_REF_NAME=v1 \
    bash "$LIB_DIR/run-env-guess.sh"

  assert_capture "env guess github env fallback" \
    "<env-guess><--ref-type><tag><--ref-name><v1><--github-output>"
}

test_wait_for_deploy_defaults() {
  local tmp
  tmp="$(mktemp -d)"
  LAZURE_CAPTURE="$tmp/capture"
  export LAZURE_CAPTURE
  with_fake_lazure "$tmp"

  LAZURE_ENV=uat \
    GITHUB_SHA=abc123 \
    bash "$LIB_DIR/run-wait-for-deploy.sh"

  assert_capture "wait for deploy defaults" \
    "<--dir><deploy><wait-for-deploy><uat><--expected-sha><abc123><--path></version><--field><commit><--timeout><5m><--interval><10s>"
}

test_wait_for_deploy_custom_args() {
  local tmp
  tmp="$(mktemp -d)"
  LAZURE_CAPTURE="$tmp/capture"
  export LAZURE_CAPTURE
  with_fake_lazure "$tmp"

  LAZURE_ENV=prd \
    LAZURE_DIR=infra \
    LAZURE_VERBOSE=true \
    LAZURE_EXPECTED_SHA=def456 \
    LAZURE_PATH=/internal/version \
    LAZURE_FIELD=git_sha \
    LAZURE_TIMEOUT=10m \
    LAZURE_INTERVAL=5s \
    LAZURE_EXTRA_ARGS=--quiet \
    bash "$LIB_DIR/run-wait-for-deploy.sh"

  assert_capture "wait for deploy custom args" \
    "<--dir><infra><-v><wait-for-deploy><prd><--expected-sha><def456><--path></internal/version><--field><git_sha><--timeout><10m><--interval><5s><--quiet>"
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
  for action in deploy sync_secrets validate env_guess wait_for_deploy; do
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
test_deploy_failure_dumps_diagnostics
test_sync_secrets
test_validate
test_env_guess
test_env_guess_input_override
test_env_guess_github_env_fallback
test_wait_for_deploy_defaults
test_wait_for_deploy_custom_args
test_invalid_bool_fails
test_install_from_cached_binary
test_install_requires_token_on_cache_miss
test_actions_do_not_build_from_source
test_dependent_actions_assume_lazure_on_path
test_install_action_surface

printf 'ok\n'

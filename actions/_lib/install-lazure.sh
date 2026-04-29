#!/usr/bin/env bash
set -euo pipefail

fail() {
  printf 'lazure install action: %s\n' "$*" >&2
  exit 1
}

require() {
  local name="$1"
  local value="$2"

  if [[ -z "$value" ]]; then
    fail "$name is required"
  fi
}

normalize_version() {
  local version="$1"

  if [[ "$version" == v* ]]; then
    printf '%s\n' "$version"
  else
    printf 'v%s\n' "$version"
  fi
}

platform_name() {
  case "$RUNNER_OS" in
    Linux) printf 'linux\n' ;;
    macOS) printf 'darwin\n' ;;
    *) fail "unsupported runner OS: $RUNNER_OS" ;;
  esac
}

arch_name() {
  case "$RUNNER_ARCH" in
    X64) printf 'amd64\n' ;;
    ARM64) printf 'arm64\n' ;;
    *) fail "unsupported runner architecture: $RUNNER_ARCH" ;;
  esac
}

install_lazure_release() {
  require "GITHUB_PATH" "${GITHUB_PATH:-}"
  require "RUNNER_TEMP" "${RUNNER_TEMP:-}"
  require "RUNNER_OS" "${RUNNER_OS:-}"
  require "RUNNER_ARCH" "${RUNNER_ARCH:-}"
  require "RUNNER_TOOL_CACHE" "${RUNNER_TOOL_CACHE:-}"

  LAZURE_REPOSITORY="${LAZURE_REPOSITORY:-investerra/lazure}"
  local input_version="${LAZURE_VERSION:-}"
  require "version" "$input_version"
  local version os arch cache_key bin_dir
  version="$(normalize_version "$input_version")"
  os="$(platform_name)"
  arch="$(arch_name)"
  cache_key="${input_version}/${RUNNER_OS}-${RUNNER_ARCH}"
  bin_dir="${RUNNER_TOOL_CACHE}/lazure/${cache_key}"
  require "version" "$version"

  local asset bin archive url
  asset="lazure_${os}_${arch}.tar.gz"
  bin="${bin_dir}/lazure"
  archive="${RUNNER_TEMP}/${asset}"
  url="https://github.com/${LAZURE_REPOSITORY}/releases/download/${version}/${asset}"

  mkdir -p "$bin_dir"

  if [[ ! -x "$bin" ]]; then
    printf 'Downloading lazure %s from %s\n' "$version" "$url"
    curl -fsSL "$url" -o "$archive"
    tar -xzf "$archive" -C "$bin_dir" lazure
    chmod +x "$bin"
  else
    printf 'Using cached lazure %s at %s\n' "$version" "$bin"
  fi

  "$bin" --version

  printf '%s\n' "$bin_dir" >> "$GITHUB_PATH"
}

install_lazure_release

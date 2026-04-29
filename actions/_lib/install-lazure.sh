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

download_asset() {
  local version="$1"
  local asset="$2"
  local archive="$3"
  local token="${GITHUB_TOKEN:-}"

  require "GITHUB_TOKEN" "$token"

  local api="https://api.github.com/repos/${LAZURE_REPOSITORY}/releases/tags/${version}"
  local asset_id
  asset_id="$(
    curl -fsSL \
      -H "Authorization: Bearer ${token}" \
      -H "Accept: application/vnd.github+json" \
      "$api" |
      jq -r --arg name "$asset" '.assets[] | select(.name == $name) | .id' |
      head -n 1
  )"

  require "release asset ${asset}" "$asset_id"

  printf 'Downloading lazure %s release asset %s\n' "$version" "$asset"
  curl -fsSL \
    -H "Authorization: Bearer ${token}" \
    -H "Accept: application/octet-stream" \
    "https://api.github.com/repos/${LAZURE_REPOSITORY}/releases/assets/${asset_id}" \
    -o "$archive"
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

  local asset bin archive
  asset="lazure_${os}_${arch}.tar.gz"
  bin="${bin_dir}/lazure"
  archive="${RUNNER_TEMP}/${asset}"

  mkdir -p "$bin_dir"

  if [[ ! -x "$bin" ]]; then
    download_asset "$version" "$asset" "$archive"
    tar -xzf "$archive" -C "$bin_dir" lazure
    chmod +x "$bin"
  else
    printf 'Using cached lazure %s at %s\n' "$version" "$bin"
  fi

  "$bin" --version

  printf '%s\n' "$bin_dir" >> "$GITHUB_PATH"
}

install_lazure_release

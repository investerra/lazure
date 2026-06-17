#!/usr/bin/env bash
# Resolve the requested lazure version to a concrete vX.Y.Z tag.
#
#   - "latest" (any case) -> the newest GitHub release's tag (via /releases/latest)
#   - "0.10.7" / "v0.10.7" -> "v0.10.7" (leading v ensured)
#
# Emits `version=<vX.Y.Z>` to $GITHUB_OUTPUT so the action can key the cache and
# the install on the RESOLVED version — that's what makes "latest" pick up new
# releases (a moved latest changes the cache key) instead of serving a stale
# cached binary.
set -euo pipefail

fail() {
  printf 'lazure resolve-version: %s\n' "$*" >&2
  exit 1
}

LAZURE_REPOSITORY="${LAZURE_REPOSITORY:-investerra/lazure}"
input="${INPUT_VERSION:-}"
[[ -n "$input" ]] || fail "version is required"

if [[ "${input,,}" == "latest" ]]; then
  token="${GITHUB_TOKEN:-}"
  [[ -n "$token" ]] || fail "GITHUB_TOKEN is required to resolve 'latest'"
  version="$(
    curl -fsSL \
      -H "Authorization: Bearer ${token}" \
      -H "Accept: application/vnd.github+json" \
      "https://api.github.com/repos/${LAZURE_REPOSITORY}/releases/latest" |
      jq -r '.tag_name'
  )"
  [[ -n "$version" && "$version" != "null" ]] || fail "could not resolve latest release tag"
else
  case "$input" in
    v*) version="$input" ;;
    *) version="v$input" ;;
  esac
fi

printf 'Resolved lazure version: %s (requested: %s)\n' "$version" "$input"
printf 'version=%s\n' "$version" >>"${GITHUB_OUTPUT:?GITHUB_OUTPUT not set}"

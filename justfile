_default:
    @just --list

# Cut a release: bump the semver tag, annotate with release notes, push,
# watch the release workflow, and send a desktop notification.
# Usage: just release [patch|minor|major]   (default: patch)
release bump='patch':
    ./scripts/release.sh {{ bump }}

_default:
    @just --list

# Cut a release: bump the semver tag, annotate with release notes, push,
# watch the release workflow, and send a desktop notification.
# Usage: just release [patch|minor|major]   (default: patch)
release bump='patch':
    #!/usr/bin/env bash
    set -euo pipefail

    case "{{ bump }}" in
        patch|minor|major) ;;
        *) echo "error: bump must be patch, minor, or major (got '{{ bump }}')" >&2; exit 1 ;;
    esac

    branch=$(git rev-parse --abbrev-ref HEAD)
    if [ "$branch" != "main" ] && [ "$branch" != "master" ]; then
        echo "error: releases must be cut from main or master (currently on '$branch')" >&2
        exit 1
    fi

    if [ -n "$(git status --porcelain)" ]; then
        echo "error: working tree is not clean; commit or stash first:" >&2
        git status --short >&2
        exit 1
    fi

    echo "fetching tags from origin..."
    git fetch origin --tags --quiet

    latest=$(git tag -l 'v[0-9]*.[0-9]*.[0-9]*' --sort=-v:refname | head -n1)
    [ -n "$latest" ] || latest="v0.0.0"
    IFS=. read -r maj min pat <<< "${latest#v}"
    case "{{ bump }}" in
        major) maj=$((maj + 1)); min=0; pat=0 ;;
        minor) min=$((min + 1)); pat=0 ;;
        patch) pat=$((pat + 1)) ;;
    esac
    new="v${maj}.${min}.${pat}"

    if [ "$latest" = "v0.0.0" ]; then
        notes=$(git log --pretty='- %s' -n 50 HEAD)
        total=$(git rev-list --count HEAD)
        [ "$total" -gt 50 ] && notes="${notes}"$'\n'"... and $((total - 50)) more"
    else
        notes=$(git log "${latest}..HEAD" --pretty='- %s')
    fi
    [ -n "$notes" ] || notes="(no changes)"

    echo
    echo "release plan:"
    echo "  bump:  {{ bump }}"
    echo "  tag:   $latest -> $new"
    echo
    echo "release notes:"
    echo "$notes" | sed 's/^/  /'
    echo
    read -r -p "proceed? [y/N] " reply
    case "$reply" in
        y|Y|yes|YES) ;;
        *) echo "aborted."; exit 1 ;;
    esac

    git tag -a "$new" -m "$new" -m "$notes"
    echo "pushing $new..."
    git push origin "$new"

    echo "waiting for the release workflow to start..."
    run_id=""
    for _ in $(seq 1 20); do
        run_id=$(gh run list --workflow=release.yml --branch "$new" --limit 1 \
            --json databaseId --jq '.[0].databaseId // empty' 2>/dev/null || true)
        [ -n "$run_id" ] && break
        sleep 3
    done

    status="unknown"
    if [ -z "$run_id" ]; then
        echo "warning: no release workflow run detected for $new (tag is pushed)." >&2
    elif gh run watch "$run_id" --exit-status; then
        status="success"
    else
        status="failure"
    fi

    just _notify "$new" "$status"

# Desktop notification, OS-aware (notify-send on Linux, terminal-notifier on macOS).
_notify tag status:
    #!/usr/bin/env bash
    set -euo pipefail
    case "{{ status }}" in
        success) title="lazure released {{ tag }}"; body="Release workflow completed successfully." ;;
        failure) title="lazure release FAILED {{ tag }}"; body="Release workflow did not succeed." ;;
        *)       title="lazure {{ tag }} pushed"; body="Could not observe the release workflow." ;;
    esac
    case "$(uname -s)" in
        Darwin)
            if command -v terminal-notifier >/dev/null 2>&1; then
                terminal-notifier -title "$title" -message "$body"
            else
                osascript -e "display notification \"$body\" with title \"$title\""
            fi
            ;;
        Linux)
            if command -v notify-send >/dev/null 2>&1; then
                notify-send "$title" "$body"
            else
                echo "$title — $body"
            fi
            ;;
        *) echo "$title — $body" ;;
    esac

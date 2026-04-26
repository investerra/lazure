# lazure

A Go CLI for deploying and managing Azure Container Apps.

## Install

> The lazure repo is **private**. `curl`-from-`raw.githubusercontent.com`
> won't work for unauthenticated requests; use `gh` (which carries your
> GitHub auth) or `go install` with a configured `GOPRIVATE`.

### Via `gh`

```sh
gh release download --repo investerra/lazure -p '*linux_amd64*' -p 'checksums.txt'
sha256sum -c checksums.txt --ignore-missing
tar -xzf lazure_linux_amd64.tar.gz lazure
sudo install lazure /usr/local/bin/
```

Pick the matching archive for your platform:
`lazure_linux_amd64.tar.gz` · `lazure_linux_arm64.tar.gz` ·
`lazure_darwin_amd64.tar.gz` · `lazure_darwin_arm64.tar.gz`.

### Via `go install` (Go 1.26+)

```sh
export GOPRIVATE=github.com/investerra/*
go install github.com/investerra/lazure@latest
```

### Subsequent updates

```sh
lazure self-update --check          # report only
lazure self-update                  # download + atomic replace
```

`self-update` reuses your `gh auth token` for the download, so private-repo
access works without setting `GITHUB_TOKEN` explicitly.

## Quick start

```sh
lazure init                         # scaffold ./deploy/
lazure secrets edit dev             # fill in + encrypt secrets
lazure doctor                       # preflight: git/editor/az/gh/auth + per-env
lazure validate dev                 # static checks (no Azure calls)
lazure render dev                   # preview the ARM payload
lazure deploy dev                   # deploy (auto-waits + tails logs on TTY)
```

## Commands

### Pipeline
`deploy` · `render` · `diff` · `validate` · `release` · `self-update`

### Operations
`status` · `logs` · `revisions` · `rollback` · `restart` · `scale` ·
`ports` · `events` · `exec`

### Diagnostics
`doctor` · `env list` · `env diff`

### Configuration
`init` · `schema` · `secrets {view,edit,verify,sync}` ·
`vars {view,edit,verify}`

### Global flags
`-v`/`--verbose`, `-q`/`--quiet`, `--log-level`, `--log-format=text|json`,
`--dir` (defaults to `./deploy`).

Run `lazure <command> --help` for flags and examples.

## Editor integration

`lazure init` scaffolds a `deploy/deploy.schema.json` and embeds the
modeline `# yaml-language-server: $schema=./deploy.schema.json` in
`deploy.yml`. VS Code (Red Hat YAML extension), Neovim, and Helix pick
this up automatically — no further setup needed for autocomplete +
inline validation.

Refresh the schema after a `self-update`:

```sh
lazure schema                       # writes <dir>/deploy.schema.json
lazure schema -                     # to stdout (pipe to validators / jq)
```

## License

MIT — see [LICENSE](LICENSE).

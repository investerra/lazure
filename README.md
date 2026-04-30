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
lazure prime                        # print the agent guide + command reference
lazure env-guess                    # map current branch/tag to deploy env
lazure secrets edit dev             # fill in + encrypt secrets
lazure doctor                       # preflight: git/editor/az/gh/auth + per-env
lazure validate dev                 # static checks (no Azure calls)
lazure render dev                   # preview the ARM payload
lazure deploy dev                   # deploy (auto-waits + tails logs on TTY)
lazure deploy dev --build           # build, push, then deploy
lazure wait-for-deploy dev          # poll /version until the new commit is live
lazure rollout uat -y               # tag, build, sync secrets, push, deploy, verify
```

## Commands

### Pipeline
`deploy` · `rollout` · `build` · `render` · `diff` · `validate` · `release` ·
`self-update`

### Operations
`status` · `logs` · `revisions` · `rollback` · `restart` · `scale` ·
`ports` · `events` · `exec` · `wait-for-deploy`

### Diagnostics
`prime` · `doctor` · `config` · `env list` · `env diff` · `env-guess`

### Configuration
`init` · `schema` · `secrets {view,edit,verify,sync}` ·
`vars {view,edit,verify}`

### Global flags
`-v`/`--verbose`, `-q`/`--quiet`, `--log-level`, `--log-format=text|json`,
`--dir` (defaults to `./deploy`).

Run `lazure <command> --help` for flags and examples.

## GitHub Actions

This repo exports composite actions for app repositories that want to run
Lazure from CI. Install Lazure once, then call the task actions. The task
actions assume `lazure` is already available in `PATH`.

For private `investerra/lazure` releases, pass a token that can read this
repository's release assets. A caller repo's default `GITHUB_TOKEN` usually
cannot read a different private repository, so use a PAT or GitHub App token
stored as a secret.

```yaml
permissions:
  contents: read
  id-token: write

steps:
  - uses: actions/checkout@v4

  - uses: azure/login@v2
    with:
      client-id: ${{ vars.AZURE_CLIENT_ID }}
      tenant-id: ${{ vars.AZURE_TENANT_ID }}
      subscription-id: ${{ vars.AZURE_SUBSCRIPTION_ID }}

  - uses: investerra/lazure/actions/install@v1
    with:
      version: v0.7.1
      # Required when the caller repo cannot read private lazure releases
      # with its default GITHUB_TOKEN.
      github-token: ${{ secrets.LAZURE_RELEASE_TOKEN }}

  - uses: investerra/lazure/actions/validate@v1
    with:
      env: dev

  - uses: investerra/lazure/actions/sync_secrets@v1
    with:
      env: dev

  - uses: investerra/lazure/actions/deploy@v1
    with:
      env: dev
      force: false

  - uses: investerra/lazure/actions/wait_for_deploy@v1
    with:
      env: dev
```

Environment detection can replace app-local branch-mapping scripts:

```yaml
  - id: env
    uses: investerra/lazure/actions/env_guess@v1

  - uses: investerra/lazure/actions/validate@v1
    with:
      env: ${{ steps.env.outputs.environment }}
```

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

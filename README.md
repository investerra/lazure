# lazure

A friendly CLI for shipping apps to Azure Container Apps.

If you're new here, start with **[What is Lazure?](#what-is-lazure)** below. If you've used it before, jump to [Commands](#commands) or [Quick start](#quick-start).

---

## What is Lazure?

Lazure is a single command-line tool (`lazure`) that takes the app you've built and runs it on Azure. It replaces a long, fiddly checklist of Azure clicks and `az`-CLI commands with one workflow.

You describe **what** you want — image, env vars, secrets, scaling, etc. — in a few YAML files in your repo. Lazure handles **how** to make Azure match that description: validating the config, encrypting secrets, building Docker images, pushing to the registry, deploying to Container Apps, watching logs, rolling back if it goes wrong.

You get a deploy in one command:

```sh
lazure deploy dev
```

…and a full safe pipeline (build, test, deploy, verify) in another:

```sh
lazure rollout uat -y
```

## When to use which command

The vocabulary up front:

- An **environment** (or "env") is a target like `dev`, `uat`, `prd`. Each env is its own Container App with its own settings.
- The **manifest** is `deploy/deploy.yml` — the YAML file that describes your app.
- **Vars** are plain-text settings (log level, image name, regions). **Secrets** are encrypted (database passwords, API keys).

Day-to-day you'll use these:

| Goal | Command |
|---|---|
| Set up a brand-new project | `lazure init` |
| Edit secrets for an env | `lazure secrets edit dev` |
| See what would happen before doing it | `lazure validate dev` then `lazure render dev` |
| Deploy a change | `lazure deploy dev` |
| Build, push, deploy a fresh image | `lazure deploy dev --build` |
| Tag → build → push → deploy a release | `lazure rollout uat -y` |
| See what's running | `lazure status dev` |
| Watch logs | `lazure logs dev --follow` |
| Roll back a bad deploy | `lazure rollback dev` |
| List what changed in Azure vs your repo | `lazure diff dev` |
| Health-check your local setup | `lazure doctor` |

Run `lazure <command> --help` for any command's full options.

## How configuration files are organized

Lazure reads a small set of YAML files from a `deploy/` directory in your repo:

```
deploy/
├── deploy.yml                   ← THE manifest. Describes the app. Required.
├── deploy.schema.json           ← Editor autocomplete. Optional.
├── vars.yml                     ← Shared vars across every env. Optional.
├── secrets.yml                  ← Shared encrypted secrets. Optional.
└── envs/
    ├── dev.vars.yml             ← Per-env vars. Optional.
    ├── dev.secrets.yml          ← Per-env encrypted secrets. Optional.
    ├── uat.vars.yml
    ├── uat.secrets.yml
    ├── prd.vars.yml
    └── prd.secrets.yml
```

Only `deploy.yml` is required. **Every other file is optional.** Lazure works even if a file is missing — `lazure doctor` will show you which files exist and which don't.

### Layered settings: shared first, per-env wins

Many projects share most of their config across environments and only differ in a handful of values (resource group name, log level). Instead of repeating yourself in three files, put the common bits in `deploy/vars.yml`:

```yaml
# deploy/vars.yml — applies to every env
acr_server: investerra.azurecr.io
service_name: api-server
docker_image: '{{ .Vars.acr_server }}/{{ .Vars.service_name }}:{{ .Vars.git_short_commit }}'
log_level: info
```

Then override only what changes in `deploy/envs/<env>.vars.yml`:

```yaml
# deploy/envs/prd.vars.yml — production overrides
log_level: warn
resource_group: prd-rg
```

Lazure merges them so production gets `log_level: warn` while dev still gets `info`. The same pattern works for secrets (`deploy/secrets.yml` shared, `deploy/envs/<env>.secrets.yml` per-env, per-env wins on conflict).

The full precedence order (lowest → highest):

```
1. Standard vars Lazure injects (app_env, git_*, keyvault_url)
2. deploy/vars.yml          ← shared, optional
3. deploy/envs/<env>.vars.yml ← per-env, optional
4. --var KEY=VALUE on the command line
```

To edit the shared files, use the reserved name `shared`:

```sh
lazure vars edit shared            # edits deploy/vars.yml
lazure secrets edit shared         # edits deploy/secrets.yml
```

To see what's there for a given env, use the `config` family:

```sh
lazure config view dev             # all vars + secrets for dev (secrets redacted)
lazure config view dev --reveal    # show full secret values
lazure config diff dev prd         # spot what differs between two envs
lazure config get dev DATABASE_URL # one value
```

## Quick start

Set up a new project:

```sh
lazure init                          # scaffold ./deploy/ with examples
lazure secrets edit dev              # add database password etc., saves encrypted
lazure doctor                        # check your setup is healthy
lazure validate dev                  # static checks (no Azure calls)
lazure deploy dev                    # ship it
```

Day-two operations:

```sh
lazure status dev                    # what's running
lazure logs dev --follow             # live log tail
lazure rollback dev                  # back to the previous revision
lazure config diff dev uat           # compare envs side-by-side
```

For a production release with the full pipeline (calver tag → build → push → secrets sync → deploy → verify):

```sh
lazure rollout prd -y
```

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

## Commands

### Pipeline
`deploy` · `rollout` · `build` · `render` · `diff` · `validate` · `release` ·
`self-update`

### Operations
`status` · `logs` · `revisions` · `rollback` · `restart` · `scale` ·
`ports` · `events` · `exec` · `wait-for-deploy`

### Diagnostics
`prime` · `doctor` · `rules` · `env list` · `env diff` · `env-guess`

### Configuration
`init` · `schema` · `secrets {new,view,edit,verify,sync,decrypt,encrypt,export}` ·
`vars {view,edit,verify,export}` ·
`config {view,export,dotenv,json,keys,get,verify,diff}`

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

## Tags

Azure resource tags (top-level key/value labels used for cost allocation,
ownership, Azure Policy, automation hints) are declared under `app.tags`
in `deploy.yml`:

```yaml
app:
  name: my-service
  location: switzerlandnorth
  resource_group: "{{ .Vars.resource_group }}"
  managed_environment_id: "{{ .Vars.managed_environment_id }}"
  identity: "{{ .Vars.user_assigned_identity_id }}"
  tags:
    service: my-service
    env: "{{ .Vars.app_env }}"        # per-env value
    managed_by: lazure
    cost_center: platform
```

Values are plain strings and accept the standard `{{ .Vars.x }}` templating,
so you can derive per-env tags from your vars files.

Lazure owns these tags on every deploy — anything in the live resource
that isn't in `app.tags` is removed. If another system (Terraform,
Azure Policy) is also stamping tags onto the same Container App, declare
them all in `deploy.yml` to keep them, or stop the other system from
touching that resource.

## Ingress

Azure rejects most combinations of `external`, `transport`, and `exposed_port`. Three combinations work:

**External HTTP** — public on the internet, browser-reachable. Most common.

```yaml
ingress:
  external: true
  target_port: 8000
  transport: http        # or http2 / auto
  # NO exposed_port — Azure rejects it on non-TCP transports
```

**Internal TCP** — reachable only from within the same Container Apps managed environment. Use for service-to-service that's not HTTP.

```yaml
ingress:
  external: false
  target_port: 8000
  exposed_port: 8000     # allowed (and effectively required) for TCP
  transport: tcp
```

**Internal HTTP** — HTTP service reachable only from inside the managed env.

```yaml
ingress:
  external: false
  target_port: 8000
  transport: http
  # NO exposed_port
```

**What Azure rejects:**

| external | transport | exposed_port | result |
|----------|-----------|--------------|--------|
| `true`   | `tcp`     | any          | `ContainerAppTcpRequiresVnet` — external TCP needs a VNet-attached managed env |
| any      | `http` / `http2` / `auto` | set | Azure ignores or rejects `exposed_port` for non-TCP transports |

## Developing

<details>
<summary> Local setup, tests, lint, build</summary>

Working on lazure itself? You need Go 1.26+ and `golangci-lint` v2.11.4 (matches CI).

Install Go 1.26 (Linux x86_64; adjust archive for darwin/arm64 as needed):

```sh
curl -fsSL https://go.dev/dl/go1.26.0.linux-amd64.tar.gz -o /tmp/go1.26.tar.gz
mkdir -p ~/sdk && tar -xzf /tmp/go1.26.tar.gz -C ~/sdk/ && mv ~/sdk/go ~/sdk/go1.26
export PATH="$HOME/sdk/go1.26/bin:$PATH"   # add to ~/.bashrc to persist
go version                                  # should print: go1.26.x
```

Install `golangci-lint` at the CI-pinned version:

```sh
curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/HEAD/install.sh \
  | sh -s -- -b ~/.local/bin v2.11.4
golangci-lint version                       # should print: 2.11.4
```

Run tests:

```sh
go test ./...                                # all packages
go test -count=1 ./...                       # bypass test cache
go test -v -run TestValidate_Ingress ./internal/lazurecfg/   # one test by regex
go test -cover ./...                         # with coverage
go test -race ./...                          # race detector
```

Run lint locally (same command CI runs):

```sh
golangci-lint run --timeout 5m
```

Build + install a local binary (replaces the one from `lazure self-update`):

```sh
go build -o ~/.local/bin/lazure .
```

After editing the manifest struct (`internal/lazurecfg/schema.go`), regenerate the embedded JSON Schema so editor tooling stays in sync:

```sh
go run ./cmd/genschema internal/schema/schema.json
```

Pre-push gate (matches CI's `lint` + `test` jobs):

```sh
golangci-lint run --timeout 5m && go test ./...
```

</details>

## Troubleshooting

If something looks wrong, run **`lazure doctor`** first. It enumerates every file Lazure expects, every Azure permission it needs, and every per-env stage of the load pipeline, and tells you exactly what's missing or broken.

Other quick diagnostics:

```sh
lazure config view dev               # see what env vars + secrets the container will get
lazure config verify dev             # confirm everything resolves before a deploy
lazure validate dev                  # static checks against the manifest
lazure render dev                    # see the exact ARM payload Lazure would send
lazure events dev --expand           # show Azure error details after a failed deploy
lazure diff dev                      # spot drift between Azure and your repo
```

## License

MIT — see [LICENSE](LICENSE).

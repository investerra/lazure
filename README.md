# lazure

A Go CLI for deploying and managing Azure Container Apps.

## Install

Download a prebuilt binary from the [releases page](https://github.com/investerra/lazure/releases):

```sh
# Linux x86_64
curl -L -o lazure.tar.gz \
  https://github.com/investerra/lazure/releases/latest/download/lazure_Linux_amd64.tar.gz
tar -xzf lazure.tar.gz lazure
sudo install lazure /usr/local/bin/
```

Or build from source (requires Go 1.26+):

```sh
go install github.com/investerra/lazure@latest
```

## Quick start

```sh
lazure init                         # scaffold ./deploy/
lazure secrets edit dev             # fill in + encrypt secrets
lazure doctor                       # preflight checks
lazure render dev                   # preview the ARM payload
lazure deploy dev --wait --logs     # deploy + stream live logs
```

## Commands

`deploy` · `render` · `diff` · `release` · `status` · `logs` · `revisions`
· `rollback` · `restart` · `exec` · `doctor` · `init` · `secrets {view,edit,verify,sync}`
· `vars {view,edit,verify}` · `self-update`

Run `lazure <command> --help` for flags and usage.

## License

MIT — see [LICENSE](LICENSE).

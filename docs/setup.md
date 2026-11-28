# Set up pluxy

How to build pluxy from source, start core (the proxy), find the files it keeps, and run it in a
container. The [quick start](../README.md#quick-start) walks through the same build and a first
request; this page is the full set of steps and choices.

## Build from source

You need [Git](https://git-scm.com/downloads), [Go](https://go.dev/dl/) (the version in
`next/go.mod`) and [Bun](https://bun.sh) 1.4.3. Run the commands from `next/`:

```bash
go generate ./internal/panel        # builds the web panel core carries inside it; needs Bun
go run ./cmd/pluxy-pack -out dist   # packs the provider extensions and their index for this machine
go build -o pluxy ./cmd/pluxy       # builds core
```

- **The web panel.** `go generate ./internal/panel` runs `bun install --frozen-lockfile` and `bun run build` in
  `next/ui` and writes the panel into `internal/panel/dist`, which `go build` embeds. Without it,
  core builds and runs, but `/panel/` answers 503 with how to build it.
- **The provider extensions.** `pluxy-pack` builds every extension in `next/extensions/` as a
  release archive and writes `dist/bundled/index.json`, the index of the bundled registry. Core
  installs extensions from it when you ask. Without a bundled registry, the panel's Setup page has no
  providers to offer and Extensions has nothing to install. [Pack extensions](extensions.md#pack-extensions)
  lists its flags, such as `-platforms` for other systems.
- **Build info.** Without `-ldflags`, `pluxy -version` gives the version as `dev`, with the commit
  and time Go recorded from the git checkout. To set the version, commit and date:

  ```bash
  go build -ldflags "-X github.com/stdotwork/pluxy/next/internal/buildinfo.Version=1.2.3 -X github.com/stdotwork/pluxy/next/internal/buildinfo.Commit=$(git rev-parse HEAD) -X github.com/stdotwork/pluxy/next/internal/buildinfo.Date=$(date -u +%Y-%m-%dT%H:%M:%SZ)" -o pluxy ./cmd/pluxy
  ```

  Add `-trimpath` and `-s -w` inside `-ldflags` for a smaller program; nothing else changes.

An install is the `pluxy` program with the packed `bundled` folder beside it, where core looks for
`bundled/index.json` by default. To keep them apart, name the index with `-bundled`.

## Start core

```bash
./pluxy serve -bundled dist/bundled/index.json
```

`pluxy` with no command serves too. Without `-config`, core uses `config.json` in the folder it
starts in when there is one, else `config.json` in pluxy's home. When neither exists, it writes a
first config in pluxy's home and starts on it: it listens on `127.0.0.1:8317` (or a free port when
8317 is taken), with one new client key and a new management key, in a file only your user can read.
Core prints the file's path and its address, never the keys: read them in the file, or run
`pluxy key management --show-key`. `pluxy init` writes the same file without starting core.
It writes nothing when the desktop app already keeps a core of its own: it then says to serve that
core's config with `-config`, or to make a separate pluxy with `pluxy init`
([Where the config is](config.md#where-the-config-is)). It also refuses a folder that other users
could write in ([A first config](config.md#a-first-config)).

To write the config yourself, it needs at least these keys; core refuses to start without a
management key:

```json
{
  "listen": "127.0.0.1:8317",
  "client_keys": ["choose-a-client-key"],
  "management_key": "choose-a-management-key"
}
```

```bash
./pluxy serve -config config.json -bundled dist/bundled/index.json
```

A first start installs no provider. Open `http://127.0.0.1:8317/panel/`, sign in with the
management key and tick the providers you use on **Set up pluxy**, or install one from the terminal
with `pluxy setup apply --pick pluxy/claude` or `pluxy ext install pluxy/claude`. Then add an account
([Providers](providers.md)) and point a client at core ([Clients](clients.md)).

[`pluxy serve`](cli.md#pluxy-serve) lists core's flags, such as `-safe-mode` (start with no
extensions) and `-watch-interval`.

## Where pluxy keeps its files

Everything a pluxy keeps is in the folder of its config file: `config.json` and, beside it, the
`credentials`, `packages`, `logs`, `usage` and `state` folders core makes. Without `-config`, that
folder is pluxy's home:

| System | pluxy's home |
| --- | --- |
| Linux and other Unix systems | `~/.local/share/pluxy` (`$XDG_DATA_HOME/pluxy` when that is set to an absolute path) |
| macOS | `~/Library/Application Support/pluxy` |
| Windows | `%LOCALAPPDATA%\pluxy` |

`PLUXY_HOME` names another folder, and `pluxy help` prints the one it finds.
[Where the config is](config.md#where-the-config-is) has the rules: which `config.json` each command
reads, what each folder holds, and the desktop app's own core.

## Run it in a container

`next/Dockerfile` builds an Alpine image with a static `pluxy`, the web panel built in, and every
extension packed as the bundled packages, which core installs only when asked. It runs as user
`65532`, not root. From `next/`:

```bash
docker build -t pluxy .
```

Core takes its settings from `config.json`, and keeps everything in `/config`, a volume: the config
and, beside it, the folders core makes. Write the config before the first start. Two keys differ
from a config on your own machine:

- `listen` must be `0.0.0.0:8400`. The container's own `127.0.0.1` cannot be reached from outside
  it, and the image's health check asks port 8400. With an empty `/config`, core writes a first
  config that listens on `127.0.0.1`, which nothing outside reaches and the health check fails.
- `management_allow_remote` must be `true` for the panel and the `pluxy` commands to work through
  the published port: requests through it arrive from Docker's network address, not from
  `127.0.0.1`, and the management API answers only its own machine unless this is on. Calls still
  need the management key.

```json
{
  "listen": "0.0.0.0:8400",
  "client_keys": ["choose-a-client-key"],
  "management_key": "choose-a-management-key",
  "management_allow_remote": true
}
```

```bash
mkdir config                         # put config.json in it
sudo chown -R 65532:65532 config     # the image's user must be able to write to it
docker run -d --name pluxy -p 127.0.0.1:8400:8400 -v "$PWD/config:/config" pluxy
```

Publish the port on `127.0.0.1` only, as above, unless other machines should reach it; then put TLS in
front of it or turn on `tls` ([config.md](config.md#tls)) first.

[`next/compose.example.yaml`](../next/compose.example.yaml) runs the same with Docker Compose, with a
read-only root file system (`/tmp` is a `tmpfs`), every capability dropped and no added privileges.
It names the image `ghcr.io/stdotwork/pluxy:latest`; to run your own build, use its `build: .` line
instead.

- **Health check.** The image asks `http://127.0.0.1:8400/healthz` every 30 seconds. With another
  listen port or with `tls` on, replace it (`docker run --health-cmd ...`, or `healthcheck:` in
  Compose).
- **Extension pages.** They are served on a port of their own, by default a free port on the host of
  `listen`, which Docker does not publish. To use them from a container, set
  `extension_pages.listen` to a fixed port such as `0.0.0.0:8401`, publish it, and set
  `extension_pages.url` ([config.md](config.md#extension_pages)).

## First launch

Written from the product as it stands; see the panel and app pages: [Web panel](panel.md) and
[Desktop app](app.md).

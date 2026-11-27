# Extensions and packages

An **extension** is a separate program that connects pluxy to one kind of upstream, such as Codex
or Claude. pluxy's core (the part that listens for requests and picks an account) knows nothing
about any provider: it starts an extension, sends it the request over a local socket, and passes
the reply back. An account (a sign-in or an API key) belongs to one extension, so an account works
only while its extension is installed and turned on.

An extension is delivered as a **package**: a release archive from a **registry** (a list of
packages and the archives that go with them). This page says how to get packages, manage them and
pack your own. To write an extension, follow the
[extension tutorial](../next/protocol/TUTORIAL.md); the protocol itself is in
[`next/protocol/README.md`](../next/protocol/README.md).

A package has three independent states:

| State | Meaning |
| --- | --- |
| Installed | Its files are in the packages folder. |
| Turned on (**enabled**) | Core runs it and routes requests to its accounts. |
| Configured | It has accounts, from the config file or added by sign-in. |

Removing a package keeps its accounts. They serve again once the package is installed again.

## Where packages come from

Core knows no online registry and fetches nothing until you ask. It reads packages from these
sources:

| Source | What it is |
| --- | --- |
| The bundled registry | The extensions pluxy ships, packed into a `bundled` folder: the folder beside the `pluxy` program, or the index that `pluxy serve -bundled <index.json>` names. It needs no network. The desktop app and the container image have it built in. Core installs nothing from it by itself: you install what you use. |
| A registry you add | An `index.json` at an `https://` address, or at a path on this machine, listed under `packages.registries` in the config file (see [Add a registry](#add-a-registry)). |
| A folder on disk | Not a package: the config's `extension` key runs an extension straight from its manifest path, which is how you try an extension you are writing (see [Run an extension from a folder](#run-an-extension-from-a-folder)). |

The extensions pluxy ships, as `pluxy ext available` lists them from the bundled registry:

| Package ID | Title | Tier |
| --- | --- | --- |
| `pluxy/codex` | Codex and ChatGPT | pick |
| `pluxy/claude` | Claude | pick |
| `pluxy/openai-compatible` | OpenAI-compatible APIs | pick |
| `pluxy/gemini` | Gemini | pick |
| `pluxy/xai` | Grok (xAI) | pick |
| `pluxy/kimi` | Kimi Code | pick |
| `pluxy/devin` | Devin | verified |
| `pluxy/meta` | Meta Muse | verified |

The tier says how far the registry vouches for a package. `pick` marks the packages the guided
setup offers on a new install; `verified` marks the rest of pluxy's own; `unverified` is anything
else a registry lists. Core shows the tier and enforces nothing by it. Which sign-in methods and
options each provider has is in [Providers](providers.md).

## Install an extension

Installing downloads the release for this machine's platform, checks it (see
[What core checks](#what-core-checks-before-it-installs)), unpacks it into the packages folder and,
when you ask for it, turns it on. Installing a release that is already installed and intact changes
no files; it only turns the package on, when you asked for that.

**Panel or desktop app.** Open **Settings**, then **Extensions**. Under **Available to install**
each package a registry offers has an **Install** button. **Install** installs the newest release
and turns it on. The desktop app shows the same page. A package that fails to start stays installed
and turned off, and the page says why.

**Command line.** The extension commands (`pluxy ext list` and the others below) call a running
core, as every client command does (see [CLI](cli.md)):

```bash
pluxy ext available                       # what each registry offers, with titles and tiers
pluxy ext install pluxy/claude            # install and turn on
pluxy ext install pluxy/claude --version <release> --registry bundled
pluxy ext list                            # installed packages: ID, version, enabled, state, registry
```

Without `--version` the install takes the first release the registry lists that speaks a protocol
version this core speaks and has an archive for this platform. Without `--registry` it takes the
first registry that lists the package, in the order of the config file, then `bundled`.

**Management API.** `POST /v0/management/packages` with `{"id": "pluxy/claude"}` installs it. The
call turns the package on only when the body has `"enable": true`. The calls are in
[Management API](management-api.md).

A first install of a package needs no restart. Add an account for it afterwards (**Accounts**, or
`pluxy account add`; see [Providers](providers.md)).

## Turn an extension off or on

Turning an extension off stops routing to it. Requests already running on it finish, for up to its
drain timeout (10 minutes by default). Its files and accounts stay.

| Where | Turn off | Turn on |
| --- | --- | --- |
| Panel or app | **Extensions**, open the package, **Turn off** | **Extensions**, open the package, **Turn on** |
| Command line | `pluxy ext disable pluxy/claude` | `pluxy ext enable pluxy/claude` |
| Management API | `POST /v0/management/packages/{id}/disable` | `POST /v0/management/packages/{id}/enable` |

## Update or replace an extension

There is no separate update command. Installing another release of an installed package replaces
it. The accounts stay. A package that was on stays on. The command line also turns on a package that
was off; the management API does so only when the call says `"enable": true`:

```bash
pluxy ext install pluxy/claude --version <release>
```

- The new release is downloaded and checked before anything changes. If the check fails, the old
  release stays.
- When the package is on, core starts the new release and waits for it to answer before it switches
  requests over. If the new release does not start, the old one keeps serving.
- Requests that started on the old release finish on it, up to the drain timeout. Routing state
  (cooldowns, blocks, session bindings, the round-robin turn) carries over for accounts whose ID,
  extension and `settings` stay the same.
- A Responses WebSocket session lives in the old release. It closes when its running turn ends; the
  client's next turn that continues it (with a `previous_response_id`, or a `response.append`) gets
  `previous_response_not_found`. A turn that carries the whole conversation and
  continues nothing opens a session on the new release.
- Installing the same release again changes nothing, unless its files were changed since the
  install: then the checked release replaces them, but only once no running or draining copy uses
  the files (turn the package off and wait for its requests to finish first; otherwise the install
  is refused as a conflict). If the registry now pins a different archive for an installed release,
  the install is refused; remove the package first.

The panel's **Install** button appears only for packages that are not installed, so the panel does
not update a package. Use the command line or the management API.

## Remove an extension

| Where | How |
| --- | --- |
| Panel or app | **Extensions**, open the package, **Remove** (it asks first) |
| Command line | `pluxy ext remove pluxy/claude` |
| Management API | `DELETE /v0/management/packages/{id}` |

Core turns the package off, then deletes its files once the requests running on it have finished.
Its accounts and its settings stay.

## What reloads without a restart

Package changes go through the same step as a config change: core reads the config file, prepares
everything with the change applied, and switches to it in one step. A config file that does not
load also refuses the package change. One package change runs at a time.

Core reloads its config when the file changes (it looks every second; `-watch-interval` changes
that, and `0` turns it off), on `SIGHUP`, on `pluxy reload`, or on `POST /v0/management/reload`.
Saving a file with the same contents does not reload; `pluxy reload` and `SIGHUP` always do. A
reload that fails at any step, such as a broken config or the `extension` entry's program not
starting, leaves the running config in place, and `pluxy status` shows why. An enabled package that
is not running and cannot start is the exception: core skips it, shows it as `failed` and serves
with the rest. A package that is running and cannot be prepared refuses the reload.

| Change | Effect |
| --- | --- |
| Install, turn on or off, remove, replace a package | Applied at once, as above. |
| An extension's manifest or program changes (a replaced release, or a rebuilt program of an extension run from a folder) | A reload starts the new version, waits for its handshake and switches to it; the old version drains. |
| An extension's accounts, or an account's `settings` field, change | Applied by the reload. An account whose `settings` change starts with fresh routing state. |
| Settings edited on an extension's own page | Saved at once; no reload. |
| `idle_stop_ms`, `drain_timeout_ms`, `restart` | Applied by the reload. |
| `listen`, `tls`, `credentials_dir`, `packages.dir`, `extension_pages.listen` | Not applied while core runs: a reload that changes one is refused, and core says to restart. |

`pluxy serve -safe-mode` starts core with no extension running: client requests fail with a 503,
and the config can still be checked and reloaded through management. The **Extensions** page says
when pluxy is in safe mode.

## How an extension runs

Core starts an extension's program at load to check it, stops it after `idle_stop_ms` without
requests, and starts it again for the next request. A program that exits on its own is restarted
after a wait that doubles with each crash in a row. After too many crashes in a row it is
**quarantined**: requests to it fail with a 503 until a reload starts it afresh.

These settings belong to one extension. In the config file they go in the `extensions` entry named
by the package ID:

```json
{
  "extensions": {
    "pluxy/claude": {
      "accounts": [],
      "idle_stop_ms": 600000,
      "drain_timeout_ms": 600000,
      "restart": {"backoff_ms": 1000, "max_backoff_ms": 60000, "quarantine_after": 5, "stable_ms": 600000}
    }
  }
}
```

| Key | Meaning | Default |
| --- | --- | --- |
| `idle_stop_ms` | Stop the program after this long without requests. `0` keeps it running. | 10 minutes |
| `drain_timeout_ms` | Longest a replaced version may keep finishing its requests. | 10 minutes |
| `restart.backoff_ms` | Wait before the first restart after a crash. It doubles with each crash in a row. | 1 second |
| `restart.max_backoff_ms` | Cap on that wait. | 1 minute |
| `restart.quarantine_after` | Crashes in a row that quarantine the extension. | 5 |
| `restart.stable_ms` | A program that ran this long before it crashed starts a new count. | 10 minutes |

The settings apply while the package is enabled. Account IDs are unique across every `extensions`
entry and the `extension` entry. The full list of config keys is in [Config](config.md).

A package's state, in `pluxy ext list`, the panel and `GET /v0/management/packages`:

| State | Meaning |
| --- | --- |
| `disabled` | Installed and turned off. |
| `starting`, `active`, `idle` | The running state of its program. `idle` means stopped for lack of requests: the next request starts it. |
| `crashed` | The program exited on its own or failed to start, and is waiting to restart. |
| `quarantined` | Too many crashes in a row. A reload starts it again. |
| `failed` | Turned on, but it could not start, with the reason. Core serves with the rest. |
| `stopped` | Turned on, but core is in safe mode. |

Core checks that a package's manifest, program and page files still have the digest recorded at
install. At each reload, a package whose files changed is not started (state `failed`, with the
reason), or, if it is running, the reload is refused. If files change under a running program, its
next start fails (state `crashed`, then `quarantined` after repeats) until a reload. To replace
the files, turn the package off, wait for its requests to finish, and install the release again.

An extension's own output goes to core's standard error, each line starting with the package ID in
square brackets. It is not written to `main.log`. See
[Usage statistics and logs](usage-and-logs.md#log-files).

## Add a registry

A registry is an `index.json` file. Add one under `packages.registries` in the config file:

```json
{
  "packages": {
    "dir": "packages",
    "registries": [{"name": "mine", "url": "https://example.com/pluxy/index.json"}]
  }
}
```

| Key | Meaning |
| --- | --- |
| `packages.dir` | Where packages are kept, relative to the config file. Default: `packages` beside it. A change needs a restart. |
| `packages.registries[].name` | The registry's name. Unique, and not `bundled`, which is the name of the bundled registry. |
| `packages.registries[].url` | An `https://` address, or a path on this machine (relative paths are read from the config file's folder). Plain `http://` is refused. |

Registries are searched in the order listed, then the bundled one. `pluxy ext available` shows what
each offers, or the reason it could not be read. An archive address in an index may be relative: it
is resolved against the index's own address. Core never shows a registry's or archive's user name,
password or query string.

The index and archive formats are in the protocol README, under
[Release archives](../next/protocol/README.md#release-archives).

Each tagged build uploads the packed extensions and a `registry.json` to its GitHub release
(`.github/workflows/next-release.yml`). To install from one, add that `registry.json` address under
`packages.registries`.

## What core checks before it installs

| Check | Limit or rule |
| --- | --- |
| Registry address | `https://` or a local path. Redirects may go only to another `https://` address. |
| Index | Valid, at most 8 MiB, read within 30 seconds. Package IDs have the form `publisher/name`. |
| Release | Speaks a protocol version this core speaks, and has an archive for this platform (such as `linux/amd64`). A release with no provider protocol is for the desktop app only and is refused here. |
| Archive | Exactly the size and SHA-256 digest the index pins. A gzip-compressed tar file of regular files and folders only, at most 10,000 entries and 512 MiB unpacked. No entry may leave the package folder. |
| Manifest | `pluxy-extension.json` at the top of the archive, naming the same ID and version the index lists, and a program for this platform that is in the archive. |

Nothing is installed when a check fails.

## Where extensions keep their files

All of these are beside the config file unless the config moves them.

| What | Where |
| --- | --- |
| A package's files | `packages/<publisher>/<name>/<release>/`, under `packages.dir`. The program runs with this folder as its working directory. |
| The record of what is installed | `packages.json` in the packages folder: each package's release, registry, archive address and digest, the digest of its unpacked files, and whether it is on. |
| An extension's settings, shown by its pages | `.settings/<publisher>/<name>.json` in the packages folder, readable only by core's user. They survive upgrades. |
| Accounts added by sign-in | The credentials folder (`credentials_dir`, default `credentials`). They stay when a package is removed. |
| Downloads in progress | `.staging` in the packages folder, cleared at start. |
| The program's socket | A temporary folder core makes for each running program. |

An extension starts with an almost empty environment: `PATH`, the protocol variables core sets, and
on Windows `SYSTEMROOT`.

Extensions may add pages to the panel, under **Settings** in the group **From extensions**. They
are served from a site of their own; see
[`next/ui/extension-pages/README.md`](../next/ui/extension-pages/README.md).

## Run an extension from a folder

To run an extension you are writing, point the config's `extension` key at its manifest. The path is
relative to the config file:

```json
{
  "extension": {
    "manifest": "my-extension/pluxy-extension.json",
    "accounts": [{"id": "test", "credential": {"kind": "api_key", "api_key": "..."}}]
  }
}
```

Rebuild the program and run `pluxy reload` (saving the config file unchanged does not reload): core
sees the changed program, starts the new version and drains the old one. The extension uses the same settings as a package
(`idle_stop_ms` and the others above). The
[extension tutorial](../next/protocol/TUTORIAL.md) builds one from scratch.

## Pack extensions

`pluxy-pack` builds the extensions in `next/extensions/` as release archives and writes the registry
index that lists them. It runs `go build` for each extension and platform, so it needs the Go
toolchain. Run it from `next/`, the folder with `go.mod`:

```bash
go run ./cmd/pluxy-pack -out dist -platforms linux/amd64,darwin/arm64
```

It writes `dist/bundled/<publisher>-<name>-<release>-<goos>-<goarch>.tar.gz` for each extension and
platform, and `dist/bundled/index.json`, which pins each archive's SHA-256 and size. Archives are
written with fixed timestamps and a fixed entry order. Start core with that index to install from it:

```bash
go build -o pluxy ./cmd/pluxy
./pluxy serve -bundled dist/bundled/index.json
```

Or put the `bundled` folder beside the `pluxy` program. The same `index.json`, uploaded beside the
archives to an `https://` address, is a registry for others to add.

| Flag | Meaning |
| --- | --- |
| `-out <folder>` | Where to write. Required. |
| `-platforms <list>` | Comma-separated `GOOS/GOARCH` pairs. Default: this machine's platform. |
| `-extensions <list>` | Pack only these extensions, by folder name or package ID. Default: all. With `-app-tools` it selects app tools instead. |
| `-archived` | Also pack the extensions under `next/archive/`, which pluxy no longer ships. Each is a Go module of its own, built in its own folder, and is listed with the tier `unverified`. Without the flag they are neither packed nor named. |
| `-app-tools <folder>` | Pack the desktop app's native tools in `next/app-tools/` instead, from programs built beforehand at `<folder>/<goos>-<goarch>/<program file name>`. It builds nothing, and writes to `dist/app-tools`. It skips the test fixture `usage-fixture`, and packs each tool only for the platforms its manifest names. Its `index.json` is for the desktop app's device registries. The app installs a native tool only with a curated signature, which `pluxy-pack` does not make. |

`go run ./cmd/pluxy-pack -h` prints the flags. Each package's title, summary and kind in the index
come from its manifest. The tier is set by `pluxy-pack`: `pick` for the packages in its list of picks
(in `cmd/pluxy-pack`), `unverified` for an archived one, `verified` for the rest. App tools are not extensions: the desktop app runs them on its own, from its
**Add-ons** page (see `app.md`). Core refuses to install one.

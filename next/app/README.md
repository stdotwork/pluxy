# pluxy desktop app

The desktop app is the UI kit's second build ([`../ui`](../ui/README.md)) in a Tauri 2 window, with
a tray icon. It shows core's pages as the web panel does and adds its own: where pluxy runs, its
versions, updates, add-ons installed on this computer, the coding-tool pages and the launcher.

- How to use it: [docs/app.md](../../docs/app.md).
- How it works inside (connection modes, the app's own core, commands, extension pages, native
  tools, updates, trust): [docs/design/app-design.md](../../docs/design/app-design.md).
- The contract app packages and native tools follow: [`app-host/README.md`](app-host/README.md).
- The native tools themselves: [`../app-tools/README.md`](../app-tools/README.md).

| Folder or file | What it holds |
| --- | --- |
| `src/` | The window: the kit's shell and pages, the app's host (`host.ts`), the first launch screen and Where pluxy runs (`firstLaunch.tsx`, `connection.tsx`, their words in `connectionCopy.ts`), the coding-tool pages (`clientPage.tsx` and one file per tool), extension page drawing (`nativePage.tsx`) |
| `page/` | The small document an extension page's webview loads, which holds the page's sandboxed frame, and the release notes' style sheet |
| `src-tauri/` | The native side: commands, the connection to core, the app's own core, extension page webviews, native tools, the updater, the tray |
| `app-host/` | The app package contract and its tutorial |
| `native/` | The native test (`run.ts`), its sessions and fixtures |
| `tests/` | The window's unit tests |
| `package.ts` | The package commands ([Packages](#packages)) |
| `core-release.json` | The core version a packaged app comes with |
| `local-start.sh` | The local build's `start` |

## Run it as a developer

The app builds the kit from `../ui`, so install that first. From this folder:

```bash
(cd ../ui && bun install --frozen-lockfile)
bun install --frozen-lockfile
bun run build                  # the window, into dist/, which the native build embeds
cd src-tauri && cargo run      # the app, as target/debug/pluxy-app
```

There is no dev server: after a change to the window, run `bun run build` again and restart the
app. A debug build has no core inside and trusts no add-on publisher. To look at it against a core
with accounts in every state, usage and request logs, start `go run ./e2e/panelcore -demo` from
`next/` ([how](../ui/README.md#a-core-with-data-to-look-at)) and give the app its `base` and `key` as
a pluxy already running on this computer. For the coding-tool pages, use [the local
build](#the-local-build).

## Build and test

Bun `1.4.3`, a stable Rust toolchain, and on Linux the WebKitGTK 4.1 and libayatana-appindicator
development packages. From this folder:

```bash
(cd ../ui && bun install --frozen-lockfile)
bun install --frozen-lockfile
bun run check          # tsc --noEmit
bun run lint           # Biome
bun run test           # unit tests
bun run build          # the window, into dist/
cd src-tauri
cargo fmt --check
cargo test --locked    # unit tests, and each mode against a real core (needs Go)
cargo build --release  # target/release/pluxy-app
```

`cargo test` runs `tests/modes.rs`: each connection mode against a real core with fake extensions,
started by `go run ./e2e/panelcore` from `next/`, on random loopback ports in temporary folders. Run
the app's tests with a temporary HOME.

`bun run test:native` is the native test: it builds the app with the `native-test` and
`artifact-test-trust` features and runs it, once per mode and per session, on a private X server
(`xvfb-run`) with a private session bus (`dbus-run-session`), with a fresh HOME and XDG folders each
time. `native/run.ts` lists the sessions and `native/shards.ts` how CI splits them. It starts real
desktop services first and fails, naming the package, when one is missing: on Ubuntu 24.04
`xdg-desktop-portal`, `xdg-desktop-portal-gtk`, `ayatana-indicator-application`, `squashfs-tools`,
`xdotool` and `openssl`, plus FUSE (`/dev/fuse`, `fusermount3`) for the document portal. It also
needs a pinned AppImage runtime, downloaded from github.com into `~/.cache/pluxy-app-native` once, so
it does not run offline on a bare machine. `bun run test:native:platform` is the Windows and macOS
sessions, which CI runs on disposable runners (`.github/workflows/next-app-platforms.yml`).

The `native-test` feature is for these tests alone; release builds never have it.

## Packages

The base `tauri.conf.json` keeps bundling off. Three commands build packages, from this folder,
after `bun run build`:

```bash
bun run package:test <out-dir>          # the test package, for the native test: an AppImage (Linux),
                                        # a folder bundle and a per-user setup program (Windows) or
                                        # an app bundle and disk image (macOS)
bun run package:local <out-dir>         # the local build (Linux x86_64): see below
bun run package:release [artifact...]   # the release gate: builds and checks; uploads nothing
```

[docs/design/app-design.md](../../docs/design/app-design.md#packages-signatures-and-trust) says what each makes
and what the release gate checks. Before packaging or publishing a release, run the release
validator from `src-tauri/` on the final Tauri config, the build's snapshots and the staged trust
folder:

```bash
cargo run --locked --example validate-release-config -- \
  /tmp/release/build/app-tauri-config.json \
  /tmp/release/build/app-package-trust.json \
  /tmp/release/stage/app-package-trust.json \
  /tmp/release/build/app-cargo-features.json \
  /tmp/release/stage/trust \
  /tmp/release/pluxy.AppImage /tmp/release/pluxy-app
```

Keep `tauri.linux.conf.json`, `tauri.macos.conf.json` and `tauri.windows.conf.json` in `src-tauri`,
even empty: `build.rs` reruns only when a file it read changes.

## The local build

The local build is the desktop app built to open and use on this Linux x86_64 computer, with a core,
the real provider extensions and the coding-tool add-ons inside. It is not a release and cannot pass
for one. From `next/`:

```bash
make app-local                        # into next/app/local-build
make app-local LOCAL=/some/folder     # into a folder of your choice
```

`make app-local` builds the web panel core embeds (`make panel`), installs and builds the window
(`bun run build`), then runs `bun package.ts local <folder>` here. It needs what the app's other
builds need, Go, and Rust for the add-ons. Its last lines give the build's version, the providers
and add-ons it carries and how to start it. It builds only into a new or empty folder or an earlier
local build. How to start, rebuild and remove it is in [docs/app.md](../../docs/app.md#get-the-app).

The folder holds:

| Path in the folder | What it is |
| --- | --- |
| `start` | What you run: it writes the trust profile below, then starts the app (`local-start.sh` here) |
| `bin/pluxy-app-local` | The app |
| `bin/pluxy` | The core the app comes with |
| `lib/pluxy Local/core/bundled` | The core's bundled registry: every provider extension in `next/extensions`, built by `go run ./cmd/pluxy-pack` |
| `lib/pluxy Local/app-tools` | The coding-tool add-ons: every tool in `next/app-tools` with a `linux/amd64` program, built with Cargo and packed by `pluxy-pack -app-tools`, the signed index the app installs them from (`index.json`), and `publisher.json`, the ID and public key of the key that signed them |
| `home/` | The home folder the app runs in by default, made by `start` |
| `local-build.json` | The app's path, `start`'s, the core's version and the IDs of the providers and add-ons it carries |

**Trust.** The build has the `local-build` and `artifact-test-trust` Cargo features, which together
load publisher roots from a trust profile in the process HOME, only when that HOME is private, holds
a marker naming it, and the app is given the marker's nonce as `PLUXY_TEST_TRUST_NONCE`. `start`
writes that profile: one root, for publisher `pluxy`, whose source is this build's
`lib/pluxy Local/app-tools/index.json` and whose key is the one in `publisher.json`. It names that
index in the app's `device-registries.json` when the file does not exist yet, and says what to add
when it exists without it. Every add-on's signature, size, SHA-256, manifest and files are still
checked as in any build, and turning one on still asks. The core and the add-ons are signed with two
keys made for that one build, whose private halves never leave the build command. The Rust test
`local_build_start_trusts_its_add_ons_and_refuses_a_flipped_byte` (`tests/artifact_trust.rs`)
checks this; with `PLUXY_LOCAL_BUILD` naming a build's folder it runs on that build.

**How it differs from a release:** identifier `dev.pluxy.desktop.local`, product name `pluxy Local`,
program `pluxy-app-local`, so its settings and data never mix with another copy; no Open at sign-in
(a login launch of it ends at once); "pluxy (local build)" in the window's title and the tray; built
in the release profile without `native-test`; no updater key, so it never updates itself; and the
release validator refuses it (its identity first, then its test features, then the test build
marker in its program).

**Building it again** checks the folder first and changes nothing if it refuses (a link, a folder
that is not a local build, a `<folder>.old` beside it, or an app or core still running from it),
builds into `<folder>.stage-<random>` beside it, then swaps it in, moving `home/` across. Each build
has its own version, the core version in `core-release.json` followed by `+local.` and 12 random hex
digits, so at its next start the app installs the core it came with, unless the person chose
another. An installed provider keeps running the copy that was installed, even when the new build
carries a changed one at the same version: remove and install it again to run the new one.

## Where the docs are

| Topic | Page |
| --- | --- |
| Using the app, platforms, troubleshooting | [docs/app.md](../../docs/app.md) |
| How the app works inside | [docs/design/app-design.md](../../docs/design/app-design.md) |
| Putting a kept copy back by hand after a failed update | [docs/design/app-design.md](../../docs/design/app-design.md#application-update) |
| App packages, native tools, the vault, launches, WSL | [`app-host/README.md`](app-host/README.md), [`app-host/TUTORIAL.md`](app-host/TUTORIAL.md) |
| The native tools | [`../app-tools/README.md`](../app-tools/README.md) |
| The web panel and the kit | [`../ui/README.md`](../ui/README.md) |

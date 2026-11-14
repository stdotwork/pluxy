# AGENTS.md

pluxy is a monorepo with three parts of one product: a proxy that serves OpenAI-, Gemini-,
Claude- and Codex-compatible APIs, a desktop app that manages it, and a web panel for it. A new
core with provider extensions is being built from scratch in `next/`, beside today's `core/`.

## Layout

| Folder | What it is | Came from |
| --- | --- | --- |
| `core/` | Go proxy server | router-for-me/CLIProxyAPI |
| `extensions/app/` | Tauri 2 desktop app (React front end, Rust back end) | router-for-me/EasyCLIProxyAPI |
| `next/` | New Go core, its extension protocol and provider extensions | written for pluxy |

`core/` and `extensions/app/` still receive pulls from their upstream
projects. Keep changes inside a folder contained: no sweeping renames, reformatting or file
moves, so later upstream merges stay easy. `core/`'s Go module path stays
`github.com/router-for-me/CLIProxyAPI/v8`. `next/` has no upstream; its module path is
`github.com/stdotwork/pluxy/next`, and it does not import code from `core/`.

## Which rules apply

A folder's own `AGENTS.md` (`core/AGENTS.md`) holds that folder's
coding rules and applies to work inside it. The app has no `AGENTS.md`; follow nearby code.
`next/` has no `AGENTS.md` either: follow nearby code, prefer the standard library (its only
other dependencies are `github.com/coder/websocket` and `gopkg.in/yaml.v3`), and change the
extension protocol only together with its README (`next/protocol/README.md`).

The upstream process rules in those files and in upstream tooling do not apply in pluxy:

- PRs go to `main`, not `dev`. There is no `dev` branch.
- There is no AGENTS.md-change guard; edit agent files like any other file.
- `core/AGENTS.md`: skip the `internal/translator/` permission check and the "file an issue and
  stop" step. Translator-only changes are ordinary PRs here.
- `core/AGENTS.md`: skip the note about updating the CLIProxyAPIHome repository.

If `.private/AGENTS.md` exists, read it too: it holds the owner's private instructions and notes.
Confirmed plans, decisions and major tradeoffs are written up in `docs/`.

## Build and test

Run each command from the folder named in its heading.

### `core/`

Go version comes from `core/go.mod` (currently `go 1.26.10`). Set `CGO_ENABLED=1`: the plugin
host needs cgo.

```bash
CGO_ENABLED=1 go build -o test-output ./cmd/server && rm test-output   # compile check
CGO_ENABLED=1 go test ./...                                          # all tests
CGO_ENABLED=1 go vet ./...
gofmt -l .                                                           # must not list files you touched
```

`gofmt -l .` lists two inherited test files under `internal/runtime/executor/`; leave them
unless your change touches them. Some `internal/runtime/executor` tests
are sensitive to machine load and to repeated runs (`-count` above 1); rerun a failure there
before assuming your change caused it.

### `next/`

Go version comes from `next/go.mod` (currently `go 1.26.10`). Besides the standard library, the
module depends on `github.com/coder/websocket` and `gopkg.in/yaml.v3`.

```bash
go build ./...
go vet ./...
CGO_ENABLED=1 go test -race -count=1 ./...   # -race needs cgo; includes e2e/ and the conformance kit
gofmt -l .                                   # must print nothing
```

The `e2e` and conformance tests build `cmd/pluxy` and the extensions with `go build`, run them on
random free ports and Unix sockets in temporary folders, and stop them when done.

Core embeds the web panel from `next/ui` (see `next/ui/README.md`), which is built, not kept in
git. Build it before building or testing core, or the panel's e2e tests skip
(`PLUXY_REQUIRE_PANEL=1` makes them fail instead, as in CI). The UI needs Bun `1.4.3`:

```bash
go generate ./internal/panel     # bun install and bun run build in ui/, writing internal/panel/dist
cd ui && bun run check && bun run lint && bun run test   # type check, Biome, unit tests
cd ui && bun run test:browser    # the browser test, headless Chromium (bunx playwright install chromium once)
```

The desktop app in `next/app` (see `next/app/README.md`) builds the kit from `next/ui`, so run
`bun install` there first. It needs Rust and, on Linux, the WebKitGTK 4.1 and
libayatana-appindicator development packages. Its tests start real cores through
`go run ./e2e/panelcore`; run them with a temporary HOME. The native test needs `xvfb-run` and
`dbus-run-session`, and gives each run its own HOME, XDG folders, X server and session bus:

```bash
cd app && bun install --frozen-lockfile && bun run check && bun run lint && bun run test && bun run build
cd app/src-tauri && cargo fmt --check && cargo test --locked
cd app && bun run test:native   # the real app on Xvfb, in each connection mode
```

`app-tools/` holds the native programs the desktop app runs on demand (such as the Codex session
reader) and the libraries they share: a cargo workspace of its own, never linked into the app.
The native test builds the ones it packs.

```bash
cd app-tools && cargo fmt --check && cargo clippy --locked --all-targets -- -D warnings && cargo test --locked
```

`corpus/` is the differential test corpus (see `next/corpus/README.md`). By default it compares
the new core with the old core's recorded replies. To run today's core live as well, or to
re-record after changing a case:

```bash
PLUXY_CORPUS_OLD_CORE=1 go test -count=1 -v -run TestCorpus ./corpus   # builds core/ with cgo
PLUXY_CORPUS_RECORD=1 go test -count=1 -run TestCorpus ./corpus        # rewrites recorded_old_core
```

### `extensions/app/`

Bun `1.3.14` (`packageManager` in `package.json`) and a stable Rust toolchain (none is pinned in
the tree; CI uses 1.97.0). Building the Rust side on Linux needs the WebKitGTK 4.1 development
packages.

```bash
bun install --frozen-lockfile
bun run check         # tsc --noEmit
bun run build         # tsc + Vite; writes dist/, which the Rust build embeds
```

Tests use `bun test` (there is no `test` script); CI runs the whole suite. While working, run
the test files for the code you change, for example `bun test tests/plugins.test.ts`.

Rust tests run from `extensions/app/src-tauri` (build the front end first so `dist/` exists):

```bash
cargo test --locked
```

The app manages agent configuration folders under your home directory (such as `~/.claude`
and `~/.codex`) and starts and stops processes, and not every test has been checked to keep to
temporary folders. Run these tests in CI or a throwaway machine or container, not on a machine
with live agent sessions or a running core. CI runs them with an empty temporary `HOME` and XDG
folders.

The `test:ui:*` and `test:desktop:*` scripts in `package.json` drive a browser or the desktop
app; they are not part of the routine checks above.

## CI

Workflows live in `.github/workflows/`, one set per folder, each limited to its folder's paths.

| Workflow | Runs on | What it does |
| --- | --- | --- |
| `core-pr-test-build.yml` | PRs and pushes to `main` touching `core/` | Refreshes the model catalog, then `go build ./cmd/server`, `go vet ./...` and `go test ./...` |
| `core-release.yaml` | `core-v*` tags | Builds release archives and creates the GitHub release |
| `app-ci.yml` | PRs and pushes to `main` touching `extensions/app/` | Front end: `bun run check`, `bun test`, `bun run build`; Rust: `cargo check --locked`, `cargo test --locked` |
| `next-ci.yml` | PRs and pushes to `main` touching `next/` | UI type check, lint and unit tests; builds the panel; `gofmt -l .`, `go build ./...`, `go vet ./...`, `go test -race ./...`; the panel's browser test in headless Chromium, and the extension pages test in Chromium, Firefox and WebKit |
| `next-app.yml` | PRs and pushes to `main` touching `next/` | The desktop app in `next/app`: type check, lint, unit tests and build of the window; `cargo fmt --check`, `cargo clippy`, `cargo test --locked` (unit tests and each connection mode against a real core); format, clippy and tests of `next/app-tools`; the native test on Xvfb, which also builds and runs the test AppImage and checks the release gate |
| `next-corpus.yml` | PRs and pushes to `main` touching `next/` or `core/` | Builds `core/` and runs the corpus against both cores live; the report is in the job summary |
| `next-release.yml` | `next-v*` tags; PRs touching `next/Dockerfile`, `next/.dockerignore`, `next/compose.example.yaml`, `next/cmd/pluxy-pack/` or the workflow | PRs: builds the container image (amd64, and arm64 without running it), starts it with a temporary config, polls `/healthz`, checks the panel and the bundled packages, and packs every extension for the release platforms. Tags: also uploads the archives and `registry.json` to the GitHub release and pushes the image to `ghcr.io` once the checks pass |

When you add a workflow, add a row here.

## How changes flow

- `main` is the only long-lived branch. Work on a task branch and open a PR against `main`.
- One concern per PR. A change in behaviour goes in its own PR, and the PR title says so.
- Commit messages and PR titles use Conventional Commits, scoped by folder: `core`, `app`,
  `next`, or `ci` for workflows, for example `fix(core): ...` or `ci: ...`. Repo-wide docs use
  `docs: ...`.
- Open PRs ready for review, not as drafts. The owner reviews and merges.
- Licensing: each folder keeps its upstream MIT `LICENSE`. The notice of the removed old web
  panel is in `THIRD_PARTY_NOTICES.md`. The root license is not decided yet; do not add one.

# AGENTS.md

pluxy is one product in three parts: a proxy (the core) that serves OpenAI-, Gemini-, Claude-
and Codex-compatible APIs through provider extensions, a desktop app that manages it, and a web
panel for it. All three are in `next/`, and all new work goes there. The old desktop app in
`extensions/app/` stays until the new app in `next/app/` runs on Windows and macOS.

## Layout

| Folder | What it is |
| --- | --- |
| `next/` | The product. Its Go code (the core, the SDK, the extensions and their tests) is one module, `github.com/stdotwork/pluxy/next` |
| `next/app/` | The desktop app: the UI kit's second build, in a Tauri 2 window |
| `next/app-tools/` | Native programs the desktop app runs on demand, and the libraries they share: a cargo workspace of its own |
| `next/ui/` | The UI kit, and the web panel, its browser build, which core embeds |
| `next/extensions/` | Provider extensions: separate programs, one per kind of upstream, that core starts |
| `next/corpus/` | The differential test corpus: the new core against the old core's recorded replies |
| `next/protocol/` | The extension protocol between core and extensions |
| `extensions/app/` | The old desktop app, a fork of router-for-me/EasyCLIProxyAPI: Tauri 2, with a React front end and a Rust back end |

`next/README.md` describes the rest of `next/`.

## Which rules apply

`next/` has no `AGENTS.md`: follow nearby code, prefer the standard library (its only other
dependencies are `github.com/coder/websocket` and `gopkg.in/yaml.v3`), and change the extension
protocol only together with its README (`next/protocol/README.md`). The old desktop app has no
`AGENTS.md` either; follow nearby code there too.

If `.private/AGENTS.md` exists, read it too: it holds the owner's private instructions and notes.
Confirmed plans, decisions and major tradeoffs are written up in `docs/`: the new core's design
in `docs/design.md`, and how the corpus holds it to the old core in `docs/corpus.md`.

## Build and test

Run each command from the folder named in its heading.

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

`corpus/` is the differential test corpus (see `next/corpus/README.md`). It compares the new core
with the old core's replies recorded in the case files. The old core is no longer in this
repository and the corpus cannot run or re-record it: the recorded replies are the reference.

### `extensions/app/` (the old desktop app)

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
Pull requests and merge-queue groups run them through `ci.yml`, whose `gate` job is the one
required check; otherwise each workflow's own triggers start it (pushes to `main`,
manual runs or tags, as its file says; the two Windows and macOS platform workflows run on
`main` once a day instead of on every push).

A pull request's run leaves out the heavy jobs: `next-app.yml`'s Linux native shards and all of
`next-app-platforms.yml`. Merge-queue groups always run them. Label a PR `full-ci` to run them on
the PR too; adding the label reruns its checks. A manual run of either workflow on the branch also
works. Label a PR whose change is mainly native or platform code, so failures show before it is
queued; docs, core-only and CI PRs go without it.

| Workflow | Runs on | What it does |
| --- | --- | --- |
| `ci.yml` | Every PR and every merge-queue group | Works out which folders the change touches (for a PR, the diff against its base; for a group, what it adds to the group ahead of it in the queue, or to `main` for the first group; sound only while the queue's ruleset keeps "Only merge non-failing pull requests" (ALLGREEN)), runs the workflows below whose paths it touches as reusable workflows, and runs `gate`, which passes only when every workflow it chose passed. A change to `ci.yml` runs them all. `next-release.yml` is not among them |
| `app-ci.yml` | PRs and merge-queue groups (through `ci.yml`) and pushes to `main` touching `extensions/app/` | The old desktop app. Front end: `bun run check`, `bun test`, `bun run build`; Rust: `cargo check --locked`, `cargo test --locked` |
| `next-ci.yml` | PRs and merge-queue groups (through `ci.yml`) and pushes to `main` touching `next/` | UI type check, lint and unit tests; builds the panel; `gofmt -l .`, `go build ./...`, `go vet ./...`, `go test -race ./...`; the panel's browser test in headless Chromium, and the extension pages test in Chromium, Firefox and WebKit; uploads the browser tests' screenshots and traces as an artifact for 7 days |
| `next-app.yml` | PRs and merge-queue groups (through `ci.yml`) and pushes to `main` touching `next/` | The desktop app in `next/app`: type check, lint, unit tests and build of the window; `cargo fmt --check`, `cargo clippy`, `cargo test --locked` (unit tests and each connection mode against a real core); format, clippy and tests of `next/app-tools`. In parallel, the native test on Xvfb, which also builds and runs the test AppImage and checks the release gate: a `native-build` job builds what the sessions run once, and shard jobs each run their own sessions on it (`next/app/native/shards.ts`) and upload each session's last screenshot as an artifact for 7 days. The `app` job passes only when every other job did |
| `next-app-platforms.yml` | PRs and merge-queue groups (through `ci.yml`) changing what it builds: `next/app/`, `next/ui/`, `next/protocol/testdata/app-manifests/`, `next/go.mod` or `next/go.sum`, the workflow, or a file other than a test in a Go package the app's tests build (`e2e/panelcore`, `e2e/testext` and `cmd/pluxy` and what they import, as `go list` finds them), and its `native` jobs for the same changes except the app-manifest cases, since their sessions start a test core with `go run ./e2e/panelcore`; once a day on `main` (03:17 UTC); `workflow_dispatch` | The desktop app on `windows-latest` and `macos-latest`: the Rust crate's format, clippy and tests, and the native test's sessions that run there; the rest of the native test stays on Linux in `next-app.yml`. The workflow file says which jobs run and what each checks; `next/app/README.md` ("Limits") says which sessions run off Linux |
| `next-app-tools-platforms.yml` | PRs and merge-queue groups (through `ci.yml`) touching `next/app-tools/` or the workflow, or the desktop app's files that exist for WSL (`next/app/src-tauri/src/environments*.rs` and its wsl-named files); once a day on `main` (03:47 UTC); `workflow_dispatch` | The app tools on `windows-latest` and `macos-latest` (clippy and tests, with the counts in the job summary), and the `WSL 2` and `WSL 1` jobs, which run the real-WSL tests on GitHub-hosted `windows-latest` (for PRs and merge-queue groups only when what they build or run changes, as `ci.yml` decides; always on the daily and manual runs). The workflow file says what each job checks |
| `next-release.yml` | `next-v*` tags; PRs touching `next/Dockerfile`, `next/.dockerignore`, `next/compose.example.yaml`, `next/cmd/pluxy-pack/` or the workflow | PRs: builds the container image (amd64, and arm64 without running it), starts it with a temporary config, polls `/healthz`, checks the panel and the bundled packages, and packs every extension for the release platforms. Tags: also uploads the archives and `registry.json` to the GitHub release and pushes the image to `ghcr.io` once the checks pass |
| `next-app-tools-pack.yml` | PRs and merge-queue groups (through `ci.yml`) touching the app tools' manifests or workspace, `next/cmd/pluxy-pack/` or the workflow; `next-v*` tags | Builds the app tools on each platform's own runner and packs them with `pluxy-pack -app-tools`, uploading nothing; the workflow file has the detail |

When you add a workflow, add a row here. A workflow that must pass before a merge to `main`
runs through `ci.yml`: give it a `workflow_call` trigger instead of a `pull_request` one, add a
path case and a job for it in `ci.yml`, and add the job to `gate`'s `needs`.

### Where jobs run

By default every job runs on GitHub's own runners. Two repository variables send some jobs to
the maintainers' own machines instead:

- `PLUXY_LINUX_RUNNER` takes the Linux jobs of `app-ci.yml` and `next-app.yml`, except
  `next-app.yml`'s native test shards and `native-build`: the
  document portal the shards start needs `/dev/fuse`, which the runner containers don't get, and
  `native-build` builds what the shards run on the same runner as theirs (GitHub's, or
  `PLUXY_LINUX_CLOUD_RUNNER`'s below). `next-ci.yml` never uses it.
- `PLUXY_WINDOWS_RUNNER` takes the Windows leg of the `tools` job in
  `next-app-tools-platforms.yml`. The `app` job in `next-app-platforms.yml` (its registry tests
  only run on GitHub's runners), macOS jobs, the `native` job in `next-app-platforms.yml` and the
  workflows a tag starts always stay on GitHub's runners.

Each variable holds a runner label as JSON, for example `"self-hosted"` or
`["self-hosted", "linux"]`. When a variable is not set, its jobs run on the GitHub runner they
named before. Jobs on the maintainers' runners restore caches but don't save them (the uplink
is slow).

`PLUXY_GATE_RUNNER` names the runner for `ci.yml`'s own `changes` and `gate` jobs in merge-queue
groups, as a JSON runs-on value, for example `"nscloud-ubuntu-24.04-amd64-2x4"` (Namespace's
smallest Linux runner). Both take seconds, but every group waits on them, and a GitHub runner can
take many minutes to start when pull requests fill GitHub's limit of 20 jobs at once. Pull
requests, forks included, keep `ubuntu-latest`. A job can't fall back to another runner by
itself: if that provider stops starting runners, delete the variable.

Only these events can use the maintainers' runners: pushes, manual runs, merge-queue groups, and
pull requests from branches of this repository, except Dependabot's. Every other event, including
pull requests from a fork, runs on GitHub's runners. That check lives in the workflow files, which a fork can edit,
so it is not the only guard: runs from forks need a maintainer's approval, and the maintainers'
runner host cancels any run from a fork that asks for its runners. Before approving a fork's run,
check whether it changes `.github/workflows/`.

A third variable, `PLUXY_MACOS_CLOUD_RUNNER`, sends some macOS jobs to another provider's
runners, such as Blacksmith's. It holds a plain runner label, for example
`blacksmith-6vcpu-macos-26`. When it is not set, those jobs run on `macos-latest`.

It applies to these jobs:
- the `app` job in `next-app-platforms.yml`;
- the `tools` job in `next-app-tools-platforms.yml`;
- the `build` jobs in `next-app-tools-pack.yml`.

Some macOS jobs and every Windows job stay on GitHub's runners:
- The `native` and `dialogs` jobs, and the app's Windows registry tests, run only on a runner that
  reports itself GitHub-hosted. Another provider's runner reports itself self-hosted.
- The Windows app-tool tests fail on Blacksmith's Windows runner.
- The WSL jobs need GitHub's Windows runners.
- The workflows a tag starts always use GitHub's runners.

A fourth variable, `PLUXY_LINUX_CLOUD_RUNNER`, sends the heavy Linux jobs to Namespace's runners:
`checks`, `native-build`, the native test shards and the `app` result job in `next-app.yml`,
and `next` in `next-ci.yml`. It holds a runner label as JSON, like the first two; the intended
value is `"nscloud-ubuntu-24.04-amd64-4x8"` (4 vCPU, 8 GB). Only the events named above for the
maintainers' runners can use it, so a fork's runs stay on GitHub's runners. `checks` and `app` take
it before `PLUXY_LINUX_RUNNER`, and the shards can run there because Namespace's runners have
`/dev/fuse`. When it is not set, these jobs run where they did before.

Namespace's runners report themselves self-hosted. In `next-app.yml` they use no Go or Bun cache
and save no Rust cache, and the Rust caches GitHub's runners save don't restore there either (the
cache key includes the image's own Rust, which differs), so their Rust builds start cold. `next`
restores and saves its Go and Bun caches there as on GitHub's runners.

Namespace's trial runs 32 vCPU at once: 8 of these jobs. A merge group that runs both workflows
asks for 9 (`checks`, `native-build`, five shards, `app` and `next`), at most 7 at once, since the
shards wait for `native-build` and `app` runs last. Past 8, jobs queue on Namespace and start as
runners free up (within about 30 seconds of one freeing, in a test with 10 at once; none failed),
so a second group in flight waits for some of its jobs.

## How changes flow

- `main` is the only long-lived branch. Work on a task branch and open a PR against `main`.
- One concern per PR. A change in behaviour goes in its own PR, and the PR title says so.
- Commit messages and PR titles use Conventional Commits, scoped by folder: `next`, or `ci` for
  workflows, for example `fix(next): ...` or `ci: ...`. Repo-wide docs use `docs: ...`. The scope
  `app` is for the old desktop app in `extensions/app/` only; work on `next/app/` uses `next`.
- Open PRs ready for review, not as drafts. The owner reviews and merges.
- Licensing: the upstream MIT notices of the removed old core and old web panel, of the old
  desktop app and of the Pi adapter in `next/app-tools/pi-setup/adapter/`, with the files `next/`
  keeps or adapts from them, are in `THIRD_PARTY_NOTICES.md`; the old desktop app also keeps its own
  in `extensions/app/LICENSE`. The root license is not decided yet; do not add one.

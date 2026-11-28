# AGENTS.md

pluxy is one product in three parts: a proxy (the core) that serves OpenAI-, Gemini-, Claude-
and Codex-compatible APIs through provider extensions, a desktop app that manages it, and a web
panel for it. All three are in `next/`, and all new work goes there.

## Layout

| Folder | What it is |
| --- | --- |
| `next/` | The product. Its Go code (the core, the SDK, the extensions and their tests) is one module, `github.com/stdotwork/pluxy/next` |
| `next/app/` | The desktop app: the UI kit's second build, in a Tauri 2 window |
| `next/app-tools/` | Native programs the desktop app runs on demand, and the libraries they share: a cargo workspace of its own |
| `next/ui/` | The UI kit, and the web panel, its browser build, which core embeds |
| `next/extensions/` | Provider extensions: separate programs, one per kind of upstream, that core starts |
| `next/corpus/` | The differential test corpus: pluxy's replies against replies recorded from CLIProxyAPI |
| `next/protocol/` | The extension protocol between core and extensions |

`next/README.md` describes the rest of `next/`.

## Which rules apply

`next/` has no `AGENTS.md`: follow nearby code, prefer the standard library (its only other
dependencies are `github.com/coder/websocket` and `gopkg.in/yaml.v3`), and change the extension
protocol only together with its README (`next/protocol/README.md`).

Code carries next to no comments, and here nearby code is not the guide: much of it still has
comments this rule does not allow. Say what the code does through names, types and tests. In code
you add or change, in any language and in workflow files, a comment may stay only when it is one of
these:

- a licence header;
- behaviour forced by something outside pluxy that it cannot reshape (a dependency, a platform, a
  vendor or a protocol), where the code alone does not make the reason clear;
- a lint suppression whose rule is wrong for that line, pedantic or style-only (one that silences a
  correctness or safety rule goes, and the code is fixed instead);
- a doc comment that defines a public API's contract;
- a link to an issue or RFC for a constraint the code cannot express.

Anything else goes: narration, section banners, commented-out code, notes that point at a doc, and
explanations of a workaround in pluxy's own code (change the code so it needs none). When unsure
whether a comment fits the list, delete it. The standard this follows is the
[no-comments skill](https://github.com/cursor/plugins/blob/main/pstack/skills/no-comments/SKILL.md)
and its [comment-sicko agent](https://github.com/cursor/plugins/blob/main/pstack/agents/comment-sicko.md).

Lines that a tool reads are not comments under this rule, and stay: `//go:build`, `//go:generate`,
`//go:embed`, a generated file's `// Code generated ... DO NOT EDIT.` line, `// prettier-ignore` and
the like.

If `.private/AGENTS.md` exists, read it too: it holds the owner's private instructions and notes.
Start with [`docs/map.md`](docs/map.md) to find the entry files, tests, fakes and handbook sections
for common changes before searching the tree.
`TestDocs` (`next/cmd/pluxy/docs_test.go`) reads the docs and fails when one names a `pluxy`
command or flag, a `PLUXY_` variable, a relative link, a repo path or a quick-start command that
does not exist, or when shipped code reads a `PLUXY_` variable no doc names. Change the doc, or the
code if the doc is right; the test's allowlists may only shrink.
Confirmed plans, decisions and major tradeoffs are written up in `docs/design/`: why pluxy is built
the way it is in `docs/design/design.md`, the confirmed plans not built yet in
`docs/design/plans.md`, and what the corpus compares pluxy with in `docs/design/corpus.md`.

## Build and test

Run each command from the folder named in its heading.

The short way: `make` in `next/` lists targets that run these same commands (`make check`,
`make test`, `make ui`, `make app`, `make app-rust`, `make tools`; `make all` runs `check`, `ui`
and `app`). `make app-local` builds the desktop app to open and use, with core, the provider
extensions and the coding-tool add-ons inside, into `next/app/local-build` (`next/app/README.md`, "The local build").
`make dev` watches `next/ui`, rebuilds the panel on each edit, and serves it from a
demo core (`PORT=8330`). The commands below stay the reference CI mirrors. `make hooks` turns on an opt-in
pre-commit hook that checks the formatting of staged files.

### `next/`

Go version comes from `next/go.mod` (currently `go 1.26.11`). Besides the standard library, the
module depends on `github.com/coder/websocket` and `gopkg.in/yaml.v3`.

```bash
go build ./...
go vet ./...
CGO_ENABLED=1 go test -race -count=1 ./...   # -race needs cgo; includes e2e/ and the conformance kit
gofmt -l .                                   # must print nothing
go run honnef.co/go/tools/cmd/staticcheck@v0.8.2 ./...         # must print nothing; not a module dependency
go run golang.org/x/vuln/cmd/govulncheck@v1.10.0 ./...         # needs network (vuln.go.dev)
```

The `e2e` and conformance tests build `cmd/pluxy` and the extensions with `go build`, run them on
random free ports and Unix sockets in temporary folders, and stop them when done.

Core embeds the web panel from `next/ui` (see `next/ui/README.md`), which is built, not kept in
git. Build it before building or testing core, or the panel's e2e tests skip
(`PLUXY_REQUIRE_PANEL=1` makes them fail instead, as in CI). The UI needs Bun `1.4.3`:

```bash
go generate ./internal/panel     # bun install and bun run build in ui/, writing internal/panel/dist
cd ui && bun run check && bun run lint && bun run test   # type check, Biome, unit tests
cd ui && bun run test:browser    # the browser test, headless Chromium (bunx playwright install chromium once); PLUXY_ALL_BROWSERS=1 also runs Firefox and WebKit, as CI does
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

`corpus/` is the differential test corpus (see `next/corpus/README.md`). It compares pluxy's
replies with CLIProxyAPI's, recorded in the case files. CLIProxyAPI is not in this repository and
the corpus cannot run or re-record it: the recorded replies are the reference.

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

When you add a workflow, add a row to the table in [`docs/ci.md`](docs/ci.md). A workflow that must
pass before a merge to `main` runs through `ci.yml`: give it a `workflow_call` trigger instead of a
`pull_request` one, add a path case and a job for it in `ci.yml`, and add the job to `gate`'s
`needs`.

See [`docs/ci.md`](docs/ci.md) for the workflow table and where jobs run (runner variables,
Namespace limits and fork guards).

## How changes flow

- `main` is the only long-lived branch. Work on a task branch and open a PR against `main`.
- One concern per PR. A change in behaviour goes in its own PR, and the PR title says so.
- Commit messages and PR titles use Conventional Commits, scoped by folder: `next`, or `ci` for
  workflows, for example `fix(next): ...` or `ci: ...`. Repo-wide docs use `docs: ...`. Work on
  `next/app/` uses `next`.
- Open PRs ready for review, not as drafts. The owner reviews and merges.
- Licensing: the upstream MIT notices of CLIProxyAPI, its Management Center and EasyCLIProxyAPI,
  whose copies this repository once held, and of the Pi adapter in
  `next/app-tools/pi-setup/adapter/`, with the files `next/` keeps or adapts from them, are in
  `THIRD_PARTY_NOTICES.md`. The root license is not decided yet; do not
  add one.

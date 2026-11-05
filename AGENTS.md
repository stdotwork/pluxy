# AGENTS.md

pluxy is a monorepo with three parts of one product: a proxy that serves OpenAI-, Gemini-,
Claude- and Codex-compatible APIs, a desktop app that manages it, and a web panel for it.

## Layout

| Folder | What it is | Came from |
| --- | --- | --- |
| `core/` | Go proxy server | router-for-me/CLIProxyAPI |
| `extensions/app/` | Tauri 2 desktop app (React front end, Rust back end) | router-for-me/EasyCLIProxyAPI |
| `extensions/panel/` | React/Vite web management panel, built to one HTML file | router-for-me/Cli-Proxy-API-Management-Center |

Each folder still receives pulls from its upstream project. Keep changes inside a folder
contained: no sweeping renames, reformatting or file moves, so later upstream merges stay easy.
The Go module path stays `github.com/router-for-me/CLIProxyAPI/v8`.

## Which rules apply

A folder's own `AGENTS.md` (`core/AGENTS.md`, `extensions/panel/AGENTS.md`) holds that folder's
coding rules and applies to work inside it. The app has no `AGENTS.md`; follow nearby code.

The upstream process rules in those files and in upstream tooling do not apply in pluxy:

- PRs go to `main`, not `dev`. There is no `dev` branch.
- There is no AGENTS.md-change guard; edit agent files like any other file.
- `core/AGENTS.md`: skip the `internal/translator/` permission check and the "file an issue and
  stop" step. Translator-only changes are ordinary PRs here.
- `core/AGENTS.md`: skip the note about updating the CLIProxyAPIHome repository.
- `extensions/panel/AGENTS.md`: the backend it calls `../CLIProxyAPI` is `core/` in this repo,
  and panel releases use `panel-v*` tags, not `vX.Y.Z`.

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

Known state on `main` today: `go vet ./...` fails on a copied lock at
`internal/pluginhost/host_test.go:1637` (a fix is in progress), and `gofmt -l .` lists two
inherited test files under `internal/runtime/executor/`. Some `internal/runtime/executor` tests
are sensitive to machine load and to repeated runs (`-count` above 1); rerun a failure there
before assuming your change caused it.

### `extensions/panel/`

Bun `1.4.3` (`packageManager` in `package.json`); CI also sets up Node.js 24.

```bash
bun install --frozen-lockfile
bun run test          # bun test
bun run lint          # ESLint; warnings do not fail it
bun run type-check    # tsc --noEmit
bun run build         # tsc + Vite; writes dist/index.html
bun run verify        # test + lint + build, what CI runs
```

Two tests in `tests/toolchainVersion.test.ts` read `.github/workflows/ci.yml` and `release.yml`
inside the panel folder. Today those files live at the repo root as `panel-ci.yml` and
`panel-release.yml`, and CI copies them in before testing. To run the full suite locally, do the
same copy first and delete the copies afterwards:

```bash
mkdir -p .github/workflows
cp ../../.github/workflows/panel-ci.yml .github/workflows/ci.yml
cp ../../.github/workflows/panel-release.yml .github/workflows/release.yml
```

### `extensions/app/`

Bun `1.3.14` (`packageManager` in `package.json`) and a stable Rust toolchain (no version is
pinned in the tree). Building the Rust side on Linux needs the WebKitGTK 4.1 development
packages.

```bash
bun install --frozen-lockfile
bun run check         # tsc --noEmit
bun run build         # tsc + Vite; writes dist/, which the Rust build embeds
```

Tests use `bun test` (there is no `test` script). On `main` today the suite does not pass: two
test files read upstream workflow files that were removed when the app moved here (a fix is in
progress); a few files, such as `tests/cssLayers.test.ts` and `tests/i18n.test.ts`, fail on
their own; and many more tests fail only when the whole suite runs together but pass when their
file runs alone. Run the test files for the code you change, for example
`bun test tests/plugins.test.ts`, and compare against `main` before blaming your change.

Rust tests run from `extensions/app/src-tauri` (build the front end first so `dist/` exists):

```bash
cargo test --locked
```

The app manages agent configuration folders under your home directory (such as `~/.claude`
and `~/.codex`) and starts and stops processes, and not every test has been checked to keep to
temporary folders. Run these tests in CI or a throwaway machine or container, not on a machine
with live agent sessions or a running core.

The `test:ui:*` and `test:desktop:*` scripts in `package.json` drive a browser or the desktop
app; they are not part of the routine checks above.

## CI

Workflows live in `.github/workflows/`, one set per folder, each limited to its folder's paths.

| Workflow | Runs on | What it does |
| --- | --- | --- |
| `core-pr-test-build.yml` | PRs touching `core/` | Refreshes the model catalog, then `go build ./cmd/server` |
| `core-release.yaml` | `core-v*` tags | Builds release archives and creates the GitHub release |
| `panel-ci.yml` | PRs and pushes to `main` touching `extensions/panel/` | `bun install --frozen-lockfile`, then `bun run verify` |
| `panel-release.yml` | `panel-v*` tags | `bun run build`, publishes `management.html` |

The app has no CI workflow yet. When you add a workflow, add a row here.

## How changes flow

- `main` is the only long-lived branch. Work on a task branch and open a PR against `main`.
- One concern per PR. A change in behaviour goes in its own PR, and the PR title says so.
- Commit messages and PR titles use Conventional Commits, scoped by folder: `core`, `app`,
  `panel`, or `ci` for workflows, for example `fix(core): ...` or `ci: ...`. Repo-wide docs use
  `docs: ...`.
- Open PRs ready for review, not as drafts. The owner reviews and merges.
- Licensing: each folder keeps its upstream MIT `LICENSE`. The root license is not decided yet;
  do not add one.

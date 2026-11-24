# CI

Reference for pluxy's GitHub Actions workflows in `.github/workflows/` and the runners they use.

## Workflows

| Workflow | Runs on | What it does |
| --- | --- | --- |
| `ci.yml` | Every PR and every merge-queue group | Works out which folders the change touches (for a PR, the diff against its base; for a group, what it adds to the group ahead of it in the queue, or to `main` for the first group; sound only while the queue's ruleset keeps "Only merge non-failing pull requests" (ALLGREEN)), runs the workflows below whose paths it touches as reusable workflows, and runs `gate`, which passes only when every workflow it chose passed. A change to `ci.yml` runs them all. `next-release.yml` is not among them |
| `next-ci.yml` | PRs and merge-queue groups (through `ci.yml`) and pushes to `main` touching `next/` | UI type check, lint and unit tests; builds the panel; `gofmt -l .`, `go build ./...`, `go vet ./...`, `staticcheck ./...` and `govulncheck ./...` (both run with `go run` at a pinned version; govulncheck warns and does not fail the job), `go test -race ./...`; the panel's browser test in headless Chromium, and the extension pages test in Chromium, Firefox and WebKit; uploads the browser tests' screenshots and traces as an artifact for 7 days; in a job of its own, `go run ./cmd/pluxy-bench -check`, which fails when pluxy's added latency or memory passes the budgets in `next/cmd/pluxy-bench/budget.go` (`docs/perf.md`); on Windows, `go vet` of `internal/cred`, `internal/private`, `internal/core`, `internal/extconfig`, `internal/logfile`, `internal/server`, `internal/usage` and `internal/configdoc`, `go test` of `internal/cred` and the Windows tests of `internal/core`, `internal/extconfig`, `internal/logfile`, `internal/server`, `internal/usage` and `internal/configdoc` (the access lists of the credential, extension settings, log and usage files, and of the config file a change writes and the client-key-ID key) |
| `next-app.yml` | PRs and merge-queue groups (through `ci.yml`) and pushes to `main` touching `next/` | The desktop app in `next/app`: type check, lint, unit tests and build of the window; `cargo fmt --check`, `cargo clippy`, `cargo test --locked` (unit tests and each connection mode against a real core); format, clippy and tests of `next/app-tools`. In parallel, the native test on Xvfb, which also builds and runs the test AppImage and checks the release gate: a `native-build` job builds what the sessions run once, and shard jobs each run their own sessions on it (`next/app/native/shards.ts`) and upload each session's last screenshot as an artifact for 7 days. The `app` job passes only when every other job did |
| `next-app-platforms.yml` | PRs and merge-queue groups (through `ci.yml`) changing what it builds: `next/app/`, `next/ui/`, `next/protocol/testdata/app-manifests/`, `next/go.mod` or `next/go.sum`, the workflow, the app tools the `tools` and `open` shards build (`next/app-tools/`'s `execution`, `lib/program` and `lib/files`, and its `Cargo.toml` and `Cargo.lock`), or a file other than a test in a Go package the app's tests build (`e2e/panelcore`, `e2e/testext` and `cmd/pluxy` and what they import, as `go list` finds them), and its `native` jobs for the same changes except the app-manifest cases, since their sessions start a test core with `go run ./e2e/panelcore` and the `tools` and `open` shards build those app tools; once a day on `main` (03:17 UTC); `workflow_dispatch` | The desktop app on `windows-latest` and `macos-latest`: the Rust crate's format, clippy and tests, and the native test's sessions that run there (among them the `tools` shard: the signed tool sessions admission, program, routing, launcher and vault; and the `open` shard, on the same build: a tool's opening of a file in the system's own app, and the app's opening of an address in the default browser); the rest of the native test stays on Linux in `next-app.yml`. The workflow file says which jobs run and what each checks; `next/app/README.md` ("Limits") says which sessions run off Linux |
| `next-app-tools-platforms.yml` | PRs and merge-queue groups (through `ci.yml`) touching `next/app-tools/` or the workflow, or the desktop app's files its real-WSL tests run (`next/app/src-tauri/src/environments*.rs`, its wsl-named files, the program host, spawn service and vault, and the Windows process, private-file and managed-core files those tests start tools with or that code uses, as `ci.yml`'s path case lists them); once a day on `main` (03:47 UTC); `workflow_dispatch` | The app tools on `windows-latest` and `macos-latest` (clippy and tests, with the counts in the job summary), and the `WSL 2` and `WSL 1` jobs, which run the real-WSL tests on GitHub-hosted `windows-latest`, the desktop app's program host and spawn service among them (`wsl_real_*` in `next/app/src-tauri`) (for PRs and merge-queue groups only when what they build or run changes, as `ci.yml` decides; always on the daily and manual runs). The workflow file says what each job checks |
| `next-release.yml` | `next-v*` tags; PRs touching `next/Dockerfile`, `next/.dockerignore`, `next/compose.example.yaml`, `next/cmd/pluxy-pack/` or the workflow | PRs: builds the container image (amd64, and arm64 without running it), starts it with a temporary config, polls `/healthz`, checks the panel and the bundled packages, and packs every extension for the release platforms. Tags: also uploads the archives and `registry.json` to the GitHub release and pushes the image to `ghcr.io` once the checks pass |
| `next-app-tools-pack.yml` | PRs and merge-queue groups (through `ci.yml`) touching the app tools' manifests or workspace, `next/cmd/pluxy-pack/` or the workflow; `next-v*` tags | Builds the app tools on each platform's own runner and packs them with `pluxy-pack -app-tools`, uploading nothing; the workflow file has the detail |

When you add a workflow, add a row here. A workflow that must pass before a merge to `main`
runs through `ci.yml`: give it a `workflow_call` trigger instead of a `pull_request` one, add a
path case and a job for it in `ci.yml`, and add the job to `gate`'s `needs`.

## Where jobs run

By default every job runs on GitHub's own runners. Two repository variables send some jobs to
the maintainers' own machines instead:

- `PLUXY_LINUX_RUNNER` takes `next-app.yml`'s `checks` job. It never takes the native test
  shards, since the document portal they start needs `/dev/fuse`, which the runner containers
  don't get, nor `native-build`, which runs on GitHub's runners or `PLUXY_LINUX_CLOUD_RUNNER`'s
  below. `next-ci.yml` never uses it.
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
runners, Namespace's. It holds a bare runner profile label, for example
`namespace-profile-macos-26` (no `;key=value` settings: the runner check refuses them). When it is not set, those jobs run on `macos-latest`.

Pull requests do not use it: all their macOS jobs run on `macos-latest`. Merge groups, pushes, the
daily runs and manual runs use it for the jobs listed below, except the `tools` job in
`next-app-tools-platforms.yml`, which in pull requests and merge groups also runs on `macos-latest`.
The app tools' Terminal launch
tests failed on GitHub's macOS runners in both runs tried on 2026-11-19 (Terminal did not take the
launch's Apple event in time, error -1712), which kept the `tools` job on Namespace; those tests now
run one at a time. Namespace runs one macOS job at a time, about 20 an hour. From 19:30
to 23:40 UTC on 2026-11-19 each merge group needed 6 to 8 of them, and three groups building at once
shared that line with pull requests' runs: one group's macOS jobs ran over 2 hours, while the rest of
a group's work takes about 23 minutes.

It applies to these jobs:
- the `app` job in `next-app-platforms.yml`;
- the macOS legs of the `native` and `dialogs` jobs in `next-app-platforms.yml`, except the `native`
  job's `open` shard, whose session runs only on a GitHub-hosted runner, and its `update` shard,
  whose sessions need passwordless `sudo`, which Namespace's macOS runner no longer gives. Their tests write
  the account's own folders, so they also check the runner (`next/app/native/runner.ts`): the job
  passes the label in `PLUXY_EPHEMERAL_CI_RUNNER`, and the check accepts only a Namespace profile
  label, a runner Namespace named and Namespace's own variable. Another provider needs a change to
  that check;
- the `tools` job in `next-app-tools-platforms.yml`;
- the `build` jobs in `next-app-tools-pack.yml`.

Some macOS jobs and every Windows job stay on GitHub's runners:
- The app's Windows registry tests run only on a runner that reports itself GitHub-hosted.
  Another provider's runner reports itself self-hosted.
- The Windows `native` and `dialogs` jobs need a signed-in desktop session, which Blacksmith's
  Windows runner lacked (it runs in the services session, where the app's window never shows), and
  the workflow names no cloud runner for Windows: Namespace's trial plan has none.
- The Windows app-tool tests fail on Blacksmith's Windows runner.
- The WSL jobs need GitHub's Windows runners.
- The workflows a tag starts always use GitHub's runners.

A fourth variable, `PLUXY_LINUX_CLOUD_RUNNER`, sends the Linux jobs that build to Namespace's
runners: only `checks` and `native-build` in `next-app.yml`. It holds a runner label as JSON, like
the first two; the intended value is `"nscloud-ubuntu-24.04-amd64-4x8"` (4 vCPU, 8 GB). Only the
events named above for the maintainers' runners can use it, so a fork's runs stay on GitHub's
runners. `checks` takes it before `PLUXY_LINUX_RUNNER`. When it is not set, these jobs run where
they did before. `next` in `next-ci.yml` never uses it: it is shorter than `native-build` and the
shards after it, and on GitHub's runners it has no slow install step (its Playwright image pull
took 0.2 to 0.4 minutes in each of 19 runs), so it leaves Namespace's vCPUs to the critical path.
`checks` stays on Namespace: on GitHub its `apt-get install` step has taken 24 minutes on a slow
mirror (the job 30.8, against about 6 on Namespace), which would put it on the critical path
through `app`.

The native test shards and the `app` result job in `next-app.yml` always run on GitHub's runners,
which are free for this repository and seldom queue. On Namespace they waited for its vCPU limit
longer than they ran: in 8 merge groups on 2026-11-18, waits on the native test's path were 8 to
87 minutes against 14 to 21 minutes of running, while pushes to `main` and pull requests held
about half of Namespace's capacity. The shards build nothing but the release gate, so a faster
runner doesn't help them, and they run what `native-build` built on Namespace (both Ubuntu 24.04
with the same packages). Their packages come from a cache, since about half of GitHub's Linux
runners fetch from a slow mirror (the install took 9 to 30 minutes there). Every job that installs
Ubuntu packages does so through `.github/actions/apt-install`, which says how the cache works and
which mirrors it tries when the runner's own fail.

Namespace's runners report themselves self-hosted. In `next-app.yml` they use no Go or Bun cache
and save no Rust cache, and the Rust caches GitHub's runners save don't restore there either (the
cache key includes the image's own Rust, which differs), so their Rust builds start cold.

Namespace's trial runs 32 vCPU at once: 8 jobs of 4 vCPU. A merge group asks for 2 (`checks` and
`native-build`), as does each push to `main`. Past 8, jobs queue on Namespace and start as runners
free up (within about 30 seconds of one freeing, in a test with 10 at once; none failed).

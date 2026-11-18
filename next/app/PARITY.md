# Clients and parity with the old desktop app

This page is a reference for two questions about the desktop app in this folder:

- **Which agent clients and usage sources does it set up or read, and how?** For each one: the
  optional tool that does the work, the files it writes or reads, their format and how they are
  written, the platforms, and the native test session that checks it.
- **What happened to each feature of the old desktop app** (`extensions/app/` in this repository):
  kept, dropped or deferred, with the reason in one line and where a kept feature lives now.

The app's [README](README.md) describes what each page and command does, and the
[app tools README](../app-tools/README.md) describes each tool's operations and file rules. This
page links to them rather than repeating them.

## Platforms

Linux is the only platform the app supports now. Windows, macOS and WSL are later work: the app
does not support them, and none of the Linux checks on this page says anything about them. Some
groundwork for them is merged and runs in CI, such as the native key entry and confirmation
dialogs, login items and one app per profile; [the README's Platforms section](README.md#platforms)
lists it and what still refuses there, which includes every native tool, so every client, usage
source and the launcher on this page.

On Linux, every native tool's manifest but `pluxy/wsl`'s names programs for `linux/amd64` and
`linux/arm64`. The native test runs on GitHub's `ubuntu-24.04` runner, which is x86-64, so it
builds and runs only the amd64 programs; no CI job runs the Linux arm64 ones.

## Agent clients

The app sets up eight agent clients, each from a page in the window and a curated native tool,
which has full access to your user account. Every client page works the same way:

- **Read** previews the client's files: its default place in your home folder, a file you select
  or a folder you select. A link to the file or folder, as stow and chezmoi make, is followed and
  kept as a link.
- **Connect in One Click** (Apply), **Clear** and **Restore Backup** write only the files that
  preview pinned, after a native confirmation that names them and the key's label. Clear keeps the
  edits you made since Apply; Restore Backup puts the original bytes back.
- **Backup and restore** keeps up to 10 manual backups of the client's files in the app's vault.
- The key comes from the vault and goes to the tool on its private pipe. It is written into the
  client's own config file, and never into a launch's arguments or environment.
- **Start CLI** (and Start App, where the client has one) starts the client through the launcher
  (`pluxy/execution`), after the launcher's native confirmation.

### Files each client's tool writes

Paths are the defaults, under the `HOME` the app gives the tool. The tools see none of the
clients' own variables that move these files (such as `CODEX_HOME` or `PI_CODING_AGENT_DIR`):
select the file or folder they name instead.

| Client | Tool | Files (default place) | Format and how they are written | Version or schema check |
| --- | --- | --- | --- | --- |
| Claude Code | `pluxy/claude-code` | `~/.claude/settings.json` | Strict JSON. Rewritten with two-space indentation, keys in the file's order and numbers with their exact digits; other keys stay | `$schema`, when present, must be Claude Code's settings schema |
| Codex | `pluxy/codex-setup` | `~/.codex/config.toml` and the model catalog `~/.codex/pluxy-model-catalog.json` | config.toml edited in place (comments and formatting stay); the catalog as JSON, like Claude Code's file | None: Codex's files carry no version |
| Claude Desktop | `pluxy/claude-desktop` | In `~/.config/Claude-3p`: `claude_desktop_config.json`, `configLibrary/00000000-0000-4000-8000-000000831700.json` (pluxy's gateway profile) and `configLibrary/_meta.json` | JSON, like Claude Code's file | None |
| OpenCode | `pluxy/opencode` | `~/.config/opencode/opencode.jsonc`, or `opencode.json` when only that exists | JSONC edited in place: comments, trailing commas and formatting stay | `$schema`, when present, must be `https://opencode.ai/config.json` |
| Oh My Pi | `pluxy/omp-setup` | `~/.omp/agent/models.yml` and `~/.omp/agent/config.yml` | YAML edited in place: comments, key order, quoting and indentation stay | None |
| Grok Build | `pluxy/grok-setup` | `~/.grok/config.toml` | TOML edited in place | None |
| Hermes Agent | `pluxy/hermes-setup` | `~/.hermes/config.yaml`, or the active profile's (`~/.hermes/profiles/<name>/config.yaml`) | YAML edited in place | `_config_version` is not checked |
| Pi | `pluxy/pi-setup` | In `~/.pi/agent`: `cliproxyapi.json` and `settings.json`; it removes `cliproxyapi-models.json`, and installs the provider plugin as the folder `pluxy-cliproxyapi-provider/` | JSON, like Claude Code's file | None. The plugin declares Pi 0.82.0 or later; the tool does not check it |

Each tool refuses to write a file its client would not read, and still lets Restore Backup put the
original back. The exact keys each tool writes are in the [app tools README](../app-tools/README.md).

### Starting each client, platforms and checks

| Client | Starts on Linux | Linux | Native session | Windows | macOS | WSL |
| --- | --- | --- | --- | --- | --- | --- |
| Claude Code | `claude` in a terminal | Done | `claude-code` | Later | Later | Later |
| Codex | `codex` in a terminal; Codex App: a program you name (Codex has no Linux app); `codex app-server daemon restart` | Done | `codex` | Later | Later | Later |
| Claude Desktop | `claude-desktop`, run directly; **Create alias in core** adds a model alias in the core | Done | `claude-desktop` | Later | Later | Later |
| OpenCode | `opencode` in a terminal; OpenCode Desktop (`/opt/OpenCode/ai.opencode`) run directly | Done | `opencode` | Later | Later | Later |
| Oh My Pi | `omp` in a terminal | Done | `omp` | Later | Later | Later |
| Grok Build | `grok` in a terminal | Done | `grok` | Later | Later | Later |
| Hermes Agent | `hermes` in a terminal | Done | `hermes` | Later | Later | Later |
| Pi | `pi` in a terminal | Done | `pi` | Later | Later | Later |

Session names are those in [`native/shards.ts`](native/shards.ts); [`native/run.ts`](native/run.ts)
says what each one checks. Every client session installs its tool, enters a key through native key
entry, applies, clears and restores the client's files to hand-written bytes, writes through a
stow link and a chezmoi-style root, refuses a replaced target and a file the client would not take,
keeps a manual backup, starts a stand-in client through the launcher with no key in its arguments
or environment, removes the tool leaving no process, and checks that the key and a login canary
never reach a webview, a confirmation or the app's output. Beyond that:

- `claude-code` also runs Beginner Mode (its `easy` step);
- `codex` also imports the core's models into the catalog and starts Codex App and the CLI service
  restart (its `import`, `app` and `service` steps);
- `claude-desktop` also creates Claude Desktop's missing configuration folder and a model alias in
  the core (its `missing` and `alias` steps);
- `pi` also updates and uninstalls the provider plugin (its `plugin_update` and
  `plugin_uninstall` steps).

## Usage sources and the Codex session reader

The usage hub, `pluxy/agent-usage`, keeps every source's token counts in its own SQLite store,
`usage.db` in its data folder, and draws the *Agent usage* page ([its README](../app-tools/agent-usage/README.md)).
Each source is a parser of its own. Parsers and the session reader only read: they never write a
client's file.

| Source | Package | What it reads (default place) | Format | Native session | Windows, macOS, WSL |
| --- | --- | --- | --- | --- | --- |
| Codex | `pluxy/usage-codex` | `~/.codex/sessions`, `~/.codex/archived_sessions` | Rollout JSONL | `usage` | Later |
| Claude Code | `pluxy/usage-claude` | `~/.claude/projects` | Transcript JSONL | `usage` | Later |
| OpenCode | `pluxy/usage-opencode` | `~/.local/share/opencode`, `~/.opencode` | SQLite (`opencode.db`, opened read-only) and older JSON message files | `usage` | Later |
| Oh My Pi | `pluxy/usage-omp` | `~/.omp/agent/sessions`, `~/.omp/sessions` | Session JSONL | `usage` | Later |
| Pi | `pluxy/usage-pi` | `~/.pi/agent/sessions`, `~/.pi/sessions` | Session JSONL | `usage` | Later |
| Codex sessions (reader) | `pluxy/codex-sessions` | `~/.codex`: `sqlite/*.db` and the other databases, `sessions/**`, `archived_sessions/**`, `session_index.jsonl` | SQLite, opened read-only, and rollout JSONL | `sessions`, `open`, `save` | Later |

The `usage` session checks the hub with a test-only fixture parser, then each of the five parsers
on a hand-written home in the run's HOME. Every report must equal a hand-written expected one, also
after a repeated sync and after files are appended or added. Prompt canaries must stay out of every
report, the hub's store and the app's output. A disabled parser scans nothing, and no hub or parser
process is left when idle.

The usage parsers and the session reader also build and pass their unit tests on Windows and macOS
in CI (`.github/workflows/next-app-tools-platforms.yml`). The app does not run native tools there,
so that is not support.

## The rest of the kept set

| Feature | Where it lives | Native session |
| --- | --- | --- |
| Beginner Mode (the old app's Easy mode) | The window's Beginner Mode page ([README](README.md#beginner-mode)) | `claude-code`, its `easy` step; the list of client pages also has unit tests in `tests/` |
| Launcher: terminals, direct launches, remembered one-click starts, the tray's Start… menu | The Launcher page and `pluxy/execution` ([README](README.md#the-launcher)) | `launcher`, `spawn` |
| Opening a tool's file in your own app (the old app's "Edit with local app") | `pluxy/execution`'s open launch; the Codex session reader's `open` | `open`, `sessions` |
| Saving a file a tool hands over | The host's save dialog ([app-host README](app-host/README.md#saving-files)) | `save`, `sessions` |
| Shared TOML and YAML editing for the setup tools | `lib/transactions` features `toml` and `yaml` ([app tools README](../app-tools/README.md#libtransactions)) | `codex` and `grok` (TOML), `omp` and `hermes` (YAML), and the library's unit tests |
| An update restart stops managed runtimes, as Quit does | `stop_owned` in `src-tauri/src/lib.rs` | None for the restart itself. `spawn` checks that Quit stops managed runtimes and leaves a detached launch running; Quit and the update restart call the same function |

## What an optional tool costs

- **Not installed:** none of its code is on disk. The client pages and Beginner Mode are part of the
  window. The navigation lists a client's page by default only when its tool is installed (the Manage
  Clients page can list any, and Beginner Mode offers them all), and a client page whose tool is
  missing only says how to install it.
- **Installed, disabled:** its files sit in the app's package store. Nothing runs, and a call to it
  is refused.
- **Enabled:** no process until something calls it. The app starts it for a call and stops it 30
  seconds after its last call ends, with every process in its group. Opening the Launcher page, or
  a client page with a Start CLI card (all but Claude Desktop's), asks the launcher for its terminal
  list, so it starts `pluxy/execution` for that one call when the launcher is enabled. A client
  page's Start CLI card asks again each time this device's packages change while it is open.
- **The usage hub** syncs only while its page is open. **The Codex session reader** reads only when
  its page asks.
- **Launches are not the tool's.** A detached launch runs on after its tool stops and after the app
  quits. A managed runtime runs until you stop it, its package is disabled or removed, or the app
  quits or restarts for an update.

Each of these is checked in the `program`, `usage`, `sessions` and `spawn` sessions; the README's
[Extensions on this device](README.md#extensions-on-this-device) section has the details.

## Keep/drop record

"Deferred" means not built in this rebuild, and still planned. "Dropped" means not planned. Two
drops are proposals the owner has not reviewed yet; their rows say so.

### Agent clients

| Old app feature | Decision | Why | Where now |
| --- | --- | --- | --- |
| Claude Code, Codex, Claude Desktop, OpenCode, Oh My Pi, Grok Build, Hermes Agent and Pi setup and launch | Kept | The main clients | A window page and a tool for each ([above](#agent-clients)) |
| Kimi Code, OpenClaw, ZCode, DeepSeek Harness, WorkBuddy, Antigravity CLI, Copilot CLI and VSCode Copilot setup and launch | Deferred | The rebuild covers the main clients first; these follow later | Not in the app yet |
| Client setup inside WSL distributions | Deferred | Part of the later Windows work | `pluxy/wsl` lists distributions and users and prepares launches, but the app's side is not built |
| Executable and folder overrides, terminal choice and recent working folders | Kept | They let a launch use the person's own install and folders | Each client's Start CLI and the Launcher page |
| Showing, hiding, searching and resetting the list of clients | Kept | The person chooses which clients the navigation shows | The Manage Clients page ([README](README.md#manage-clients)): by default the navigation lists the clients whose tool is installed |
| Codex's OAuth sign-in method and its OpenAI provider-identity switch | Deferred | Not built now: the core takes no ChatGPT login from a client, so the sign-in could not reach it. A later change could add a separate Codex profile for it | Codex uses a client key |
| Codex model catalog fetched silently | Dropped | The catalog changes only when the person asks | **Import models from core** on the Codex page |
| Pi's provider plugin installed with `pi install` from npm | Replaced | npm serves whatever is published at that moment | A reviewed copy carried by `pluxy/pi-setup` |
| Claude Desktop model aliases written into the core's config file | Replaced | A setup tool does not edit the core's files | **Create alias in core**, through the management API |
| Old command entry points with no visible feature | Dropped | Nothing used them | — |

### Usage and sessions

| Old app feature | Decision | Why | Where now |
| --- | --- | --- | --- |
| Codex, Claude Code, OpenCode, Oh My Pi and Pi usage | Kept | Sources of the kept clients | The usage hub and its parsers ([above](#usage-sources-and-the-codex-session-reader)) |
| Copilot, Gemini CLI, Antigravity, Kimi and ZCode usage | Deferred | They follow with the deferred clients | Not in the app yet |
| MCode usage | Dropped in this rebuild, pending the owner's review | Only a generic reader, with no kept client and no known sample to check it against | — |
| Prices, price sync and estimated cost | Dropped | Prices are not what was billed; tokens are kept | The *Agent usage* page shows tokens, cache and reasoning |
| Codex sessions: list, detail, export as JSONL, open in your own app | Kept | Read-only, so the Codex home is never changed | `pluxy/codex-sessions` and its *Codex sessions* page |
| Codex sessions: sync historical sessions, check invalid records, remove invalid index entries, delete with a backup and restore | Deferred | Writes to Codex's files need their own checks | Not in the app yet |
| Deleting a session without a backup | Dropped | A delete should always leave a way back | — |
| Experimental context authoring (editing messages, roles and session settings) | Dropped in this rebuild, pending the owner's review | It rewrites Codex's history, which risks recovery for little proxy-management value | Read-only detail and export stay |

### Guided setup

| Old app feature | Decision | Why | Where now |
| --- | --- | --- | --- |
| Easy mode, shown as Beginner Mode: sign in, then set up an agent | Kept | The guided workflow is useful | The window's Beginner Mode page |
| Its spotlight tour | Dropped | It pointed at elements of pages the guide now shares with the rest of the app | Each step shows its own short text |
| Its "API Access" form | Dropped | It sent the API key through the core's request proxy; API-key sign-in is part of Add Account | Step 1 is Add Account |
| Its full-screen mode, Exit button and own theme and language switchers | Dropped | The app's shell owns navigation, themes and language | — |

### The app itself

| Old app feature | Decision | Why | Where now |
| --- | --- | --- | --- |
| Three connection modes, the tray, and closing webviews when the window closes | Kept | Core of the app | [README](README.md#how-the-app-reaches-core) |
| Native management key entry and rotation | Kept | Keeps the key out of webviews | [README](README.md#how-the-app-reaches-core) |
| Managed-core recovery and the tray's Start, Stop and Restart | Kept | | [README](README.md#the-window-and-the-tray) |
| One app per profile, startup preferences, close and window preferences | Kept | | [README](README.md#one-app-per-profile) |
| Privacy mode | Kept | | [README](README.md#privacy-mode) |
| Release feed, release notes, channels, signed app update and rollback | Kept | | [README](README.md#version-management-and-release-notes) |
| Portable replacement helper, upstream release addresses, the kernel version picker and preset mirrors | Dropped | The app updates only through the signed updater, from a feed address the person sets, which replaces the old app's core-version page (its `versions` page) | — |
| MCP server and skills management | Dropped | The owner's decision; app packages are not agent skills | — |
| Windows installers: the old app's NSIS setup and MSI package (`bundle.targets` `all`), which its updater ran to update | Replaced | One per-user NSIS setup program, no administrator rights; the MSI is dropped: it installs machine-wide, for managed deployment, which is not a goal. The app updates the installed program set itself and never runs the setup, and the setup refuses while the app runs instead of closing it by force. Test package only until release signing | [README](README.md#packages) |
| Windows and macOS | Deferred | Later platform work. Some groundwork is merged (native key entry and confirmations, login items, one app per profile, test packages), but no native tool runs there | [README](README.md#platforms) |

### Smaller old features not carried

| Old app feature | Decision | Why | Where now |
| --- | --- | --- | --- |
| Model health sweep ("Check All") | Dropped | One click would spend a request on every model; the account's Test checks one model and says it spends one request | The account's Test |
| Reverse proxy ("External access") | Dropped | Core serves TLS itself, so a second forwarder in the app is not needed | `listen` and the TLS settings in core's config |
| Follow system proxy | Dropped | Core sends provider credentials through a proxy only when told to, so it does not read the system's or the environment's; set `proxy_url` once | `proxy_url` in core's config, or on an account's Edit form |
| Browser choice for login links | Dropped | The sign-in link is shown, and can be copied or opened in any browser the person chooses | — |
| Codex "Allow parallel tool calls" in the model catalog editor | Dropped | Codex no longer reads it: `supports_parallel_tool_calls` is not in Codex's model information, so the setting would change nothing | — |
| Windows title bar and window materials | Dropped | Decoration with per-version rules that needs native code | — |
| Thinking and Fast aliases ("Model Aliases") | Deferred | The same effect exists in config: a thinking suffix such as `gpt-5(high)`, a payload rule, or an account's model alias; no form yet | Core's config, in the Source tab |
| Open-folder buttons | Deferred | Small value, and a new native action | — |
| Installed and version badges on clients | Deferred | The client list says which clients can be set up; badges for what is installed come later | [README](README.md#beginner-mode) |
| Languages (English, Simplified and Traditional Chinese, Japanese) | Deferred | English only for now; the string layer is ready for more | — |
| Allow-LAN switch | Deferred | Exposing a service that holds provider credentials to the network is a choice that should be a visible edit. By hand: set `listen` to `0.0.0.0:<port>` in the Source tab and restart; the management API stays on this machine | `listen` in core's config |
| Usage page of core requests (ranges, trend, analysis, request details, data management) | Kept in part | Totals and chart are on the Dashboard, and the panel's Usage page lists requests with a CSV export; prices and the storage cap are dropped, and analysis is not built | Dashboard and Usage in the panel |

## Evidence

Checked against `main` at `168f83888`, in the merge-group run
[43838704720](https://github.com/stdotwork/pluxy/actions/runs/43838704720) of the CI workflow
(and before it at `fe42982d5`, in [43831091359](https://github.com/stdotwork/pluxy/actions/runs/43831091359),
with the same results). Its four native jobs each ended with their shard's pass line:

- `native test shard usage passed: program, transactions, usage`;
- `native test shard tools passed: core steps, admission, routing, vault, spawn, sessions, launcher, claude-code, open, codex, save, claude-desktop, opencode, omp, grok, hermes, pi`;
- `native test shard app passed: managed, startup, window, window-no-tray, privacy, access, early exit, instances, update, local, remote, packaged`;
- `native test shard release passed: release gate`.

In the same run the browser tests, the panel's in Chromium and the extension pages test in
Chromium, Firefox and WebKit, passed 90 and skipped 1: `screenshots in two themes`, which runs
only when `PANEL_SHOTS_DIR` is set. They test the shared kit and core's extension pages, not the
app's own pages, which the native sessions above check. Beginner Mode's own merge ran in
[43829776833](https://github.com/stdotwork/pluxy/actions/runs/43829776833), and the shared TOML and
YAML editing's in [43823480514](https://github.com/stdotwork/pluxy/actions/runs/43823480514), both
green.

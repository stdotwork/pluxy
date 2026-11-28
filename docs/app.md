# The desktop app

The desktop app is pluxy's web panel in a window of its own, plus what only a program on your
computer can do: run pluxy for you, set up the coding tools installed on this computer, start them,
and keep a management key out of any web page. It shows the same pages as the panel
([panel.md](panel.md)), in the same places, and adds pages of its own.

The app either runs pluxy itself, on this computer, or connects to a pluxy that is already running,
here or on another computer, with its address and management key (the key that unlocks pluxy's
management API). How the app works inside is in [app-design.md](design/app-design.md).

## What works today

There is no release of the app yet: you build it from the repository. The build to use is the
**local build** (below), which builds on Linux x86_64 only.

| | Linux x86_64 | Windows, macOS, Linux arm64 |
| --- | --- | --- |
| The app: window, tray, the three ways to reach pluxy, Updates, Add-ons | Yes | Not supported yet. On Windows and macOS the app builds and parts of it run in CI's tests, but no build is offered |
| Running pluxy on this computer | Yes, the pluxy the local build comes with | Not supported yet |
| Coding-tool pages, Launcher, Agent usage, Codex sessions | Yes, in the local build | Not supported yet |
| Giving a page add-on access to files (Access) | Yes | No |

What a build can do depends on how it was built:

- **The local build** (`make app-local`) comes with pluxy, every provider extension and every
  coding-tool add-on, and trusts the add-ons it was built with. Everything on this page works in it.
- **A plain build** (`cargo build --release` in `next/app/src-tauri`) has no pluxy inside and trusts
  no add-on publisher: it can connect to a pluxy you run yourself, or run one you point it at
  (**Your own pluxy program**), and it can install unverified page add-ons. It cannot install the
  coding-tool add-ons, so the tool pages, the Launcher, Agent usage and Codex sessions cannot run.
- **Updates:** no build updates itself yet. No build has an update key, and there is no built-in
  update source; build the app again to update it.

## Get the app

The local build needs Go, Bun 1.4.3, Rust, and the WebKitGTK 4.1 and libayatana-appindicator
development packages ([`next/app/README.md`](../next/app/README.md#build-and-test) lists them).
From `next/` in the repository:

```bash
make app-local                        # into next/app/local-build
make app-local LOCAL=/some/folder     # or into a folder of your choice
```

Start it from the repository's root:

```bash
next/app/local-build/start                # in a home folder of its own, home/ in the build's folder
next/app/local-build/start --real-home    # on your own home folder and coding tools
```

- **`start`** runs the app with `home/` inside the build's folder as its home. Its settings,
  accounts and the files the tool pages write (such as `home/.claude/settings.json`) all stay in
  that folder, so nothing of yours changes. Use it to try the app.
- **`start --real-home`** runs the app on your own home folder, so the tool pages set up your real
  coding tools (`~/.claude`, `~/.codex` and the rest). Before it starts, it prints every file and
  folder it writes and the commands that remove them. It refuses to start when your home folder is a
  link, belongs to another user, or other users can open it (`chmod 700` it).

The local build says "pluxy (local build)" in its window title and tray, and has settings folders of
its own (`dev.pluxy.desktop.local`), so it never shares anything with another copy of the app. It
never opens at sign-in: it starts only with `start`.

**Build it again** after a change: quit the app (from the tray if it is there), then run
`make app-local` again with the same folder. Your accounts and settings stay. After a rebuild the
coding-tool add-ons you installed show as **Unverified** and turned off, since each build signs its
add-ons with a key of its own: on Add-ons, **Remove** each one, **Install** it again and turn it on.
What Connect wrote into your tools' settings stays meanwhile.

**Remove it:** quit the app, then delete the build folder. If you used `--real-home`, first press
**Disconnect** on each tool page you connected, then delete what `start` printed.

## First launch

When you open the app for the first time, it asks **Where should pluxy run?** with two choices:

- **On this computer:** starts the app's own pluxy here while the app is open, with nothing to type.
- **Connect to a pluxy that is already running:** asks for that pluxy's address and management key
  ([Where pluxy runs](#where-pluxy-runs)).

Once connected and signed in to a new pluxy with no installed providers and no accounts, the window
opens **Set up pluxy** in three steps:

1. Add your AI providers and accounts. Providers whose coding tool is installed or signed in on this
   computer are ticked for you (for example Claude when Claude Code is installed, and Codex when
   Codex is installed).
2. Set up your coding tools, using the same rows as [Tools](#the-coding-tool-pages). Opening a tool
   page from here shows a strip at the top with **Back to setup**.
3. Send a test request.

After you finish setup or press **Leave setup for now**, the app opens on Home, and **Setup** stays
under Settings (in the **pluxy** group) with every step folded. To build or run pluxy without the
app, see [setup.md](setup.md).

## Where pluxy runs

The app asks once, **Where should pluxy run?**, and you can change the answer later on **Where pluxy
runs** (Settings, in the **This device** group).

| Choice | What the app does | When the app quits |
| --- | --- | --- |
| **On this computer** | Runs its own pluxy, with a config of its own on a free port of this computer. Nothing to type | Stops that pluxy |
| **Connect to a pluxy that is already running**, at a local address (`127.0.0.0/8`, `::1`, `localhost` or `localhost.`) | Connects to a pluxy someone else runs on this computer, such as a service. `http://` or `https://` is allowed | Leaves it running |
| **Connect to a pluxy that is already running**, anywhere else | Connects over `https://`, over plain `http://` carried by Tailscale (encrypted by Tailscale; the app checks that this computer has a Tailscale interface and that the resolved address is in Tailscale's ranges and not this computer's own, and sends nothing if Tailscale stops carrying it), or over plain `http://` on a network you say you trust in the key window (**Trust this network and connect**). For a certificate you made yourself over `https://`, open **It uses a certificate I made myself** and give its **Certificate authority file** (press **Choose…** on Linux) | Leaves it running |

To connect, type the **Address** (scheme, host and port only), then **Continue** (or **Connect**).
Before opening the key window, the app checks the address with no key so a wrong address, an
untrusted certificate, or remote management being off is reported before you type anything. A small
window, **pluxy management key**, then asks for the key. On the computer pluxy runs on,
`pluxy key management --show-key` prints it. For plain `http://` that Tailscale does not carry, the
key window warns that anyone on the network can read the key and what the app sends, and its confirm
button reads **Trust this network and connect**; that trust is saved for that address only. Tick
**Keep the key on this device** to have the app sign in by itself next time; otherwise it forgets
the key when it quits. The key is checked before the app switches, and a wrong key or **Cancel**
keeps the connection you had. If the check fails for a reason other than a wrong key, the app holds
the key you typed in memory for 10 minutes for your next try at the same address and certificate
authority file (closing the window to the tray or changing the address drops it).

On **Where pluxy runs**, the status line under the address says how a remote connection travels:
**Only over Tailscale, which encrypts the connection.** while Tailscale carries the last
connection; **Only over Tailscale: nothing is sent unless Tailscale is on and reaches it.** before a
connection or when Tailscale is off; or **Over plain http, on a network you said you trust.** for a
trusted plain-HTTP network.

The key never reaches the app's web pages: you type it into that window, and the app adds it to each
call. To change the key, press **Replace key** in the **Keys** section of **Config** (Settings tab):
the app asks in a window of its own, keeps the new key the way the old one was kept, and shows it
once with Copy, for other programs that used the old key.

When pluxy is not running or the app is not signed in, the window shows none of pluxy's pages, only
one pane that says what happened and offers the way on, such as **Start pluxy**, **Enter management
key** or **Change where pluxy runs** ([Troubleshooting](#troubleshooting) lists the cases).

### The app's own pluxy

With **On this computer**, the app runs only copies of pluxy it keeps in its own store, never a
program in place. The local build brings one, which the app copies in at its first start.
**Versions and advanced** on Where pluxy runs lists the versions installed (**Came with the app**,
**Signed release**, **Your own program**), which one runs and which ran before it.

- **Your own pluxy program:** name a `pluxy` program you built; the app shows its path, size and
  digest in a confirmation window, then copies it in and runs the copy. Only the program is copied,
  not the extensions beside it.
- **Switch to this version** goes back to an installed version, after a confirmation.
- **Install again** repairs a version whose files changed: the app checks every file before each
  start and does not start a changed one.
- **Release list** names an index file of signed pluxy releases to install from. No build trusts a
  release publisher yet, so this refuses every release for now.

When a switch fails to start, the app puts back pluxy's config and runs the version that worked
before. When the app's own pluxy stops with an error, the app starts it again after 2, 5 and 15
seconds, at most three times in five minutes; then it stops trying and the tray says
**pluxy: Stopped restarting**.

## The window and the tray

**When you close the window** (on Where pluxy runs) chooses what closing does: **Keep in the tray**
(the default), **Quit**, or **Ask**. Keeping the app in the tray closes the window and its web pages
entirely, so they use no memory. Quitting stops pluxy only when the app runs it (**On this
computer**); a pluxy you connected to keeps running.

The tray menu's first line says how pluxy is: **pluxy: Running**, **Stopped**, **Restarting**,
**Stopped restarting** or **Not installed** when the app runs it; **pluxy: Connected to <host>** (once
signed in) or **pluxy: Not connected** otherwise; **pluxy: Working…** while a tray action runs.
Then:

| Item | What it does |
| --- | --- |
| **Start pluxy**, **Stop pluxy**, **Restart pluxy** | Control the app's own pluxy, with the window closed too. Off when the app connects to a pluxy it did not start |
| **Open pluxy** | Opens the window (so does a left click on the icon) |
| **Start…** | Your remembered launches ([Launcher](#launcher)) |
| **Remembered launches…** | A window to review and revoke remembered launches |
| **Quit** | Quits the app; it reads **Quit (stops pluxy)** when the app runs pluxy |

When the desktop has no usable tray, closing the window quits the app. If the app runs pluxy, it
asks first, **Close and stop pluxy?**, since your coding tools then stop getting answers, unless you
chose **Don't ask again** there.

The window reopens at the size and place it last had.

## One app per profile

The app runs once per user and data folder. Opening it again (from the menu, a shortcut or at
sign-in) shows the window of the copy already running and exits. A different `HOME` or `XDG_*`
folder is a separate profile, with its own app and pluxy.

## Startup

Where pluxy runs has up to three startup rows, each saved as soon as you change it. The last two show
only when the app runs pluxy itself.

| Row | Default | What it does |
| --- | --- | --- |
| **Open at sign-in** | Off | Opens the app when you sign in (an entry in your autostart folder). The local build does not offer it, and then shows no Start in the tray either |
| **Start in the tray** | Off | At sign-in, starts in the tray without a window. Needs Open at sign-in, a usable tray, and the app running pluxy itself |
| **Start pluxy with the app** | On | Starts the app's own pluxy when the app opens. Off: pluxy stays stopped until you press Start pluxy |

## Hiding names

**Hide names** in Settings, General, hides account e-mail addresses and names on every page, for
sharing your screen, and the tool pages also hide paths, user names and account IDs. While it is on,
the header shows **Names hidden**, which turns it off. The app keeps the choice itself, so clearing
the window's storage does not turn it off.

## Saving files

Saving a file from a page (a theme file on Themes, a log on Logs, the request table on Activity's
Requests tab) puts it in your Downloads folder and shows `Saved to <path>`. A file of the same name
is never overwritten: the new one gets ` (1)`, ` (2)` and so on.

## Pages the app adds

| Page | Where | What it is for |
| --- | --- | --- |
| **Where pluxy runs** | Settings, This device | Where pluxy runs, its versions, startup and close choices |
| **Updates** | Settings, This device | The app's and pluxy's versions, release notes, the update source |
| **Add-ons** | Settings, This device | Add-ons installed on this computer |
| **Launcher** | Settings, This device | Starting coding tools in one click |
| Claude Code, Codex, Claude Desktop, OpenCode, Oh My Pi, Grok Build, Hermes Agent, Pi | Opened from rows on Tools and Setup | Set up and start each coding tool (each opens as `Tools / {Tool}`) |
| **Agent usage**, **Codex sessions** | Settings, From extensions | Pages of add-ons, when installed and on |

Tools and Setup's step 2 show the same eight coding-tool rows: the five tools pluxy scans, plus
Claude Desktop, Grok Build and Hermes Agent, which the app reads itself. When connected to a pluxy
on another computer, Tools and Setup show that computer's tools with the panel's own buttons (for
Claude Code and Codex, **Connect**, which writes their settings on the computer pluxy runs on, and
the paste steps under **Set up by hand**; for the others, the paste steps under **Set up**) and list
links to this computer's eight tool pages underneath.

### Add-ons

Add-ons are packages installed on this computer for the app, apart from pluxy's own extensions
(installing one never installs it in pluxy). There are two kinds:

- **Page add-ons** add pages. Their pages get no settings, files or tools unless you allow them.
- **Coding-tool add-ons** are programs that run as you, with your permissions: they read and write
  your coding tools' settings, start programs, and read usage records. Only a **Verified** add-on
  can be one: an add-on from a source the app trusts, signed by that source's key. The local build
  trusts its own add-ons.

Add-ons come from **Available to install** (the sources listed in `device-registries.json` in the
app's config folder, which the local build sets up) or from **Install from file…**. Anything else is
**Unverified**; an entry the app would refuse is marked **Blocked** with the reason.

A coding-tool add-on installs turned off. **Turn on** opens a confirmation window (**pluxy needs
your OK**) that says first in plain words what the add-on does and that it runs as you with full
access to your user account, puts any text from the add-on's manifest last, and keeps the exact
action under **Details**. Only Confirm there turns it on; once shown, a confirmation window stays
open for 10 minutes before closing as cancelled. Updating, going back to an earlier version or
turning it off ends that consent, so the next version asks again. A coding-tool add-on runs only
while something uses it and stops 30 seconds after.

For a page add-on, **Allow tool access…** lets a Verified one use coding-tool add-ons, and **Approve
publisher…** then **Choose a file…** (or **Choose a folder…**) lets it read a file or folder you pick
(Linux only). Each asks in a confirmation window. A page add-on can send what it reads anywhere its
pages can reach, so give it only what you would share.

**Go back to <version>** returns to the version an update replaced. **Remove** deletes the add-on
and anything you allowed it.

### The coding-tool pages

All eight coding tools appear as rows on **Tools** (and in step 2 of **Setup**). Each tool's page
opens from its row as `Tools / {Tool}` and needs that tool's add-on. On a pluxy running on this
computer, each row offers one button that moves the tool forward in one press:

| Add-on state | Row button |
| --- | --- |
| On | **Open** when the tool is Connected, else **Set up**; both open the tool's page |
| Installed, off | **Turn on add-on**: opens the add-on's confirmation window, then opens the tool's page |
| Offered by a registry | **Install add-on** (shows **Installing…**), then opens the turn-on confirmation window and the tool's page |
| None available | No app button; the row keeps the panel's own buttons: for Claude Code and Codex while pluxy can write their settings, **Connect** and the paste steps under **Set up by hand** (**Disconnect** once pluxy connected the tool); else the paste steps under **Set up**, or **Needs its add-on** |

A tool page shows how the tool stands (`Connected`, `Installed · not connected`, `Settings need update`,
`Cannot change settings`, `Not installed`, `Not checked`) and the one thing to press next:

| Button | What it does |
| --- | --- |
| **Turn on add-on** | Turns the tool's add-on on, in its confirmation window |
| **Connect {tool}** | Makes a key for the tool in pluxy (named after the tool, so Activity and Tools show which tool uses it), then writes pluxy's address, the key and the models into the tool's settings in one confirmation window naming both the files and the key |
| **Update {tool}** | The same, for settings that point elsewhere or miss part of what pluxy needs |
| **Disconnect** | Takes pluxy's values out of the tool's settings; your own edits stay |
| **Restore backup** | Puts the settings back as they were before pluxy first changed them |
| **Start {tool}** | Starts the tool, in a terminal or as its app, through the Launcher add-on, after a confirmation window |

The page reads the tool's settings from their usual place under your home folder. Under **Settings
file, key and backups** you can choose another file or folder (**Read this file**), use a key you
already have (**Use an existing key…**), see exactly what Connect changes, and keep up to 10 backups
of the files (**Create backup**). A link to the file, as stow or chezmoi make, is followed and kept as
a link. Each write goes only to the files you read; if a file changed since, the add-on refuses and
asks you to read it again.

| Tool | Add-on | Files (usual place) | Start | Notes |
| --- | --- | --- | --- | --- |
| Claude Code | `pluxy/claude-code` | `~/.claude/settings.json` | `claude` in a terminal | Models for Opus, Sonnet, Haiku and Fable, subagents, auto-compaction. If Claude Code has not run yet (`~/.claude` does not exist), Connect creates `~/.claude` in the same write (named in the confirmation) so its first start uses pluxy without asking to sign in |
| Codex | `pluxy/codex-setup` | `~/.codex/config.toml` and Codex's model list beside it | `codex` in a terminal; **Restart Codex service**; **Start Codex App** (a program you name) | **Import models from pluxy** fills the model list. Codex hides conversations of another provider from resume |
| Claude Desktop | `pluxy/claude-desktop` | Three files in `~/.config/Claude-3p` | `claude-desktop`, directly | Its third-party gateway (Linux beta). Takes only Claude model names: **Create alias in pluxy** adds an alias for another model. Quit and reopen it after a change |
| OpenCode | `pluxy/opencode` | `~/.config/opencode/opencode.jsonc` (or `opencode.json`) | `opencode` in a terminal; **Start OpenCode Desktop** | **Import models from pluxy** fills the model list |
| Oh My Pi | `pluxy/omp-setup` | `~/.omp/agent/models.yml` and `config.yml` | `omp` in a terminal | Sets the default model; Oh My Pi lists pluxy's other models itself |
| Grok Build | `pluxy/grok-setup` | `~/.grok/config.toml` | `grok` in a terminal | **Import models from pluxy** fills the model list |
| Hermes Agent | `pluxy/hermes-setup` | `~/.hermes/config.yaml` (or the active profile's) | `hermes` in a terminal | Sets the default model |
| Pi | `pluxy/pi-setup` | In `~/.pi/agent`: `cliproxyapi.json`, `settings.json` and the provider plugin's folder `pluxy-cliproxyapi-provider/`; it removes the plugin's model cache `cliproxyapi-models.json` | `pi` in a terminal | Connect is **Install plugin** until the plugin is in place; **Update plugin**, **Uninstall plugin**. If Pi has not run yet (`~/.pi/agent` does not exist), Install plugin creates `~/.pi/agent` in the same write (named in the confirmation; refused if `~/.pi` is a link or not a folder) |

A tool started from another program keeps its old settings until you start it again: the app never
stops a program it did not start.

These eight are the coding tools the app sets up. For any other tool, point it at pluxy by hand
([clients.md](clients.md)).

### Launcher

The Launcher (Settings, This device) starts coding tools and terminals in one click, from the page or
the tray's **Start…** menu. It needs the Launcher add-on (`pluxy/execution`).

A **New launch** takes a **Tool name**, **How to run** (**In a terminal** or **Directly**), a
**Program** (a name found on `PATH`, such as `claude`, or a full path), **Arguments**, a **Folder**, a
**Terminal** and, for a direct launch, **When the app quits** (**Keep it running** or **Stop it**; a
terminal always keeps running). **Start…** opens a confirmation window that shows exactly what will
run; tick **Remember this exact executable, argv and cwd** to add it to **Remembered launches**.

A remembered launch starts again with **Start**, with no window, only while the program, its
contents, arguments, folder and environment are exactly what you confirmed; anything changed asks
again. **Forget** revokes it. **Running now** lists what the Launcher started that still runs, with
**Restart** and **Stop**.

### Agent usage and Codex sessions

**Agent usage** (add-on `pluxy/agent-usage`) counts the tokens, cache and reasoning your coding tools
recorded on this computer, with a parser add-on for each tool. It reads only while its page is open,
and never writes a tool's files. It shows tokens, not costs.

| Tool | Parser add-on | What it reads |
| --- | --- | --- |
| Codex | `pluxy/usage-codex` | `~/.codex/sessions`, `~/.codex/archived_sessions` |
| Claude Code | `pluxy/usage-claude` | `~/.claude/projects` |
| OpenCode | `pluxy/usage-opencode` | `~/.local/share/opencode`, `~/.opencode` |
| Oh My Pi | `pluxy/usage-omp` | `~/.omp/agent/sessions`, `~/.omp/sessions` |
| Pi | `pluxy/usage-pi` | `~/.pi/agent/sessions`, `~/.pi/sessions` |

**Codex sessions** (`pluxy/codex-sessions`) lists Codex's conversations from `~/.codex`, shows them,
exports one as JSONL or opens it in your own app; it only reads, so Codex's files never change. Both
pages appear under Settings, From extensions, once their add-on is installed and on.

### Updates

The Updates page shows this app's version and pluxy's, the newest on the chosen channel (**Stable** or
**Preview**), and the app release's notes. It reads nothing until you set an **Update source**, the
`https://` address of a release feed, which asks in a confirmation window. **Check for updates** reads
the feed. **Update now** appears only for a build with an update key; no build has one yet.

## What the app does not do

A few things are outside what the app does today, or work only in part:

- **Other coding tools.** Only the eight tools listed under [The coding-tool pages](#the-coding-tool-pages)
  have setup pages and add-ons (not Kimi Code, OpenClaw, ZCode, DeepSeek Harness, WorkBuddy,
  Antigravity CLI, Copilot CLI or VSCode Copilot). Point any other tool at pluxy by hand
  ([clients.md](clients.md)).
- **Other usage parsers.** **Agent usage** reads only the five tools listed under
  [Agent usage and Codex sessions](#agent-usage-and-codex-sessions) (not Copilot, Gemini CLI,
  Antigravity, Kimi, ZCode or MCode).
- **WSL setup.** Only the Claude Code add-on runs inside WSL distributions on Windows (and Windows
  itself is not supported yet); the other seven tool add-ons have no WSL setup.
- **Codex OAuth sign-in and catalog options.** Codex connects to pluxy with a client key only (no
  OAuth sign-in or OpenAI provider-identity switch). The Codex model list changes only when you
  press **Import models from pluxy**, and its editor omits `supports_parallel_tool_calls`, which
  Codex does not read.
- **Editing or deleting Codex sessions.** **Codex sessions** is read-only: it lists, views, exports
  as JSONL and opens sessions, and does not sync historical sessions, repair index entries, edit
  messages or delete session files.
- **Hiding or filtering the tool list.** All eight coding tools appear on **Tools**; there is no
  separate screen to hide or search that list.
- **Prices and deeper usage analysis.** Home shows totals and the chart, Activity's **Requests** tab
  lists requests and exports a spreadsheet, and Activity's **Usage** tab totals requests and tokens
  over 24 hours, 7 days or 30 days by provider, model, account or key. Neither **Activity** nor
  **Agent usage** estimates prices or costs, caps the usage record by disk size (retention is by
  days via `usage.keep_days`), or runs deeper usage analysis.
- **Model health sweep.** There is no one-click check across every model and account. Use **Send a
  test request** on an account's page to test one model at a time (each test spends one request).
- **Thinking and Fast alias form.** There is no form for thinking or fast aliases. Use a thinking
  suffix such as `gpt-5(high)`, a payload rule, or an account's model alias in **Config**
  (**Settings file** tab; see [routing.md](routing.md)).
- **Built-in reverse proxy or Allow-LAN switch.** The app runs no reverse proxy of its own and has
  no single Allow-LAN toggle. To let other computers reach pluxy, set **Address pluxy listens on**
  (for example `0.0.0.0:8317`) in **Config** (**Settings** tab, **Other computers**) and restart
  pluxy; to let them also manage it, turn on **Let other computers manage pluxy** there, which takes
  effect at once. Configure TLS directly in `config.json` ([config.md](config.md#tls)).
- **Following the system proxy.** pluxy does not read the system or environment proxy for provider
  requests. Set **Outbound proxy** in **Config** (**Settings** tab), `proxy_url` in `config.json`,
  or the proxy field on an account's Edit form.
- **Built-in update mirrors or core version downloads.** There is no built-in release feed or mirror
  list. Use the core the build came with, a local **Release list**, or **Your own pluxy program** on
  **Where pluxy runs**.
- **MCP servers and agent skills.** The app does not manage MCP servers or agent skills; add-ons are
  only pages and coding-tool programs.
- **Browser picker, open-folder buttons, languages, custom window chrome.** Sign-in links can be
  copied and opened in any browser you choose. There are no buttons to open pluxy's folders in a
  file manager, no custom Windows title bar or window materials, and the app is in English only.

## Safe mode

Start the app with `--safe-mode` (for example `next/app/local-build/start --safe-mode`) to pause every
add-on: no add-on page opens and no coding-tool add-on runs. The Add-ons page still works, so you can
turn off or remove the add-on that causes trouble.

## Files the app keeps

On Linux the app's settings are in `~/.config/dev.pluxy.desktop/` and its data in
`~/.local/share/dev.pluxy.desktop/` (`dev.pluxy.desktop.local` for the local build, under its `home/`
unless it runs with `--real-home`). Files are readable only by you.

| File or folder | Folder | What it holds |
| --- | --- | --- |
| `connection.json` | settings | Where pluxy runs, and the management key if you kept it |
| `window.json` | settings | The close choice and the window's size and place |
| `startup.json` | settings | The startup rows |
| `privacy-mode.json` | settings | Hide names |
| `release.json` | settings | The update source and channel |
| `launcher.json`, `remembered-launches.json` | settings | Launches and their remembered approvals |
| `access.json` | settings | Publishers you approved and files you gave page add-ons |
| `device-registries.json` | settings | The sources Add-ons installs from |
| `core/` | data | The config, credentials and extensions of the app's own pluxy |
| `cores/` | data | The versions of pluxy the app installed |
| `device-packages/` | data | Installed add-ons |
| `instance/` | data | What lets a second launch find the first |

A settings file the app cannot read is moved aside (such as `startup.damaged-<random>.json` or
`launcher.json.corrupt-<time>`) and the defaults are used.

## Troubleshooting

| What you see | What to do |
| --- | --- |
| **No pluxy is installed** | A plain build has no pluxy inside. Press **Use this program** if the app found one, name your own under **Your own pluxy program**, or use the local build |
| **pluxy kept stopping, so the app stopped restarting it** | Read why on the pane and in pluxy's log (Settings, Logs, or the files in `core/` in the app's data folder), fix the config, then press **Start pluxy** |
| **Not signed in to pluxy on this computer** (or **at <host>**) | Press **Enter management key**. Print the key with `pluxy key management --show-key` on the computer pluxy runs on |
| "That pluxy refused the key. Check it and try again." | The key is wrong for that pluxy. Print it again with `pluxy key management --show-key` there and enter it again |
| "This pluxy answers only the computer it runs on." | That pluxy's remote management is off. On its computer, turn on **Let other computers manage pluxy** in Config (Settings tab, Other computers), or set `"management_allow_remote": true` in its `config.json` (`pluxy help` there says where the file is) and save; pluxy picks it up without a restart, so then sign in again |
| **Cannot connect to pluxy at <host>** (or **on this computer**) | Check pluxy runs there, the address and port, and for `https://` the certificate (give the **Certificate authority file** that signed it; if a self-signed certificate cannot be checked, use its Tailscale address or plain `http://` on a network you trust) |
| **pluxy at <host> is not answering** (or **on this computer**) | Nothing answered as pluxy at that address while the app checked its key; the line under the heading says why (no answer in time, a name not found, a certificate that could not be checked). Fix that, then press **Enter management key** |
| A tool page says its add-on is missing or off | Press **Install add-on** or **Turn on add-on** on the tool's row on Tools (or install it under **Available to install** on Add-ons), then confirm turning it on |
| Add-ons show **Unverified** after `make app-local` | Expected after a rebuild: **Remove** each, **Install** it again and turn it on |
| A tool page refuses to write: the file changed | The tool or you edited it since the page read it. Press **Read this file** (or **Try again**) and write again |
| `Cannot change settings` on a tool page | The file is one the tool itself would not read (for example not strict JSON for Claude Code). Fix it by hand, or **Restore backup** |
| **Start pluxy** and the tray items are greyed out | They control only the app's own pluxy; with a pluxy you connected to, start and stop it where it runs |
| Closing the window quits the app | The desktop has no usable tray (no StatusNotifier host). Install one, such as an AppIndicator extension, or keep the window open |
| The app does not open at sign-in | The local build never does. Another build needs **Open at sign-in** on |
| The window opens on a blank or broken page after an add-on change | Start the app with `--safe-mode`, then turn off or remove the add-on |
| A second launch prints an error and exits after 20 seconds | The first copy did not answer. Quit it from the tray, or end its process, and start again |

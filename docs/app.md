# The desktop app

The desktop app is pluxy's web panel in a window of its own, plus what only a program on your
computer can do: run pluxy for you, set up the coding tools installed on this computer, start them,
and keep a management key out of any web page. It shows the same pages as the panel
([panel.md](panel.md)), in the same places, and adds pages of its own.

The app either runs pluxy itself, on this computer, or connects to a pluxy that is already running,
here or on another computer, with its address and management key (the key that unlocks pluxy's
management API). How the app works inside is in [app-design.md](app-design.md).

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

See [setup.md](setup.md).

## Where pluxy runs

The app asks once, **Where should pluxy run?**, and you can change the answer later on **Where pluxy
runs** (Settings, in the **This device** group).

| Choice | What the app does | When the app quits |
| --- | --- | --- |
| **On this computer** | Runs its own pluxy, with a config of its own on a free port of this computer. Nothing to type | Stops that pluxy |
| **Connect to a pluxy that is already running**, at a local address (`127.0.0.1`, `::1`, `localhost`) | Connects to a pluxy someone else runs on this computer, such as a service. `http://` is allowed | Leaves it running |
| **Connect to a pluxy that is already running**, anywhere else | Connects over `https://` only. For a certificate you made yourself, open **It uses a certificate I made myself** and choose its **Certificate file** | Leaves it running |

To connect, type the **Address** (scheme, host and port only), then **Continue** (or **Connect**). A
small window, **pluxy management key**, asks for the key. On the computer pluxy runs on,
`pluxy key management --show-key` prints it. Tick **Keep the key on this device** to have the app
sign in by itself next time; otherwise it forgets the key when it quits. The key is checked before
the app switches, and a wrong key or **Cancel** keeps the connection you had.

The key never reaches the app's web pages: you type it into that window, and the app adds it to each
call. To change the key, use **Rotate** on the Config Panel's Connectivity tab: the app confirms in a
window of its own, keeps the new key the way the old one was kept, and shows it once with Copy, for
other programs that used the old key.

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
| **Beginner Mode** | Tools | Sign in, then set up a coding tool, in two steps |
| **Manage Clients** | Tools | Which coding-tool pages Tools lists |
| Claude Code, Codex, Claude Desktop, OpenCode, Oh My Pi, Grok Build, Hermes Agent, Pi | Tools | Set up and start each coding tool |
| **Agent usage**, **Codex sessions** | Settings, From extensions | Pages of add-ons, when installed and on |

Setup (Settings, in the **pluxy** group) is the panel's, with one addition: in step 2, a coding tool
the app has a page for gets a **Set up** link to that page, when pluxy runs on this computer.

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

A coding-tool add-on installs turned off. **Turn on** opens a confirmation window that says it gets
full access to your user account and shows who published it; only Confirm there turns it on.
Updating, going back to an earlier version or turning it off ends that consent, so the next version
asks again. A coding-tool add-on runs only while something uses it and stops 30 seconds after.

For a page add-on, **Allow tool access…** lets a Verified one use coding-tool add-ons, and **Approve
publisher…** then **Choose a file…** (or **Choose a folder…**) lets it read a file or folder you pick
(Linux only). Each asks in a confirmation window. A page add-on can send what it reads anywhere its
pages can reach, so give it only what you would share.

**Go back to <version>** returns to the version an update replaced. **Remove** deletes the add-on
and anything you allowed it.

### The coding-tool pages

Each coding tool has a page under Tools, which needs that tool's add-on. Tools lists a tool's page
once its add-on is installed; **Manage Clients** lets you list any of them, or fewer.

A page shows how the tool stands (`Connected`, `Installed · not connected`, `Settings need update`,
`Cannot change settings`, `Not installed`, `Not checked`) and the one thing to press next:

| Button | What it does |
| --- | --- |
| **Turn on add-on** | Turns the tool's add-on on, in its confirmation window |
| **Connect {tool}** | Makes a key for the tool in pluxy (named after the tool, so Activity shows which tool sent each request) and writes pluxy's address, the key and the models into the tool's settings, after a confirmation window naming the files |
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
| Claude Code | `pluxy/claude-code` | `~/.claude/settings.json` | `claude` in a terminal | Models for Opus, Sonnet, Haiku and Fable, subagents, auto-compaction |
| Codex | `pluxy/codex-setup` | `~/.codex/config.toml` and Codex's model list beside it | `codex` in a terminal; **Restart Codex service**; **Start Codex App** (a program you name) | **Import models from pluxy** fills the model list. Codex hides conversations of another provider from resume |
| Claude Desktop | `pluxy/claude-desktop` | Three files in `~/.config/Claude-3p` | `claude-desktop`, directly | Its third-party gateway (Linux beta). Takes only Claude model names: **Create alias in pluxy** adds an alias for another model. Quit and reopen it after a change |
| OpenCode | `pluxy/opencode` | `~/.config/opencode/opencode.jsonc` (or `opencode.json`) | `opencode` in a terminal; **Start OpenCode Desktop** | **Import models from pluxy** fills the model list |
| Oh My Pi | `pluxy/omp-setup` | `~/.omp/agent/models.yml` and `config.yml` | `omp` in a terminal | Sets the default model; Oh My Pi lists pluxy's other models itself |
| Grok Build | `pluxy/grok-setup` | `~/.grok/config.toml` | `grok` in a terminal | **Import models from pluxy** fills the model list |
| Hermes Agent | `pluxy/hermes-setup` | `~/.hermes/config.yaml` (or the active profile's) | `hermes` in a terminal | Sets the default model |
| Pi | `pluxy/pi-setup` | In `~/.pi/agent`: `cliproxyapi.json`, `settings.json` and the provider plugin's folder `pluxy-cliproxyapi-provider/`; it removes the plugin's model cache `cliproxyapi-models.json` | `pi` in a terminal | Connect is **Install plugin** until the plugin is in place; **Update plugin**, **Uninstall plugin** |

A tool started from another program keeps its old settings until you start it again: the app never
stops a program it did not start.

These eight are the coding tools the app sets up. For any other tool, point it at pluxy by hand
([clients.md](clients.md)).

### Beginner Mode

Beginner Mode (Tools) walks through two steps. **Connect a model** signs in to an AI account or adds
an API key, as Add an account does; **Next: connect an agent** turns on once pluxy has an account.
**Connect an agent** lists the coding tools this app can set up whose add-on is installed or offered,
with **Install**, **Enable** and **Set up** for each: Install installs the add-on (turned off),
Enable asks in its confirmation window, and Set up shows the tool's page below, where Connect writes
the tool's settings. Beginner Mode writes no tool's settings itself, and leaving it or cancelling a
confirmation changes nothing.

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
| **Cannot connect to pluxy at <host>** (or **on this computer**) | Check pluxy runs there, the address and port, and for `https://` the certificate (add its **Certificate file** if you made it yourself) |
| **pluxy at <host> is not answering** (or **on this computer**) | Nothing answered as pluxy at that address while the app checked its key; the line under the heading says why (no answer in time, a name not found, a certificate that could not be checked). Fix that, then press **Enter management key** |
| A tool page says its add-on is missing or off | Install it on Add-ons (in the local build it is under **Available to install**), then **Turn on add-on** |
| Add-ons show **Unverified** after `make app-local` | Expected after a rebuild: **Remove** each, **Install** it again and turn it on |
| A tool page refuses to write: the file changed | The tool or you edited it since the page read it. Press **Read this file** (or **Try again**) and write again |
| `Cannot change settings` on a tool page | The file is one the tool itself would not read (for example not strict JSON for Claude Code). Fix it by hand, or **Restore backup** |
| **Start pluxy** and the tray items are greyed out | They control only the app's own pluxy; with a pluxy you connected to, start and stop it where it runs |
| Closing the window quits the app | The desktop has no usable tray (no StatusNotifier host). Install one, such as an AppIndicator extension, or keep the window open |
| The app does not open at sign-in | The local build never does. Another build needs **Open at sign-in** on |
| The window opens on a blank or broken page after an add-on change | Start the app with `--safe-mode`, then turn off or remove the add-on |
| A second launch prints an error and exits after 20 seconds | The first copy did not answer. Quit it from the tray, or end its process, and start again |

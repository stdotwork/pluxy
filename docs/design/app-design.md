# How the desktop app works

This page explains the desktop app's insides for someone changing it: how it reaches core, how it
runs a core of its own, what the window may call, how extension pages and native tools are kept
apart from it, how it updates itself and which signatures it trusts. How to use the app is in
[app.md](../app.md); how to build and test it is in [`next/app/README.md`](../../next/app/README.md).
The rules app packages and native tools must follow are the app package contract,
[`docs/app-packages.md`](../app-packages.md).

The app is a Tauri 2 program. Its window shows the UI kit (`next/ui`), the same pages as the web
panel, built a second time with an app host (`next/app/src/host.ts`) in place of the browser's.
Everything that holds a secret, touches a file or starts a process is on the native side, in Rust
(`next/app/src-tauri/src`). The window asks the native side for things through Tauri commands; it
never holds a management key, a client key or a secret, and it never starts a program itself.

## The parts

| Part | Where | What it may do |
| --- | --- | --- |
| The window | One webview, labelled `main` | Calls the app's commands. The only webview any capability names |
| Extension page webviews | `page-<n>`, one per open page | Nothing native: no capability, no command. Talks to the window through a checked channel |
| Release notes webviews | `notes-<n>`, at most two | Nothing: no script, no capability |
| Native tools | Separate programs on private pipes | Only the operations the router lets through (below) |
| The app's own core | A pluxy program from the app's store | Serves the management API the window uses, in managed mode |

## How the app reaches core

The connection is one of three modes, kept in `connection.json` in the app's config folder:

| Mode | Core | Address rule |
| --- | --- | --- |
| `managed` | The app's own core, from its store (below) | A loopback address the app picks once (`pluxy init -port 0`) |
| `local` | A core someone else runs on this computer | A loopback origin (`127.0.0.0/8`, `::1`, `localhost` or `localhost.`), `http://` or `https://` |
| `remote` | A core on another computer | `https://` (with an optional PEM certificate authority file), plain `http://` carried by Tailscale, or plain `http://` on a network the person trusted in the key window (`trust_network` in `connection.json`, kept for that address only; the window cannot set it) |

Addresses are origins only (scheme, host and port). For plain `http://` to another computer,
`tailnet.rs` counts the connection as over Tailscale only when this computer has an up interface in
`100.64.0.0/10` or `fd7a:115c:a1e0::/48` and the resolved address is in those ranges and is not one
of this computer's own addresses (`100.64.0.0/10` is also ISP shared address space, so the address
shape alone is not enough); that check runs again before each new connection and sends nothing if it
stops holding. Plain `http://` never goes through a proxy from the environment (`proxied` in
`client.rs`).

The native side holds the management key. Key entry is a native dialog (GTK on Linux, Win32 on
Windows, an AppKit sheet on macOS) with a hidden field and "Keep the key on this device"; where it
asks about trusting a plain-HTTP network, OK reads **Trust this network and connect** and is not the
default button (`key_entry.rs`). The key goes from that dialog to the native connection state and no
webview receives it. Before the dialog opens, native code checks the mode, the origin and the
certificate authority file, resolves the address once (`KeyTarget::route` in `conn.rs`; through an
environment proxy on `https://`, the proxy's address is resolved instead), and sends both an
unauthenticated pre-check (`GET /v0/management/status` with no key, so a wrong address, bad
certificate or `remote_disabled` is reported before a key is typed) and the key check to that
resolved address. A key is checked against core before the app switches to it, and refused if the
connection changed during the check. If the key check fails for a reason other than a refused key,
the app holds the typed key in memory (zeroized on drop) for 10 minutes (`HOLD_FOR` in `conn.rs`)
and reuses it on the next try at the same address and certificate authority file; a refused key is
not held, and changing address or certificate authority, signing out, or closing to the tray drops
it. A kept key is stored in `connection.json`, not in the system keyring; an unkept one lasts until
the app quits.

The window calls core with one command, `core_call`. The native side adds the key, sends the call
to `<core>/v0/management/...` only, follows no redirects, uses no proxy from the environment for a
core on this computer or over plain `http://`, refuses path segments that are or decode to `.` or
`..`, and refuses every path under `/management-key`, however it is spelled.

Replacing the key (**Replace key** in the Keys section of the Config page) is a command of its own,
`management_key_rotate`. The page asks nothing itself: the app's host tells the kit it confirms
natively (`confirmsKeyReplacement` in `next/app/src/host.ts`), and a native confirmation names the
core and how the new key will be kept, with Replace key and Cancel. Then the native side calls `POST /v0/management/management-key/rotate`, keeps the new key as the old
one was kept (in `connection.json`, for the session only, or, for the app's own core, in that
core's config, which core writes) and shows it once in a native window with Copy and Close. The
window learns only how it went. A call that left with the old key during a rotation is sent once
more with the new one. The app waits at most 30 seconds for core's reply.

Settings files are written through a unique exclusive temporary file at mode 0600, then file sync,
rename and directory sync on Unix. At startup the app removes a leftover temporary file of that
name and nothing else.

Until the app is connected and signed in, the window shows none of core's pages: the first launch
screen when no place is chosen, otherwise the frame with its destinations off and one pane that
says what happened (the case is chosen from the connection alone, `paneOf` in
`next/app/src/connectionCopy.ts`).

## The app's own core

In managed mode the app runs only core versions it installed itself, each in its own folder under
`cores/versions/` in the app's data folder (`next/app/src-tauri/src/cores.rs`). It never runs a
pluxy program in place.

**The core's folder.** The app's own core keeps its config, credentials and packages in `core/` in
the app's data folder. On Unix the app makes that folder 0700 whatever the umask, and tightens one of
the user's that group or others may use, since core's first `init` refuses a config folder that is a
link, another user's, or group- or other-writable (`core_folder` in `managed.rs`).

**Where versions come from.** Three ways, all ending in a version folder of the store:

- A release from a **release list**: the `core_source` setting, the full path of a local index file
  in the release feed's format ([below](#release-feed-format)). The app reads only releases with
  `product` `core`; each artifact carries `platform` (`<os>/<arch>` as Go names them), `url` (a file
  name beside the index, for now), `descriptor` (the exact signed descriptor bytes as a JSON
  string: `id` `pluxy/core`, `tier` `native`, the version and platform), `signature` (detached
  Ed25519 over those bytes) and `key_id`. There is no built-in source.
- The **core the app comes with**: a packaged app carries a signed core release of the version
  `next/app/core-release.json` names. At packaging, `bundle-core`
  (`next/app/src-tauri/examples/bundle-core.rs`) checks that release against its signed descriptor
  and writes a manifest of each file's path, size, SHA-256 and execute bit; `build.rs` compiles the
  manifest into the app's program (`PLUXY_BUNDLED_CORE` names it). At each managed start the app
  copies the listed files, and only those, into its store as a version named after the core
  version, with no confirmation: installing the app was the consent, and the manifest is part of
  the app's own program (`next/app/src-tauri/src/bundled_core.rs`). A dev build has no manifest
  and so no bundled core.
- An **adopted program**: a pluxy program the person names. After a native confirmation showing
  its path, size and the first 12 characters of its SHA-256, the app copies the program alone (not
  `bundled/` beside it) into a version named `custom-<SHA-256>`, if it is still a regular,
  executable file of that size and digest, at most 512 MiB. Release versions may not start with
  `custom-`. An adopted program has no signature.

**Install checks.** A release archive is a gzip tar of the core's folder (`pluxy` at the top, and
anything beside it such as `bundled/`): regular files and folders only, no links, devices or names
that leave the folder, at most 10,000 entries and 512 MiB unpacked. The app copies the archive
into its own folder, checks the signature over the exact descriptor bytes with a publisher key
trusted for that exact source (publisher `pluxy`), then the size and SHA-256 (the shared verifier,
`artifact.rs`, and the app's trust, `trust.rs`). Only then does the native confirmation open. It
unpacks into a new version folder, each entry made from the folder held open before it without
following links, records each file's SHA-256 in `cores/records/`, and makes the files read-only.

**Before every start** it opens the version folder and every file under it without following
links, checks each is a regular file of the recorded size, still read-only, with the recorded
digest, and starts the core from the file it just checked (on Linux the open file itself; on
macOS and Windows the path, after checking it still names that file; `next/app/src-tauri/src/owned/`).
A changed version does not start until it is installed again.

**Switching** (an install, a rollback, an adoption) stops the running core, keeps a copy of
`core/config.json`, records the new version as the candidate and starts it. It counts as started
once the core says it listens on its `listen` address, every socket listening on that port is the
started process's (`owns_listener` in `managed.rs`: `/proc` on Linux, the system's TCP table on
Windows, a check of its own on macOS), it answers `GET /v0/management/status` with the app's key and, for a release, reports the version it was
signed as, and the selection is saved. Otherwise the app puts the config back, removes a version
it installed for this switch and runs the one that worked before. If the config cannot be put
back, no core runs and the next start tries again. The store keeps the running version, the one
before it and the bundled one, and removes older ones after a switch.

**Which version runs.** `state.json` records who chose the selected version, the app or the person,
and the app version that last applied its bundled core. On a fresh profile, and at the first start
after the app's version changes, a selection the app made moves to the bundled core (with the
switch above, so a bundled core that fails is not tried again until the app version changes). A
version the person chose stays.

**The listening socket.** On Linux and macOS the app binds the `listen` address itself and hands
the socket to the core (`-listen-fd`), and keeps its own copy while anything in the app could
still send there, so no other program can take the address while a crashed core's exit is noticed
or while the core drains after a stop. On Windows the core binds it, and before the app sends
anything on a new connection to its core it checks in the system's TCP table that the other end
belongs to the core's process.

**Stopping.** The app only ever stops the core process it started: SIGTERM, then a kill after 10
seconds. On Linux the core also gets SIGTERM if the app dies (a parent-death signal, which follows
the thread that started it, so a supervisor thread starts the core and stays until it exits). A
core the app did not start is never signalled.

**Recovery.** An owned core that exits unexpectedly is restarted after 2, 5 and 15 seconds, at most
three attempts in a rolling five-minute window; a successful recovery does not reset the budget.
An explicit Start, Stop or Restart, or a mode change, cancels a scheduled recovery and clears the
budget. `core.stop_reason` in the connection (`stopped_by_user` or `not_started`) tells a stopped
core from one the app was set not to start. Recovery history lasts for the app process
(`next/app/src-tauri/src/recovery.rs`).

Each core change works once: while one waits for its confirmation, another is refused. The window
hears `pluxy-connection` when it has ended, and `core_versions` says whether it was done, cancelled
or failed.

**What the checks guard against:** a damaged or substituted download, and other users of this
computer. They do not guard against the person's own user account, which can rewrite the store as
it can replace the app itself.

## Commands

Every app command is declared in the command manifest (`next/app/src-tauri/build.rs`, from the list
in `next/app/src-tauri/src/command_names.rs`), so Tauri checks every call against the capabilities,
also from the app's own pages. The grant (`next/app/src-tauri/capabilities/main.json`) names the
`main` webview, not the window, since a window grant would cover the extension page webviews inside
it, and each command also refuses any caller but `main`.

To add a command: add it to `command_names.rs`, to `generate_handler!` in `lib.rs`, and its
`allow-<name>` permission to `capabilities/main.json`. Unit tests check that the three agree.

No command takes an approval, a gesture or a token from the window. A command that changes
something the person must agree to (installing a core, enabling a native tool, changing the
release feed, granting a file) opens a native confirmation and applies nothing itself.

| Group | Commands |
| --- | --- |
| Connection | `connection_get` (without the key), `connection_set` (no key), `key_entry` (native entry; returns `signed_in`, `wrong_key`, `cancelled` or `error`), `certificate_choose` (native file chooser on Linux for a remote core's CA file; read only on connect), `sign_out`, `management_key_rotate` |
| The app's own core | `core_start`, `core_stop`, `core_restart` (managed mode only), `core_versions`, `core_install`, `core_rollback`, `core_adopt` |
| Core's API | `core_call` |
| Startup, window, privacy | `startup_get`, `startup_set`, `window_get`, `window_set`, `window_close`, `privacy_get`, `privacy_set` |
| Extension pages | `page_open`, `page_place`, `page_close`, `page_send`, `device_page_open` |
| Updates and release notes | `release_get`, `release_check`, `release_channel_set`, `release_source_set`, `release_notes_open`, `release_notes_place`, `release_notes_close`, `release_notes_full`, `app_update_get`, `app_update_start`, `app_update_cancel`, `app_update_rollback` |
| Packages on this device | `device_list`, `device_registries`, `device_install`, `device_install_wsl`, `device_install_file`, `device_enable`, `device_remove`, `device_rollback`, `device_imports` |
| Access | `access_list`, `access_signer`, `access_grant`, `access_revoke` |
| Native tools | `program_call`, `program_cancel`, `program_status` |
| Vault and keys | `vault_secret_add`, `vault_secrets`, `vault_secret_remove`, `client_key_capture`, `client_key_enter`, `core_alias_create` |
| Launcher and WSL | `launcher_state`, `launcher_terminals`, `launcher_launch`, `launcher_start`, `launcher_forget`, `launcher_settings`, `environments_get`, `environment_add`, `environment_remove` |
| Confirmations | `confirmation_request` (review of an action native code already prepared, by its opaque ID), `confirmation_settings` (remembered launches) |
| Files | `file_save` (a file one of the window's pages made, into the Downloads folder, never over another file) |

## Extension pages

Each extension page the window shows gets a webview of its own, `page-<n>`, placed over the page
area. No capability names it, so it can call no command and no Tauri plugin. It has its own web
context, apart from the window's storage, in the app's cache folder and cleared at each start.

The webview loads one app document from the `pluxy-page:` scheme (`next/app/page/`), which shows
the extension's page in a frame with `sandbox="allow-scripts"` from core's pages site, as the
panel does, and does the panel's side of the page's hello
([extension pages](../extension-pages.md)). Its `/send` address is the webview's one
way out: the native side tags each message with the webview it came from and passes it to the
window, which answers with the kit's channel. No page receives a key.

Native code checks every message before forwarding it:

| Limit | Value |
| --- | --- |
| Transport body | 256 KiB (an oversized body is refused without scanning for an ID) |
| Channel call | 64 KiB, exact version-0 call schema, known methods only |
| Per document | one hello, 30 admitted calls per rolling second, 8 awaiting answers |
| Per extension | 4 pages, 60 admitted calls per rolling second, 16 in flight |
| Replies | 2 MiB each |
| Kept | at most 16 page webviews and 64 recent package budgets |

Requests and replies carry the document's native generation, so a second document cannot
reconnect. These bounds apply once a request reaches the scheme handler; memory the browser engine
uses before that is not bounded.

On Linux, Tauri stacks a window's webviews in a box and ignores where they should be, so the app
moves the window's webview into a GTK overlay and places each page webview over it
(`next/app/src-tauri/src/place.rs`), answering the overlay's `get-child-position` signal with the
page area. On Windows the page document's address is `http://pluxy-page.localhost/`, and the app
holds a page's frame to its site through WebView2's frame navigation event
(`next/app/src-tauri/src/page_frames.rs`).

## Packages on this device

The Add-ons page manages app packages ([the app package contract](../app-packages.md))
installed on this computer, apart from core's extensions: `app.tier` `web` (pages only) and
`native` (a native tool, a program the app runs on demand). Code: `device.rs` (catalog and store),
`program.rs` (the program host), `route.rs` (the router), `jobs.rs`, `vault.rs`.

**Sources.** A release archive (the [protocol's format](../protocol.md#release-archives))
comes from a file the person picks in the native file chooser (the window never names a path), or
from a registry listed in `device-registries.json` in the app's config folder:
`{"version": 0, "registries": [{"name": "...", "url": "..."}]}`, each `url` a registry index (the
[protocol's format](../protocol.md#registry-index)) at an absolute local path or an
`https://` address. The archive must have the size and SHA-256 the index lists for this platform.
An install that names no version takes the highest stable release the index lists, or the highest
prerelease when it lists no stable one. An index entry may carry the release's publisher signature
(`key_id`, `public_key`, `descriptor`, `signature`: all four or none), checked before the archive
is unpacked.

**The store.** The archive is checked (regular files and folders only, at most 10,000 entries and
512 MiB unpacked, a valid manifest version 1 with an app contribution for host contract 0), unpacked
into staging and moved into `device-packages/store/<archive SHA-256>/` with read-only files. The
catalog, `device-packages/catalog.json`, records each package's source and each release's version,
digest, sizes, formats, pages and per-file SHA-256. A package keeps the source it was installed
from. Installing a newer release keeps the one it replaces as the last good release, for Roll back.

**Curated, unverified and reserved.** A registry is curated when its index address equals the
`source` of a native-tier trust root exactly. A package from a curated registry whose entry carries
a descriptor signed by the root's key, naming that key as its `public_key`, is curated (the Add-ons
page says **Verified**). Any other package is **Unverified**. An ID that claims a reserved
publisher (`pluxy`, or any publisher a root names) from another source is refused. A native tool
installs only as curated. Release builds compile no roots (`next/app/src-tauri/trust/production.json`
is `{"roots":[],"updater":null}`), so outside a build with test trust nothing is curated and no
native tool can be installed.

**Admission.** A native tool installs disabled. Enabling it opens a native confirmation saying it
has full access to the person's user account, with its ID, source, digest, signer, contracts,
operations and program; the catalog records the admission against the release, source, signer and
key. Disabling, updating, rolling back or removing it ends the admission. A native tool is enabled
only when its required imports are enabled tools that export them, with no cycle.

**The program host** runs an admitted tool only on demand, at its first call, after checking the
admission, the root, the signature and every installed file again. It runs the opened file in its
package folder with a cleared environment, only its three private pipes, a process group of its
own (a job object on Windows) and on Linux a parent-death signal. Calls follow the
[app program contract v0](../app-packages.md#app-program-contract-v0), one at a time
per tool. A tool stops 30 seconds after its last call; any change to its package, and quitting the
app, stop it at once (stdin closed, its process group killed after five seconds, reaped). A tool
that crashes restarts only on a later call, after a backoff of one second doubling to a minute;
five crashes in a row quarantine it until it is disabled and enabled again, or updated. A call the
tool took and never answered has an unknown outcome: it is never sent again, and the tool's next
start is told of it.

**Swapping a running tool.** Updating or rolling back an enabled tool stages the new release and
shows its native confirmation; once confirmed, the host starts it and has it describe itself, and
only then commits it. New calls go to the new release while the old one finishes its calls (at most
ten minutes) and stops. A release that fails to start or describe itself changes nothing.

**The router** (`route.rs`) checks every call of a native tool's operation against the catalog at
that moment, whether it comes from the window, a page (its `native.call` method) or another tool
(a call on its pipe): a tool calls only what it imports and its target exports; a native tool's page
calls only its own tool; a curated web package's page calls only its imports, once the person
allowed them in a native confirmation. An unverified package never gets that. The host sets each
call's chain (the requester, then each tool that called on), refuses a call back into the chain or
a chain longer than five, and asks a native confirmation naming the whole chain for each `write` or
`launch` call. [Calls between packages](../app-packages.md#calls-between-packages) has
the manifest fields and frames, and [Jobs](../app-packages.md#jobs) the data a tool
produces and consumes.

**The vault** keeps secrets the person types into native key entry, client keys, and tools'
backups and journals, in a file only the user may read in the app's data folder (not the system
keyring). The window names a secret only by its ID; a confirmation names each secret and the tool
that gets it; a tool receives the secret on its pipe, or a reference it may pass on only in that
call. Secret values and references are redacted from tools' logs, status, errors and outputs. A
write a tool or the app did not finish is reconciled at its next start, never written again.
[Secrets, references and the vault](../app-packages.md#secrets-references-and-the-vault)
has the details.

**Device pages** open like core's pages, in a page webview, but their frame loads from the
`pluxy-app:` scheme, which serves a page webview only the files of the release it was opened for,
while that release is active and enabled and each file still has its recorded SHA-256, each served
sandboxed as core's pages site does. On Windows WebView2 serves the scheme as
`http://pluxy-app.localhost`. Installing, rolling back, disabling or removing a package closes its
open page webviews at once.

**Safe mode** (`--safe-mode`) lists, opens and serves no device page and admits no native tool;
the Add-ons page still manages them.

## Access

Access lets a page-only package read files and folders the person chooses for it, through a
broker in the app (`access.rs`, `access_broker.rs`). A package declares `app.resources`, each a
`file` or a `subtree` with the operations it wants; the app grants `read` of a file and `list` and
`read` in a folder, nothing else. A page core lists gets no Access.

A grant needs a web release whose registry listed a publisher signature, an approved signer
(publisher key from that registry; the same key from another registry starts with nothing) and the
person's pick in the native file chooser, each confirmed natively. The root folder, the home folder,
the document portal's store and the app's own folders (and anything holding or inside them),
recognised by device and inode, are refused. A grant binds the package, source, release digest,
signer and resource to the target's path, device, inode and mount ID (and a link's own identity and
text, when a link was chosen), and every read checks all of it again: a retargeted link or a
replaced file answers `changed` until granted again. Reads go through three channel methods
answered by native code, never the window: `resources.status`, `resources.read` (regular files, at
most 1 MiB per call) and `resources.list` (stops at 1,000 entries); a folder grant reads through a
folder handle (`cap-std`) that refuses `..`, absolute paths and links that leave it, and nothing on
another mount. Revoking bumps a revision every read checks; a read under way answers `revoked`.
A file from the document portal is granted for that run of the app only. Grants and signers are in
`access.json`. Making signers and grants works on Linux only (5.8 or later, for the `statx` mount
ID).

This cooperates with the person; it is not a sandbox: another program of the same user can change
files between a check and a read.

## Native confirmations

Native code prepares each action as an immutable record: its requester chain, the executing package
with its full source, digest, signer, contract and generation, the files it names (chosen and
resolved), and, for a launch, the executable, argv, folder and environment
(`next/app/src-tauri/src/confirmation.rs`). The window may only ask to review a preparation by its
opaque ID. The dialog (GTK on Linux, Win32 on Windows, AppKit on macOS), titled "pluxy needs your
OK", says first in plain words what will happen, written by native code from verified records (the
add-on's manifest title, what the operation's effect does such as "will make changes on this
computer", the launch program, secret vault labels, who asked when it was not the window, and for a
native tool that it runs as you with full access to your user account). Manifest text (the add-on's
summary, and an operation's description quoted as "The add-on says: …") comes last, after all of
the app's own words, so a long one can only push its own end out of view. The exact action sits
below under **Details**; the plain words bind nothing — what is confirmed and remembered is the
exact action. Only the dialog's native response handler issues an approval: a one-use token that
expires 60 seconds after confirmation (native code spends it at once) and fails on a changed file
identity or executable metadata. A pending review lapses if its dialog has not shown within 60
seconds; once shown, it stays open for 10 minutes (`SHOWN_LIFETIME`) and then closes as cancelled
(answering checks the files and program again; quitting or closing the window withdraws it). Page
webviews have no confirmation capability.

Each preparation records its entry point: Main, Tray, NativeUi, Page or Import. Only the window's
own direct call, started by a native gesture (which expires after five seconds), may use a
remembered approval, and only those dialogs offer **Remember this exact executable, argv and cwd**.
A page's start always opens a fresh dialog, with one pending confirmation per package, one attempt
per second, Cancel focused and Confirm disabled for one second, and shows a native notice ("pluxy
notice"; Close button on Linux, close box on Windows and macOS) with requester, plain words, exact
action under "Details", and outcome including refusals. Every answer to a page's or tool's request
gets that notice; window, tray and native-UI requests do not (they show their own outcome), unless
remembering failed. Pending page and import entries are limited to 16; trusted requests to 64.

An executable is opened before its canonical path, device and inode, size, modification and change
times and mode are checked; its SHA-256 is computed once for that metadata and cached. A changed
file or same-path replacement needs review again. A consumed token hands the checked open
executable and folder to the launcher, for `fexecve` and `fchdir`.

Remembered launches are in `remembered-launches.json` in the app's config folder, with a format
version. A damaged, unversioned or unsupported file is moved aside as
`remembered-launches.json.corrupt-<time>`; links, files of another user and group- or
world-writable files are refused, and remembering stays off if an unsafe file cannot be kept aside.
Revoking an approval also invalidates its outstanding tokens. Host code must invalidate pending
preparations when a package, contract, environment or secret policy record changes.

Non-UTF-8 paths are refused before review; format, control and line-separator characters are shown
as escaped Unicode.

## The launcher

The Launcher page and the tray's **Start…** menu start coding tools and terminals through the
curated execution tool `pluxy/execution` (`launcher.rs`, `src/launcherPage.tsx`). The tool prepares
the executable, argv and folder; the app's confirmation shows them in full with the terminal's
program, the lifetime and the environment. A launch is detached (keeps running after the app quits,
in a transient systemd scope of its own when a systemd user manager runs) or managed (stopped when
the app quits); a launch in a terminal is always detached, since many terminals hand their window to
a server process and exit at once. A remembered launch starts again in one click only while it is
exactly what was remembered: the same executable (path, identity and contents), argv, folder,
environment, secrets and execution tool release. The list, recent folders and notice setting are in
`launcher.json`, which holds no secret (a secret field holds a vault secret's ID).

On Windows a launch can also start in WSL, prepared inside the distribution by `pluxy/wsl`
([Tools in WSL](../app-packages.md#tools-in-wsl-windows)).

## The coding-tool pages

The eight tool pages (Claude Code, Codex, Claude Desktop, OpenCode, Oh My Pi, Grok Build, Hermes
Agent, Pi) are the window's own pages, not extension pages, because Connect gives the tool a key:
only the window can name a vault secret for an operation, and the key's value never reaches the
page. They share one frame (`next/app/src/clientPage.tsx`), and each needs a curated native tool
that does the reading and writing ([`docs/app-tools.md`](../app-tools.md)).

The frame's contract:

- It reads this device's packages first, and reads no file until the page's add-on is installed
  and on.
- Every read pins its targets; every write sends the pins of the reading on screen, and the add-on
  refuses a file that changed since. A read that fails after a good one keeps the old reading on
  screen, marked possibly out of date, and nothing is written until a read succeeds.
- Each write's `program_call` carries a ticket; leaving the page withdraws a confirmation still open
  (`program_cancel`).
- Connect uses the vault's key bound to this add-on and to the connected core, or makes one with
  `client_key_capture` (named for the tool, in the vault and in core). `client_key_capture` asks no
  native confirmation of its own: the key leaves the vault only in the tool's write confirmation,
  which names it.
- For Claude Code (`~/.claude`) and Pi (`~/.pi/agent`), when the default folder does not exist yet,
  `preview` returns `create_folder` with null identities instead of refusing, and `apply` creates
  the folder in the same confirmed write (Pi refuses if `~/.pi` is a link or not a folder).
- `CallError::user_message` (`next/app/src-tauri/src/program.rs`) strips protocol error codes before
  add-on errors reach people, and every native reply goes through `nativeReply`
  (`next/app/src/toolWords.ts`), which turns the native side's words into the page's. In privacy
  mode `useShown` in the same file masks what the page shows: paths (the home folder's user name,
  and all but the last two parts), IDs such as a WSL user or an account ID, and e-mail addresses,
  URL credentials and paths in free text.

The Tools page (`#/tools`) and Setup's step 2 receive the same tool rows, buttons, notes and refresh
through `toolHooks` (`next/app/src/toolHooks.ts`): it adds Claude Desktop, Grok Build and Hermes
Agent (`readExtraTools` in `clientPage.tsx`) to the five tools core scans, and gives Tools
`keyOwners` (`next/app/src/keyOwners.ts`). Each tool page names the add-on it drives (`AgentPage` in
`next/app/src/addonItems.ts`), listed in `agentPages` in `next/app/src/agentPages.ts`;
`SETUP_CLIENT_PAGES` maps core's tool IDs to page IDs. `toolAction` picks each row's one button
from `items` (`addonItems.ts`), and `setUpAddon` (`next/app/src/toolRows.tsx`) runs install, turn-on
confirmation and opening the tool's page in one press. When opened from Setup, `SetupReturn` shows
the strip back to `#/setup`. Old addresses `#/easy-mode` and `#/clients` redirect to `#/setup` and
`#/tools` (`movedTo` in `agentPages.ts`).

## Updates

### Release feed format

The release feed is one JSON document the person points the app at (an `https://` address, kept in
`release.json`); there is no built-in feed. It is not signed: https is its only protection, and
nothing in it is trusted as markup. Signed update artifacts carry their own signatures.

```json
{
  "format": 1,
  "releases": [
    {
      "product": "app",
      "version": "2.1.0-preview.1",
      "channel": "preview",
      "published": "2026-11-05T09:00:00Z",
      "notes": "## What changed\n\nMarkdown text.",
      "notes_url": "https://example.invalid/releases/2.1.0-preview.1"
    }
  ]
}
```

| Field | Rule |
| --- | --- |
| `format` | `1`; a reader ignores fields it does not know, and any other change of meaning takes a new format number |
| `releases` | At most 256 |
| `product` | `app` or `core` |
| `version` | A semantic version without a leading `v`, at most 64 characters; one release per product and version precedence |
| `channel` | `stable` or `preview`; a stable release has no pre-release part |
| `published` | RFC 3339 in UTC, ending in `Z` |
| `notes` | Optional Markdown, at most 64 KiB |
| `notes_url` | Optional `https://` address with a host and no user name or password, at most 2048 bytes |
| `update` | Optional, app releases only: signed update artifacts by updater target name (1 to 64 characters), each `{"url", "signature", "size"}`. The app reads its own system's: `linux-x86_64-appimage` (an AppImage), `windows-x86_64-set` or `windows-aarch64-set` (the program set as a `.tar.gz`), `darwin-aarch64-app` or `darwin-x86_64-app` (a `.app.tar.gz`) |

A feed is read over https with the public certificate roots, no redirects, at most 1 MiB, within 30
seconds; a feed with any bad release is refused whole. The last good feed is kept in
`release-feed.json` in the app's cache folder. Only the latest check of the current feed counts. The
window gets only versions, channels, dates and whether a release has notes; never notes text or a
notes address.

Release notes render natively from Markdown (CommonMark with tables and strikethrough;
`next/app/src-tauri/src/notes.rs`) into a document served by the `pluxy-notes:` scheme to a notes
webview of its own. Raw HTML is left out, images show as their alternative text, only absolute
`https://` links stay links, and the document's policy allows no script, image, connection, frame or
form. Following a link opens it in the browser only when the notes link to that exact address, at
most one a second.

### Application update

The app checks and installs its own updates in `next/app/src-tauri/src/updater/`. It uses the
stock Tauri updater plugin for the release check and the signature check only, never for installing.
In order:

1. **The updater gate** (`trust::updater_gate`) must pass: it is the only place the updater gets its
   key. A build without the `artifact-test-trust` feature has only production updater trust, which
   is not configured, so it is refused. A test build passes only when its compiled identifier and
   product name are the test package's, the test trust profile in HOME loads, and the profile's
   updater key equals the compiled one.
2. **The installation must be one this updater can replace**, or nothing is downloaded: on Linux an
   AppImage of the user's that no one else can write to (`updater/appimage.rs`; a .deb or .rpm is
   refused with how to update it); on Windows the program set of a folder bundle or of a copy the
   per-user setup program installed, on a fixed local drive (`updater/exe.rs`, `updater/set.rs`; an
   .msi copy is refused); on macOS an `.app` bundle of the user's on an APFS volume, not translocated,
   in a folder of the user's or the stock `/Applications` when the user is an administrator
   (`updater/app.rs`, `updater/applications.rs`). Nothing runs an installer, asks for elevation or
   touches the quarantine attribute.
3. **The plugin restores the release from the feed**: only a version newer than the running one, and
   the signature must carry the announced version, so the feed can never offer a downgrade.
4. **The app downloads the artifact itself**: https only, no redirects, exactly the announced size;
   a server that sends nothing for 30 seconds fails it.
5. **The plugin checks the signature** against the compiled updater key, then the app checks the
   bytes are this system's and unpacks an archive into a private staging folder (folders and regular
   files only, at most 4096 entries and 1 GiB).
6. **The running copy is kept** in `updates/kept/` (the last three versions), pinned by its SHA-256
   in `updates/history.json`, and `updates/pending.json` records the install. If the installed copy
   changed since the app started, nothing is installed. Then the new version goes in place: on Linux
   a rename over the AppImage; on Windows part by part (`bundled\`, `pluxy.exe`, the program), each
   renamed aside first, undone at the next start if the app stopped part way; on macOS one atomic
   swap of the two bundles (`renameatx_np` with `RENAME_SWAP`). What an install leaves behind is
   recorded in `updates/leftovers.json` and removed through that record only.
7. **The app restarts into the new version**: it stops as Quit does (native tools, managed runtimes,
   then its own core), starts the new version as its successor, handing over the profile (below), and
   waits up to 90 seconds for it to accept itself by writing `updates/accepted.json`. If it ends first
   or does not accept in time, the app ends it, puts the kept copy back (checked against its digest)
   and starts the earlier version again.

A rollback (**Go back to <version>…**) restores only a kept copy that still has its pinned digest,
after the gate and a native confirmation, then follows step 7. One that crashes after accepting
itself is not put back, and if the old app is killed during the 90 seconds nothing puts the earlier
version back.

**Putting a kept copy back by hand.** When the app cannot put the kept copy back itself, it starts
nothing and says which copy in `updates/kept/` (in the app's data folder; on Windows the local
application data folder) to use. Quit the app and put that copy in place of the installed one: the
AppImage on Linux, the `.app` bundle on macOS, and on Windows the three parts of the program set
(the app's program, `pluxy.exe` and `bundled\`). On Windows, if the app stopped between renaming the
old program aside and the new one in, rename `.pluxy-old-<hex>-<program>` in the install folder back
to the program's name and start it: that start puts the other parts back from its record.

## One app per profile

A profile is the user plus the app's data folder; another `HOME` or `XDG_*` folder is another
profile (`next/app/src-tauri/src/instance.rs`). Before anything else the app takes an exclusive
`flock` on `instance/lock` in the data folder (a plain file of the user's, mode 600, opened without
following a link). It then listens on a socket named by a digest of the data folder, in
`pluxy-app/` in `XDG_RUNTIME_DIR` or else `pluxy-app-<uid>/` in the temporary folder (skipping a
folder another user could swap), and writes the socket's path to `instance/address`. A later launch
connects, checks the socket is the user's and sends one fixed message; the first app accepts only
the same user (peer credentials), reads at most 64 bytes for at most 2 seconds, serves up to 32 at
once, and shows its window. The later launch exits 0 once it is shown, or fails after 20 seconds.
On Windows it is a named pipe, on macOS a Unix socket. The lock is not inherited by the app's core.

An update or rollback hands the profile to the new version without it ever being free: on Linux and
macOS the new version adopts the lock's open file; on Windows, where a lock cannot be handed on, the
app writes `instance/successor.json` (a random nonce, the program's path and the digest of its
program set) and starts the new version with the nonce in `PLUXY_APP_SUCCESSOR`; that launch
listens at once and waits up to 120 seconds for the lock.

## Packages, signatures and trust

**The verifier** (`next/app/src-tauri/src/artifact.rs`) checks the exact descriptor bytes with
Ed25519 before reading the archive, then its size and SHA-256 before extraction, and hands the
caller the rewound archive reader so the file is never reopened by name
([the signed descriptor contract](../app-packages.md#signed-descriptor-bytes)). It
neither unpacks nor runs anything. Descriptor signatures and Tauri updater signatures are separate
formats.

**Production trust is empty.** `next/app/src-tauri/trust/production.json` holds no publisher roots
and no updater key, and the trust loader reports `not configured` for native-tier and updater
trust, never substituting test keys. So a build without test trust installs no curated add-on, no
core release and no update.

**Test trust.** The `artifact-test-trust` Cargo feature, in a build that also has `native-test` (the
native tests) or `local-build` (the local build), loads roots from a profile in the process HOME, and
only when that HOME is private (mode 0700 on Unix), holds a marker naming it
(`.pluxy-test-trust-profile.json`), and the app is given the marker's nonce as
`PLUXY_TEST_TRUST_NONCE`. The roots are in `.pluxy-test-trust-roots.json`:

```json
{"roots":[{"kind":"test","publisher":"example","source":"https://example.invalid/index.json","key_id":"generated-key-id","public_key":"<64 lower-case hex digits>"}],"updater":null}
```

With the feature alone, in any other build, a profile is refused (`trust::PROFILE_TRUST`). The local
build's `start` writes such a profile with one root: the key its add-ons were signed with, for its own
add-on index only. That is weaker than compiled-in roots, since any program running as the user can
write a profile, and acceptable for a build that never ships.

**The release gate.** `bun run package:release` builds the release app with no features and no
config overlay, builds the release validator (the `validate-release-config` example in
`next/app/src-tauri`) and runs it on the build's config, trust and feature
snapshots, the staged trust folder, the binary and each artifact given. `build.rs` writes those
snapshots in Cargo's `OUT_DIR` and embeds a digest of them in the app; the validator requires that
receipt in each artifact and refuses test roots or features, private trust material, updater keys,
test identifiers and products, and the test build marker `PLUXY_TEST_BUILD_DO_NOT_RELEASE`, which
test-feature builds carry. It checks our own build for test material; it does not stand against a
hostile builder. Keep the three platform config files in `next/app/src-tauri`
(`tauri.linux.conf.json`, `tauri.macos.conf.json`, `tauri.windows.conf.json`) even when empty:
`build.rs` reruns only when a file it read changes, and Cargo cannot watch for a file being added.

**Package commands** (`next/app/package.ts`): `package:test` builds the test package for checks
(identifier `dev.pluxy.desktop.test`, a temporary updater key, two builds at the app's version and at
0.2.0 for the native test to update between, each with a test-signed core); `package:local` builds the
local build; `package:release` is the gate above and turns on no bundle format, since release formats
are not chosen yet. The Windows test package includes a per-user NSIS setup program whose hooks
(`next/app/src-tauri/nsis/hooks.nsh`) refuse while the app runs and remove what the app itself left
in the install folder.

## Platforms and limits

Linux x86_64 is the one platform the app is checked on end to end and the only one a usable build
(the local build) is made for. What runs where, for a user, is in
[app.md](../app.md#what-works-today). For a developer:

- **Windows and macOS.** The app's crate builds, passes clippy and runs its Rust tests there
  (`.github/workflows/next-app-platforms.yml`), and the native stack is compiled there: native key
  entry and confirmations (Win32, AppKit), one app per profile, the login item (the `Run` key, a
  LaunchAgent), the program host (a job object, a process group; `next/app/src-tauri/src/owned/`),
  the vault, the launcher and the self-update of the test package. CI's native sessions there run
  the app against a fixture tool, test cores and test packages; the eight coding-tool add-ons and
  the usage parsers run there only in their own unit tests (`next-app-tools-platforms.yml`), never
  inside the app.
- **WSL.** On Windows the app can run the Claude Code add-on's Linux build inside a WSL distribution
  (`pluxy/wsl`, [Tools in WSL](../app-packages.md#tools-in-wsl-windows)); CI tests that at
  host level only.
- **Linux arm64.** The add-ons are built for it (`next-app-tools-pack.yml`) but no job builds or runs
  the app there, and the local build and test package refuse it.
- **Intel Macs and Windows on ARM.** The code has branches for them; no CI job builds the app for
  them.

Limits a developer should know:

- No production trust roots or updater key: outside test trust, nothing is curated, no core release
  installs and the app never updates itself.
- The app's own core comes only from a local index file; an https source is not built.
- Native tools need Linux 5.11 or later (the program host uses `close_range` with
  `CLOSE_RANGE_CLOEXEC`).
- On Windows and macOS a native tool's `files` frames (saving a file) and installing a package from
  a file are refused, and Access grants nothing (on Windows its commands are refused; on macOS listing
  and revoking answer, with nothing granted).
- Hidden key entry keeps keys out of the webview API, not away from another process of the same
  user; on Windows a keyboard hook of the same user sees what is typed.
- On Windows, other programs calling the app's core (such as coding tools with client keys) are not
  covered by the app's TCP table check after a core crash; only the app's own calls are.
- The tray host is asked about only when the window closes: if it goes away while the app sits in the
  tray, the app has no way back but being stopped.

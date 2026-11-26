# pluxy desktop app

The desktop app is the UI kit's second build ([`../ui`](../ui/README.md)), in a Tauri 2 window,
with a tray icon. It shows core's pages in the same places and with the same themes as the web
panel, then adds its own: Beginner Mode, Manage Clients and the client pages as the topics of Tools, and Connection, Version Management, Device
extensions and Launcher among the Settings topics. Extension pages, from core and
from packages installed on this device, open in webviews of their own.

Linux is the only platform the app supports now; Windows, macOS and WSL are later work
([Platforms](#platforms)). [`PARITY.md`](PARITY.md) lists the agent clients and usage sources the
app sets up or reads, with their files and the native test session that checks each, and records
what happened to each feature of the old desktop app.

| Folder | What it holds |
| --- | --- |
| `src/` | The window: the kit's shell and pages, the app's host (`host.ts`), the Connection page, and how extension pages are drawn (`nativePage.tsx`) |
| `page/` | The small document an extension page's webview loads, which holds the page's sandboxed frame, and the release notes' style sheet |
| `src-tauri/` | The native side: commands, the connection to core, the app's own core, extension page webviews, the tray |
| `native/` | The native test (`run.ts`), its sessions and fixtures |
| `package.ts` | The test-package and release-package commands ([Packages](#packages)) |
| `PARITY.md` | The agent clients and usage sources, and the record of the old desktop app's features |

## How the app reaches core

The Connection page picks one of three modes. The app keeps the choice in `connection.json` in
its config folder (on Linux `~/.config/dev.pluxy.desktop/`), readable only by your user. On
Windows it is in the local application data folder instead (`%LOCALAPPDATA%\dev.pluxy.desktop\`),
and so is the app's own core's folder (`core/`), since both can hold a management key: the config
and data folders are in the roaming one (`%APPDATA%`), which a roaming profile copies to its
server and to the person's other computers.

| Mode | What the app does | When the app quits |
| --- | --- | --- |
| Run core here (`managed`) | Runs one of its own core versions ([below](#the-apps-own-core)) with a config of its own, `core/config.json` in the app's data folder. The first start has the core write that config (`pluxy init -port 0`): a free loopback port, which then stays the core's, new random keys, and folders for credentials and packages beside it. After that the file is yours to edit; the app only reads its `management_key`. | Stops that core (SIGTERM, then a kill after 10 seconds). On Linux the core also gets SIGTERM if the app dies without stopping it; on macOS it runs on, holding its port, until it is stopped. |
| Core on this computer (`local`) | Connects to a core that already runs on a loopback address (`127.0.0.0/8`, `::1` or `localhost`), such as a system service. You give its address and management key. | Leaves it running. The app never starts or stops it. |
| Remote core (`remote`) | Connects over https only, so the key is never sent in the clear. You can name a PEM file of certificate authorities to trust for that core, for a certificate you made yourself; without one, the usual public roots are used. The core's address is shown next to the app's name. | Leaves it running. |

Addresses are origins only (scheme, host and port). A key you give is checked against that core
(`GET /v0/management/status`) before the app switches to it. It is kept in `connection.json` only
when you tick "Keep the key on this device"; otherwise it lasts until the app quits.

The native side holds the management key. "Enter management key" on the Connection page opens a
native dialog with a hidden entry, "Keep the key on this device", and OK/Cancel: a GTK dialog on
Linux, a Win32 dialog with a password edit control on Windows, and a sheet on the window with an
`NSSecureTextField` on macOS. On Windows and macOS the dialog is modal to the app's window only:
the app's other native dialogs and the tray stay usable meanwhile.
Before opening it, native code validates the mode, origin and certificate authority file. The
dialog identifies the target origin, any custom certificate authority, and an origin switch.
The origin switch names the connection you last confirmed and the app saved (not the client
in memory, which may be down). Reopening after Cancel waits one second.
The key goes directly from that dialog to native connection state; no webview receives it.
Cancel, a refused key or a failed settings write preserves the old connection. Settings use a
unique exclusive temporary file at mode 0600, followed by file sync, rename and directory sync
on Unix; at startup the app removes a leftover temporary file of that name (a crash mid-save),
and nothing else. The app checks the key against core without holding its connection state, so
other commands go on meanwhile, and refuses the key if the connection changed during the check. Validation and transport failures report specific reasons without request or key text.
The window calls core through the
`core_call` command, and the native side adds the key and sends the call to
`<core>/v0/management/...` only, following no redirects and, for a core on this computer, no
proxy from the environment. Path segments that are, or decode to, `.` or `..` are refused.

Rotate on the Config page's Connectivity tab rotates the management key natively: the app opens
its native confirmation, which names the core and how the new key will be kept, then
calls core's `POST /v0/management/management-key/rotate` itself. It keeps the new key as the old
one was kept (in `connection.json`, for this session only, or, for the app's own core, in that
core's config file, which core writes) and shows it once in a native window with Copy and
Close, for other programs that used the old key and for signing in again. On Windows Copy marks
the copy to stay out of clipboard history, cloud clipboard and clipboard monitors; on macOS it adds
the `org.nspasteboard.ConcealedType` type clipboard managers take to mean a password. That window stays open
when the main window closes to the tray, and a later rotation's window replaces it. The window
gets only how it went: the key never reaches a webview. `core_call` refuses every path under
`/management-key`, however it is spelled. If the new key cannot be saved, the app keeps using
it until it quits and says to copy it and enter it again with "Keep the key on this device".
A call that left with the old key during a rotation is sent once more with the new one. The app
waits at most 30 seconds for core's reply to a rotation, since other calls and Quit wait for it;
without a reply it says the key may have changed and where to find it.

Until the app is connected and signed in, the window shows only the Connection page, so a wrong
address or key can always be fixed there.

## The app's own core

In managed mode the app runs only core versions it installed itself, each in a folder of its own
under `cores/versions/` in the app's data folder (on Windows the local application data folder,
which stays on this computer; a store there that is not on a fixed local drive, such as a network
share, is refused). A packaged app installs the core it comes with
([below](#the-core-the-app-comes-with)). Until one is installed, the app runs no core
and the window shows only the Connection page, with **Core versions**. There, the app lists
what is installed, which version runs and which ran before it, and what the core source offers.

The **core source** is the full path of an index file. The app has no built-in source and reads
nothing until you set one. The index has the release feed's format (format 1, `releases`); the
app reads only releases with `product` `core`, their `version`, and their `artifacts`:

```json
{"product":"core","version":"2.0.0","channel":"stable","published":"2026-11-01T00:00:00Z",
 "artifacts":[{"platform":"linux/amd64","url":"pluxy-core-2.0.0-linux-amd64.tar.gz",
   "descriptor":"{\"id\":\"pluxy/core\",\"version\":\"2.0.0\",\"tier\":\"native\",\"platform\":\"linux/amd64\",\"size\":1234,\"sha256\":\"…\"}",
   "signature":"<128 lower-case hex digits>","key_id":"…"}]}
```

| Field | Rule |
| --- | --- |
| `platform` | `<os>/<arch>` as Go names them, such as `linux/amd64` |
| `url` | For now a file name beside the index |
| `descriptor` | The exact signed descriptor bytes ([signed descriptor contract](app-host/README.md#signed-descriptor-bytes)), as a JSON string: `id` `pluxy/core`, `tier` `native`, the release's `version` and this platform |
| `signature` | The detached Ed25519 signature of those bytes |
| `key_id` | The publisher key that signed it |

The archive is a gzip tar file of the core's folder, as provider releases are packed: `pluxy` (on
Windows `pluxy.exe`) at the top, and anything beside it, such as `bundled/`. It may hold only
regular files and folders, no links, devices or names that leave the folder, at most 10,000
entries, and at most 512 MiB decompressed in all (headers and the data of folder entries count
too). On Windows it may also hold no two names that differ only in case, compared as Windows
compares them (`σ` and `ς` too), and no name Windows cannot hold plainly (a `:` stream, a device
name such as `CON`, a trailing dot or space). The folder it is unpacked into is opened by its path, refusing a
link there; below it each folder and file is made from the folder held open before it, following
no links (on Windows relative to the folder's handle, which is held so no one can rename that
folder meanwhile). Release versions may not start with `custom-`, which adopted programs use.

**Install** (or **Update to** a newer version): the app copies the archive into its own folder and
checks it with the shared verifier (`src-tauri/src/artifact.rs`) and the app's trust
(`src-tauri/src/trust.rs`): the signature over the exact descriptor bytes with a publisher key
trusted for this exact source (publisher `pluxy`), then the archive's size and SHA-256. Only then
does the native confirmation open, showing the version, source, key and digest, and that a core
has full access to your user account. A release that fails a check is refused before any
dialog, and nothing is installed. Release builds have no publisher keys yet, so they refuse
every release; test builds trust only a test runner's temporary roots (below).

Once confirmed, the app unpacks the archive into a new version folder, records each file's
SHA-256 (in `cores/records/`, apart from the folders), and makes the files read-only (on Windows
by their access lists: this user and SYSTEM may read them, no one may write; the folders are
private to this user and SYSTEM). Before every start it opens the version's folder by its path
(refusing a link there) and every file beneath it without following links, checks that it is a
regular file of the recorded size and still read-only, then its digest, and starts the core from
the program file it just checked: on Linux from the open file itself; on macOS and Windows from
the path that file has now, after checking that the path still names that same file
(`src-tauri/src/owned/`). A changed version does not start:
**Install … again** installs the same release over it, through the same checks and confirmation;
a changed adopted copy is copied in again by adopting the same program again. Writing a
version again moves its old folder aside first, which Windows refuses while a program in it runs,
so there the app stops the core first when it runs that version, and starts it again if the
install fails.

**Switching** to a version (an install, **Roll back to** an installed version, or an adoption)
stops the running core, keeps a copy of `core/config.json` (or a mark that there was none) and
records the new version as the candidate, then starts it. On Linux and macOS the app binds the
`listen` address in `core/config.json` itself (an IP address and port, or `localhost` and a port)
and hands the listening socket to the core (`-listen-fd`); on Windows the core binds it. It counts
as started once it says it listens there, on this computer, every socket listening on that port
belongs to the process the app started (read from `/proc` on Linux and from the system's TCP
table on Windows; on macOS the core still runs and the address is the loopback socket the app
bound and handed over, which it still holds and no other user can bind over, while other
processes of your own user are not looked at, as they can read the core's config and its key
anyway), before the app sends anything there, it answers
`GET /v0/management/status` with the app's management key and, for a release, reports the
version it was signed as, and the app has saved the new selection. If not, the app puts the
config back as it was, removes a version it installed for this switch, and runs the version that
worked before (when the switch was to the selected version itself, and reinstalled it, copied a
damaged adopted program in again, or ran it with **Roll back to** and it failed, the one before it,
which is then selected once it runs); the page says why the new one did not start. If the config
cannot be put back, no core runs: the candidate stays recorded with the copy, and the app tries
again at its next start. Once it is put back, a mark beside the config says so, and a later start
does not put it back again over your edits, unless the app could save neither the failure nor that
mark: then the next start puts the same config back once more. If the app stops while a version
starts, it does the same at its next start, also when that was a reinstall or rollback of the
selected version; a copy it cannot read is an error, and the config is left as it is. From the
moment the app begins to stop its core, or sees it exit, it sends nothing more to that address.
On Windows the core binds the address itself and the app keeps no copy of the socket. Another
user's program can then get the core's connections: one that starts listening on that address
between a crash of the core and the moment the app sees it exit (up to a tenth of a second: it
checks every 100 ms), or one that stays bound to the wildcard address `0.0.0.0` on that port the
whole time the core holds its loopback address, which Windows allows another account, and which
receives the connections as soon as the core's socket closes. So before the app sends anything
on a new connection to its core, it reads the system's TCP table: the other end of that very
connection (its own addresses and ports) must belong to the core's own process. If it does not,
or the table does not show it for at least a second, the app sends nothing on it, and the call
fails with the reason (addresses and process IDs only): which process accepted the connection, or
that the connection is not in this computer's table. Each client of the core holds its own
handle on the core's process for as long as it lives, so the core's process ID cannot pass to
another process meanwhile, even after the core has ended. On Linux and macOS the app keeps its
own copy of the core's listening socket for as long as anything in the app could still send
there, so no other program can listen on that address in between: not while a crashed
core's exit is being noticed, and not while the core drains after a stop, the app's or a SIGTERM
from elsewhere (the core closes its listener as it starts draining). A call made in that time
reaches the core or waits in the app's socket, and fails once the core has exited. This holds
for other programs calling the core too: a new connection in that time is no longer refused at
once, but waits (up to the core's drain, 10 seconds or more) and is then closed, possibly after
its request was sent. The core takes the socket before it starts any extension, so no program it
starts holds it. The
app keeps the running version, the one before it and the core it came with, and removes older
ones after a switch. Rolling back reaches only an installed version.

A **core program** you name on the Connection page (or `pluxy` beside the app, unless that is the
core the app came with) is never run in place. **Adopt core** shows its path, size and the first 12 characters of its SHA-256 in the native confirmation;
once confirmed, the app copies it into a version of its own, `custom-<its SHA-256>`, if it is
still a regular file of that size and digest (at most 512 MiB; a FIFO or device is refused
without being read; on Unix it must be executable, on Windows a program image named `.exe` or
`.com`), and runs the copy. Only the program is copied, nothing beside it (such as
`bundled/`), so an adopted core has none of the bundled packages a release brings to install. An adopted
program has no signature.

The app only ever stops the core process it started. A core it did not start, such as a system
service or another core of yours on this computer, is never stopped or signalled. The native
test checks it on Linux, Windows and macOS: a core the runner started before the app still
answers afterwards, with its config and program unchanged.

Each change works once: while one waits for its confirmation, another is refused. The window
hears `pluxy-connection` when it has ended, and `core_versions` says whether it was done,
cancelled or failed.

What these checks guard against: a damaged or substituted download, and other users of this
computer. They do not guard against your own user account. A program running as your user can
rewrite the app's core store, as it can replace the app itself: the installed records and version
folders (or the folders above them), a staged archive between its check and its unpacking, and a
checked program, or the files beside it, before the core runs or reads them.

### The core the app comes with

A packaged app comes with a core: a tagged core's signed release, of the version the app names in
`core-release.json`, pinned file by file. When the app is packaged, `bundle-core`
(`src-tauri/examples/bundle-core.rs`) finds that release for the target system in a core source's
index, checks its archive against its signed descriptor with the publisher key, as an install
from a core source does, and unpacks it with the same rules. It writes a manifest of what it
unpacked: the core version, the SHA-256 of the signed descriptor, and each file's path, size,
SHA-256 and execute bit. `build.rs` checks the manifest and compiles it into the app's program
(`PLUXY_BUNDLED_CORE` names it), so whatever covers the app's program covers the manifest too. A
build without one, such as a dev build, has no bundled core. The package carries the files:

| System | Core program | Its packages (`bundled/`) |
| --- | --- | --- |
| Windows (folder bundle) | `pluxy.exe` beside the app's program | `bundled\` beside it |
| macOS | `Contents/MacOS/pluxy` | `Contents/Resources/core/bundled/` |
| Linux (AppImage) | `usr/bin/pluxy`, beside the app's program | `usr/lib/<product name>/core/bundled/` |

In managed mode, each time it starts its core, the app copies the core it came with into its own
cores, unless they hold it already, as a version named after the core version, listed as one that
came with the app. It reads only the files the manifest lists, each opened without following a
link from the folder opened before it, a regular file of the listed size, its SHA-256 computed
while it is copied. Anything else beside them is never read or copied. There is no confirmation:
installing the app was the consent, and the files are checked against the manifest in the app's
own program. The app never runs the files beside it, only the copy, with the checks every version
gets before it starts. A copy that no longer passes them is copied in again at the next start.

- A file that differs from the manifest, is missing or is a link: nothing is installed, and the
  Connection page says that the core that came with the app is damaged, and to reinstall or
  update the app.
- The app's cores already hold that version with other files (a release of the same number from
  another core source): nothing is installed, the installed one stays, and the Connection page
  says that the app's cores hold that version with other files than the one that came with the
  app.

Which version runs: `state.json` records who chose the selected version, the app or you, and the
app version that last applied the core it came with. On a fresh profile, and at the first start
after the app's version changes (an update, or an older app), the app moves a selection it made
to its own core, with the switch above, which puts back the earlier version if the new one does
not start. A core it came with that does not start is not tried again until the app's version
changes. A version you chose stays: an install, an adoption, a rollback, also to the core an
earlier app came with. **Roll back to** the core the app came with switches to it as any rollback
does, with the native confirmation. A selection made before the app recorded who made it counts
as yours.

`pluxy` beside the app is not offered to adopt when it is the core the app came with (its size
and SHA-256 are the manifest's): the app installs that itself. A program you name still goes
through Adopt.

## The window and the tray

What closing the window does is the Connection page's **Close behavior**: **Minimize to tray**
(the default), **Ask every time** or **Exit app**. Ask every time shows "What would you like to
do?" with Minimize to tray, Exit app, Cancel and **Remember this choice**. Exit app quits as the
tray's Quit does. Minimize to tray destroys the window and its webviews, so they hold no memory
while the app sits in the tray. With a tray, a window that does not answer the question within
three seconds (its page still loading, or busy) closes to the tray. In local and remote mode,
quitting the app leaves pluxy running; only in managed mode does it stop the core the app runs.

The tray menu's first line says what pluxy is doing: "pluxy: Running", "pluxy: Stopped",
"pluxy: Restarting", "pluxy: Stopped restarting" or "pluxy: Not installed" in managed mode,
"pluxy: Connected to <host>" in local and remote modes once the app is signed in, "pluxy: Not
connected" otherwise. The
menu has "Open pluxy" (a new window), "Start…" (remembered launches,
[The launcher](#the-launcher)) and "Quit", which reads "Quit (stops pluxy)" in managed mode,
where quitting stops the core the app runs. In managed mode, "Start pluxy", "Stop pluxy" and
"Restart pluxy" control only the core the app started. They work with the window closed and no
renderer running. "Restart pluxy" is enabled only while the core runs. While one of them runs,
the tray says "pluxy: Working…", disables all three and refuses another click. They are disabled
in local and remote modes, and the native side refuses them there too, even if a desktop
delivers a click on a disabled item.

When there is no usable tray (the app made no tray icon, or the session's
`org.kde.StatusNotifierWatcher` is missing or has no registered host, asked at each close, since
GTK makes an icon even with no desktop to show it), closing the window quits the app, whatever
the choice, and the setting says so. In local and remote mode it quits without asking, and pluxy
keeps running. In managed mode, where quitting stops the app's own core, the app first asks the
window (`__pluxyClosePrompt("no-tray")`): "Close and stop pluxy?", saying that closing stops
pluxy and the coding tools stop getting answers, with **Keep open** (also Escape), which keeps the
window and sends nothing, **Close and stop pluxy**, which quits, and **Don't ask again**. A window
that does not answer within three seconds stays open. Don't ask again is kept as
`ask_without_tray: false`, apart from the Close behavior, which stays as it was for a session
that has a tray. The answer is kept by the prompt the person answered, even if a tray shows by
the time they answer.

When an owned core exits unexpectedly, the app retries after 2, 5 and 15 seconds, with at most
three automatic recovery attempts in a rolling five-minute window. A successful recovery keeps
that budget, so a core that keeps crashing still stops being restarted. After the third failed
recovery or another crash within that window, the tray shows "pluxy: Stopped restarting" and the
Connection page shows the reason. An explicit Start, Stop or Restart, from the tray or the
window, or a mode change, cancels any scheduled recovery and clears the budget. A core you
stopped stays stopped, and so does one that Start core on launch left unstarted; the
connection's `core.stop_reason` tells them apart (`stopped_by_user` or `not_started`) until
something starts it.

The window opens at the size and place it last had, kept when it closes and when the app quits:
the size in logical pixels, the place in physical ones, never noted while minimized, maximized
or full screen. On opening, the size is kept between 640×480 and the monitor's work area, and
the window is moved into the work area of the monitor holding its place (or the primary one).
The first window is 1100×760, where the desktop puts it. The choice and the geometry are in
`window.json` beside `connection.json`, readable only by your user; a damaged file is kept aside
as `window.damaged-<random>.json` and the defaults used.

A minimized window's pages are hidden (`document.visibilityState`), so the kit's polling, which
runs only while its page is visible, stops until the window is restored.

## One app per profile

The app runs once per profile: your user and the app's data folder (on Linux
`~/.local/share/dev.pluxy.desktop/`), not the place the program is installed. Launching it again
on that profile, from the app menu, a shortcut or the login autostart entry (Silent launch applies
only to a login launch that is the first), shows the first app's window (a new one if it was
closed to the tray) and exits with status 0, starting no core, tray icon or webview. Another `HOME` or `XDG_*` folder is another
profile, with its own app and core.

Before anything else, the app takes an exclusive lock on `instance/lock` in the data folder
(`flock`: the system releases it when the app ends, however it ends, so a killed app leaves nothing
to recover). The folder must be your user's and not a link; the lock is opened inside it without
following a link, and used only if it is a plain file of yours with mode 600. The app then listens
on a socket in a short folder of yours: `pluxy-app/` in `XDG_RUNTIME_DIR`, else `pluxy-app-<uid>/`
in the temporary folder, so a long data folder path does not matter. The app skips a folder that
another user could swap for theirs: one with a link above it, or below a folder that belongs to
another user (other than root) or that others may write in without its sticky bit (as `/tmp` has
it). The socket's name is a digest
of the data folder, and the app writes its path to `instance/address`. The folders, the lock, the
address and the socket are readable by your user only. A later launch reads the address, connects
without blocking, checks the socket is your user's, and sends it one fixed message; the first app
accepts only a connection from the same user (peer credentials), reads at most 64 bytes from each
for at most 2 seconds, serves up to 32 at once, and ignores anything but that message. The launch
exits once the first app has shown its window, or fails with a message on stderr if the first does
not answer within 20 seconds, or at once if the first could not listen. A socket a killed app left
behind is removed by the next app, which holds the lock.
The lock is not inherited by the app's core.

An update or rollback ([Application update](#application-update)) hands the profile to the
version it starts without the profile ever being free. On Linux and macOS the app hands that
version the lock itself (its open file, which the new version adopts). On Windows a lock cannot be
handed on, so the app stops listening, writes `instance/successor.json` (a random nonce, the
program's path and the digest of its program set, as the update pins it) and starts the new
version with that nonce in `PLUXY_APP_SUCCESSOR`. A launch that finds the lock held, its nonce in
that record (at most five minutes old), and itself the program the record names, its program set
having that digest, removes the record and is
the app at once: it listens, so a later launch raises its window, while it waits for the lock,
which the earlier version holds until it ends. Without the lock within 120 seconds it exits. Any
other launch is an ordinary later launch.

## Privacy mode

The **Privacy mode** toggle at the header strip's end masks account e-mail addresses and identifiers where
the kit shows them, as in the panel (`ui/kit/privacy.ts`), and extension pages are told in their
theme snapshot. The app keeps the choice natively, in `privacy-mode.json` beside
`connection.json` (readable only by your user), not in the window's storage, so losing that
storage cannot turn it off. A file that cannot be read or parsed counts as on.

## Files the window saves

The Themes page's **Export**, the Logs page's downloads and the Usage page's **Export** save
a file. In the panel that is a browser download. The app's window opens no address but the app's
own, so a download link, whose address starts with `blob:`, does nothing there; these pages hand
the file to the `file_save` command instead (`src-tauri/src/export.rs`). It saves the file in
your Downloads folder (`Downloads` in your home folder when the desktop names none) under the
page's name for it, and the page shows `Saved to <path>`. A name already there is kept, and the
new file gets ` (1)`, ` (2)` and so on before its extension.

## Startup preferences

The Connection page has three startup choices:

- **Launch at startup** registers pluxy for your Linux login. Saving it writes
  `$XDG_CONFIG_HOME/autostart/dev.pluxy.desktop.desktop` (or
  `~/.config/autostart/dev.pluxy.desktop.desktop`). Its Exec command names the app by absolute
  path with `--autostart`. Silent launch controls whether a login launch shows a window.
  Turning Launch at startup off and saving removes pluxy's entry. Merely opening the app does
  not register it. The displayed choice is read from that entry, including desktop disable flags.
  A file without pluxy's ownership marker is shown as a conflict and left alone.
- **Silent launch** suppresses the window only for an autostart launch in managed mode when
  the session has a StatusNotifier watcher and a registered tray host. Local and remote modes
  host no core, so they open normally. In that case no window or webview is
  created. Without a host the app logs why and opens a window. Opening the app from its menu
  or shortcut always shows the window.
- **Start core on launch** starts the app's own core in managed mode. Turning it off leaves
  the saved managed connection stopped until you press Start core. Local and remote modes
  only connect; their core processes are never started or stopped by these choices.

The defaults are login registration off, Silent launch off, and Start core on launch on.
Preferences live in `startup.json` beside `connection.json`, readable only by your user.
A malformed file is moved to `startup.damaged-<random>.json` and defaults are used; failure to
read or keep it aside is logged and still does not prevent startup. Saving preferences does
not restart or change the current connection. No system service or second core supervisor is
created. This section describes Linux; Windows and macOS are later work ([Platforms](#platforms)).
Registration resolves the executable
when you enable it: the AppImage path if present, otherwise the running executable. It must be
an existing regular file; paths containing percent or backslash are refused. Saving unrelated
choices leaves the entry unchanged.

## Commands

The window's webview, labelled `main`, is the only webview any capability names
(`src-tauri/capabilities/main.json`). Every app command is declared in the app's command
manifest (`src-tauri/build.rs`, from the list in `src-tauri/src/command_names.rs`). With a
manifest, Tauri checks every call against the capabilities, also from the app's own pages;
without one, a webview with no capability that shows a page from the app could call any command.
The grant names the webview, not the window: a grant for the window would also cover the
extension page webviews inside it. Each command also refuses any caller but `main`.

| Command | What it does |
| --- | --- |
| `startup_get` | Reads the native startup preferences |
| `startup_set` | Saves the startup choices and explicitly writes/removes Linux login registration |
| `connection_get` | The connection, without the key |
| `connection_set` | Changes connection settings without accepting a key |
| `key_entry` | Opens native key entry, checks the key against core, and returns only signed-in, wrong-key, cancelled or error status plus the connection without its key |
| `core_start` | Starts the app's own core only if it is not running (managed mode) |
| `core_stop` | Stops the app's own core, as the tray's Stop does (managed mode); refused in local and remote modes, which it never stops |
| `core_restart` | Starts the app's own core again (managed mode) |
| `core_versions` | The app's own core versions, what the core source offers, and how the last change ended |
| `core_install`, `core_rollback`, `core_adopt` | Asks to install a release from the core source, to run an installed version again, or to adopt the chosen core program; each opens a native confirmation and applies nothing itself |
| `core_call` | One management API call to the connected core; refuses the management key's own routes |
| `management_key_rotate` | Rotates the management key natively after the native confirmation, keeps and shows the new key natively, and returns only how it went (200 with how the key is kept, 204 when cancelled) |
| `sign_out` | Forgets the key (local and remote modes) |
| `page_open`, `page_place`, `page_close` | Opens an extension page's webview over part of the window, moves it, closes it |
| `page_send` | Passes the window's channel messages to a page's webview |
| `confirmation_request` | Opens native review for an opaque action ID already prepared by native code; returns no approval or token |
| `confirmation_settings` | Opens native settings to inspect and revoke exact remembered launches |
| `release_get`, `release_check` | The release feed's view (versions, dates, status); `release_check` reads the feed first ("Check for updates") |
| `release_channel_set` | Picks the Stable or the Preview (latest) channel |
| `release_source_set` | Asks to change or remove the feed's address; opens a native confirmation and applies nothing itself |
| `release_notes_open`, `release_notes_place`, `release_notes_close` | Opens a release's notes in a webview of their own over part of the window, moves it, closes it |
| `release_notes_full` | Opens the release's notes address from the feed in the browser ("View full release notes") |
| `device_list` | This device's extension catalog |
| `device_registries` | What the registries in `device-registries.json` offer this computer |
| `device_install` | Installs or updates a package from one of those registries |
| `device_install_wsl` | Installs a native tool's Linux build (`linux/amd64` or `linux/arm64`) for its active release, through a native confirmation ([Linux build for WSL](app-host/README.md#linux-build-for-wsl)) |
| `device_install_file` | Opens the native file chooser and installs or updates the package chosen there (Linux) |
| `device_enable`, `device_remove`, `device_rollback` | Enables or disables (a native tool only through its native confirmation), removes, or rolls back a package on this device |
| `device_imports` | Allows a curated web package's native imports, through a native confirmation, or revokes them |
| `device_page_open` | Opens a webview for a page of a package on this device, at the address its catalog keeps |
| `access_list` | The approved signers and every grant of Access |
| `access_signer` | Opens a native confirmation to approve a device package's signer for its source |
| `access_grant` | Opens the native file chooser for a package's declared resource, then a native confirmation, and keeps the grant |
| `access_revoke` | Revokes a grant, answering once the reads under it have finished |
| `program_call`, `program_cancel` | Calls an operation of an admitted native tool, starting it if it is not running, on this computer or in a WSL environment the window names (`environment`, Windows); cancels a call by the ticket it was given |
| `program_status` | Whether a native tool runs, its leases, how it last stopped, its crashes in a row and backoff, calls of unknown outcome, replaced releases still draining, its vault journals, open and recently ended, or why they cannot be read, and its managed runtimes, running, ended or `unknown` (left running by a run of the app that did not stop them) |
| `vault_secret_add` | Opens native key entry and keeps the secret typed there in the vault under a label; returns only its ID |
| `vault_secrets`, `vault_secret_remove` | The vault's secrets, by ID and label (and a client key's binding), never their values; removes one |
| `client_key_capture` | After a native confirmation, asks the connected core for a new client key for a native tool and keeps it in the vault, bound to that core and tool; returns its ID, label and binding, never the key |
| `client_key_enter` | Opens native key entry for an existing client key of the connected core and keeps it the same way once core accepts it |
| `core_alias_create` | After a native confirmation, adds a model alias (`fork`, so the model keeps its name) to the `model_alias` of each account added by login that serves the model in the connected core; refuses an alias core already serves for anything else and a model only accounts set in core's config serve, and takes the alias out again when core then does not serve it; returns the accounts that have it (Linux) |
| `launcher_state` | The launcher's state: whether the execution program can be used, the remembered launches with their source, the recent folders and the notice setting |
| `launcher_terminals` | The terminals the execution program finds, Automatic first |
| `launcher_launch` | A new launch, prepared by the execution program (in a WSL environment the window names, by `pluxy/wsl`); it always opens the native confirmation |
| `environments_get` | The WSL environments the app knows, and whether WSL can be used here (Windows only) |
| `environment_add`, `environment_remove` | Adds a WSL distribution and user, as `pluxy/wsl` checks them (which starts the distribution), or checks one again; forgets one, stopping the tools running there |
| `launcher_start` | Starts a remembered launch again in one click, from the window only, with its remembered approval |
| `launcher_forget` | Forgets a remembered launch, revoking its approval |
| `launcher_settings` | Whether a one-click start shows a launch notice |
| `app_update_get` | The App update card's state |
| `app_update_start` | **Update now**: downloads, checks and installs the newer release, then restarts the app; the window hears `pluxy-app-update` as it goes |
| `app_update_cancel` | **Cancel download** |
| `app_update_rollback` | Rolls back to a version that ran here and was kept, by its digest, after the native confirmation |
| `window_get`, `window_set` | Reads or saves the Close behavior, and whether there is a usable tray and whether closing without one asks first (`ask_without_tray`) |
| `window_close` | Answers the close prompt of the kind it showed (`ask` or `no-tray`): Exit app or Minimize to tray (only `ask`), and whether to remember it (for `no-tray`, remembering only stops that prompt) |
| `privacy_get`, `privacy_set` | Reads or saves privacy mode |
| `file_save` | Saves a file one of the window's own pages made (a theme file, a log, the usage table) in the Downloads folder, never over another file, and returns its path |

The window may also listen to the app's events. To add a command: add it to
`command_names.rs`, to `generate_handler!` in `lib.rs`, and its `allow-<name>` permission to
`capabilities/main.json`. The unit tests check that these three agree.

## Extension pages

Each extension page the window shows gets a webview of its own (`page-<n>`), placed over the
page area. No capability names that webview, so it can call no app command and no Tauri plugin.
It has a web context of its own, apart from the window's storage, kept in the app's cache folder
and cleared at each start. It may show only the app's page document and the pages site.

On Linux, Tauri stacks a window's webviews one under another in a box and ignores where they
should be, so the app lays them out itself: it moves the window's webview into a GTK overlay and
puts each page webview over it at the page area (`src-tauri/src/place.rs`). GTK would give each
one its natural size, its document's, so the app's handler for the overlay's
`get-child-position` signal gives it exactly the page area instead.

That webview loads one app document from the `pluxy-page:` scheme (`page/`). The document shows
the extension's page in a frame with `sandbox="allow-scripts"`, from core's pages site, as the
panel does, and does the panel's side of the page's hello
([extension pages](../ui/extension-pages/README.md)). Its `/send` address is the webview's one
way out: the native side tags each message with the webview it came from and passes it to the
window, which answers with the kit's channel, bound to the extension core lists the page under.
Before forwarding, native code enforces the 256 KiB transport body, 64 KiB channel call,
exact version-0 call schema and known methods. Each document gets one hello, 30 admitted calls
per rolling second and eight calls awaiting answers. A call remains in flight until the native
reply callback confirms delivery, or the page closes. Requests and replies carry the document's
native generation; a second document cannot reconnect. Native subframe navigation detection is
verified on Linux WebKit ([Platforms](#platforms) covers the other systems). Direct frame requests
use these same checks.

One extension may open four pages, with 60 admitted calls per rolling second and 16 in flight
across them. Closing pages preserves the package's recent rate budget. At most 16 page webviews
and 64 recent package budgets are retained. Replies are limited to 2 MiB each, covering 50 settings with 4,096 runes even when each rune
needs six JSON bytes. Pending call replies use their existing in-flight reservations; theme
events retain one delivery and one replacement per page. Failed call deliveries send a small
native error; if even that cannot reach the renderer, the channel closes. The wrapper sends a
bounded FIFO of eight calls one at a time; hello and closed messages always enter that FIFO. Refusals return an HTTP status and, for bodies within the transport limit with a readable id,
the matching channel error. Oversized transport bodies are rejected without scanning for an id.
Permissions and extension settings still use the shared kit's channel. No page receives a key.

## Version Management and release notes

The Version Management page (a Settings topic) shows, for the app ("Desktop app") and for
core, the current version, the latest version on the chosen channel and when it was published,
then the app release's notes. Stable shows the
highest stable version; Preview (latest) shows the highest version on either channel, so it is a
stable release when that is the newest. Versions compare by semantic version precedence. The
current core version is what the connected core reports in `GET /v0/management/status`.

The app has no built-in feed address and reads nothing until the person sets one. Setting,
changing or removing the address opens the native confirmation (below), with the old and the
new address; only its confirmed, one-use token applies the change, and cancelling, closing the
window or letting it expire keeps the old address. While a change waits, the page does not let
the channel be changed (the `release_channel_set` command itself still accepts a channel); a
confirmed change the settings outran (they changed while it waited, as a direct channel change
does) or that cannot be saved is not applied, and the page shows why. The address must be `https://`, with a host
and no user name or password. The choice is kept in `release.json` next to `connection.json`,
readable only by your user. A file the app cannot use (unreadable, larger than 64 KiB, or with an
address that fails the checks above) is shown as an error saying to fix or delete it, and is never
overwritten; until then no feed is used. The channel is a plain setting there too. The kept feed
is read only up to 2 MiB.

"Check for updates" reads the feed: https only, the public certificate roots, no redirects, at
most 1 MiB, 30 seconds. The last good feed is kept in `release-feed.json` in the app's cache
folder with the time it was read, and shown again after a restart. When a check fails (offline,
an error answer, a feed the app refuses) the page says why and keeps showing the last good feed
with that time. A feed with any bad release is refused whole. Changing the address drops the copy.
Only the latest check of the current feed is taken: an earlier check that finishes later, or one
started before a feed change (even back to the same address), is ignored.

The window gets only versions, channels, dates and whether a release has notes or a notes
address; never notes text or a notes address. Each release's notes open in a webview of their
own (`notes-<n>`, at most two), placed over the part of the notes area that shows: inside the pane, never over
the header strip or the navigation, and hidden when none of it shows, and placed again whenever the page moves the area (scrolling,
resizing, a notice appearing above it), so it never covers the rest
of the window. It has a web context of its own, cleared at each start. No capability names it. It loads one document from the
`pluxy-notes:` scheme, which serves each notes webview only its own document and style sheet:
the notes' Markdown (CommonMark, with tables and strikethrough) as HTML, rendered natively
(`src-tauri/src/notes.rs`). Raw HTML in the notes is left out, comments included, as the old
app's notes were; an image shows as its alternative text, and only absolute `https://` links
stay links (a link to an IPv6 address does not: the renderer's escaping of its brackets would
break it). The document has no script,
and its policy (`script-src 'none'`, `img-src 'none'`, `connect-src 'none'`, no frames or
forms) allows none and loads nothing but its style sheet, with the window's theme colours and
fonts, which native code checks as the kit checks theme values. The notes are expanded at first;
Collapse hides them and closes their webview, Expand opens a new one. The notes webview never
navigates: following a link opens it in the browser,
only when the notes link to that exact address (as the rendered link writes it), and at most one
address a second, notes links and "View full release notes" together; the code that starts the
browser refuses anything but `https://` itself. The notes webview refuses every download, on every platform,
and, on Linux only (the code that turns the menu off does nothing elsewhere), shows no context
menu. Since the document runs no script, only a person
activating a link navigates it.

### Release feed format

A feed is one JSON document, format 1. A later slice's fields in a release (for example its
signed artifacts) are ignored by readers that do not know them; any other change of meaning
takes a new format number, which this app refuses.

```json
{
  "format": 1,
  "releases": [
    {
      "product": "app",
      "version": "0.3.0-preview.1",
      "channel": "preview",
      "published": "2026-11-05T09:00:00Z",
      "notes": "## What changed\n\nMarkdown text.",
      "notes_url": "https://example.invalid/releases/0.3.0-preview.1"
    }
  ]
}
```

| Field | Rule |
| --- | --- |
| `format` | `1` |
| `releases` | At most 256 |
| `product` | `app` or `core` |
| `version` | A semantic version (2.0.0) without a leading `v`, at most 64 characters; one release per product and version precedence |
| `channel` | `stable` or `preview`; a stable release has no pre-release part |
| `published` | RFC 3339 in UTC, ending in `Z`, on a day its month has; second 60 (a leap second) is refused |
| `notes` | Optional Markdown, at most 64 KiB |
| `notes_url` | Optional `https://` address with a host and no user name or password, at most 2048 bytes as given and once normalised (percent-encoded) |
| `update` | Optional, app releases only: signed update artifacts by the updater's target name; the app reads `linux-x86_64-appimage` (an AppImage), `windows-x86_64-set` (the program set as a `.tar.gz`) and `darwin-aarch64-app` or `darwin-x86_64-app` (the app bundle as a `.app.tar.gz`). Each is `{"url": <https address, as notes_url>, "signature": <Tauri updater signature, base64, at most 4096 characters>, "size": <exact bytes, 1 to 1 GiB>}` |

The whole feed is at most 1 MiB. The feed is not signed: https is its only protection, and
nothing in it is trusted as markup. Signed update artifacts have their own signatures. Production
feed addresses, keys and formats are decided at release; until then there is no built-in feed.

### Application update

Version Management's App update card offers **Update now** when the feed's latest app
release on the chosen channel is newer than the running version and has an artifact for this
system, and says why not otherwise. It uses the stock Tauri updater plugin
(`tauri-plugin-updater` 2.14.1) for its release check and signature check only, never for
installing, and puts the new version in place itself (`src-tauri/src/updater/`). What it replaces
is the installed copy:

- Linux: the AppImage (`updater/appimage.rs`);
- Windows: the program set of a folder bundle, or of a copy the per-user setup program installed
  ([Packages](#packages)), in a folder of yours: the app's program (one self-contained `.exe`),
  the core beside it (`pluxy.exe`) and the core's packages (`bundled\`) (`updater/exe.rs`,
  `updater/set.rs`). Anything else in the folder, such as the setup's `uninstall.exe`, is not
  part of it;
- macOS: the `.app` bundle (`updater/app.rs`).

In this order:

1. The updater gate ([below](#the-updater-gate)) must pass. The plugin is registered only when
   the gate passes as the app starts, and the gate runs again before each use: a build without
   updater trust never loads it and says the updater is not configured.
2. The installation must be one this updater can replace, or nothing is downloaded, even when the
   feed has a signed update:
   - Linux: the bundle type Tauri's bundler compiles into the program is AppImage, `APPIMAGE`
     names an absolute regular file (not a link) of this user's that no one else can write to, in
     such a folder, with a type-2 AppImage header, and the program runs from inside it (`APPDIR`).
     A .deb or .rpm install is refused with how to update through the package manager, and
     anything else as an unknown format. The stock plugin would itself run `pkexec dpkg -i` or
     `rpm -U` for those.
   - Windows: the program carries no bundle type, or the one the per-user setup program patches
     into the copy it installs (an .msi patches in its own: such a copy is refused with how to
     update through the .msi; an unknown type is refused too), is on a fixed local drive (not a
     network share or a mapped drive), and it and its folder are plain (no link or junction),
     yours, and writable by no one else (SYSTEM and Administrators aside). The stock plugin would
     run the installer for those, and nothing here does: no installer, `msiexec`, elevation,
     scheduled task or service. Two copies of the app with one identifier share their update
     records, so an update or rollback that another copy left unfinished (its `pending.json`) is
     left alone and refuses this one until that copy starts and finishes it (or, if that copy is
     gone, the file is deleted); so does one of this copy's own whose parts are still aside.
   - macOS: the program runs as `<name>.app/Contents/MacOS/<program>` of a bundle whose
     `Info.plist` names this app's identifier and that program; it was started by a path with no
     link in it, neither the program nor any folder above it (Tauri refuses such a path on macOS,
     and the plugin needs it: start the app from its real location, for example from Finder); the
     bundle is not running from a translocated copy (macOS runs an app opened from Downloads or a
     disk image from a read-only copy: move it into a folder of your own such as `~/Applications`
     first); the bundle is yours and writable by no one else, neither by its mode nor by an
     extended access list entry that lets another account than you or root do more than read
     (deny entries, such as the stock `group:everyone deny delete`, are fine); its folder is
     either yours and writable by no one else in the same way, or the stock `/Applications` under
     the rule below; and the volume is APFS, which swaps two folders in one step (an app run from
     its mounted disk image, an HFS+ volume, is refused for that: drag it to Applications first).
     The stock plugin would ask for an administrator's
     password when it cannot write the bundle; nothing here does (no `osascript`, `sudo`,
     `installer` or `authopen`), and the quarantine attribute is neither added nor removed.

     A bundle in `/Applications` (`updater/applications.rs`) is updated when all of these hold,
     each read through the folder, and the bundle in it, opened without following a link:
     - the folder's path, links resolved, is exactly `/Applications` (not a folder inside it);
     - it is owned by root, its group is the one named `admin` (looked up when the app checks,
       not a fixed number), and others cannot write to it;
     - no allow entry of its access list gives anyone but root or the `admin` group more than
       reading, entries that only what is put in it inherits included; entries are told apart by
       their UUIDs, so group 0 (`wheel`) is not taken for root;
     - the bundle is a folder of yours that no one else can write to, and no allow entry of its
       own access list gives anyone but you or root more than reading;
     - the app's data folder, where the update is unpacked and checked, is on the same volume;
     - your account is in `admin`. Otherwise the page says, whoever owns the bundle: "pluxy is
       in /Applications, which only administrators can change, and this account is not one. Move
       pluxy to Applications in your home folder (~/Applications) with Finder, open it from
       there, and update then."

     Any other refusal says which condition failed and what to do: for the folder, or when what
     the rule reads cannot be read, the same advice; for a bundle that is not yours, to copy it to
     `~/Applications` and open the copy; for a bundle others can write to, the `chmod` that takes
     their write permission or the access list entry away.

     The folder's other writers are this Mac's administrators, and any of them can already move
     your bundle aside and put one of their own at its name, which your next start runs; the
     updater adds nothing to that. It unpacks and checks the update in your private data folder,
     makes one change in `/Applications` (it swaps the bundle's own entry, through the folder it
     checked) and never lists or deletes anything there.
3. The plugin restores the release from the feed (`restore_update`): its default comparison
   allows only a version newer than the running one, and the signature must carry the announced
   version. No custom comparison is set, so the feed can never offer a downgrade.
4. The app downloads the artifact itself, as it reads the feed (https only, no redirects, the same
   certificate roots), exactly the announced size, showing progress. A server that sends nothing
   for 30 seconds fails the download; **Cancel download** stops it within that time and changes
   nothing.
5. The plugin checks the signature against the compiled updater key. Then the bytes must be this
   system's: a type-2 AppImage; on Windows a `.tar.gz` of the program set; or, on macOS, a
   `.app.tar.gz` holding one `<name>.app` folder. Either archive holds folders and regular files
   only (no links, devices, `..`, absolute paths, or setuid, setgid or sticky bits; at most 4096
   entries and 1 GiB) and is unpacked into a new staging folder of yours: on Windows
   `.pluxy-update-<hex>` inside the program's folder, taking that folder's access list, and
   holding at its top exactly the program (by this program's own name), `pluxy.exe` and the
   folder `bundled`, every name one Windows holds as it is (no device name such as `CON`, no `:`
   stream, no trailing dot or space, no backslash, none in the form of a short (8.3) name such as
   `PACKAG~1`, which may be another entry's second name) and no two that differ only in case,
   with a 64-bit Windows program (not a library) for this processor; on macOS a staging folder
   named `updates/staging/<hex>` in the app's data folder when that is on the bundle's volume, and
   only if every folder from your home folder down to it is yours, not a link, and writable by no
   one else, by the same rule as the bundle's folder (mode and access list; checked for the
   folders that exist before anything is made in them, and again once the staging folder is made;
   otherwise nothing is unpacked, and the page says which folder to fix); only on another volume
   is it `.pluxy-update-<hex>` next to the bundle, and only when the bundle's folder is yours and
   writable by no one else (in `/Applications` the update is refused instead); the staged `.app`'s
   `Info.plist` names this app's identifier and program and the announced version
   (`CFBundleShortVersionString`), with a program for this Mac.
6. The running version's copy is kept in the app's data folder (`updates/kept/`, the last three
   versions; on Windows the local data folder, on a fixed local drive, so it stays on this
   computer when the profile roams), pinned in `updates/history.json` by its SHA-256 (on macOS, a
   digest of the bundle's whole tree: every folder, file, size, content digest and execute bit; on
   Windows, a digest of the program set: every folder and file of its three parts, with sizes and
   content digests, and nothing else in its folder), and `updates/pending.json` records the
   install (on Windows with the parts it moves, before any moves). The app records that digest as
   it starts; if the installed copy has changed since, nothing is kept or installed and the page
   asks for a restart first. Then the new version goes in place:
   - Linux: a temporary file in the same folder, flushed to disk, renamed over the AppImage, and
     the folder flushed: the AppImage is the old one or the new one.
   - Windows: part by part, `bundled\` first, then `pluxy.exe`, then the program: each part is
     renamed aside (`.pluxy-old-<hex>-<part>`) and the new one renamed into its place from the
     staging folder, each rename written through to disk. A running program can be renamed but
     not replaced, and nothing runs the core files beside the app (the app runs its own core from
     its store), so the parts move while the app runs. A part another program holds (a virus
     scanner, or a file open in `bundled\`) is waited out for 5 seconds; if it is still held, the
     parts already moved are put back. Should the app stop part way through (a crash, a power
     loss), the earlier version undoes it as it next starts, before it takes its start digest or
     removes anything: it puts each part aside back, in reverse order, each only if it matches the
     same part of the kept copy, and only when the version starting is the one the install came
     from, started from the program its kept copy holds, in the folder the record names. A part it
     cannot put back is left as it is, with the record; that version then installs nothing and the
     page says why. `pending.json` and the record of what is left aside are written through to
     disk too before any part moves, so the record the undo needs is there after a power loss.
     Between the program's own two renames the folder has no program under its name: should the
     app stop right then (a crash, a power loss), rename `.pluxy-old-<hex>-<program>` back to the
     program's name and start it; that start puts the other parts back by the record. What is
     left aside, and the staging folder, are removed once the new version runs and the old one has
     ended.
   - macOS: the unpacked bundle and the installed one swap places in one step (`renameatx_np`
     with `RENAME_SWAP`, by name in the staging folder and the bundle's folder, each opened
     without following a link, the staging folder next to the bundle through the bundle's open
     folder; read through those open folders, the bundle's folder must still pass the rule that
     admitted it (yours and writable by no one else, by its mode and its access list, or the stock
     `/Applications` with the staging folder private, not next to the bundle), and the staging folder still the one made, yours and
     0700): the bundle is the old one or the new one. With private
     staging, nothing else in the bundle's folder is made, changed or removed. The old bundle, now
     in the staging folder, is removed once the new version runs and the old one has ended.

   What an install leaves (on Windows beside the installed copy, on macOS its staging folder) is
   recorded in `updates/leftovers.json` before the install goes on, with what identifies it beyond
   its name: the staging folder right after it is created (read through one open of it, which must
   be an empty folder and, on macOS, yours alone, and is given an empty access list there, so it
   takes no entry from its folder; by its device and inode on macOS), and, on Windows, each part
   just before it is renamed aside (by its volume serial number and file ID). When the record
   cannot be written, the install deletes what it just made and stops. Should the app stop in the
   moment between making the folder and recording it, that empty folder stays unrecorded, and no
   later start removes it. Removal at a later start goes through the record only. An entry that is
   still the recorded file or folder (and, on macOS, a folder of yours) is removed without
   following a link: on Windows through its open handle, a folder emptied through it first (each
   entry in it opened relative to it, a link or junction removed as a link); on macOS what is in it through the open
   folder, never entering a folder in it whose device differs from its own (a file system mounted
   there; a mount that keeps the device, as a bind mount from the same file system does, is not
   seen), then the emptied folder by its name only while that name still holds it. An entry whose
   name is gone or now holds something else (on Windows a folder too) is dropped from the record
   and nothing at that name is deleted; one still in use, or with a file system mounted in it,
   stays recorded for the next start, and so do, on Windows, the staging folder and aside parts of
   an install whose `pending.json` is not finished. A failed or cancelled install removes what it
   made the same way, only while it is still what it made. Neither the folder the installed copy
   is in nor the private staging folder is ever listed, so whatever someone else put there, even
   under one of these names, stays.
7. The app stops as Quit does (its native tools, its managed runtimes and its own core stop, in
   that order; detached launches run on) and hides its window, but keeps its profile and starts the
   new version as its successor, handing the profile over so no other launch takes it meanwhile
   ([One app per profile](#one-app-per-profile)); the tray stays responsive while it waits. On
   macOS the new version's program is started directly, not through Launch Services. The app
   holds that process (on Linux and macOS in a process group of its own, on Windows by its process
   handle, outside the app's own jobs) and waits up to 90 seconds for it to accept itself: once it
   has started, with the installed copy's digest the one pinned, it writes
   `updates/accepted.json`, and the app lets it go and exits. If it ends first or does not accept
   in time, the app ends that process (and only it, with what it started), puts the kept copy
   (checked against its digest) back the same way, the page later says the update failed, and the
   earlier version starts again with the profile. On Windows that put-back is recorded in
   `pending.json` first, with its own parts; should it stop part way, whichever version's program
   starts next finishes it, each part checked against the kept copy, and a version that is not
   the kept one then installs nothing until the app is restarted. If the kept copy cannot be put back, nothing is
   started, and the page says which copy in `updates/kept/` to put in place of the installed one.

   On Windows, a version that accepts itself after an update or a rollback then writes its own
   version as `DisplayVersion` into the per-user setup program's uninstall entry
   (`HKEY_CURRENT_USER\Software\Microsoft\Windows\CurrentVersion\Uninstall\<product name>`), so
   Settings > Apps and the setup show the version installed. It writes only when that entry
   exists and its `InstallLocation` names the program's own folder (its quotes and a `\\?\`
   prefix aside, compared without case), and never makes one: a folder bundle, or another install
   of the same product, is left alone. The setup program itself is never run.

**Roll back to <version>…** lists versions that ran here and were kept. It restores only such a
copy, only while it still has its pinned digest, and only after the native confirmation (with
the version, digest and installed copy); then the same restart and acceptance follow. It passes the
updater gate first, as Update now does, so a build without updater trust refuses it. A version
never installed here, or a kept copy that changed, is refused without a dialog. While its
confirmation is open, Update now and other rollbacks wait.

## Extensions on this device

The Device extensions page manages app packages (see
[the app package contract](app-host/README.md)) installed on this computer: page-only packages
(`app.tier` `web`) and curated native tools (`native`). They are apart from
the extensions of the core the app is connected to: installing one never installs it in core,
and core's Extensions page never lists it. Their pages are listed among the Settings topics with core's
extension pages.

A package comes from a release archive (the [protocol's format](../protocol/README.md#release-archives))
in one of two ways:

- from a file the person chooses in the native file chooser. The window only asks for the
  chooser and never names a path;
- from a registry named in `device-registries.json` in the app's config folder (on Linux
  `~/.config/dev.pluxy.desktop/`), as core's config names its registries. The file holds
  `{"version": 0, "registries": [{"name": "...", "url": "..."}]}`; each `url` is an index (the
  [protocol's format](../protocol/README.md#registry-index)) at an absolute local path or an
  `https://` address. The archive must have the size and SHA-256 the index lists for this
  computer's platform, and hold the ID and version it lists. An install that names no version, and
the version the window offers, take the highest release the index lists for this platform, whatever
order it lists them in: the highest stable version, or the highest prerelease when it lists no
stable one. A version that is not a semantic version is never taken that way; it installs only when
named. An index may also list the
release's publisher signature (`key_id`, `public_key`, `descriptor` and `signature`, see
[the app package contract](app-host/README.md#signed-descriptor-bytes)): all four or none. The
signature is checked before the archive is unpacked and kept with the release, for Access.

Before anything is kept, the app checks the archive: regular files and folders only, no links,
devices or names that leave the folder, at most 10,000 entries and 512 MiB unpacked. The
manifest must be a valid manifest version 1 with an app contribution for host contract 0: a web
one with an existing file for each page, or a curated native one (below). The app unpacks it into a staging folder and moves it into an
immutable store, `device-packages/store/<archive SHA-256>/` in the app's data folder, with
read-only files. The catalog (`device-packages/catalog.json`) records each package's source and
each release's version, digest, sizes, formats, pages and the SHA-256 of each page file. A catalog
holding a native tool is saved as version 1, so an app from before native tools, which reads only
version 0, moves it aside with a warning instead of reading the tool as a page package.

A registry is curated when its index address equals the `source` of a native-tier trust root
exactly. This build compiles no roots (production roots are a release decision), so nothing is
curated outside a test build with a test trust profile. A package from any other source is
labelled **Unverified**, and one whose ID claims a reserved publisher (`pluxy`, or any publisher a
root names) is refused, before a registry's archive is fetched and again after a local archive is
unpacked. A package installed before a root reserved its publisher is disabled when the
catalog opens, with a warning, and cannot be enabled while the app's roots reserve its
publisher and its release does not verify as curated against them. A package keeps the source it was installed from: a release from another source is
refused until the package is removed. A web package from a curated registry whose index entry
carries a descriptor signed by the root's key, as for native tools below, installs as **Curated**;
only such a package may call native tools (below). Packages get no settings here: their pages'
settings calls answer `unavailable`. Web packages get files and folders only through Access, below.

A native tool installs only from a curated registry whose index entry carries a descriptor signed
by the root's key for that publisher and source, and names that key as its `public_key`; the
signature, then the archive's size and SHA-256, are checked before it is unpacked, and the
manifest's ID, version and tier after. A curated registry lists only its root's keys: any other
signature in it is refused, whatever the package's publisher. A signature another registry lists
is checked the same way with the `public_key` the index names, as a web package's (a native tool
signed so is refused), and kept for Access; the package stays **Unverified**. The Device
extensions page marks an entry the index already shows install would refuse. Its
program for this computer must be an executable file in the package, and the SHA-256 of every
file is recorded. It is labelled **Curated** and installs disabled. Enabling it opens a native
confirmation that says it has **full access to your user account** and shows its ID, source,
digest, signer, contracts, operations and program; only Confirm there enables it, and the catalog
records that admission against the release, source, signer and key. Disabling, updating, rolling
back or removing it ends the admission, so a new release asks again. A native release replaces
another only if it reads the state version the other writes.

The program host (`src-tauri/src/program.rs`; on Linux, Windows and macOS) runs an admitted tool only on demand: at its
first call from the window, from what `Catalog::admitted` returns then, after checking the
admission, the root, the signature and every installed file again. It opens the program, checks
its SHA-256 against the record and runs that opened file, in its package folder, with a cleared
environment, only its three private pipes, a process group of its own (on Windows a job object)
and on Linux a parent-death signal, from a supervisor thread that stays until the tool has been reaped. Calls follow [app program
contract v0](app-host/README.md#app-program-contract-v0), one at a time. When the last call
ends, the tool stops after 30 seconds idle; closing the window cancels its calls, so their tools
stop when idle. Any change to its package (disable, update, roll back, remove) and quitting the
app stop it at once, and once the app is quitting no call starts a tool. Stopping closes its
stdin, kills its whole process group after five seconds and reaps it: no process or thread of it
is left. If the app itself dies, the tool gets SIGTERM on Linux, sees its input close on macOS,
and ends with its job on Windows ([Windows and macOS](app-host/README.md#windows-and-macos)), so
a tool must end the processes it started when its input ends and on SIGTERM. Pages, including the tool's own,
have no program command and no route to its pipes; they reach operations only through the router,
below.

Every call of a native tool's operation goes through the router (`src-tauri/src/route.rs`):
from the window, from a page (its `native.call` method, answered in the page's own webview) and
from another native tool (a call on its pipe). The router checks each call against the catalog at
that moment: a tool calls only what it imports and its target exports, under an accepted export
contract; a native tool's page calls only its own tool; a curated web package's page calls only
its imports, once the person allowed them in a native confirmation from the Device extensions
page (**Allow native imports**, revoked with **Revoke native imports**). An Unverified package
never gets that. A native tool is enabled only when its required imports are enabled tools that
export them, and only if its imports lead back to it nowhere; its enable confirmation lists them.
The host sets each call's chain (the requester, then each tool that called on), refuses a call
back into the chain or a chain of more than five, and asks a native confirmation naming the whole
chain for each `write` or `launch` call. [Calls between packages](app-host/README.md#calls-between-packages)
has the manifest fields, frames and rules.

A native tool may also produce and consume declared kinds of data as jobs (`produces`,
`consumes`). A consumer lists the registrations of a kind it consumes, which the catalog gives as
the producers it admitted (package, release digest, generation, source, signer), and starts a job
of one through the router, as a call; the host then delivers the producer's batches to the
consumer's deliver operation with their source, at most four unacknowledged, and cancels the job
when either package changes (`src-tauri/src/jobs.rs`). [Jobs](app-host/README.md#jobs) has the
rules.

Secrets the person types into native key entry (`vault_secret_add`) are kept in the app's
private vault. An operation's declared secret fields carry them: the window names a secret by its
ID, a confirmation names each secret and the tool that gets it, and the tool receives the secret
on its pipe, or only a reference to it, which it may pass on only in that call and only to an
operation its import allows. Secret values and references are redacted from tools' logs, status,
errors and outputs. Tools keep backups and journals in the vault, and a write a tool or the app
did not finish is reconciled at its next start, never written again.
[Secrets, references and the vault](app-host/README.md#secrets-references-and-the-vault) has the
details.

Updating or rolling back a native tool that is enabled swaps it while it serves on. The new
release is staged and shown in its own native confirmation; once confirmed, the program host
starts and describes it, with no call. Only then is it committed, enabled and admitted, and do new
calls go to it, while the old release's tool finishes the calls it has (for at most ten minutes)
and stops. Calls to one tool run one at a time, whichever release they reach. A cancelled
confirmation, a package disabled or removed while it was open, or a release that fails to start
or describe itself, installs nothing and leaves the old release as it was. A tool that crashes is
started again only by a call after a backoff (one second, doubling to a minute); five crashes in a
row, with no clean stop between them, quarantine it until it is disabled and enabled again, or
updated. A call the tool took and never answered has an unknown outcome: it is
never sent again, and the tool's next start is told of it.

Installing a newer release keeps the one it replaces as the last good release. A candidate that
fails any check leaves the active release, its files and its open pages as they were. **Roll
back** makes the last good release active again, after checking that its files are unchanged.
Starting the app with `--safe-mode` lists no page of a package on this device and opens or serves
none, and admits no native tool; the Device extensions page still shows and manages them.

A device page opens like a core page, in a page webview with the app's page document, but its
frame loads from the `pluxy-app:` scheme. That scheme serves a page webview only the files of the
release folder it was opened for, only while that release is active and enabled, and only while
each file still has the SHA-256 recorded at install. Each file is served sandboxed, as core's
pages site does: scripts, styles, images and fonts from the scheme only, no requests from script,
no frames or forms. On Windows WebView2 serves the scheme as `http://pluxy-app.localhost`, so the
policy names that origin, which also lets the page ask for `https://pluxy-app.localhost`; the
app serves nothing there, so a page webview answers every `https://pluxy-app.` request with 403
itself, before it leaves the app. The webview may not navigate its frame out of that folder. Installing,
rolling back, disabling or removing a package closes its open page webviews at once; the window
then opens the new release's page, if the page is still listed.

## Access (Linux)

Access lets a page-only package on this device read files and folders you choose for it, through
a broker in the app. A package asks with `app.resources`: each resource is a `file` or a
`subtree` (a folder), with the operations it wants. This app grants `read` of a file, and `list`
and `read` in a folder; other operations are shown and never granted. A declaration grants
nothing by itself, and a page core lists gets no Access at all.

Granting takes three things, each in native UI the window can only start:

1. **A signed web release.** Only a web package's release whose registry listed a publisher
   signature can be granted files; one installed from a file, or listed unsigned, is refused, and
   so is a native tool or a package whose publisher a trust root reserves, whoever signed it.
2. **An approved signer.** The package's detail on the Device extensions page shows the signer
   (publisher, key ID and full public key) and the registry it came from. **Approve signer…**
   opens a native confirmation showing them. The approval holds for that key from that registry
   only: the same package, key and bytes from another registry start with no approval and no
   grants.
3. **A grant.** **Grant…** next to a resource opens the native file chooser (the desktop portal's
   when GTK uses portals) for a file or a folder. A folder that is or holds the root folder,
   your home folder or the document portal's store is refused. So is anything that is, holds or
   lies inside one of the app's own folders: its settings folder (`connection.json`,
   `access.json`), its data folder (the vault, the extension store, native tools' data), and its
   cache folder; one not made yet counts from the folder that would hold it, so a folder above
   it is refused too. The root folder, your home folder and the app's folders are recognised by their
   identity (device and inode), not their path, so another path to them, such as a bind mount
   or a link, is refused too; a bind mount of a folder *inside* one of the app's folders, on its
   own, is not recognised. A native confirmation then shows the package with its source, digest and signer, the
   resource and its operations, what you chose and what it resolves to, and says the package's
   pages can send what they read anywhere they can reach. Cancelling the chooser or the
   confirmation grants nothing.

A grant binds the package, its source, its release digest, its signer and the resource to what
you confirmed: the path you chose, the file or folder it resolves to and that target's device,
inode and mount (Linux's mount ID), and, when you chose a link (as stow and chezmoi make), the link's own identity and text.
Each read checks all of it again. A newer release, a rollback to another release, or the same
release from another source is not granted what the first was; granting again replaces the
grant. A link that points elsewhere, or a target replaced (as editors save, by rename), refuses
reads with `changed` until you grant it again, even if it is put back. An error that may pass
(permissions, an I/O error, too many open files) answers `unavailable` and ends nothing.
Removing the package removes its grants. A grant whose target has come to be, hold or lie inside
one of the app's own folders since it was granted (such as a folder granted before the app's
data folder was moved into it) refuses reads with `refused`: this is checked before every read and
listing, also one a handle opened earlier would serve.

Pages read with three channel methods, answered by native code, never by the window, so the
bytes never reach the window: `resources.status`, `resources.read` and `resources.list`
([the app package contract](app-host/README.md#access-for-web-packages)). The page webview's
native binding names the package and release; nothing in the call can name another. A file
grant reads that file only. A folder grant reads through a folder handle (`cap-std`) opened on
the granted folder: `..`, absolute paths, empty parts and links that leave the folder are
refused, while links inside it are followed. Nothing on another mount is read or listed, such as
a filesystem mounted inside the granted folder. Only regular files are read, at most 1 MiB per
call. A listing stops after 1,000 entries and says it was cut. Hard links inside a granted
folder disclose the files they name.

**Revoke** removes a grant and bumps a revision every read checks before it answers. A read
already under way answers `revoked`, not its bytes, and the revocation answers only once such
reads have finished. A revocation cannot take back what a page already read.

A file the document portal offers (a path under `$XDG_RUNTIME_DIR/doc/`) is granted for this
run of the app only: its grant is never saved, so the next launch asks again. The document
portal's D-Bus interface (`org.freedesktop.portal.Documents`, version 5) takes persistence only as
a flag when a file is added and has no call that reports it, so no portal grant is kept. When the portal stops offering the file, reads answer `unavailable`;
the broker never reads the file behind it.

Grants and approved signers are in `access.json` beside `connection.json`, readable only by your
user (mode 0600, folder 0700, and on macOS no extended access list entry on the folder, so the
file inherits none), written by an exclusive temporary file, sync and rename. A damaged
or unsupported file is moved to `access.json.corrupt-<time>` and Access starts with no grants; if
it cannot be moved, Access refuses grants and reads until it is fixed. Handles opened for reads
are kept while the app runs; after a restart each target is opened again and its identity
checked.

This is a check that cooperates with the person, not a sandbox: another program of the same user
can change files between a check and a read.

## Native confirmations

Native code prepares each immutable action with its requester chain, executing package, full
source/digest/signer/contract/generation, selected and resolved file identities, and launch recipe
where applicable. The main window can ask to review that preparation's opaque ID. A native dialog
(GTK on Linux, Win32 on Windows, AppKit on macOS) shows the action and, for a native tool, “full access to your user account”. Only its native
response handler issues approval. No IPC command accepts an approval result, gesture flag or token
consumption request. Page webviews have no confirmation capability.

The dialog offers **Remember this exact executable, argv and cwd**, unchecked by default for each
launch review. Remembered launches are stored privately in `remembered-launches.json` next to
`connection.json`. The tray's **Remembered launches…** menu opens native settings with individual
revocation buttons. Approval binds the whole caller chain, executor, terminal adapter, ordered
argv, opened cwd identity, environment-profile revision, reviewed environment changes and permitted
secret-reference uses. Native callers must never put secret bytes in argv or settings.

The settings file has a format version. Damaged, unversioned or unsupported settings are moved
to a sibling `remembered-launches.json.corrupt-<time>` file. The app starts with no approvals,
a native notice and a log message. Symlinks, files owned by another user and
group/world-writable files are refused; remembering stays disabled if the unsafe file cannot be
safely preserved. Newly created settings folders use mode 0700 and files use 0600. On open and
save, an existing folder owned by this user is tightened to 0700 if group/world-writable; a folder
owned by another user is refused. On macOS the folder is also emptied of extended access list
entries, and a settings file with any entry is refused like a group/world-writable one. Disabled
persistence hides Remember. If saving fails after confirmation, the one-time token is still issued and a native notice/log reports the failure.
On Windows the same checks read the owner and access list instead of the mode: the file and
folder must be owned by this user, by the owner this process gives what it makes (for an elevated
administrator, the Administrators group) or by SYSTEM, and have a protected access list (one that
takes nothing from its folder's) whose allow entries are only this user's and SYSTEM's; a missing
access list is refused. A new or unsafe folder, and each file the app saves, gets such a list
(the folder's inherited by what is made in it). Reparse points (symbolic links and junctions) are
refused as symlinks are on Unix. The app tools' file library (`next/app-tools/lib/files`) uses the
same rule.

An executable is opened before checking its canonical path, device/inode, size, mtime, ctime and
mode (on Windows: the volume serial number and file index, size, last write and change times, and
attributes; a program is a file ending in `.exe`, `.com`, `.bat` or `.cmd`). SHA-256 is computed once for that metadata and cached in memory; unchanged metadata reuses
it. A new app process hashes once before using persisted approvals. Metadata changes or same-path
replacement require review again. A consumed token exposes the checked open executable and cwd
handles for a later launcher to use with `fexecve` and `fchdir`. The executable handle is opened
with `O_CLOEXEC`: in the child, a launcher must clear `FD_CLOEXEC` before `fexecve` of a `#!`
script, or Linux returns `ENOENT`. The interpreter sees `/dev/fd/N` as the script's path.
Wrapper scripts check only the script, not its interpreter or code it loads. This is a cooperative
local-file check, not a defense against malicious same-user metadata manipulation.

Each preparation records its native entry point's origin: Main, Tray, NativeUi, Page or Import.
Page and Import origins cannot use remembered approval, even with a fresh native gesture; this
rule is tested at the unit level. The router prepares a write or launch from a chain a page
started through Page Start, one with any tool hop in its chain as an Import, and only the
window's own direct call as a Main action; only the latter, started by a native gesture, may use
a remembered approval, and only those dialogs offer Remember. Gesture construction
is private to native handlers and fixtures, and a gesture expires after five seconds.

Page Start uses the separate native entry point, always opens a fresh dialog, permits one pending
confirmation per original package and one attempt per second, and shows a native notice with
requester, action and outcome, including refusals. Page/Import pending entries have their own
limit of 16; trusted requests have a separate limit of 64. Page dialogs focus Cancel and keep
Confirm disabled for one second after mapping. (On macOS a button takes the keyboard focus only
with full keyboard access turned on; Cancel is the default button there, which Return presses.)
A launch that opens a file in the person's own app takes the app the system picks: on Linux by
the file's content type, on Windows and macOS by its extension
([Launches and managed runtimes](app-host/README.md#launches-and-managed-runtimes)). Non-UTF-8 paths are refused before review;
Unicode format characters (including bidi and zero-width characters), control characters and
Pango line separators (U+2028/U+2029) are displayed as escaped Unicode.

The launcher ([The launcher](#the-launcher)) is a caller of this module. Host
callers must invalidate pending preparations/tokens when package, contract, environment or secret
policy records change, and prepare a new action. Tokens and pending reviews expire after 60
seconds, work once for their preparation, and fail on changed file identity or executable metadata.
Revoking a remembered approval also invalidates its outstanding tokens.

## The launcher

The Launcher page and the tray's **Start…** menu start agents and terminals, drawn from host data
(`src-tauri/src/launcher.rs`, `src/launcherPage.tsx`). They need the curated execution program
`pluxy/execution` ([app tools](../app-tools/README.md#execution)), installed and enabled on the
Device extensions page; without it the page says so.

A **new launch** names a client label, a kind (in a terminal or directly), a program (a name found
on the launch environment's `PATH`, or a full path), an optional executable override, arguments,
a working folder (the page offers the client's eight most recently used), a terminal (**Automatic**
or one found on `PATH`) and a lifetime (detached or managed; **managed is for direct launches only**,
since many terminals hand their windows to a server process and exit at once, so the app could not
stop an agent run in one: a launch in a terminal is always detached, and the host refuses it
otherwise). The execution program prepares the
executable, argv and cwd; the app's native confirmation shows them in full, with the program a
terminal runs, the lifetime and the environment, and offers **Remember this exact executable,
argv and cwd**. A remembered launch joins the page's list (client, command, folder, the execution
program's release that runs it) and the tray's Start… menu.

**Start** on the page, or a click in the tray's Start… menu, starts a remembered launch again in
one click, with no dialog, while it is exactly the launch remembered: the same executable and
program (path, file identity and contents), argv, folder, environment, secrets and execution
program release, from the same window. A terminal launch remembered as managed by an earlier
version is refused when started (**Forget** it and launch it again, detached). Anything changed asks again, and remembering it again
replaces the old approval; a page's Start or a launch through another package always asks. One
start of a launch per second; after each the app shows its launch notice ("Started …" or why it
did not), unless **Show a notice after a one-click start** is off. **Forget** revokes the
approval; so does revoking it in the tray's **Remembered launches…**, which also takes it out of
the menu. The list, the recent folders and the notice setting are in `launcher.json` beside
`connection.json`, readable only by your user; it holds no secret (a secret field holds a vault
secret's ID), and a damaged file is moved aside as `launcher.json.corrupt-<time>`.

On Windows, a launch can also start **in WSL**, in a distribution and as a user the app knows
([Tools in WSL](app-host/README.md#tools-in-wsl-windows)): `pluxy/wsl` prepares it inside the
distribution (the program found on the `PATH` that `wsl.exe --exec` gives, with no login shell, or
the full path given; the folder in the distribution, by default the user's home), and the execution
program launches that `wsl.exe` command. It opens in the Windows console (no other terminal is
offered there); a terminal launch takes no standard input and is detached only. The confirmation
shows where it starts and that the program inside the distribution is found when it starts, not
checked by the app, which cannot hold a file there. Its recent folders and its remembered launch go
by the client and the environment, such as **Claude Code (WSL Ubuntu/bob)**; a launch remembered
for one distribution or user never starts in another.

Detached launches keep running after the app quits, each in a transient systemd scope of its own
when a systemd user manager runs ([Launches](app-host/README.md#launches-and-managed-runtimes)). The
page lists the managed runtimes the execution program started, with **Stop** and **Restart** (each
confirmed natively, never remembered); disabling or removing a package with managed runtimes running
asks first, and cancelling leaves both as they are.

## The tool pages

The Claude Code, Codex and Claude Desktop pages share one frame (`src/clientPage.tsx`, styled by
`src/toolPage.css` on the kit's hairlines); the other tool pages below keep their own until they
move to it. Each is the window's own page, not an extension page, because Connect gives the tool a
key: only the window can name a vault secret for an operation, and the key's value never reaches
the page.

- **The head** says how the tool stands in the same six words as the Tools list (`Connected`,
  `Installed · not connected`, `Settings need update`, `Cannot change settings`, `Not
  installed`, `Not checked`), its version and settings file, and the one thing to press next.
  Whether the tool is installed comes from pluxy's own look at this computer (`GET /setup`, only
  when pluxy runs here); settings that are set up or refused mean it is; with neither, the page
  says `Not checked`.
- **The add-on first:** the page reads this device's packages before anything else. With the
  tool's add-on missing it says so and links to Add-ons; with it off it offers **Turn on add-on**
  (the native confirmation enables it); in safe mode it says tool add-ons are paused. It reads no
  file then. When the packages cannot be read, it says so with **Try again**, and reads and offers
  nothing until they are.
- **Reading:** the default settings file is read as the page opens, again for another WSL
  environment, and again when the person comes back to **Default**. Another file is chosen under
  **Settings file, key and backups** and read with **Read this file**; choosing another file drops
  the reading, and an answer for a file no longer shown, good or failed, is dropped. A read that
  fails after a good one leaves the reading on screen with a line saying it may be out of date, and
  nothing is written until a read succeeds (**Try again**).
- **Writes go to the file shown:** each write sends the pinned targets of the reading on screen
  (the add-on refuses a file that changed since). While a write or its confirmation waits, the
  file and environment choices are locked. Each write's `program_call` carries a ticket, and
  leaving the page withdraws a confirmation still open (`program_cancel`); a Connect whose key
  window closes after the page was left writes nothing.
- **Connect {tool}** is one press. It uses the vault's key bound to this add-on and to the pluxy the
  app is connected to (or the one the person chose); with none, it makes one with
  `client_key_capture`, named for the tool (`Claude Code`, `Codex`, `Claude Desktop`), in its
  own native confirmation, then writes the settings in the add-on's confirmation. A cancel stops
  quietly and a key that could not be made writes nothing. When the key was made but the write did
  not happen, the page says the key is kept, and the next Connect uses it, even before the key
  list is read again. Connect, Update and Save say each reason they are held, such as the app not
  being connected to pluxy. **Update {tool}** does the same for
  settings that point elsewhere or miss part of what pluxy needs, and **Save**, in the section
  edited, for a connected tool. **Disconnect** (the add-on's `clear`: pluxy's values go, the
  person's edits stay) and **Restore backup** (the copy from before pluxy first changed the file)
  ask only in the add-on's window. A file pluxy will not change shows why, with **Read this file**
  and **Restore backup**, and no Connect.
- **Models:** each page's model fields suggest what pluxy serves (`GET /models`, read once as the
  page opens) and say `Your accounts do not offer {model}.` under a model no account serves;
  Connect still writes it. **Remove model** asks first, inline.
- **Start** (one section for every tool, through the launcher, `pluxy/execution`): the folder,
  with the recent ones for the tool, the terminal, and **Start {tool}**, which, for a tool that
  starts in a folder, stays off until one is chosen (Claude Desktop starts with none); without the
  Launcher add-on it says so and links to Add-ons. The terminal list
  loads again whenever this device's packages change (`pluxy-device`); a chosen terminal the list
  no longer offers falls back to Automatic (`src/startCli.ts`). The launcher's confirmation shows
  each launch in full. No key is part of a launch: each tool reads it from its settings.
- **Settings file, key and backups** (collapsed; it opens itself on a file pluxy will not change):
  which file, the program to start instead of the tool's own, the key (`Created for {tool}` or
  `Entered by you`, its masked form, and **Use an existing key…**, which enters a key natively with
  `client_key_enter`, checked against pluxy and bound to the add-on), what Connect changes (the
  files, the address now and after, whether a key is in the file), **Restore backup**, and the
  backups (`src/backups.tsx`): **Create backup** saves the files as they are on disk in the app's
  vault through the add-on's standard backup operations
  ([app tools](../app-tools/README.md#libtransactions)), the newest 10 kept; choosing a saved copy
  shows whether each file was present and its size, and the settings restoring it would add,
  change or remove, by name only (never a value); **Restore** and **Delete** each ask in the
  add-on's window.
- **What the native side says** (`src/toolWords.ts`): every native call of these pages goes
  through `nativeReply`, which turns the native side's words into the page's (`core` becomes
  `pluxy`, a client key a key, the vault the app). In privacy mode the pages mask what they show
  of paths (the home folder's user name, and all but the last two parts), WSL users' names,
  account IDs, and the errors and reasons the native side gives.

`readExtraTools` (`src/clientPage.tsx`) reads Claude Desktop, Grok Build and Hermes Agent for
the Tools list in the app: each add-on that is on previews its tool's default settings; a reading
that has settings in it says the tool is installed and how it stands, and a reading of nothing set
up, or none at all, is marked `Not checked` unless pluxy's own look at this computer says whether
the tool is installed.

## Claude Code

The Claude Code page (`src/claudeCode.tsx`) needs the curated native tool `pluxy/claude-code`
([app tools](../app-tools/README.md#claude-code)). Its default file is Claude Code's user settings
(`~/.claude/settings.json`); the others are a file (a link, as stow makes, is followed and kept)
or a file in a folder. A file Claude Code would not take (not strict JSON, or another `$schema`)
is one pluxy will not change. **Models** holds Opus, Sonnet, Haiku and Fable (each with **1M
context**; Fable follows Sonnet when empty), **Starts with** (a model, or one of Claude Code's
aliases), **Subagents**, and under **Context and auto-compaction** the **Auto-compact window**, the
percentage it compacts at and **Turn off auto-compaction**. Opus, Sonnet and Haiku each need a model
(not only `[1m]`), and the window and percentage are whole numbers in Claude Code's ranges; until
they are, Connect is held and the head says why (`src/claudeCodeForm.ts`). **Start Claude Code**
asks the tool for its recipe (`claude` in a terminal, no arguments).

**In WSL** (Windows): the page's **Where Claude Code runs** section (`src/environmentBlock.tsx`)
chooses **Windows** or **WSL**, then a **Distribution** and a **User**. Choosing WSL lists the
distributions without starting any (`pluxy/wsl`, which must be installed and enabled); reading a
distribution's users starts it, which the section says. Picking a user has the app check that
distribution and user and keep its record ([Tools in WSL](app-host/README.md#tools-in-wsl-windows));
**Detect again** lists them again. Every call of the page then goes to Claude Code's Linux build
running in that distribution as that user, so the default file is that user's
`~/.claude/settings.json` and typed paths are paths in the distribution (a
`\\wsl.localhost\<distribution>\…` or `\\wsl$\…` path of the chosen distribution becomes its Linux
path; any other network path is refused there). The first use asks to **Install the Linux build for
WSL**, in its own native confirmation. **Start Claude Code** starts it in that distribution through
the launcher, in the Windows console. The page keeps its last choice for the window; the app checks
it against its records on every call. **Connect** makes and keeps the key on Windows, as on any
computer (the vault and its keys work on Linux, Windows and macOS), and the add-on's write in the
distribution gets it after its confirmation.

## Codex

The Codex page (`src/codex.tsx`) needs the curated native tool `pluxy/codex-setup`
([app tools](../app-tools/README.md#codex)). It reads and writes Codex's `config.toml` and model
list file together: by default in `~/.codex`, or two files (links followed and kept), or a folder
(such as a `CODEX_HOME`). Before Connect, when the provider Codex uses now is not pluxy's, the head
says that Codex hides conversations of another provider from resume, and that the app does not
change past conversations. **Models** holds the **Default model** (one from the model list; Connect
is held, with the reason, until there is one), the **Approval model** (Codex's default or a listed
model), **Remote compaction**, **Subagents** (collapsed) and the **Model list**: **Import models from
pluxy** reads what pluxy serves only when pressed and adds those Codex has a template for (the ones
it has none for are named, until the files are read again); each model's display name, description,
context windows, context used, compaction point, reasoning levels, input types, visibility, tool
search and system prompt can be edited, or the model removed. **Start** adds **Restart Codex
service** (after its own question, since it stops every running Codex session; it runs `codex
app-server daemon restart` detached) and **Start Codex App** (the program the person names, in the
folder chosen above: Codex has no Linux app).

## Claude Desktop

The Claude Desktop page (`src/claudeDesktop.tsx`) sets up Claude Desktop's third-party inference
gateway (its Linux beta). It needs the curated native tool `pluxy/claude-desktop`
([app tools](../app-tools/README.md#claude-desktop)). It reads and writes three files together: by
default in `~/.config/Claude-3p` (`claude_desktop_config.json`, pluxy's gateway profile in
`configLibrary` and `configLibrary/_meta.json`), or in a folder (which may be a link), or three
files (links followed and kept). When nothing is at `~/.config/Claude-3p/configLibrary` yet
(Claude Desktop makes it only once a configuration is saved in its own window), the head says so
and offers **Create settings folder**, in a native confirmation. **Models** lists up to 16 models,
each with an alias and **1M context**; three Claude models to start with. Claude Desktop takes only
Claude model names, so a non-Claude model needs an alias, which pluxy must serve too. For a row whose
alias pluxy does not list (`GET /models`), the page offers **Create alias in pluxy**: after a
native confirmation naming pluxy, the alias, the model and the accounts, the app adds the alias
through the management API (`PATCH /v0/management/accounts/{id}`, its `model_alias`) to every
account added by sign-in that serves the model. An account set in pluxy's settings file is changed
only there, so it is named and left alone, and a model only such accounts serve is refused; so is an
alias pluxy already serves for anything else. Disconnect and Restore backup leave an alias made in
pluxy. **Start Claude Desktop** asks the tool for its recipe (`claude-desktop`, run directly with
no arguments in the home folder); Claude Desktop reads its settings at launch, so quit and reopen it
after a change.

## OpenCode

The OpenCode page sets up and starts OpenCode with this core (`src/opencode.tsx`). It needs the
curated native tool `pluxy/opencode` ([app tools](../app-tools/README.md#opencode)) and, for
**Start CLI** and **Start app**, the execution program, both installed and enabled on the Device
extensions page. Like the Claude Code page it is the window's own page, because **Apply** gives the
tool the client key.

**Refresh** previews the config file: OpenCode's global config
(`~/.config/opencode/opencode.jsonc`, or `opencode.json` when only that exists, as OpenCode picks
the file it writes), a selected file (for example the one `OPENCODE_CONFIG` names; a link, as stow
makes, is followed and kept) or a file in a selected folder (for example `OPENCODE_CONFIG_DIR`). It
shows whether pluxy's integration is applied, the file it writes to, whether a key is in it and the
default model, never the key, and warns when the old desktop app's `cpa-gui` provider is still
there, or when OpenCode 2's own `providers.pluxy` is (OpenCode uses that one, so Apply stays off
until it is removed). A file OpenCode would not take is shown with the reason: Apply and Clear
stay off, Restore backup and the backups still work on it. Choosing another file
drops the last preview: every write uses the pins of the preview on screen. **Models** lists the models OpenCode shows under the pluxy provider: **Import models from
core** adds the core's served models, **Add model** one by ID; each has a display name, an
**Attachments** switch (images and PDFs; new models start from the old app's list of model names
that take images) and **Remove**; **Default model** is one of them. The **Client key** is
one of the vault's secrets (plain ones, or client keys bound to this tool), or a new one through
native key entry (**Add key…**).

**Apply** (or **Update** / **Repair** once applied),
**Clear** and **Restore backup** each preview the file again and call the
tool, whose native confirmation names the file it resolves to, the key's label for Apply, and that
an edit made in the same instant by another program may be lost; comments and everything else in
the file stay. **Backup and restore** is the shared backup browser. **Start CLI** asks the tool for
its CLI recipe (`opencode` in a terminal, no arguments; `~/.opencode/bin/opencode`, where OpenCode's
install script puts it, when that exists) and starts it through the launcher in the chosen working
folder (recently used ones offered), terminal and optional program path. **Start app** starts
OpenCode Desktop (`/opt/OpenCode/ai.opencode`, or OpenCode 1's `ai.opencode.desktop` there, where its
.deb and .rpm install it, or a given path such as an AppImage) in the home folder. Each launch's confirmation shows it in full; the key is not
part of a launch: OpenCode reads it from its config. There is no Restart App: the app never stops a
program it did not start, so quit OpenCode and start it again to load a new configuration.

## Oh My Pi

The Oh My Pi page sets up and starts Oh My Pi with this core (`src/omp.tsx`). It needs the curated
native tool `pluxy/omp-setup` ([app tools](../app-tools/README.md#omp-setup)) and, for **Start
CLI**, the execution program, both installed and enabled on the Device extensions page. Like the
Claude Code page it is the window's own page, because **Apply** gives the tool the client key.

**Refresh** previews Oh My Pi's two files, `models.yml` and `config.yml`: in its agent folder
(`~/.omp/agent`), in a selected agent folder (for example a profile's
`~/.omp/profiles/<name>/agent`, or the folder `PI_CODING_AGENT_DIR` names; the folder may be a link,
as chezmoi can make) or as two selected files (a link, as stow makes, is followed and kept). It
shows whether pluxy's integration is applied, the files it writes to, whether a key is in the models
file and the default model role, never the key, and warns when the old desktop app's
`easy-cliproxyapi` provider is still there. **Default model** is the model Oh My Pi starts with
through this core (the core's served models are suggested; **Refresh models** reloads them); Oh My
Pi lists the core's other models itself. The **Client key** is one of the vault's secrets (a captured
client key only when it is this tool's), or a new one through native key entry (**Add key…**).

Files Oh My Pi would not take show as refused, with the reason: **Apply** and **Clear**
stay off, and **Restore backup** and the backups stay available. Choosing other files drops the last
preview. **Apply** (or **Update** / **Repair** once applied),
**Clear** and **Restore backup** call the tool with the files the last preview pinned
(the tool refuses them if a file changed since), and its native confirmation names both files they
resolve to, the key's label for Apply, and that an edit
made in the same instant by another program may be lost; comments and everything else in the files
stay, including the person's own models under the pluxy provider. **Backup and restore** is the
shared backup browser. **Start CLI** asks the tool for its recipe (`omp` in a terminal, no
arguments; `~/.bun/bin/omp` or `~/.local/bin/omp`, where bun and Oh My Pi's install script put it,
when one exists) and starts it through the launcher in the chosen working directory (recently used
ones offered), terminal and optional program path. Each launch's confirmation shows it in full; the
key is not part of a launch: Oh My Pi reads it from its models file. Oh My Pi has no desktop app.

## Grok Build

The Grok Build page sets up and starts Grok Build with this core (`src/grok.tsx`). It needs the
curated native tool `pluxy/grok-setup` ([app tools](../app-tools/README.md#grok-setup)) and, for
**Start CLI**, the execution program, both installed and enabled on the Device extensions page. Like
the Claude Code page it is the window's own page, because **Apply** gives the tool the client key.

**Refresh** previews Grok Build's `config.toml`: its user config (`~/.grok/config.toml`),
a selected file (such as `$GROK_HOME/config.toml`; a link is followed and kept) or a file in a
selected folder; it shows the status, the file it writes to, Grok Build's default model now and
whether a key is in pluxy's entry, never the key, and notes model entries an older app left
(`cpa-gui/…`), which pluxy leaves alone. A file Grok Build would not take shows as refused, with the
reason: Apply and Clear stay off, Restore backup and the backups stay available. The **Model catalog** holds the models pluxy's entries
list: those the file has, and the core's served models, added (all selected, as the old app wrote
them all) when **Refresh models** is pressed, never in the background. Each can be left out or
given its **Context window**; the **Default model** is one of the selected models. Apply stays off,
with a notice, while more than 256 models are selected. The **Client key** is one of the
vault's plain secrets or this tool's client keys, or a new one through native key entry.

**Apply** (or **Update** / **Repair** once applied),
**Clear** and **Restore backup** call the tool on the file the last preview
pinned (changing the file selection drops that preview; the tool refuses a file changed since),
whose native confirmation names the file, the key's label for Apply, and that an edit made in
the same instant by another program may be lost; **Backup and restore** keeps the file in manual
backups, as on the Claude Code page. **Start CLI** asks the tool for its recipe (`~/.grok/bin/grok`
when Grok Build's installer put it there, else `grok` from PATH, in a terminal with no arguments)
and starts it through the launcher in the chosen working folder, terminal and optional program
path. The key is not part of the launch: Grok Build reads it from config.toml. Grok Build has no
Linux desktop app.

## Hermes Agent

The Hermes Agent page sets up and starts Hermes Agent with this core (`src/hermes.tsx`). It needs
the curated native tool `pluxy/hermes-setup` ([app tools](../app-tools/README.md#hermes-setup))
and, for **Start CLI**, the execution program, both installed and enabled on the Device extensions
page. Like the Claude Code page it is the window's own page, because **Apply** gives the tool the
client key.

**Refresh** previews Hermes Agent's `config.yaml`: in its default home (`~/.hermes`, or the
profile `~/.hermes/active_profile` names), in a selected Hermes home folder (the folder
`HERMES_HOME` names, or a profile's `~/.hermes/profiles/<name>`; the folder may be a link, as
chezmoi can make) or as a selected file (a link, as stow makes, is followed and kept). It shows
whether pluxy's integration is applied, the file it writes to, the provider's address, whether a
key is in the file, the selected provider and the default model, never the key, and warns when the
old desktop app's `cpa-gui` provider is still there. **Default model** is the model Hermes Agent
starts with through this core (the core's served models are suggested; **Refresh models** reloads
them). The **Client key** is one of the vault's secrets (a captured client key only when it
is this tool's), or a new one through native key entry (**Add key…**).

A file Hermes would not take shows as refused, with the reason: **Apply** and **Clear**
stay off, and **Restore backup** and the backups stay available. Choosing another file or folder
drops the last preview. **Apply** (or **Update** / **Repair** once applied), **Clear** and **Restore backup** call the tool with the file
the last preview pinned (the tool refuses it if the file changed since), and its native
confirmation names the file it resolves to, the key's label for Apply, and that an edit made
in the same instant by another program may be lost; comments and everything else in the file stay,
including the person's own settings under the pluxy provider. **Backup and restore** is the shared
backup browser. **Start CLI** asks the tool for its recipe (`hermes` in a terminal, no arguments,
which starts a chat; `~/.local/bin/hermes`, where Hermes's installer puts it, when it exists) and
starts it through the launcher in the chosen working directory (recently used ones offered),
terminal and optional program path. Each launch's confirmation shows it in full; the key is not
part of a launch: Hermes Agent reads it from its config file. Hermes Agent's desktop app is not
offered: its Linux packages are not released.

## Pi

The Pi page sets up and starts Pi with this core (`src/pi.tsx`). It needs the curated native tool
`pluxy/pi-setup` ([app tools](../app-tools/README.md#pi-setup)) and, for **Start CLI**, the
execution program, both installed and enabled on the Device extensions page. Like the Claude Code
page it is the window's own page, because **Apply** gives the tool the client key.

Pi reaches the core through the CLIProxyAPI provider plugin. The tool carries a reviewed copy of
it and installs it as a local Pi package in Pi's agent folder; it never runs `pi install` or npm.
**Refresh** previews Pi's files, `cliproxyapi.json`, `settings.json` and the plugin's model
cache `cliproxyapi-models.json`, in its agent folder (`~/.pi/agent`) or in a selected agent folder
(the one `PI_CODING_AGENT_DIR` names; the folder may be a link, as chezmoi can make), or as the
linked files in a folder (links, as stow makes, are followed and kept). Files Pi would not take
are still read: the page says why, leaves Apply, Clear and Uninstall off, and keeps Restore backup
and the backups. It shows whether the plugin is
installed and its version (with **Update available** when the tool carries another), whether
pluxy's integration is applied, the files it writes to, whether a key is in `cliproxyapi.json` and
Pi's default provider and model, never the key; it warns when `settings.json` still lists the npm
copy an older app installed, or when a folder of the plugin's name is not pluxy's. **Model** is the
model Pi starts with through this core (the core's served models are suggested; **Refresh models**
reloads them; the plugin itself lists the core's Codex catalog in Pi). The **Client key**
is one of the vault's secrets, or a new one through native key entry (**Add key…**).

**Install plugin** (or **Apply**, **Update** / **Repair** once installed) installs the plugin when it is not there and writes the core's
address, the key and Pi's defaults. **Plugin management** has **Update plugin** (the tool's copy
replaces the installed one) and **Uninstall plugin** (its entry and folder go; the connection
settings stay). **Clear** and **Restore backup** also remove the plugin's folder once
`settings.json` no longer lists it. Each of these previews the files again and calls the tool,
whose native confirmation names the files it resolves to (or the agent folder, for Update), the
key's label for Apply, and that an edit made in the same instant by another program may be lost;
everything else in the files stays. **Backup and restore** is the shared backup browser. **Start
CLI** asks the tool for its recipe (`pi` in a terminal, no arguments; `~/.pi/agent/bin/pi` or
`~/.local/bin/pi`, where Pi's installer puts it, when one exists) and starts it through the
launcher in the chosen working directory (recently used ones offered), terminal and optional
program path. The key is not part of a launch: the plugin reads it from `cliproxyapi.json`. Pi has
no desktop app.

## Agent usage and Codex sessions

Two more pages come from optional native tools' own pages, listed among the Settings topics with
core's extension pages once their package is installed and enabled: **Agent usage**, from the usage hub
`pluxy/agent-usage` and its parsers ([its README](../app-tools/agent-usage/README.md)), and
**Codex sessions**, from the read-only reader `pluxy/codex-sessions`
([app tools](../app-tools/README.md#codex-sessions)). They are extension pages in page webviews,
as above, and reach their tools only through the router.

## Setup

On a first run of a core (a new "Run core here" core is one), the window opens the kit's Setup
page ([`../ui`](../ui/README.md), `#/setup`) in place of the Dashboard when the window's address
names no page, as the web panel does:
what core found on its machine, the providers to install, an account, the clients, and Finish. It
opens once the app is connected and signed in, so the Connection page still comes first. After
Finish the page stays among the Settings topics and shows the same things as they are now.

The app adds one thing to the page. Under "Point a client at pluxy", a client core found gets a
**Set up** button beside "Show what to paste" when the window has a page for it and its optional
tool is installed on this device or offered by a registry, which is the list Beginner Mode
offers. Set up opens the client's own page, where the app writes the client's settings itself
after its native confirmation; a tool that is not installed or enabled yet is named there. A
client whose settings file pluxy would not change gets no button, only the paste text. The
paste text stays for every client. With a remote core the button is left out: the page then lists
the clients on core's machine, and the app sets a client up on this computer only.

`SETUP_CLIENT_PAGES` in `src/agentPages.ts` maps core's ID for each client to the ID of its
page here, and `setupClientAction` beside it decides which clients get the button. Beginner Mode
is still there and unchanged.

## Beginner Mode

Beginner Mode (`src/easyMode.tsx`, the first topic of Tools, so the Tools entry opens it) is the guided setup of the old app's Easy
mode, in its two steps. **Connect a model** is the Add Account page's own sign-in (every browser,
device code and API-key method core's extensions offer); it counts core's accounts, and **Next:
connect an agent** stays off until there is one. **Connect an agent** lists the agent clients this
window can set up (the eight client pages above, Claude Code to Pi) whose optional tool is
installed on this device or offered by a registry, and the launcher (`pluxy/execution`) that
**Start CLI** runs through: **Install** installs a tool from its registry, **Enable** opens the
tool's native confirmation ("full access to your user account"), answered in that dialog only,
and **Set up** shows the client's own page below, where its configuration is applied and the
client started as on that page. The guide writes nothing itself and keeps no draft: a cancelled
confirmation, or leaving the guide, leaves files and packages as they were; Disconnect (Clear,
on the pages not yet on the shared parts) and Restore backup on the client's page undo an apply.

A client joins the list by being an agent page: a window page that names the package it drives
(`AgentPage` in `src/easyMode.tsx`), listed in `agentPages` in `src/agentPages.ts`. The list itself
(`src/easyModeItems.ts`) has no per-client code.

### Manage Clients

Tools lists a client's page among its topics only while the person wants it there. By default (automatic) it
lists the clients whose tool is installed on this device, enabled or not, with the installed ones
first; a tool a registry only offers is not listed, since Beginner Mode is where it is installed. The
**Manage Clients** page (a topic of Tools) has a checkbox for every client with a search box, **Restore
automatic display** and **Save**; Save is off for an empty selection ("Keep at least one client
visible."). The choice is kept in the window's prefs (`pluxy.agent-clients`, on this device, like the
Themes page's layout choices), and saving automatic removes it. Whichever client page is open stays listed while it is
open, so hiding it never strands the person. Beginner Mode still offers every client, and the list
logic is in `src/clientListItems.ts`.

## Build and check

Bun `1.4.3`, a stable Rust toolchain (CI uses 1.97.0), and on Linux the WebKitGTK 4.1 and
libayatana-appindicator development packages. The app builds the kit from `../ui`, so install
that first. From this folder:

```bash
(cd ../ui && bun install --frozen-lockfile)
bun install --frozen-lockfile
bun run check          # tsc --noEmit
bun run lint           # Biome
bun run test           # unit tests
bun run build          # the window, into dist/, which the native build embeds
cd src-tauri
cargo test --locked    # unit tests, and each mode against a real core (needs Go)
cargo build --release  # target/release/pluxy-app
```

`cargo test` runs `tests/modes.rs`: each mode against a real core with fake extensions,
started by `go run ./e2e/panelcore` from `next/`, on random loopback ports in temporary folders.
To look at the app against a core with accounts in every state, usage and request logs, start
`go run ./e2e/panelcore -demo` ([how](../ui/README.md#a-core-with-data-to-look-at)) and give the
app its `base` and `key` as a core on this computer.

`bun run test:native` runs the native test: it builds the app with the `native-test` and
`artifact-test-trust` features and runs it on a private X server (`xvfb-run`) with a private session bus (`dbus-run-session`),
a fresh HOME and XDG folders, once in each mode. The app runs only a core in its own store, so
each managed run but the core steps' (below) starts with one there: the signed test release 2.0.0,
installed as a confirmed install would (`session_probe install-core`), whose bundled test
packages the sign-ins and pages need beside the core's program (a core's first start installs
none: the session installs them from the bundled registry before its steps); the startup runs and the
early-exit run instead adopt their core program first (`session_probe adopt-core`), which copies
the program alone. In each it checks that the window's webview
reaches every command, that at the top of an extension page's webview Tauri refuses every app
command and the plugin commands tried, and that no app command is reached from that webview
or its frame. It also runs a test-only sandboxed frame
that posts directly to the native scheme: over-rate, over-in-flight, oversize, unknown method,
invalid schema, repeated hello, wrong generation, explicit close and frame navigation while
the wrapper's close message is suppressed. A separate real HTTP pages-site probe records whether
that frame can reach the scheme, and navigation of that real frame closes native admission. It compares native handler arrivals,
admissions, forwarding and real core settings operations, with a successful call on the same page.
It checks that the page webview covers the page area, also after the window shrinks (when GTK
would still give it its old, taller natural height), with the window at the top and partly
scrolled, that
closing the window leaves no webview and
no web process, and that quitting stops only the app's own core. With the window closed, it
finds the app's real StatusNotifierItem through the session's tray watcher and sends the actual
Start/Stop/Restart menu events over com.canonical.dbusmenu. It kills only the reported owned
core as an on-demand crash fixture, after checking that the process is still the app's live
child. It checks recovery, the three-attempt cutoff, and that an explicit Start clears the
budget. It checks that a pending recovery cannot undo a manual Stop and that no recovery attempt
ran before that Stop. After each step it reads which of Start, Stop and Restart the menu
enables, including while an action is held busy. A click on a disabled item still reaches the
app's handler, which the test confirms from the handler's own refusal; in local and remote modes
every lifecycle click must be refused that way. It ends with a core running and checks that
the core does not outlive the app once it quits, whichever of the app's ways of stopping it
does so. Every tray operation asserts zero renderer processes and webviews.
A second managed run starts a stand-in core that serves on the socket the app hands it, says
where it listens, answers the app's readiness check and then exits, and holds the app's setup until that core's exit callback has run, before the app state exists. It
checks that the app still starts the real core again, with one recovery attempt, and that the
core does not outlive the app.
In separate runs, the startup sequence enables login registration through the main webview command,
checks the entry in its temporary XDG config folder, launches it with the real Desktop Entry consumer (gio launch), checks
zero windows/webviews and no web process, and verifies that the app's real tray item belongs to
its PID in the tray watcher's registered item list. It checks login with Silent launch on and
off, an app-menu launch with Silent launch on, managed-core startup, core startup off, foreign
core ownership, and removal. In the core-startup-off launch it waits past the first recovery
delay and checks that no core started and no recovery ran, then starts the core with the
tray's Start pluxy and crashes it: that core is recovered. A separate session with no watcher verifies that a silent login
opens exactly one window and webview. Visible launches are positive controls for the same
renderer-process count used by silent launches.

In managed mode the same run checks this device's extension catalog (`native/device.ts` builds
the archives and a local registry; `src-tauri/src/device_native.rs` drives the app). From the
registry, a reserved-publisher package is refused and an Unverified package installs and its page
answers through the channel; a newer release replaces it while its page is open, closing the old
page webview; a candidate needing another host contract is refused and the working page keeps
answering; the asset scheme serves the active release to its page webview and refuses it the
replaced release's files; roll back, disable, enable and remove each leave no page webview behind
and remove leaves nothing in the store. Through the native file chooser, driven with `xdotool`,
cancel changes nothing, a reserved-publisher file is refused and an Unverified file installs, and
its page stays open for the window's close check. With a valid test trust profile in its HOME,
this normal build still refuses a curated registry's signed native tool and installs that
registry's web package.

One more managed session runs a build with `artifact-test-trust` against a test trust profile
(`native/admission.ts` generates the publisher key, signs the native fixture and writes the
profile; `src-tauri/src/admission_native.rs` drives the app). From an uncurated registry the
curated signed entry and an unsigned lookalike are refused; from the curated one a package signed
by an unknown key and an unsigned one are refused; with the profile's roots emptied, then with its
marker removed, the signed tool is refused. Each refusal has an Unverified web install on the same
route as its positive control. The signed tool then installs disabled, the page shows its
full-access warning, Cancel in the real GTK confirmation leaves it disabled and not admitted, and
Confirm enables it with an admitted record naming its store folder, program, source and key.
Another curated source for it is refused; removed and installed from there, it starts disabled
again.

One more session with that build checks the program host (`native/program.ts` packs the
disposable summary tool, `src-tauri/examples/program_fixture`, into a signed package with a page;
`src-tauri/src/program_native.rs` drives the app and reads `/proc`). Installed and enabled it runs
no process, and a call while disabled is refused. Three calls from the window at once start it
once and each gets the expected summary from that one process, which leads its own process group,
sees only the contract's three environment variables (not a canary the app has) and only stdin,
stdout and stderr, and runs in its package folder; the command thread that started it has ended
while it runs on under its supervisor thread; its pipes are held by the app and the tool only. A
call is cancelled and the next call is answered by the same process. From the tool's own page
webview every program command is refused and none reaches the tool. Left alone it stops after 30
seconds with its child and the host's threads; the next call starts it again; disabling it stops
it at once. Enabled and running, it then goes on to the swap check (`src-tauri/src/swap_native.rs`,
with releases 1.0.0, 1.1.0 and an incompatible 1.2.0). A slow call runs on 1.0.0 while 1.1.0 is
installed and confirmed: 1.1.0 runs before the swap returns and 1.0.0 drains, the slow call is
answered by 1.0.0 and the next call by 1.1.0, after it; then 1.0.0 is reaped, and the install's
thread has ended while 1.1.0 runs on. A cancelled confirmation for 1.2.0 changes nothing; a
confirmed 1.2.0 is refused as incompatible and deleted, and 1.1.0 serves on in the same process.
Five crash calls in a row each fail with an unknown outcome; a call during each backoff is
refused and starts nothing; the next call after it is the restarted tool's first, and its describe
was told of the crash call; the fifth crash quarantines it until it is disabled and enabled
again. A call is cancelled, the tool stops after 30 seconds idle; a cancelled rollback changes
nothing, one confirmed after the tool was disabled from the window while its confirmation was
open is refused and starts nothing, and a rollback to 1.0.0 asks again and runs it. Running when the app quits, its process and child are gone afterwards.

In managed mode, another session runs the Access checks (`native/access-suite.ts`, which
launches the app twice, through the stages in `src-tauri/src/access_native.rs`, with fixtures from
`native/access.ts`: archives signed with an Ed25519 key made for that run, whose private half never
leaves the fixture builder). Its choosers are the desktop portal's (`GTK_USE_PORTAL=1`), driven
with `xdotool`, as are its native confirmations. An unsigned package and an unapproved signer are
refused a grant; cancelling the signer's approval, the chooser or the confirmation changes
nothing; a confirmed grant of a config-manager link reads its target's bytes in the page; a path
with a file grant, an undeclared or ungranted resource, another package, a page core lists (even
posting to the native scheme directly), `..`, absolute paths and a link out of a granted folder
are refused, while the folder lists and reads, also through a link inside it; a retargeted link
and a replaced target are refused until granted again; a read held in flight answers `revoked`
and the revocation waits for it; a document portal file is granted for the session only and
refused once the portal deletes it; a newer release and the same release from another registry
get nothing. The second launch reads the kept grant and has no session-only one. The suite also
checks every file the broker opened: never the files a page was refused or the portal file's host
file.

In managed mode, two more sessions run the window checks (`native/window.ts`, which launches
the app through the stages in `src-tauri/src/native_window.rs`), one with a tray watcher and
one without. They close the window as its window manager would (a GTK delete event) and answer the prompt in the window:
the default Minimize to tray closes it without asking; with Ask every time saved, Cancel keeps
it, Minimize to tray and the remembered choices close it with no webview or web
process left, and the app's own tray item (through the watcher and its menu, `session_probe tray`)
opens it again at the size and place it had; Exit app, from the prompt or the setting, quits and
stops the app's own core; the next launch opens where the window was. A pending test sign-in's
polls are counted while the window shows, while it is unmapped (as a window manager minimizes it;
GDK then reports it minimized) and once it is mapped again. Without a tray watcher, closing asks
the window first (as `no-tray`); a window that does not answer stays open with the core running,
and Exit app with Remember this choice quits and saves `ask_without_tray: false` with the Close
behavior unchanged; after that, closing quits without asking, with Minimize to tray or Ask saved.

One more session in managed mode (`native/privacy.ts`) runs the privacy mode checks
(`src-tauri/src/native_privacy.rs`) and then the themes checks (`src-tauri/src/native_themes.rs`):
the T3 Code theme file `ui/tests/fixtures/t3-docs-nightfall.json`, pasted on the Themes page and
added, paints the window in its canvas colour with the root's `dark` class, the picker lists it,
`GET /themes` through the app's host has it as `current`, Export saves `nightfall.json` in the
session's Downloads folder with the theme file's JSON (read from disk at the path the page shows),
and the next launch paints in it. The session ends on the Themes page in that theme.

One more managed session (`native/setup.ts`, with the stages in `src-tauri/src/native_setup.rs`)
walks [Setup](../management/README.md#setup) on the app's first start, on the build with
`artifact-test-trust`. Its core is a first run: nothing installed and no accounts, a bundled
registry that offers `pluxy/claude` as a pick (`native/core-steps.ts` `firstRunSource`), and a
stand-in `claude` printing `2.4.1 (Claude Code)` first on the PATH it inherits from the app; a
curated registry offers the Claude Code page's package (`native/first-run.ts`). The window opens
on Setup by itself (`#/setup`, "Set up pluxy"); "Found on this machine" names Claude Code 2.4.1;
the Claude pick is ticked; the Claude Code client row has "Set up" beside "Show what to paste";
Start installs the pick, which then says Installed, and `GET /packages` through the app's host
lists it; Finish lands on the Dashboard and `GET /setup` has `first_run` false. The next launch
opens on the Dashboard; Setup, opened, says "Setup is finished." and has no Finish button. The
session keeps a screenshot of each screen it holds (Claude Code found, after Start, finished) and
ends on that page in GTK's dark variant.

Then, in a session of its own (`native/instances-session.ts`), with the core program adopted in
each profile, it launches the app again and again on one profile and checks that each later launch exits 0 within 10 seconds, starts no app or core of its own (it counts the
app and core processes), and has the first show its window, also after the window was hidden and
after it was closed to the tray, and for a login launch (`--autostart` with Silent launch on) as
well; that the socket is in the runtime folder, where `instance/address`
says; that a launch with other `HOME` and `XDG_*` folders is an app and
core of its own; that after the first app is killed with SIGKILL (its lock file and socket stay)
the next launch becomes the app, starts its core and is the one a later launch raises; and that
the socket and its address are gone when the app quits. The other profile's data folder is too long
for a socket path, and a second launch of it still raises it.

Then, in another session (`native/update.ts`, `native/update-session.ts`, with the cases in
`native/update-cases.ts` that the platform runner's update session shares, and `update_native.rs`),
the [application update](#application-update) on one profile with a test trust profile, against
a fake https feed whose releases the check switches: the test package refuses Update now without
its trust profile's nonce, and as its own program with the bundle type a .deb, an .rpm or no
package gives it (patched as Tauri's bundler patches it; the .deb one with `APPIMAGE` pointing at
the AppImage), with a rollback refused for the same reason each time, before any download and with stand-ins for `pkexec`, `sudo`, `dpkg`, `rpm`, `apt`,
`dnf` and `zypper` never run; then the AppImage, in managed mode, refuses a lower feed, a
signature by an untrusted key (with the trusted key's ID), a signed version other than the
announced one and the window's direct call of the plugin, cancels a slow download, undoes an
update whose new version exits instead of accepting itself, updates 0.1.0 to 0.2.0 with its core
stopped and started again (while 0.2.0 holds its acceptance open, a third launch raises its window
and exits, and 0.1.0's process has ended once 0.2.0 runs), refuses rollbacks to a version never installed and to a changed kept
copy without a dialog, cancels one in the native confirmation, and rolls back to 0.1.0. The
session checks the installed AppImage's digest after each step, that nothing is left beside it,
its feed's downloads and the versions that ran itself.
Then, in another session (`native/bundled.ts`, `native/bundled-session.ts`,
`native/bundled-steps.ts`, `bundled_native.rs`), the [core the test package comes
with](#the-core-the-app-comes-with), each case on a fresh profile in managed mode: the AppImage
runs its core from the app's own cores, with no confirmation, and that core's bundled registry
offers the packages its `bundled/index.json` lists (a first start installs none); the 0.2.0 build moves the app's own selection to its
core, the person rolls back to 0.1.0's core through the native confirmation, and that choice
stays when 0.1.0 and then 0.2.0 run again; in a copy of the package (the AppImage extracted, run
through its `AppRun`) a file added to `bundled/` is not copied, and a changed `bundled/index.json`
is refused with the damaged message, nothing installed; and a version in the app's cores with the
bundled core's number and other files gives the conflict message and stays as it was. The same
scenario runs on Windows and macOS in the package session.
The `native-test` feature is for that test alone; release builds never have it.

In local and remote modes, the test also drives the real GTK dialog with `xdotool`: good keys
authenticate, bad keys and Cancel preserve the connection and saved settings, and the native
keep choice controls persistence. Random keys are generated at runtime. The test inspects the
main and page webviews' DOM, input values, storage and native callback/event payloads, and the
app's stdout/stderr, for key canaries without passing those canaries to JavaScript.

The core steps run in a managed session of their own, on the build with the
`artifact-test-trust` feature, which starts with no core installed and with signed test releases
the runner makes in a temporary folder: cores 1.0.0 and 2.0.0 (built with Go, the version set at link time, with
the bundled test packages), a 3.0.0 that breaks its config and exits, and a 2.1.0 whose signature
does not verify, signed by an Ed25519 key generated for the run that never leaves the runner's
memory. Its trust profile starts with no roots. Through the window's commands and the real
dialog (`src-tauri/src/native_core.rs`), it checks that the app is not connected with no core;
that an install is refused without roots, before any dialog and with nothing installed; that the
same install with the run's root, confirmed, runs 1.0.0 from the app's folder and connects; that
the bad signature is refused before any dialog; that 2.0.0 replaces 1.0.0 (whose process is gone)
with the config unchanged; that after 3.0.0 the config is restored and 2.0.0 runs again; that an
unknown rollback target is refused, a cancelled rollback changes nothing and a confirmed one runs
1.0.0; that a core program does not run until it is adopted, after which the app's copy runs
and the program itself does not; that with a file of the installed 1.0.0 changed, rolling back to
it is refused before any dialog, by the same check of every installed file (`Store::program`)
that runs before every start, and installing it again, confirmed, repairs it and runs it; and
that installing the running version again runs it in a new process. A sentinel core the runner started before the app must still
answer, with its config and program unchanged, after the app has quit. `cargo test` covers a
crash while a version starts,
changed installed files, hostile archives and a version that is not what it was signed as
(`tests/core_updates.rs`).

The native run also builds the [test package](#packages) and runs the AppImage itself through the
same checks in local mode, native key entry included. Every run's app first reports its updater
gate, with the same test trust profile in its HOME each time: the test package passes it, and the
dev binary, whose compiled identifier is the normal one, is refused. It then runs the release command on a clean
release build, on an AppImage packed around that build, on an AppImage packed the same way
around the test-feature dev binary, and on the test package under a neutral name and under its
own: the first two pass and the rest are refused.

The same native run uses fixture actions (compiled only with `native-test`) and real X input
from `xdotool` (on Windows and macOS, the UI driver's input: see [Limits](#limits)) to confirm, cancel, opt into remembering, click a native Start button and revoke
in native settings. It checks one-use token consumption and open handles, page command/message
spoof refusal, exact native-gesture reuse without a dialog, different-chain review, changed
executable metadata and same-path replacement review, revoked-token refusal, and page Start flood
refusal with a visible host-owned notice. Unit tests cover action binding, cache reuse, persistence,
expiry, cancellation, invalidation, gesture freshness, Page/Import origin denial, separate pending
limits, superseded approvals, unsafe/corrupt settings and Unicode/path handling. A unit test forks
a child and runs a fixture script through `fexecve`, checking `FD_CLOEXEC` behavior and the
`/dev/fd/N` script path. The test prefers a target temporary folder and skips explicitly only
when both it and the system temporary folder are mounted noexec. Native GUI fixture actions do
not execute a program.

The same run checks the release feed (`src-tauri/src/release_native.rs`) on the Version
Management page,
against a fake https feed the runner serves on a random loopback port (`native/feed.ts`, with
`native/release-feed.json`) and a certificate authority made with `openssl` for that run. Only
native-test builds read that authority (`PLUXY_APP_TEST_FEED_CA`); other builds check a feed
against the public roots only. A stand-in browser, the run's default `https` handler through
its own XDG folders, logs each address it is asked to open. The check confirms a feed address in
the real dialog, then cancels a second change and finds the old address, its file and its copy
kept; asks for a third, finds Channel disabled, changes the channel by a direct call and confirms,
and finds the change not applied and the page saying why; checks for updates and reads the stable and the Preview (latest) versions, and finds
Update now not offered and refused (a build without updater trust is not configured for it, and the
fixture has no signed update for the test package); opens hostile
notes and finds no script, image, frame, handler or extra link, the Markdown rendered and the
raw HTML left out; probes every app and plugin command from the notes webview (each refused)
with the window's `release_get` as control; navigates the notes webview to an address the notes
do not link to (nothing opens) and follows the rendered link, a link whose address has characters the
renderer escapes (`a[1]?q=x|y`) and "View full release notes" (the stand-in browser gets exactly
those); asks the notes webview to download its own style sheet
(refused, nothing saved) and right-clicks it (no menu); collapses the notes (their webview closes) and expands
them again (a new one, over the notes area); scrolls the pane and
finds the notes webview over only the part of its area inside the pane, hidden once the area is
out of sight, and back over it unscrolled; stops the feed and finds the offline error with the last good copy and its time, and the notes webview moved down
with its area when that notice appeared above it; and
leaves the page, closing the notes webviews. The runner checks the feed server saw exactly one
feed read and nothing else, so the hostile image and frame were never loaded.

Before the app, each run's session starts desktop services (`native/session.ts`, with the
packages and paths in `native/services.ts`) and checks them for real; a missing or broken one
fails the run at setup, naming its package, and never skips:

- the portal frontend with the gtk backend (`portals.conf` in the run's own config folder selects
  it), which start the document portal and the permission store. A random file goes to the
  document portal over D-Bus with a file descriptor (the probe `src-tauri/examples/session_probe.rs`,
  built with gio, since gdbus and busctl cannot pass descriptors) and its bytes are read back
  through the portal's mount;
- the tray watcher service, which owns `org.kde.StatusNotifierWatcher`: the probe registers a
  status notifier item with it and finds the item in `RegisteredStatusNotifierItems`;
- a disposable AppImage, built from a pinned release of the AppImage type 2 runtime (downloaded
  into `~/.cache/pluxy-app-native`, its SHA-256 checked; a refused or failed request (403, 429, 5xx,
  a network error) is tried again within a minute, bytes with another digest never; `mksquashfs` builds the image), which runs
  with `APPIMAGE_EXTRACT_AND_RUN=1`, the FUSE-free way to run an AppImage, in the run's cleared
  environment. It is a fixture, not the app's own package.

When the app has exited, the session stops what it started. On Ubuntu 24.04 the packages are
`xdg-desktop-portal` (frontend, document portal, permission store), `xdg-desktop-portal-gtk`,
`ayatana-indicator-application`, `squashfs-tools`, `xdotool` and `openssl`; the document portal also needs FUSE
(`/dev/fuse`, `fusermount3`). CI installs them on a pinned `ubuntu-24.04` runner.

## Packages

The base `tauri.conf.json` keeps bundling off. Three commands build packages, from this folder,
after `bun run build`; all use the Tauri CLI pinned in `package.json`:

```bash
bun run package:test <out-dir>          # the test package, for checks: an AppImage (Linux), a
                                        # folder bundle and a per-user setup program (Windows) or
                                        # an app bundle and disk image (macOS)
bun run package:local <out-dir>         # the local build, to open and use (Linux): see below
bun run package:release [artifact...]   # the release gate: builds and checks; uploads nothing
```

`package:test` is for checks only, never for release. It builds a debug app with the
`native-test` and `artifact-test-trust` features and a config overlay given with `--config`:
identifier `dev.pluxy.desktop.test`, product name `pluxy Test`, binary `pluxy-app-test`, the
AppImage bundle, and a temporary updater key made for that build (its private key stays in the
command's memory and is never written). It builds the package twice, at the app's version and at
0.2.0, and signs the second with that key (minisign's prehashed signature, with the version in its
trusted comment, as Tauri's updater reads it: `native/minisign.ts`), for the native test to update
to. It checks that each result is an AppImage of type 2 (an ELF file with `AI\x02` at offset 8)
and writes `<out-dir>/pluxy-test_<version>_amd64.AppImage` for each, the first build's program as
built (`pluxy-app-test`, before the bundler patches its bundle type) and `test-package.json` (the
paths, the public key and the update's signature).

Each of the two builds comes with a core ([The core the app comes with](#the-core-the-app-comes-with)):
the app's version with the version `core-release.json` names, 0.2.0 with core 0.2.0. Each is a
test release made by the command, never published: a core built with Go at that version as a
release builds it (no cgo, `-trimpath`), with the test packages of `go run ./e2e/panelcore -probe
-prepare` as its `bundled/`, packed, described and signed with a publisher key made for the
command, whose private key stays in its memory. `bundle-core` checks each against its signed
descriptor with that key before the build, and the build compiles its manifest and carries its
files (on Linux and macOS through Tauri's `externalBin` and `resources`; on Windows copied into
the folder bundle). `test-package.json` names each build's core version and the package IDs its
`bundled/index.json` lists. Tauri's AppImage bundler would download the tools it runs
(linuxdeploy, its AppImage plugin and AppRun) into its tools folder when they are missing there,
unpinned. The test command puts pinned copies there first, each checked against its SHA-256
(`BUNDLER_TOOLS` in `package.ts`), and gives the AppImage plugin the pinned AppImage runtime
(`LDAI_RUNTIME_FILE`), which it would otherwise download too. The build runs with a temporary
HOME and cache folder, a temporary Cargo home holding only links to the caller's dependency
caches (so not the caller's Cargo config or credentials), none of the caller's other environment,
Cargo offline after `cargo fetch --locked`, and every proxy variable pointing at a closed port, so any download still tried fails
the build. Afterwards the tools folder must hold only the pinned tools and the bundler's own two
plugin scripts. The AppImage's type is still checked on the output.

On Windows and macOS `package:test` builds the same two debug apps with the same overlay, with
the `app` bundle on macOS. On macOS the first build is also bundled as Tauri's disk image (`dmg`:
the bundle and a link to `/Applications` on an HFS+ volume, unsigned), copied to
`<out-dir>/pluxy-test_<version>_<arch>.dmg`. On Windows each build's program goes into a plain
folder bundle of its own, `<out-dir>/pluxy Test/` and `<out-dir>/update/pluxy Test/`, each holding the program set the
updater replaces: `pluxy-app-test.exe`, and its core's `pluxy.exe` and `bundled\`. The update is the second
folder's set as a `.tar.gz` the command makes (`native/set-archive.ts`: its parts at the top,
folders and regular files only, no owner or time), signed. The first Windows build is also
bundled as the per-user setup program, `<out-dir>/pluxy-test_<version>_x64-setup.exe` (Tauri's
NSIS bundle, `installMode` `currentUser`, with no code signing, as the test package has no
identity to sign with). It installs the same program set, its core through Tauri's `externalBin`
(`pluxy.exe`) and `resources` (`bundled\`), without administrator rights: its manifest asks to run
as the user who starts it, and it writes only to that user's folders and registry. It installs
into `%LOCALAPPDATA%\pluxy Test`, with an uninstall entry under `HKEY_CURRENT_USER`, a Start menu
shortcut and, when run silently (`/S`), a desktop shortcut. Its hooks
(`src-tauri/nsis/hooks.nsh`) change two things in Tauri's setup:

- The setup and its uninstaller refuse while a program of the app's runs from the install folder
  (the app, `pluxy.exe`, or a program an interrupted update set aside), saying to quit it first
  (on the console when run silently or passively), where Tauri's would close it by force. They
  refuse too when Windows' Restart Manager, which they ask, cannot tell.
- Before Tauri's uninstaller removes the files it knows, the uninstaller removes what the app
  itself leaves in the folder, which it would not know (it stays the one the first version
  installed): an update's `.pluxy-*` staging folder and asides, `bundled\` and `pluxy.exe`. So
  Tauri's uninstaller then finds nothing it cannot remove, and never leaves a folder for Windows
  to delete at the next restart. It reads each entry before removing it and removes a link or
  junction itself, never what it points to (NSIS's own recursive delete would follow a
  junction). It enters real folders at most 32 deep and leaves anything deeper. It removes the
  app's login item (its `Run` value and Task Manager's value beside it) when that item runs this
  install's program. The app's data stays.

The app updates such a copy itself and never runs the setup for it
([Application update](#application-update)). Tauri's NSIS bundler would download NSIS 3.13 and
its `nsis_tauri_utils` plugin; the command puts pinned copies, each checked against its SHA-256
(`NSIS_TOOLS` in `package.ts`), in the bundler's tools folder in the target folder first
(`bundle.useLocalToolsDir`), and removes the copies of the core Tauri's build leaves next to the
program in the target folder. On
macOS each build's bundle is copied to `<out-dir>/pluxy Test.app` and
`<out-dir>/update/pluxy Test.app` (with `ditto`, keeping modes and the program's signature, no
extended attributes or quarantine), and the update is the second bundle as a `.app.tar.gz` made
by the command (one top folder, no macOS metadata), signed; no signing identity or notarization
is used. `test-package.json` names the bundle, the program in it, the public key, the update
(its bundle, payload, feed target `windows-x86_64-set` or `darwin-aarch64-app`, version and
signature), on Windows the setup program and on macOS the disk image. The builds download nothing
there, so they run in the caller's environment, Cargo offline after `cargo fetch --locked` and
every proxy variable pointing at a closed port.

`package:release` turns on no bundle format: release formats are not chosen yet. It builds the
release app with no features and no overlay (`tauri build --no-bundle`), finds that build's
snapshots by its receipt, and runs the [release validator](#validate-release-inputs) on them,
the staged trust folder (`src-tauri/trust`), the built binary (`pluxy-app.exe` on Windows) and
each artifact given: an AppImage, a Windows folder bundle or a macOS app bundle. Its last
line says `release-package: valid`, `release-package: refused: <reason>` or, when it could not
check, `release-package: failed: <reason>`.

### The local build

The local build is the desktop app built to open and use on this Linux computer, with a core and
the real provider extensions inside. It is not a release and cannot pass for one. From `next/`:

```bash
make app-local                        # into next/app/local-build
make app-local LOCAL=/some/folder     # into a folder of your choice
```

`make app-local` builds the web panel core embeds (`make panel`), installs and builds the window
(`bun run build`), then runs `bun run package:local <folder>` here. It needs what the app's other
builds need ([Build and check](#build-and-check)) and Go. Its last lines give the build's version,
the providers it carries, where it is and how to start it. It builds only into a new or empty
folder or an earlier local build ([Building it again](#building-it-again)).

The folder holds:

| Path in the folder | What it is |
| --- | --- |
| `bin/pluxy-app-local` | The app. Start it from here. |
| `bin/pluxy` | The core the app comes with ([The core the app comes with](#the-core-the-app-comes-with)). |
| `lib/pluxy Local/core/bundled` | The core's bundled registry: every provider extension in `next/extensions`, built by `go run ./cmd/pluxy-pack`. |
| `local-build.json` | The app's path, the core's version and the IDs of the providers it carries. |

Start it with `next/app/local-build/bin/pluxy-app-local` from the repository's root (or from the
folder you gave). On first launch, choose **Run core here** and press **Connect** with both fields empty: the app copies the
core it came with into its own cores, starts it on a free loopback port and connects. Setup and
Extensions then offer the providers; none is installed until you install it, and installing one
needs no network, since the archives are in the folder.

How it differs from a release:

- **Its own name and folders.** Identifier `dev.pluxy.desktop.local`, product name
  `pluxy Local`, program `pluxy-app-local`. Its settings go to
  `~/.config/dev.pluxy.desktop.local`, its data and cores to
  `~/.local/share/dev.pluxy.desktop.local` (under `$XDG_CONFIG_HOME` and `$XDG_DATA_HOME` when
  set), and its login entry, if you turn one on, to its own file
  (`dev.pluxy.desktop.local.desktop`), so it never shares anything with an installed release. The
  entry's name is written when you turn **Launch at startup** on, so an entry an earlier build
  wrote keeps that build's name until you turn it off and on again.
- **Marked as a local build.** The window's title, the tray's tooltip and its Open item, and the
  login item's name say "pluxy (local build)". The `local-build` Cargo feature also puts the test
  build marker in the program, so the [release validator](#validate-release-inputs) refuses the
  program given as an artifact (`test build marker/identity in artifact`), refuses the build's own
  config for its identifier and product name, and refuses the `local-build` feature (`test cargo
  feature in release`).
- **No test checks.** It is built in the release profile without the `native-test` feature, so it
  runs no checks and does not exit by itself, as the test package does.
- **Keys made for the build.** Its core is a release the command makes, as the test package's
  are, signed with a publisher key made for that one build; the private key stays in the
  command's memory and is gone when it ends. Nothing is signed with a key kept in the repository.
  The build has no updater key, so it never updates itself: build it again to update it.
- **No coding-tool add-ons yet.** The native tools in `next/app-tools` are not built into it,
  and it has no trusted source for them, so it cannot install them.

To remove it, quit the app (its core stops with it), then delete the build folder and its data:

```bash
rm -rf next/app/local-build ~/.config/dev.pluxy.desktop.local ~/.local/share/dev.pluxy.desktop.local
rm -f ~/.config/autostart/dev.pluxy.desktop.local.desktop   # only if you turned on Launch at startup
```

### Building it again

After a change, quit the app (from its tray menu when it is in the tray), then run
`make app-local` again with the same folder. The build:

1. **Checks the folder first, and changes nothing if it refuses.** It refuses a link, the folder
   itself or one among the folders above it (name the folder by its own path); a file; a folder
   with files in it that is not a local build: a local build has a `local-build.json` that names
   this kind of build and `<folder>/bin/pluxy-app-local` as its app, and that app, the core
   `bin/pluxy` and `lib/pluxy Local/core/bundled/index.json` are files in the folder, not links;
   any folder while a `<folder>.old` is beside it; and a folder that the app, or a core it started
   from there, is still running from, or when it cannot tell (it cannot list `/proc`, or cannot
   read the program of one of your processes that holds no special rights). Each refusal says what
   to do.
2. **Builds into a new folder beside it,** `<folder>.stage-<random>`. If the build fails or you
   stop it (Ctrl-C) before the swap, that folder is removed and the last line says your folder is
   as it was. Ctrl-C during the swap takes effect when the swap has ended: the last line then says
   that the new build is in your folder.
3. **Swaps the new build in:** your folder becomes `<folder>.old`, which is checked again as in
   step 1 (if something else took its place during the build, it is renamed back and nothing is
   removed); the new build takes its name, a `home/` folder in the old build moves into the new
   one, and then `<folder>.old` is removed. If a step fails, the message says where the old build
   or its `home/` is, and nothing that still holds `home/` is removed.

So a rebuild replaces `bin/`, `lib/` and `local-build.json`, keeps `home/`, and removes anything
else in the folder. `home/` is where the app keeps its settings and data when you start it with
`HOME` pointing there; started as above, they are in `~/.config/dev.pluxy.desktop.local` and
`~/.local/share/dev.pluxy.desktop.local`, which a rebuild does not touch either.

Each build has a version of its own, the core version the app names (`core-release.json`) followed
by `+local.` and 12 random hex digits, such as `0.1.0+local.90a173983d0c`. The app and its core
both carry it, so at its next start the app installs the core it came with and switches to it,
unless the core it runs is one you chose (an install, an adoption or a rollback): that one stays
([The core the app comes with](#the-core-the-app-comes-with)). The core it ran before stays
installed, to switch back to; older ones are removed. Accounts, settings
and installed providers stay. An installed provider keeps running the copy you installed, even
when the new build carries a changed one at the same version: to run the new one, remove the
provider and install it again (on Extensions, or `pluxy ext remove` and `pluxy ext install`); its
accounts stay.

### The updater gate

`trust::updater_gate` is what an updater must call, and pass, before any updater plugin call,
and the only place it gets its key. A build without the `artifact-test-trust` feature has only
production updater trust, which is not configured, so it is refused. A test build passes only
when its compiled identifier and product name are the test package's, the runner's trust
profile in HOME loads ([test trust](#package-signatures-and-trust)), and the profile's updater
key equals the compiled `plugins.updater.pubkey`. With the `native-test` feature the app reports
the gate's result, with the marker, identifier and product name it read, as its first step.
The app's updater ([Application update](#application-update)) calls it.

## Platforms

Linux is the only platform the app supports now: what this README describes is Linux's unless a
passage names another system (as the management key's dialogs, [Native
confirmations](#native-confirmations) and [Packages](#packages) do), and so is every check in
[Build and check](#build-and-check). Windows, macOS and WSL are later work, and the Linux checks
say nothing about them. Some groundwork for them is merged, and CI runs it on disposable
Windows and macOS runners (`.github/workflows/next-app-platforms.yml`), but it does not make the
app supported there:

- The app's crate builds and passes clippy and its Rust tests there.
- The management key's native entry and rotation, and the native confirmations, have Win32
  dialogs on Windows and AppKit dialogs on macOS ([How the app reaches core](#how-the-app-reaches-core),
  [Native confirmations](#native-confirmations)).
- Seven parts of the native test run there:
  - the platform session (`native/platform.ts`, `PLATFORM_SHARDS` in `native/shards.ts`): one app
    per profile (a named pipe on Windows, a Unix socket on macOS), the login item (the `Run` key on
    Windows, a LaunchAgent on macOS), the tray and the close preference;
  - the package session: the test package, the core it comes with (the Linux bundled core
    session's scenario) and the release gate ([Packages](#packages));
  - the tools sessions (`native/platform-tools.ts`, the `tools` shard): the signed tool sessions
    run.ts runs on Linux (admission, the program host with its swaps, routing with jobs, the vault
    with client keys), and a launcher check of its own
    (`src-tauri/src/launcher_platform_native.rs`), in the app built with the test trust, in local
    mode against a test core. The client keys are made on that test core, not on the app's own core
    as on Linux. On Windows the client-key check reads the agent's command line from Windows, but
    its environment is the agent's own report, as Windows gives no supported way to read another
    process's. The launcher check starts a stand-in agent in each terminal kind (Console window,
    Command Prompt and PowerShell on Windows; a real Terminal window on macOS), through an npm
    command shim run by the runner's Node (Windows), directly with a vault secret by reference and
    on its stdin, and managed; starts a remembered terminal launch from the tray; and checks the
    refusals of Windows' program rules and of a terminal launch with stdin or a managed lifetime.
    It leaves out Linux's key consumers and its crash, chain, page, changed and idle steps, which
    wait for their own slices; on macOS its tray step calls the menu's event handler with the
    item's ID, as no menu shows without a person holding it open (on Windows it sends the menu's
    own command message);
  - the open session (`native/platform-tools.ts`, the `open` shard, on the tools shard's build):
    a tool's opening of a file through the system's own route
    (`src-tauri/src/open_platform_native.rs`: on Windows the execution program's `open` mode, on
    macOS `/usr/bin/open`) and the open rules' refusals, and the app's opening of an address in
    the system's handler for https addresses (normally the default browser), with the session's
    own https server on 127.0.0.1. It opens a `.txt` file
    in Notepad or TextEdit, and on Windows a `.jsonl` file in the fixture program, through a
    per-user file type of the check's own that it removes again; on macOS no app opens `.jsonl`
    files on GitHub's image, so it checks that refusal there ([Limits](#limits));
  - the update session: the test package (a folder bundle's program set on Windows, an app bundle
    on macOS) updates itself to its signed newer version and rolls back, and crafted installs are
    refused; on Windows an update is also ended between two parts of the set, and the earlier
    version, started again on the half-replaced folder, undoes it; on macOS it does so from
    `~/Applications` and from the stock `/Applications`, where an access list entry, another
    owner or an account outside `admin` is refused
    ([Application update](#application-update));
  - the install session (with the update session, in the job that builds the test package): on
    Windows the per-user setup program, run silently, installs into the user's local application
    data folder with nothing machine-wide and no elevation asked; the installed app updates itself
    in place and rolls back, the uninstall entry's version following; the setup and the
    uninstaller refuse while the app runs; uninstalling removes the folder (with what the app left
    in it), the login item and the uninstall entry, keeps the app's data, and does not follow a
    junction in `bundled\` ([Packages](#packages)); on macOS the test package's disk image is
    attached and its bundle, copied out with `ditto` into `/Applications` and `~/Applications`,
    updates itself and rolls back from each; run from the mounted image it is refused because the
    image's volume is not APFS; what `spctl` and `syspolicy_check` say is recorded, not judged
    (the image is unsigned and, built on the runner, never quarantined, so Gatekeeper's first
    open is not tested there);
  - the dialogs session (`native/dialogs.ts`, a job of its own): the app in local mode against a
    test core, with the window's and page's command probes, the native confirmations, key entry and
    key rotation; then a second run in managed mode, against signed test core releases, with the
    app's own core steps as Linux's core steps session runs them (install, a bad signature
    refused, update, a failed start put back, rollback, adopt, a changed version refused and
    repaired, the running version installed again), each change through the native confirmation,
    whose text is checked to show what will run; a core the runner started before the app must
    still answer afterwards, untouched. [Limits](#limits) says how its input is made.
- The vault's private storage is built there, and its unit tests and the tools shard's vault
  session check it ([Limits](#limits)).
- How the app starts, holds and stops processes is built there (`src-tauri/src/owned/`: a job
  object on Windows, a process group on macOS), and CI checks that each of its lifetime tests ran
  and passed ([Windows and macOS](app-host/README.md#windows-and-macos)). Native tools, their
  managed runtimes, confirmed launches and the app's own update (`owned::Successor`: the new
  version, held until it accepts itself) reach it at run time there, and so does the app's own
  core ([The app's own core](#the-apps-own-core)), its changes confirmed natively.
- Native tools run there as on Linux: enabling a package and its native imports, the router, the
  program host, the launch service and the launcher, and the vault for tools' backups and journals,
  secret entry and client keys, with the differences
  [Windows and macOS](app-host/README.md#windows-and-macos) lists. A client page reaches its writer
  tool only where that tool has a program for the system ([app tools](../app-tools/README.md) says
  which do). Clippy, the Rust tests and the tools sessions check this there; outside tests nothing
  can be installed to run, as there are no production trust roots ([Limits](#limits)).
- On Windows, the page document's address is `http://pluxy-page.localhost/`, and the app holds a
  page's frame to its site through WebView2's frame navigation event (`src-tauri/src/page_frames.rs`).
- The app tools' readers, usage parsers and execution program, the writer library and the
  Claude Code and Codex setup tools build and pass their tests there, and `pluxy/wsl` lists WSL
  distributions and users ([app tools](../app-tools/README.md)).

Access gives no page anything there: approving a signer and making a grant are Linux only, so no
signer or grant can be made. On Windows its commands and a page's Access calls are refused; on
macOS its list, revocation and a page's calls answer, with nothing granted.

Still refused at run time there: a native tool's `files` frames (saving a file), core aliases and
installing a package from a file. Changing the release feed works there through the native
confirmation. The app updates itself, with test updater trust only, as an AppImage on Linux, on
Windows x86_64 as the program set of a folder bundle or of a copy the per-user setup program
installed, and as an app bundle on macOS
([Application update](#application-update)).

## Limits

- Device extensions: no production trust roots (so no curated source outside tests) or settings
  yet, and native tools run on Linux (5.11 or later: the program host needs `close_range` with
  `CLOSE_RANGE_CLOEXEC`), Windows and macOS, where a tool's `files` frames (saving a file) are
  refused; the host starts only a launch the person confirmed,
  when the tool serving that call asks ([Launches and managed runtimes](app-host/README.md#launches-and-managed-runtimes));
  installing from a file needs the Linux file chooser. Safe mode is covered by unit tests, not a
  native run.
- On macOS a launch you confirm, in a terminal or not, may run a program from a folder that every
  administrator of the Mac can change, such as `/Applications` or Homebrew's folders. Another
  administrator account can swap that program in the moment between the last check and its
  start, or, for a program inside an app, what it later starts from that app. For any program
  that uses those folders, wherever it is, that account can also change at any time what it loads
  from them (Homebrew's libraries, a script's interpreter). A script's interpreter opens the
  checked script again by its path, as for a direct launch. The app's
  own core and native tools never run from such a folder: the app refuses them there.
- On macOS the app refuses a program, the app's own core and native tools included, that is on a
  volume that ignores ownership (Finder's "Ignore ownership on this volume", the default for many
  external drives), or that lies below a folder on one: there macOS shows every file as yours and
  lets any user change it. Turn ownership on for that volume, or move the program.
- On Linux a terminal finds a launch's program by its path as it starts, so the app refuses a
  terminal launch whose program another user could change or redirect by then: a program below a
  folder that others, or a group other than your own private group, may write, one reached
  through a link such a user could change, or one such a group may write. Your private group
  (your primary group, named as you are, with no other member, no other user's primary group,
  and no other group with its number) may write them: Homebrew on Linux makes its folders
  writable by it, and so does a umask of 002. Users and groups are read from `/etc/passwd` and
  `/etc/group`: ones from LDAP, sssd or systemd-userdb are not seen. The refusal names the path and a
  `chmod g-w,o-w` that fixes it. Nothing stops your own processes from swapping the program
  before the terminal starts it. A direct launch runs the file the app checked, wherever it is.
- On Windows the terminal (the execution program) runs a launch's program by its path, so it
  refuses a program that another account may change, or whose folder it may change, or that it
  could swap by removing or renaming what a folder above holds: only you, SYSTEM,
  Administrators and TrustedInstaller may. The app does not check this before the launch, so the
  refusal shows in the terminal's window, naming the path and why. A folder whose access list
  has an entry of a kind the terminal does not read (such as a conditional entry) is refused
  too. Nothing stops your own processes, or an
  administrator's, from swapping the program before the terminal holds it.
- Vault: on Windows it is in the
  local data folder, private by its access list. Secrets are kept in a file only the user may read
  (0600 on Linux and macOS) in the app's data folder, not in the system keyring, and only exact
  values are redacted. On macOS, where an extended access list can let others in whatever the mode,
  a vault folder with any entry is given an empty list (as a too-open one is tightened to 0700, so
  nothing made in it inherits an entry) and a vault file with any is refused. The app's other
  private folders (Access's store, remembered launches, cores, extensions, the instance folder and
  the app's core's folder) are emptied the same way; files written privately are made, where the
  volume supports access lists, with an empty list that takes nothing from their folder; and the
  remembered launches file, the instance folder's lock and address and an installed core's files are
  refused with any entry. In the core's folder its config, its `credentials` and `packages` folders
  and each credential file (not one that is a link) are emptied of entries before the core starts,
  not refused: the core owns them. Other files and folders already inside these folders keep any
  entry they had from before. The window has no vault page yet. Secrets reach native tools' pipes
  and the environment of a process a confirmed launch starts (the launching tool holds only a
  reference); a client key is checked at each use, also at a launch's spawn and each restart.
  Backups have no retention: a package keeps at most 256.
- Access: signers and grants can be made on Linux only (5.8 or later: grants record the `statx`
  mount ID), so no page gets access elsewhere ([Platforms](#platforms)); web packages only; reads only (no writes or watchers); a signature only
  from a registry, not for a package installed from a file; document portal grants last one
  session.
- The native test runs on Linux, except its platform, package, tools, open, update, install and
  dialogs sessions, which CI runs on Windows and macOS (the install session on Windows only;
  [Platforms](#platforms) says what they check and what still refuses there). The setup's refusal
  when Restart Manager cannot start a session is not run: the test cannot make it fail.
- The setup program and its uninstaller check that the app is not running, and then Tauri's
  setup checks again: nothing keeps the app from starting in the moment between the two checks,
  and a copy started then is closed by force by Tauri's check (silently and passively, without
  asking), and the uninstall goes on.
- On Windows the launcher and open sessions start the app outside the job the runner's steps
  run in, which lets no detached launch leave it: WMI's `Win32_Process.Create` starts the fixture
  program, which starts the app as its child, with the app's output in files the session reads.
  Its terminal launches run the execution program's test build, whose Console window does not wait
  for Enter and whose Command Prompt or PowerShell after the agent runs one fixed command and exits,
  so the session does not see the prompt a person gets there.
- The open session runs only on GitHub-hosted runners, and its steps that change the machine (the
  `.jsonl` file type, the browser, and on macOS the test certificate authority) fail anywhere
  else. On macOS the browser asks for the session's address only from a server it
  trusts, so the session adds a test authority made for the run to the System keychain's trust,
  then deletes the authority's certificate; its trust setting stays, as removing that asks for a
  password, but nothing can use it: the authority's key is never written down. The step closes
  only the browser it started: on Windows the windows of the processes started with the session's
  address, on macOS the default browser, which must not be running before. On Windows the opens
  of `.txt` and `.jsonl` files are detached launches, which the app refuses while it runs in a
  job that keeps what it starts, so the session starts the app outside the runner's job as the
  launcher session does.
- On Windows the execution program's `open` and `app` modes start with no console
  (`CREATE_NO_WINDOW` instead of a console of their own), so none flashes. No test checks that
  the flag reaches the process: the unit tests cannot start a detached launch inside the job
  GitHub's runners run them in, and the native open session checks the app that opened the file,
  not the opener's console.
- On Windows the program session does not check which processes hold the tool's pipes, as it
  does on Linux and macOS: Windows lists no process's handles to an unprivileged one. It checks
  instead that the tool's three standard handles are pipes and that a file the app holds through
  an inheritable handle is not open in the tool.
- The dialogs session runs only on disposable CI runners (`native/runner.ts`): on Windows the app's
  folders are the account's known folders, which no environment variable redirects, so it refuses to
  start when they exist and removes only the ones it made. Its input, and the keys the tools shard's
  vault session types into native key entry, do not come from a keyboard: on Windows they are
  `SendInput` when the dialog has the foreground, otherwise the same key messages posted to the
  focused control; on macOS key and mouse events made in the app and given to AppKit. Both go
  through the app's real message handling and the dialogs' controls, but miss what only a person at
  the machine would hit, such as another app taking the keyboard. The dialogs session's summary
  counts which way each input went.
- No release installer yet: the only packages are the test AppImage, the test folder bundle and
  per-user setup program (Windows; nothing is code-signed) and the test app bundle and its disk
  image (macOS), which are not for release. They come with test-signed cores; the release gate
  builds no package with a core yet, since the core release the app names and a production
  publisher key do not exist yet. How Windows SmartScreen treats the unsigned setup program is not
  tested.
  The app updates only itself: as an AppImage on Linux x86_64, on Windows x86_64 as the program
  set of a folder bundle or of a copy the per-user setup program installed, and as an app bundle
  on macOS, and only with test updater trust until release
  keys exist. The native test updates it on Linux, on Windows and on Apple silicon Macs; Intel
  Macs and Windows on ARM are built for but not run, and how a stock Mac's Gatekeeper and privacy
  prompts or Windows SmartScreen treat an updated copy is not tested (the app's own download
  carries no quarantine or Mark of the Web). Rollback keeps the last three versions that ran on
  this profile; a new version that hangs is stopped after 90 seconds and the earlier one put back,
  but one that crashes later, after accepting itself, is not. If the old app itself is killed
  during those 90 seconds, nothing puts the earlier version back: put the newest copy in
  `updates/kept/` in the app's data folder (on Windows the three parts of the set in the local
  data folder's `updates\kept\`) in place of the installed one to run it again. On
  Windows a new version waits at most 120 seconds for the old one to end and free the profile,
  then exits.
- On macOS, deleting the app (dragging it to the Trash) leaves its login item behind: the file
  `~/Library/LaunchAgents/dev.pluxy.desktop.plist` stays and names a program that is gone. Turn
  **Launch at startup** off before deleting the app, or delete that file.
- Opening an address in the system's handler for https addresses (normally the default browser; an
  app that claims the address's domain may take it instead) works on all three systems: GIO on
  Linux, the `https` class's `open` verb (`ShellExecuteExW`) on Windows, `NSWorkspace` on macOS;
  only an https address reaches it.
- Release notes tables show their columns left-aligned whatever alignment the Markdown asks for:
  the renderer writes alignment as inline style, which the notes' policy refuses.
- The app's own core comes only from a local index file for now; an https source joins with the
  release feed. On Windows and macOS its steps run in the dialogs session's core run; the
  managed-core client-key check runs on Linux only. The store's Windows rules are checked by its
  unit tests on the Windows CI runner.
- On Windows, other programs that call the app's core, such as agents with client keys, are not
  covered by the app's check: after a crash of the core, another user's program listening on
  the core's port (on its address, or on the wildcard address all along) gets their calls and
  the client keys in them. Only the app's own calls are checked.
- Recovery history lasts for the current app process; quitting the app ends its recovery window.
  A supervisor thread starts the core and stays until it exits, since on Linux the parent-death
  signal follows the thread that started it.
- Native key entry and confirmations have no webview entry fallback on any system
  ([Platforms](#platforms) says where they run).
- The native dialogs on Windows and macOS are not modal to the whole app: only key entry is modal,
  to the window. No dialog runs a modal loop of its own, which would hold the app's other work.
- Hidden native entry keeps keys out of the webview API. It does not isolate them from another
  process with the same user's operating-system access, and transient native buffers are not
  guaranteed to be erased from memory. macOS turns on Secure Event Input while the secure field
  has the focus; Windows has nothing like it, so a keyboard hook of the same user sees what is
  typed.
- A kept key is stored in `connection.json`, not in the system keyring.
- The Linux native suite checks the app's own tray item in the watcher's registration list,
  drives its Start, Stop and Restart through the item's D-Bus menu, and checks startup without a
  watcher. It does not check every desktop's tray UI.
- The tray host is asked about only when the window closes: if it goes away while the app sits
  in the tray, the app stays there with no way back to it but stopping it.
- The native test checks that the first app's raised window is shown, not that it has the
  keyboard focus: that is the desktop's to give.
- The native test needs the desktop services above, and the pinned AppImage runtime from
  github.com (or a copy in its cache folder), so it does not run offline on a bare machine.
- Native admission bounds work after a scheme request reaches the handler. They do not bound
  memory used by the browser engine before that point. Frames can also post to WebKit's IPC
  handler; Tauri's capabilities refuse their commands before any app command runs.

## Package signatures and trust

The shared Rust verifier is `src-tauri/src/artifact.rs`. It verifies the exact descriptor bytes
with Ed25519 before reading the archive, then checks its byte size and SHA-256 before extraction.
The returned `VerifiedArchive<R>` retains the descriptor, signature and rewound archive reader.
Consumers call `into_archive()` instead of reopening the pathname and prevent concurrent writes.
After extraction, callers must
check the manifest through that record before admitting a package. Installation, native admission,
consent and installed-tree verification are separate consumers; this module does not extract or
execute anything. See [the signed descriptor contract](app-host/README.md#signed-descriptor-bytes).

Production publisher and updater roots are empty. The shared trust loader reports
`not configured` for native-tier and updater trust and never substitutes test keys.
The `artifact-test-trust` cargo feature permits a test runner to load public roots only from a
private temporary HOME with a runner-created trust marker. A temporary HOME alone grants no trust.
Private signing keys are generated at runtime and kept only in temporary fixture directories.
Tauri updater signatures have their own format; descriptor signatures cannot replace them.

The runner creates a private mode-0700 temporary HOME (on Windows: a folder owned by the user
whose access list lets in only the user, SYSTEM and Administrators, given to the app as HOME;
the profile's files there must be ones no one else can change), calls
`trust::write_test_marker(home)`,
passes its returned per-run random nonce to the child as `PLUXY_TEST_TRUST_NONCE`, and writes
`.pluxy-test-trust-roots.json` there. Both marker and nonce must match; TMPDIR grants no trust:

```json
{"roots":[{"kind":"test","publisher":"example","source":"https://example.invalid/index.json","key_id":"generated-key-id","public_key":"<64 lower-case hex digits>"}],"updater":null}
```

An optional `updater` object has `kind: "test"` and `public_key` in the updater verifier
format. This loader checks profile isolation, not the updater signature. The updater consumer
must check its compiled test marker, identifier, product name and profile before any updater
plugin call. Publisher signatures and updater signatures remain separate.

### Validate release inputs

From `src-tauri/`, run this before packaging or publishing, with the final generated Tauri JSON
(after all overrides), build snapshots from that app build, and the complete staged trust folder:

```bash
cargo run --locked --example validate-release-config -- \
  /tmp/release/build/app-tauri-config.json \
  /tmp/release/build/app-package-trust.json \
  /tmp/release/stage/app-package-trust.json \
  /tmp/release/build/app-cargo-features.json \
  /tmp/release/stage/trust \
  /tmp/release/pluxy.AppImage /tmp/release/pluxy-app
```

`build.rs` writes the config using tauri-utils' base/platform merge followed by `TAURI_CONFIG`,
plus the trust and feature snapshots, in Cargo's `OUT_DIR`. A digest of all three is embedded in
the app. The validator compares its own compiled inputs and requires the matching receipt in an
artifact. It also merges the config files in `src-tauri` again as `build.rs` does (with
`TAURI_CONFIG` from its own environment) and refuses a config snapshot that differs: Cargo reruns
`build.rs` only when a config file it read changes, so a build from before a new platform file
keeps the old snapshot. For the same reason `src-tauri` keeps a platform config file for each
desktop system, `tauri.linux.conf.json`, `tauri.macos.conf.json` and `tauri.windows.conf.json`,
even when it is empty (`{}`): `build.rs` watches the files it read, and Cargo cannot watch for a
file being added, so a platform file added later would not rerun `build.rs`. Keep them.
Supply the actual app executable along with every package. The validator streams each
regular file for test markers, test identifiers and Tauri updater keys or signatures. Each
artifact must be a well-formed executable, ELF (type, machine and program headers inside the
file), PE (an executable image for x86, x86-64 or arm64, its optional header inside the file) or
Mach-O (a 64-bit x86-64 or arm64 executable, library or bundle with its load commands inside the
file, or a universal file of such parts), or a type-2 AppImage, a Windows folder bundle or a
macOS app bundle (a folder named `*.app`), and carry the build's receipt itself; anything else
is refused, since it cannot be looked inside. A bundle folder is read in place, every entry
checked as an AppImage's are below, and its app executable is `<mainBinaryName>.exe` at its top,
a PE file, or `Contents/MacOS/<mainBinaryName>`, a Mach-O file. An app bundle's
`Contents/Info.plist` must be a property list (XML or binary) naming `dev.pluxy.desktop` as
`CFBundleIdentifier` and the app executable as `CFBundleExecutable`; it is decoded and every
key, string and data in it checked for test markers. An app bundle's archive
(`.app.tar.gz`) is refused like any compressed file. An AppImage (found by its header,
never run) must have its squashfs image where its runtime's header ends, with nothing after it
but zero padding under 4 KiB. `unsquashfs` (package `squashfs-tools`) first lists the image
(only folders, regular files and links; at most 100,000 entries, 32 folders deep and 2 GiB
unpacked), then unpacks it into a private temporary folder, each within 10 minutes and with its
output capped (64 MiB of listing, 1 MiB otherwise). Every name, link and file inside is checked:
links are resolved one name at a time as the system would (only a folder can have more of the
path after it) and must lead to an entry inside the package; compressed and archive files with a known signature are refused; JSON (named `.json`,
or whose first byte past whitespace is `{`) is decoded before it is checked, trust-shaped JSON
must be empty trust whatever its name, JSON over 1 MiB or invalid is refused, and so is any
unnamed file that starts like JSON and does not parse; the receipt must be in the package's app
executable, `usr/bin/<mainBinaryName>` (`usr/bin/pluxy-app` by default), which must be a
well-formed ELF executable as above.
The gate checks our own build for test material; it does not stand against a hostile builder.
A compressed file whose encoding has no known signature (raw deflate, brotli) is scanned as it
is, so test material inside one would pass. Then every byte of the AppImage is
scanned as well. The unpacked folder is removed, and a failure to remove it is an error.
The receipt checks consistency for trusted packaging tools, not a malicious packager rewriting
the binary and its evidence. Every supplied artifact must exist.

Test-feature and native-test apps retain `PLUXY_TEST_BUILD_DO_NOT_RELEASE`, referenced at startup
even in optimized builds. Normal release builds omit the marker writer and validator code. Run
the validator with a debug tooling build using the same config and features as the app.
It rejects test roots/features, private trust material, updater keys, test identifiers/products,
and test artifact basenames. Parent folders such as `.private` and `ci-test` are allowed.
Referenced `bundle.resources` and staged trust files are checked too; resource paths must be
expanded, without globs. Currently only empty trust JSON resources are allowed. Production key
configuration needs an explicit policy change.

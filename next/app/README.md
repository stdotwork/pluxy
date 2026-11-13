# pluxy desktop app

The desktop app is the UI kit's second build ([`../ui`](../ui/README.md)), in a Tauri 2 window,
with a tray icon. It shows core's pages in the same places and with the same themes as the web
panel, then adds its own Connection and Device extensions pages. Extension pages, from core and
from packages installed on this device, open in webviews of their own.

| Folder | What it holds |
| --- | --- |
| `src/` | The window: the kit's shell and pages, the app's host (`host.ts`), the Connection page, and how extension pages are drawn (`nativePage.tsx`) |
| `page/` | The small document an extension page's webview loads, which holds the page's sandboxed frame |
| `src-tauri/` | The native side: commands, the connection to core, the app's own core, extension page webviews, the tray |
| `native/` | The native test (`run.ts`) |

## How the app reaches core

The Connection page picks one of three modes. The app keeps the choice in `connection.json` in
its config folder (on Linux `~/.config/dev.pluxy.desktop/`), readable only by your user.

| Mode | What the app does | When the app quits |
| --- | --- | --- |
| Run core here (`managed`) | Starts the core program (`pluxy` beside the app, or one you name by full path) with a config of its own, `core/config.json` in the app's data folder. The first start writes that config with a free loopback port, which then stays the core's, new random keys, and folders for credentials and packages beside it. After that the file is yours to edit; the app only reads its `management_key`. | Stops that core (SIGTERM, then a kill after 10 seconds). On Linux the core also gets SIGTERM if the app dies without stopping it. |
| Core on this computer (`local`) | Connects to a core that already runs on a loopback address (`127.0.0.0/8`, `::1` or `localhost`), such as a system service. You give its address and management key. | Leaves it running. The app never starts or stops it. |
| Remote core (`remote`) | Connects over https only, so the key is never sent in the clear. You can name a PEM file of certificate authorities to trust for that core, for a certificate you made yourself; without one, the usual public roots are used. The core's address is shown next to the app's name. | Leaves it running. |

Addresses are origins only (scheme, host and port). A key you give is checked against that core
(`GET /v0/management/status`) before the app switches to it. It is kept in `connection.json` only
when you tick "Keep the key on this device"; otherwise it lasts until the app quits.

The native side holds the management key. On Linux, "Enter management key" on the Connection
page opens a GTK dialog with a hidden entry, "Keep the key on this device", and OK/Cancel.
Before opening it, native code validates the mode, origin and certificate authority file. The
dialog identifies the target origin, any custom certificate authority, and an origin switch.
Reopening after Cancel waits one second.
The key goes directly from that dialog to native connection state; no webview receives it.
Cancel, a refused key or a failed settings write preserves the old connection. Settings use a
unique exclusive temporary file at mode 0600, followed by file sync, rename and directory sync
on Unix. Validation and transport failures report specific reasons without request or key text.
The window calls core through the
`core_call` command, and the native side adds the key and sends the call to
`<core>/v0/management/...` only, following no redirects and, for a core on this computer, no
proxy from the environment. Path segments that are, or decode to, `.` or `..` are refused.

Rotate on the Config page's Connectivity tab rotates the management key natively, on Linux: the
app opens its native confirmation, which names the core and how the new key will be kept, then
calls core's `POST /v0/management/management-key/rotate` itself. It keeps the new key as the old
one was kept (in `connection.json`, for this session only, or, for the app's own core, in that
core's config file, which core writes) and shows it once in a native window with Copy and
Close, for other programs that used the old key and for signing in again. That window stays open
when the main window closes to the tray, and a later rotation's window replaces it. The window
gets only how it went: the key never reaches a webview. `core_call` refuses every path under
`/management-key`, however it is spelled. If the new key cannot be saved, the app keeps using
it until it quits and says to copy it and enter it again with "Keep the key on this device".
A call that left with the old key during a rotation is sent once more with the new one. The app
waits at most 30 seconds for core's reply to a rotation, since other calls and Quit wait for it;
without a reply it says the key may have changed and where to find it.

Until the app is connected and signed in, the window shows only the Connection page, so a wrong
address or key can always be fixed there.

## The window and the tray

What closing the window does is the Connection page's **Close behavior**: **Ask every time**
(the default), **Minimize to Tray** or **Exit App**. Ask every time shows "What would you like to
do?" with Minimize to Tray, Exit App, Cancel and **Remember this choice**. Exit App quits as the
tray's Quit does. Minimize to Tray destroys the window and its webviews, so they hold no memory
while the app sits in the tray. A window that does not answer the question within three seconds
(its page still loading, or busy) closes to the tray.

The tray menu shows what the app is connected to, and has "Open pluxy" (a new window) and "Quit
pluxy". In managed mode, "Start core", "Stop core" and "Restart core" control only the core the
app started. They work with the window closed and no renderer running. "Restart core" is
enabled only while the core runs. While one of them runs, the tray says "Core working…",
disables all three and refuses another click. They are disabled in local and remote modes, and
the native side refuses them there too, even if a desktop delivers a click on a disabled item.
Closing the window quits the app, whatever the choice, when there is no usable tray: the
app made no tray icon, or the session's `org.kde.StatusNotifierWatcher` is missing or has no
registered host (asked at each close, since GTK makes an icon even with no desktop to show it).
The setting then says so.

When an owned core exits unexpectedly, the app retries after 2, 5 and 15 seconds, with at most
three automatic recovery attempts in a rolling five-minute window. A successful recovery keeps
that budget, so a core that keeps crashing still stops being restarted. After the third failed
recovery or another crash within that window, the tray shows "Core recovery paused" and the
Connection page shows the reason. An explicit Start, Stop or Restart, from the tray or the
window, or a mode change, cancels any scheduled recovery and clears the budget. A core you
stopped stays stopped, and so does one that Start Core on Launch left unstarted.

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
on that profile, from the app menu, a shortcut or the login autostart entry (Silent Launch applies
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

## Privacy mode

The navigation's **Privacy mode** toggle masks account e-mail addresses and identifiers where
the kit shows them, as in the panel (`ui/kit/privacy.ts`), and extension pages are told in their
theme snapshot. The app keeps the choice natively, in `privacy-mode.json` beside
`connection.json` (readable only by your user), not in the window's storage, so losing that
storage cannot turn it off. A file that cannot be read or parsed counts as on.

## Startup preferences

The Connection page has three startup choices:

- **Launch at startup** registers pluxy for your Linux login. Saving it writes
  `$XDG_CONFIG_HOME/autostart/dev.pluxy.desktop.desktop` (or
  `~/.config/autostart/dev.pluxy.desktop.desktop`). Its Exec command names the app by absolute
  path with `--autostart`. Silent Launch controls whether a login launch shows a window.
  Turning Launch at startup off and saving removes pluxy's entry. Merely opening the app does
  not register it. The displayed choice is read from that entry, including desktop disable flags.
  A file without pluxy's ownership marker is shown as a conflict and left alone.
- **Silent Launch** suppresses the window only for an autostart launch in managed mode when
  the session has a StatusNotifier watcher and a registered tray host. Local and remote modes
  host no core, so they open normally. In that case no window or webview is
  created. Without a host the app logs why and opens a window. Opening the app from its menu
  or shortcut always shows the window.
- **Start Core on Launch** starts the app's own core in managed mode. Turning it off leaves
  the saved managed connection stopped until you press Start core. Local and remote modes
  only connect; their core processes are never started or stopped by these choices.

The defaults are login registration off, Silent Launch off, and Start Core on Launch on.
Preferences live in `startup.json` beside `connection.json`, readable only by your user.
A malformed file is moved to `startup.damaged-<random>.json` and defaults are used; failure to
read or keep it aside is logged and still does not prevent startup. Saving preferences does
not restart or change the current connection. No system service or second core supervisor is
created. Login registration is implemented on Linux only. Registration resolves the executable
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
| `core_restart` | Starts the app's own core again (managed mode) |
| `core_call` | One management API call to the connected core; refuses the management key's own routes |
| `management_key_rotate` | Rotates the management key natively after the native confirmation, keeps and shows the new key natively, and returns only how it went (200 with how the key is kept, 204 when cancelled) |
| `sign_out` | Forgets the key (local and remote modes) |
| `page_open`, `page_place`, `page_close` | Opens an extension page's webview over part of the window, moves it, closes it |
| `page_send` | Passes the window's channel messages to a page's webview |
| `confirmation_request` | Opens native review for an opaque action ID already prepared by native code; returns no approval or token |
| `confirmation_settings` | Opens native settings to inspect and revoke exact remembered launches (Linux) |
| `device_list` | This device's extension catalog |
| `device_registries` | What the registries in `device-registries.json` offer this computer |
| `device_install` | Installs or updates a package from one of those registries |
| `device_install_file` | Opens the native file chooser and installs or updates the package chosen there (Linux) |
| `device_enable`, `device_remove`, `device_rollback` | Enables or disables (a native tool only through its native confirmation), removes, or rolls back a package on this device |
| `device_imports` | Allows a curated web package's native imports, through a native confirmation, or revokes them (Linux) |
| `device_page_open` | Opens a webview for a page of a package on this device, at the address its catalog keeps |
| `program_call`, `program_cancel` | Calls an operation of an admitted native tool, starting it if it is not running; cancels a call by the ticket it was given (Linux) |
| `program_status` | Whether a native tool runs, its leases, how it last stopped, its crashes in a row and backoff, calls of unknown outcome, and replaced releases still draining (Linux) |
| `window_get`, `window_set` | Reads or saves the Close behavior, and whether there is a usable tray |
| `window_close` | Answers the close prompt: Exit App or Minimize to Tray, and whether to remember it |
| `privacy_get`, `privacy_set` | Reads or saves privacy mode |

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
puts each page webview over it at the page area (`src-tauri/src/place.rs`).

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
verified on Linux WebKit. Windows currently reports only top-level navigation through this callback;
subframe closure there still depends on the wrapper's load notification and has not been verified.
Direct frame requests use these same checks.

One extension may open four pages, with 60 admitted calls per rolling second and 16 in flight
across them. Closing pages preserves the package's recent rate budget. At most 16 page webviews
and 64 recent package budgets are retained. Replies are limited to 2 MiB each, covering 50 settings with 4,096 runes even when each rune
needs six JSON bytes. Pending call replies use their existing in-flight reservations; theme
events retain one delivery and one replacement per page. Failed call deliveries send a small
native error; if even that cannot reach the renderer, the channel closes. The wrapper sends a
bounded FIFO of eight calls one at a time; hello and closed messages always enter that FIFO. Refusals return an HTTP status and, for bodies within the transport limit with a readable id,
the matching channel error. Oversized transport bodies are rejected without scanning for an id.
Permissions and extension settings still use the shared kit's channel. No page receives a key.

## Extensions on this device

The Device extensions page manages app packages (see
[the app package contract](app-host/README.md)) installed on this computer: page-only packages
(`app.tier` `web`) and curated native tools (`native`). They are apart from
the extensions of the core the app is connected to: installing one never installs it in core,
and core's Extensions page never lists it. Their pages are listed in the navigation with core's
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
  computer's platform, and hold the ID and version it lists.

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
only such a package may call native tools (below). Packages get no resources or settings here:
their pages' settings calls answer `unavailable`.

A native tool installs only from a curated registry whose index entry carries a descriptor signed
by the root's key for that publisher and source, and names that key as its `public_key`; the
signature, then the archive's size and SHA-256, are checked before it is unpacked, and the
manifest's ID, version and tier after. A signature the index lists for any other package is
ignored, and the Device extensions page marks an entry the index already shows install would
refuse. Its
program for this computer must be an executable file in the package, and the SHA-256 of every
file is recorded. It is labelled **Curated** and installs disabled. Enabling it opens a native
confirmation that says it has **full access to your user account** and shows its ID, source,
digest, signer, contracts, operations and program; only Confirm there enables it, and the catalog
records that admission against the release, source, signer and key. Disabling, updating, rolling
back or removing it ends the admission, so a new release asks again. A native release replaces
another only if it reads the state version the other writes.

The program host (`src-tauri/src/program.rs`, Linux) runs an admitted tool only on demand: at its
first call from the window, from what `Catalog::admitted` returns then, after checking the
admission, the root, the signature and every installed file again. It opens the program, checks
its SHA-256 against the record and runs that opened file, in its package folder, with a cleared
environment, only its three private pipes, a process group of its own and a parent-death signal,
from a supervisor thread that stays until the tool has been reaped. Calls follow [app program
contract v0](app-host/README.md#app-program-contract-v0), one at a time. When the last call
ends, the tool stops after 30 seconds idle; closing the window cancels its calls, so their tools
stop when idle. Any change to its package (disable, update, roll back, remove) and quitting the
app stop it at once, and once the app is quitting no call starts a tool. Stopping closes its
stdin, kills its whole process group after five seconds and reaps it: no process or thread of it
is left. If the app itself dies, the tool gets SIGTERM and nothing kills its group, so a tool must
end the processes it started when its input ends and on SIGTERM. Pages, including the tool's own,
have no program command and no route to its pipes; they reach operations only through the router,
below.

Every call of a native tool's operation goes through the router (`src-tauri/src/route.rs`, Linux):
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
no frames or forms. The webview may not navigate its frame out of that folder. Installing,
rolling back, disabling or removing a package closes its open page webviews at once; the window
then opens the new release's page, if the page is still listed.

## Native confirmations (Linux)

Native code prepares each immutable action with its requester chain, executing package, full
source/digest/signer/contract/generation, selected and resolved file identities, and launch recipe
where applicable. The main window can ask to review that preparation's opaque ID. A GTK dialog
shows the action and, for a native tool, “full access to your user account”. Only its native
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
owned by another user is refused. Disabled persistence hides Remember. If saving fails after
confirmation, the one-time token is still issued and a native notice/log reports the failure.

An executable is opened before checking its canonical path, device/inode, size, mtime, ctime and
mode. SHA-256 is computed once for that metadata and cached in memory; unchanged metadata reuses
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
started through Page Start, and one from any other chain as a Main action naming the whole chain;
only the latter, started by a native gesture, may use a remembered approval. Gesture construction
is private to native handlers and fixtures, and a gesture expires after five seconds.

Page Start uses the separate native entry point, always opens a fresh dialog, permits one pending
confirmation per original package and one attempt per second, and shows a GTK notice with
requester, action and outcome, including refusals. Page/Import pending entries have their own
limit of 16; trusted requests have a separate limit of 64. Page dialogs focus Cancel and keep
Confirm disabled for one second after mapping. Non-UTF-8 paths are refused before review;
Unicode format characters (including bidi and zero-width characters), control characters and
Pango line separators (U+2028/U+2029) are displayed as escaped Unicode.

Real launchers are added separately; this module contains no launcher. Host
callers must invalidate pending preparations/tokens when package, contract, environment or secret
policy records change, and prepare a new action. Tokens and pending reviews expire after 60
seconds, work once for their preparation, and fail on changed file identity or executable metadata.
Revoking a remembered approval also invalidates its outstanding tokens.

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

`bun run test:native` runs the native test: it builds the app with the `native-test` feature
and runs it on a private X server (`xvfb-run`) with a private session bus (`dbus-run-session`),
a fresh HOME and XDG folders, once in each mode. In each it checks that the window's webview
reaches every command, that at the top of an extension page's webview Tauri refuses every app
command and the plugin commands tried, and that no app command is reached from that webview
or its frame. It also runs a test-only sandboxed frame
that posts directly to the native scheme: over-rate, over-in-flight, oversize, unknown method,
invalid schema, repeated hello, wrong generation, explicit close and frame navigation while
the wrapper's close message is suppressed. A separate real HTTP pages-site probe records whether
that frame can reach the scheme, and navigation of that real frame closes native admission. It compares native handler arrivals,
admissions, forwarding and real core settings operations, with a successful call on the same page.
It checks that the page webview covers the page area, that
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
A second managed run starts a stand-in core that says where it listens and exits at once, and
holds the app's setup until that core's exit callback has run, before the app state exists. It
checks that the app still starts the real core again, with one recovery attempt, and that the
core does not outlive the app.
In separate runs, the startup sequence enables login registration through the main webview command,
checks the entry in its temporary XDG config folder, launches it with the real Desktop Entry consumer (gio launch), checks
zero windows/webviews and no web process, and verifies that the app's real tray item belongs to
its PID in the tray watcher's registered item list. It checks login with Silent Launch on and
off, an app-menu launch with Silent Launch on, managed-core startup, core startup off, foreign
core ownership, and removal. In the core-startup-off launch it waits past the first recovery
delay and checks that no core started and no recovery ran, then starts the core with the
tray's Start core and crashes it: that core is recovered. A separate session with no watcher verifies that a silent login
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

In managed mode, two more sessions run the window checks (`native/window.ts`, which launches
the app through the stages in `src-tauri/src/native_window.rs`), one with a tray watcher and
one without. They close the window as its window manager would (a GTK delete event) and answer
the prompt in the window: Cancel keeps it, Minimize to Tray and the remembered choices close it
with no webview or web process left, and the app's own tray item (through the watcher and its
menu, `session_probe tray`) opens it again at the size and place it had; Exit App, from the
prompt or the setting, quits and stops the app's own core; the next launch opens where the
window was. A pending test sign-in's polls are counted while the window shows, while it is
unmapped (as a window manager minimizes it; GDK then reports it minimized) and once it is mapped
again. Without a tray watcher, closing quits without asking, with Minimize to Tray or Ask saved.

Then, in a session of its own (`native/instances-session.ts`), it launches the app again and
again on one profile and checks that each later launch exits 0 within 10 seconds, starts no app or core of its own (it counts the
app and core processes), and has the first show its window, also after the window was hidden and
after it was closed to the tray, and for a login launch (`--autostart` with Silent Launch on) as
well; that the socket is in the runtime folder, where `instance/address`
says; that a launch with other `HOME` and `XDG_*` folders is an app and
core of its own; that after the first app is killed with SIGKILL (its lock file and socket stay)
the next launch becomes the app, starts its core and is the one a later launch raises; and that
the socket and its address are gone when the app quits. The other profile's data folder is too long
for a socket path, and a second launch of it still raises it.
The `native-test` feature is for that test alone; release builds never have it.

In local and remote modes, the test also drives the real GTK dialog with `xdotool`: good keys
authenticate, bad keys and Cancel preserve the connection and saved settings, and the native
keep choice controls persistence. Random keys are generated at runtime. The test inspects the
main and page webviews' DOM, input values, storage and native callback/event payloads, and the
app's stdout/stderr, for key canaries without passing those canaries to JavaScript.

The same native run uses fixture actions (compiled only with `native-test`) and real X input
from `xdotool` to confirm, cancel, opt into remembering, click a native Start button and revoke
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
  into `~/.cache/pluxy-app-native`, its SHA-256 checked; `mksquashfs` builds the image), which runs
  with `APPIMAGE_EXTRACT_AND_RUN=1`, the FUSE-free way to run an AppImage, in the run's cleared
  environment. It is a fixture, not the app's own package.

When the app has exited, the session stops what it started. On Ubuntu 24.04 the packages are
`xdg-desktop-portal` (frontend, document portal, permission store), `xdg-desktop-portal-gtk`,
`ayatana-indicator-application`, `squashfs-tools` and `xdotool`; the document portal also needs FUSE
(`/dev/fuse`, `fusermount3`). CI installs them on a pinned `ubuntu-24.04` runner.

## Limits

- Device extensions: no production trust roots (so no curated source outside tests), resources
  or settings yet, and native tools run on Linux only (5.11 or later: the program host needs
  `close_range` with `CLOSE_RANGE_CLOEXEC`); a confirmed launch-class call is passed to its
  operation, and the app starts nothing itself yet; installing from a file needs the Linux file chooser. Safe mode is covered by unit tests, not a
  native run.
- Tested on Linux only. On Windows the page document's address is
  `http://pluxy-page.localhost/`; neither Windows nor macOS has been run.
- No installer or bundled core yet (`bundle.active` is off).
- Recovery history lasts for the current app process; quitting the app ends its recovery window.
  A supervisor thread starts the core and stays until it exits, since on Linux the parent-death
  signal follows the thread that started it.
- Native key entry is implemented on Linux only. Windows and macOS refuse key entry until
  their native dialogs are implemented; there is no webview entry fallback.
- Hidden native entry keeps keys out of the webview API. It does not isolate them from another
  process with the same user's operating-system access, and transient native buffers are not
  guaranteed to be erased from memory.
- A kept key is stored in `connection.json`, not in the system keyring.
- The Linux native suite checks the app's own tray item in the watcher's registration list,
  drives its Start, Stop and Restart through the item's D-Bus menu, and checks startup without a
  watcher. It does not check every desktop's tray UI.
- The tray host is asked about only when the window closes: if it goes away while the app sits
  in the tray, the app stays there with no way back to it but stopping it.
- Raising the first app's window is Unix only (a Unix socket); where there is none, a second
  launch exits with an error and the first's window stays as it was. The native test checks that
  the raised window is shown, not that it has the keyboard focus: that is the desktop's to give.
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

The runner creates a private mode-0700 temporary HOME, calls `trust::write_test_marker(home)`,
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
artifact. Supply the actual app executable along with every compressed package: the validator
streams each regular file for test markers; it cannot inspect compressed inner executables.
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

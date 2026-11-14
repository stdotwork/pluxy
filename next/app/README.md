# pluxy desktop app

The desktop app is the UI kit's second build ([`../ui`](../ui/README.md)), in a Tauri 2 window,
with a tray icon. It shows core's pages in the same places and with the same themes as the web
panel, then adds its own Connection and Device extensions pages. Extension pages, from core and
from packages installed on this device, open in webviews of their own.

| Folder | What it holds |
| --- | --- |
| `src/` | The window: the kit's shell and pages, the app's host (`host.ts`), the Connection page, and how extension pages are drawn (`nativePage.tsx`) |
| `page/` | The small document an extension page's webview loads, which holds the page's sandboxed frame, and the release notes' style sheet |
| `src-tauri/` | The native side: commands, the connection to core, the app's own core, extension page webviews, the tray |
| `native/` | The native test (`run.ts`) |
| `package.ts` | The test-package and release-package commands ([Packages](#packages)) |

## How the app reaches core

The Connection page picks one of three modes. The app keeps the choice in `connection.json` in
its config folder (on Linux `~/.config/dev.pluxy.desktop/`), readable only by your user.

| Mode | What the app does | When the app quits |
| --- | --- | --- |
| Run core here (`managed`) | Runs one of its own core versions ([below](#the-apps-own-core)) with a config of its own, `core/config.json` in the app's data folder. The first start writes that config with a free loopback port, which then stays the core's, new random keys, and folders for credentials and packages beside it. After that the file is yours to edit; the app only reads its `management_key`. | Stops that core (SIGTERM, then a kill after 10 seconds). On Linux the core also gets SIGTERM if the app dies without stopping it. |
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

## The app's own core

In managed mode the app runs only core versions it installed itself, each in a folder of its own
under `cores/versions/` in the app's data folder. Until one is installed, the app runs no core
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

The archive is a gzip tar file of the core's folder, as provider releases are packed: `pluxy` at
the top, and anything beside it, such as `bundled/`. It may hold only regular files and folders,
no links, devices or names that leave the folder, at most 10,000 entries, and at most 512 MiB
decompressed in all (headers and the data of folder entries count too). Each file is made from
the folder before it, following no links. Release versions may not start with `custom-`, which
adopted programs use.

**Install** (or **Update to** a newer version): the app copies the archive into its own folder and
checks it with the shared verifier (`src-tauri/src/artifact.rs`) and the app's trust
(`src-tauri/src/trust.rs`): the signature over the exact descriptor bytes with a publisher key
trusted for this exact source (publisher `pluxy`), then the archive's size and SHA-256. Only then
does the native confirmation open, showing the version, source, key and digest, and that a core
has full access to your user account. A release that fails a check is refused before any
dialog, and nothing is installed. Release builds have no publisher keys yet, so they refuse
every release; test builds trust only a test runner's temporary roots (below).

Once confirmed, the app unpacks the archive into a new version folder, records each file's
SHA-256 (in `cores/records/`, apart from the folders), and makes the files read-only. Before
every start it opens every file beneath the version's folder without following links, checks
that it is a regular file of the recorded size, then its digest, and runs the core from the
program file it just checked, not from its path again. A changed version does not start:
**Install … again** installs the same release over it, through the same checks and confirmation;
a changed adopted copy is copied in again by adopting the same program again.

**Switching** to a version (an install, **Roll back to** an installed version, or an adoption)
stops the running core, keeps a copy of `core/config.json` (or a mark that there was none) and
records the new version as the candidate, then starts it. It counts as started once it says
where it listens, on this computer, every socket listening on that port belongs to the process
the app started (read from `/proc` on Linux, before the app sends anything there), it answers
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
moment the app begins to stop its core, or sees it exit, it sends nothing more to that address. The
app keeps the running version and the one before it, and removes older ones after a switch. Rolling
back reaches only an installed version.

A **core program** you name on the Connection page (or `pluxy` beside the app) is never run in
place. **Adopt this core program** shows its path, size and SHA-256 in the native confirmation;
once confirmed, the app copies it into a version of its own, `custom-<its SHA-256>`, if it is
still a regular file of that size and digest (at most 512 MiB; a FIFO or device is refused
without being read), and runs the copy. Only the program is copied, nothing beside it (such as
`bundled/`), so an adopted core starts without the bundled packages a release brings. An adopted
program has no signature.

The app only ever stops the core process it started. A core it did not start, such as a system
service or another core of yours on this computer, is never stopped or signalled.

Each change works once: while one waits for its confirmation, another is refused. The window
hears `pluxy-connection` when it has ended, and `core_versions` says whether it was done,
cancelled or failed.

What these checks guard against: a damaged or substituted download, and other users of this
computer. They do not guard against your own user account. A program running as your user can
rewrite the app's core store, as it can replace the app itself: the installed records and version
folders (or the folders above them), a staged archive between its check and its unpacking, and a
checked program, or the files beside it, before the core runs or reads them.

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
| `core_versions` | The app's own core versions, what the core source offers, and how the last change ended |
| `core_install`, `core_rollback`, `core_adopt` | Asks to install a release from the core source, to run an installed version again, or to adopt the chosen core program; each opens a native confirmation (Linux) and applies nothing itself |
| `core_call` | One management API call to the connected core; refuses the management key's own routes |
| `management_key_rotate` | Rotates the management key natively after the native confirmation, keeps and shows the new key natively, and returns only how it went (200 with how the key is kept, 204 when cancelled) |
| `sign_out` | Forgets the key (local and remote modes) |
| `page_open`, `page_place`, `page_close` | Opens an extension page's webview over part of the window, moves it, closes it |
| `page_send` | Passes the window's channel messages to a page's webview |
| `confirmation_request` | Opens native review for an opaque action ID already prepared by native code; returns no approval or token |
| `confirmation_settings` | Opens native settings to inspect and revoke exact remembered launches (Linux) |
| `release_get`, `release_check` | The release feed's view (versions, dates, status); `release_check` reads the feed first ("Check for Updates") |
| `release_channel_set` | Picks the Stable or the Preview (latest) channel |
| `release_source_set` | Asks to change or remove the feed's address; opens a native confirmation (Linux) and applies nothing itself |
| `release_notes_open`, `release_notes_place`, `release_notes_close` | Opens a release's notes in a webview of their own over part of the window, moves it, closes it |
| `release_notes_full` | Opens the release's notes address from the feed in the browser ("View full release notes") |
| `device_list` | This device's extension catalog |
| `device_registries` | What the registries in `device-registries.json` offer this computer |
| `device_install` | Installs or updates a package from one of those registries |
| `device_install_file` | Opens the native file chooser and installs or updates the package chosen there (Linux) |
| `device_enable`, `device_remove`, `device_rollback` | Enables or disables (a native tool only through its native confirmation), removes, or rolls back a package on this device |
| `device_imports` | Allows a curated web package's native imports, through a native confirmation, or revokes them (Linux) |
| `device_page_open` | Opens a webview for a page of a package on this device, at the address its catalog keeps |
| `access_list` | The approved signers and every grant of Access |
| `access_signer` | Opens a native confirmation to approve a device package's signer for its source |
| `access_grant` | Opens the native file chooser for a package's declared resource, then a native confirmation, and keeps the grant |
| `access_revoke` | Revokes a grant, answering once the reads under it have finished |
| `program_call`, `program_cancel` | Calls an operation of an admitted native tool, starting it if it is not running; cancels a call by the ticket it was given (Linux) |
| `program_status` | Whether a native tool runs, its leases, how it last stopped, its crashes in a row and backoff, calls of unknown outcome, replaced releases still draining, and its vault journals, open and recently ended, or why they cannot be read (Linux) |
| `vault_secret_add` | Opens native key entry and keeps the secret typed there in the vault under a label; returns only its ID (Linux) |
| `vault_secrets`, `vault_secret_remove` | The vault's secrets, by ID and label, never their values; removes one (Linux) |
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

## Version Management and release notes

The Version Management page (Control group) shows, for the app ("Desktop Application") and for
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

"Check for Updates" reads the feed: https only, the public certificate roots, no redirects, at
most 1 MiB, 30 seconds. The last good feed is kept in `release-feed.json` in the app's cache
folder with the time it was read, and shown again after a restart. When a check fails (offline,
an error answer, a feed the app refuses) the page says why and keeps showing the last good feed
with that time. A feed with any bad release is refused whole. Changing the address drops the copy.
Only the latest check of the current feed is taken: an earlier check that finishes later, or one
started before a feed change (even back to the same address), is ignored.

The window gets only versions, channels, dates and whether a release has notes or a notes
address; never notes text or a notes address. Each release's notes open in a webview of their
own (`notes-<n>`, at most two), placed over the part of the notes area that shows: below a
navigation bar pinned at the top, and hidden when none of it shows, and placed again whenever the page moves the area (scrolling,
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

The whole feed is at most 1 MiB. The feed is not signed: https is its only protection, and
nothing in it is trusted as markup. Signed update artifacts have their own signatures. Production
feed addresses, keys and formats are decided at release; until then there is no built-in feed.

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
  computer's platform, and hold the ID and version it lists. An index may also list the
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
no frames or forms. The webview may not navigate its frame out of that folder. Installing,
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
   your home folder or the document portal's store is refused. A native confirmation then shows the package with its source, digest and signer, the
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
Removing the package removes its grants.

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
user (mode 0600, folder 0700), written by an exclusive temporary file, sync and rename. A damaged
or unsupported file is moved to `access.json.corrupt-<time>` and Access starts with no grants; if
it cannot be moved, Access refuses grants and reads until it is fixed. Handles opened for reads
are kept while the app runs; after a restart each target is opened again and its identity
checked.

This is a check that cooperates with the person, not a sandbox: another program of the same user
can change files between a check and a read.

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
started through Page Start, one with any tool hop in its chain as an Import, and only the
window's own direct call as a Main action; only the latter, started by a native gesture, may use
a remembered approval, and only those dialogs offer Remember. Gesture construction
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

`bun run test:native` runs the native test: it builds the app with the `native-test` and
`artifact-test-trust` features and runs it on a private X server (`xvfb-run`) with a private session bus (`dbus-run-session`),
a fresh HOME and XDG folders, once in each mode. The app runs only a core in its own store, so
each managed run but the core steps' (below) starts with one there: the signed test release 2.0.0,
installed as a confirmed install would (`session_probe install-core`), whose bundled test
packages the sign-ins and pages need beside the core's program; the startup runs and the
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
A second managed run starts a stand-in core that says where it listens, answers the app's
readiness check and then exits, and holds the app's setup until that core's exit callback has run, before the app state exists. It
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
Cancel keeps it, Minimize to Tray and the remembered choices close it with no webview or web
process left, and the app's own tray item (through the watcher and its menu, `session_probe tray`)
opens it again at the size and place it had; Exit App, from the prompt or the setting, quits and
stops the app's own core; the next launch opens where the window was. A pending test sign-in's
polls are counted while the window shows, while it is unmapped (as a window manager minimizes it;
GDK then reports it minimized) and once it is mapped again. Without a tray watcher, closing quits
without asking, with Minimize to Tray or Ask saved.

Then, in a session of its own (`native/instances-session.ts`), with the core program adopted in
each profile, it launches the app again and again on one profile and checks that each later launch exits 0 within 10 seconds, starts no app or core of its own (it counts the
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
1.0.0; and that a core program does not run until it is adopted, after which the app's copy runs
and the program itself does not. A sentinel core the runner started before the app must still
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

The same run checks the release feed (`src-tauri/src/release_native.rs`) on the Version
Management page,
against a fake https feed the runner serves on a random loopback port (`native/feed.ts`, with
`native/release-feed.json`) and a certificate authority made with `openssl` for that run. Only
native-test builds read that authority (`PLUXY_APP_TEST_FEED_CA`); other builds check a feed
against the public roots only. A stand-in browser, the run's default `https` handler through
its own XDG folders, logs each address it is asked to open. The check confirms a feed address in
the real dialog, then cancels a second change and finds the old address, its file and its copy
kept; asks for a third, finds Channel disabled, changes the channel by a direct call and confirms,
and finds the change not applied and the page saying why; checks for updates and reads the stable and the Preview (latest) versions; opens hostile
notes and finds no script, image, frame, handler or extra link, the Markdown rendered and the
raw HTML left out; probes every app and plugin command from the notes webview (each refused)
with the window's `release_get` as control; navigates the notes webview to an address the notes
do not link to (nothing opens) and follows the rendered link, a link whose address has characters the
renderer escapes (`a[1]?q=x|y`) and "View full release notes" (the stand-in browser gets exactly
those); asks the notes webview to download its own style sheet
(refused, nothing saved) and right-clicks it (no menu); collapses the notes (their webview closes) and expands
them again (a new one, over the notes area); scrolls the page with the navigation bar on top and
finds the notes webview over only the part of its area below that bar, hidden once the area is
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
  into `~/.cache/pluxy-app-native`, its SHA-256 checked; `mksquashfs` builds the image), which runs
  with `APPIMAGE_EXTRACT_AND_RUN=1`, the FUSE-free way to run an AppImage, in the run's cleared
  environment. It is a fixture, not the app's own package.

When the app has exited, the session stops what it started. On Ubuntu 24.04 the packages are
`xdg-desktop-portal` (frontend, document portal, permission store), `xdg-desktop-portal-gtk`,
`ayatana-indicator-application`, `squashfs-tools`, `xdotool` and `openssl`; the document portal also needs FUSE
(`/dev/fuse`, `fusermount3`). CI installs them on a pinned `ubuntu-24.04` runner.

## Packages

The base `tauri.conf.json` keeps bundling off. Two commands build packages, from this folder,
after `bun run build`; both use the Tauri CLI pinned in `package.json`:

```bash
bun run package:test <out-dir>          # the test package: a disposable AppImage for checks
bun run package:release [AppImage...]   # the release gate: builds and checks; uploads nothing
```

`package:test` is for checks only, never for release. It builds a debug app with the
`native-test` and `artifact-test-trust` features and a config overlay given with `--config`:
identifier `dev.pluxy.desktop.test`, product name `pluxy Test`, binary `pluxy-app-test`, the
AppImage bundle, and a temporary updater public key made for that build (its private key is never
written). It checks that the result is an AppImage of type 2 (an ELF file with `AI\x02` at
offset 8) and writes `<out-dir>/pluxy-test_<version>_amd64.AppImage` and `test-package.json`
(the AppImage's path and the key). Tauri's AppImage bundler would download the tools it runs
(linuxdeploy, its AppImage plugin and AppRun) into its tools folder when they are missing there,
unpinned. The test command puts pinned copies there first, each checked against its SHA-256
(`BUNDLER_TOOLS` in `package.ts`), and gives the AppImage plugin the pinned AppImage runtime
(`LDAI_RUNTIME_FILE`), which it would otherwise download too. The build runs with a temporary
HOME and cache folder, a temporary Cargo home holding only links to the caller's dependency
caches (so not the caller's Cargo config or credentials), none of the caller's other environment,
Cargo offline after `cargo fetch --locked`, and every proxy variable pointing at a closed port, so any download still tried fails
the build. Afterwards the tools folder must hold only the pinned tools and the bundler's own two
plugin scripts. The AppImage's type is still checked on the output.

`package:release` turns on no bundle format: release formats are not chosen yet. It builds the
release app with no features and no overlay (`tauri build --no-bundle`), finds that build's
snapshots by its receipt, and runs the [release validator](#validate-release-inputs) on them,
the staged trust folder (`src-tauri/trust`), the built binary and each AppImage given. Its last
line says `release-package: valid`, `release-package: refused: <reason>` or, when it could not
check, `release-package: failed: <reason>`.

### The updater gate

`trust::updater_gate` is what an updater must call, and pass, before any updater plugin call,
and the only place it gets its key. A build without the `artifact-test-trust` feature has only
production updater trust, which is not configured, so it is refused. A test build passes only
when its compiled identifier and product name are the test package's, the runner's trust
profile in HOME loads ([test trust](#package-signatures-and-trust)), and the profile's updater
key equals the compiled `plugins.updater.pubkey`. With the `native-test` feature the app reports
the gate's result, with the marker, identifier and product name it read, as its first step.
There is no updater yet; the signed-update slice calls this gate.

## Limits

- Device extensions: no production trust roots (so no curated source outside tests) or settings
  yet, and native tools run on Linux only (5.11 or later: the program host needs
  `close_range` with `CLOSE_RANGE_CLOEXEC`); a confirmed launch-class call is passed to its
  operation, and the app starts nothing itself yet; installing from a file needs the Linux file chooser. Safe mode is covered by unit tests, not a
  native run.
- Vault: Linux only. Secrets are kept in a 0600 file in the app's data folder, not in the system
  keyring, and only exact values are redacted. The window has no vault page yet; no secret is
  bound to a core or client yet, and no agent is started with one: secrets reach only native
  tools' pipes. Backups have no retention: a package keeps at most 256.
- Access: Linux only (5.8 or later: grants record the `statx` mount ID); web packages only; reads only (no writes or watchers); a signature only
  from a registry, not for a package installed from a file; document portal grants last one
  session.
- Tested on Linux only. On Windows the page document's address is
  `http://pluxy-page.localhost/`; neither Windows nor macOS has been run.
- No installer or bundled core yet: the only package is the test AppImage, which is not for release.
  Version Management shows releases; it does not install them.
- Changing the release feed needs the native confirmation, so it works on Linux only, and so
  does opening an address in the browser.
- Release notes tables show their columns left-aligned whatever alignment the Markdown asks for:
  the renderer writes alignment as inline style, which the notes' policy refuses.
- The app's own core comes only from a local index file for now; an https source joins with the
  release feed. Installing, rolling back and adopting need the native confirmation, so they work
  on Linux only, and the core store builds on Unix only.
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
artifact. Supply the actual app executable along with every package. The validator streams each
regular file for test markers, test identifiers and Tauri updater keys or signatures. Each
artifact must be a well-formed ELF executable (type, machine and program headers inside the
file) or a type-2 AppImage, and carry the build's receipt itself; anything else is refused,
since it cannot be looked inside. An AppImage (found by its header,
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

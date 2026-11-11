# pluxy desktop app

The desktop app is the UI kit's second build ([`../ui`](../ui/README.md)), in a Tauri 2 window,
with a tray icon. It shows core's pages in the same places and with the same themes as the web
panel, then adds its own Connection page. Extension pages open in webviews of their own.

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
no links, devices or names that leave the folder, at most 10,000 entries and 512 MiB.

**Install** (or **Update to** a newer version): the app copies the archive into its own folder and
checks it with the shared verifier (`src-tauri/src/artifact.rs`) and the app's trust
(`src-tauri/src/trust.rs`): the signature over the exact descriptor bytes with a publisher key
trusted for this exact source (publisher `pluxy`), then the archive's size and SHA-256. Only then
does the native confirmation open, showing the version, source, key and digest, and that a core
has full access to your user account. A release that fails a check is refused before any
dialog, and nothing is installed. Release builds have no publisher keys yet, so they refuse
every release; test builds trust only a test runner's temporary roots (below).

Once confirmed, the app unpacks the archive into a new version folder, records each file's
SHA-256, and makes the files read-only. Before every start it checks every file against those
digests and runs the core from the program file it just checked, not from its path again. A
changed version does not start: install it again.

**Switching** to a version (an install, **Roll back to** an installed version, or an adoption)
keeps a copy of `core/config.json`, stops the running core and starts the new one. It counts as
started once it says where it listens, on this computer, and answers `GET /v0/management/status`
with the app's management key and, for a release, reports the version it was signed as. If it
does not, the app puts the config back as it was, removes a version it installed for this
switch, and runs the version that worked before; the page says why the new one did not start.
If the app stops while a version starts, it does the same at its next start. The app keeps the
running version and the one before it, and removes older ones after a switch. Rolling back
reaches only an installed version.

A **core program** you name on the Connection page (or `pluxy` beside the app) is never run in
place. **Adopt this core program** shows its path, size and SHA-256 in the native confirmation;
once confirmed, the app copies it into a version of its own, `custom-<first 12 digits of its
SHA-256>`, if it still has that digest, and runs the copy. An adopted program has no signature.

The app only ever stops the core process it started. A core it did not start, such as a system
service or another core of yours on this computer, is never stopped or signalled.

Each change works once: while one waits for its confirmation, another is refused. The window
hears `pluxy-connection` when it has ended, and `core_versions` says whether it was done,
cancelled or failed.

## The window and the tray

Closing the window destroys it and its webviews, so they hold no memory while the app sits in
the tray. The tray menu shows what the app is connected to, and has "Open pluxy" (a new window)
and "Quit pluxy". If the app cannot make its tray icon, closing the window quits the app.

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
| `connection_get` | The connection, without the key |
| `connection_set` | Changes connection settings without accepting a key |
| `key_entry` | Opens native key entry, checks the key against core, and returns only signed-in, wrong-key, cancelled or error status plus the connection without its key |
| `core_restart` | Starts the app's own core again (managed mode) |
| `core_versions` | The app's own core versions, what the core source offers, and how the last change ended |
| `core_install`, `core_rollback`, `core_adopt` | Asks to install a release from the core source, to run an installed version again, or to adopt the chosen core program; each opens a native confirmation (Linux) and applies nothing itself |
| `core_call` | One management API call to the connected core |
| `sign_out` | Forgets the key (local and remote modes) |
| `page_open`, `page_place`, `page_close` | Opens an extension page's webview over part of the window, moves it, closes it |
| `page_send` | Passes the window's channel messages to a page's webview |
| `confirmation_request` | Opens native review for an opaque action ID already prepared by native code; returns no approval or token |
| `confirmation_settings` | Opens native settings to inspect and revoke exact remembered launches (Linux) |

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
rule is tested at the unit level. Import routing itself is separate work. Gesture construction
is private to native handlers and fixtures, and a gesture expires after five seconds.

Page Start uses the separate native entry point, always opens a fresh dialog, permits one pending
confirmation per original package and one attempt per second, and shows a GTK notice with
requester, action and outcome, including refusals. Page/Import pending entries have their own
limit of 16; trusted requests have a separate limit of 64. Page dialogs focus Cancel and keep
Confirm disabled for one second after mapping. Non-UTF-8 paths are refused before review;
Unicode format characters (including bidi and zero-width characters), control characters and
Pango line separators (U+2028/U+2029) are displayed as escaped Unicode.

Import routing and real launchers are added separately; this module contains no launcher. Host
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
no web process, and that quitting stops only the app's own core. The `native-test` feature is
for that test alone; release builds never have it.

In local and remote modes, the test also drives the real GTK dialog with `xdotool`: good keys
authenticate, bad keys and Cancel preserve the connection and saved settings, and the native
keep choice controls persistence. Random keys are generated at runtime. The test inspects the
main and page webviews' DOM, input values, storage and native callback/event payloads, and the
app's stdout/stderr, for key canaries without passing those canaries to JavaScript.

The managed run starts with no core installed and with signed test releases the runner makes
in a temporary folder: cores 1.0.0 and 2.0.0 (built with Go, the version set at link time, with
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
answer, with its config and program unchanged, after the app has quit. The native build has the
`artifact-test-trust` feature for this; `cargo test` covers a crash while a version starts,
changed installed files, hostile archives and a version that is not what it was signed as
(`tests/core_updates.rs`).

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

- Tested on Linux only. On Windows the page document's address is
  `http://pluxy-page.localhost/`; neither Windows nor macOS has been run.
- No installer or bundled core yet (`bundle.active` is off).
- The app's own core comes only from a local index file for now; an https source joins with the
  release feed. Installing, rolling back and adopting need the native confirmation, so they work
  on Linux only, and the core store builds on Unix only.
- In managed mode the app does not restart a core that stops; the tray and the Connection page
  show that it stopped and why, and the page offers to start it. A supervisor thread starts the
  core and stays until it exits, since on Linux the parent-death signal follows the thread that
  started it.
- Native key entry is implemented on Linux only. Windows and macOS refuse key entry until
  their native dialogs are implemented; there is no webview entry fallback.
- Hidden native entry keeps keys out of the webview API. It does not isolate them from another
  process with the same user's operating-system access, and transient native buffers are not
  guaranteed to be erased from memory.
- A kept key is stored in `connection.json`, not in the system keyring.
- Whether a desktop shows the tray icon is not checked: on a desktop with no tray host, closing
  the window leaves the app running with no way back to it but quitting it. The native test's
  session has a real tray watcher, but only a probe item is registered with it, not the app's own
  icon.
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

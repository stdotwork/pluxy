# App package contract

This is the reference for the app part of a pluxy package: what its manifest, archive, signature
and program must look like, and what the desktop app (the host) does with them. The host is the
desktop app's Rust code in `next/app/src-tauri/src/`; signature checks are in `artifact.rs`, the
device catalog in `device.rs`, the program host in `program.rs`, the router in `route.rs` and the
launch service in `spawn.rs`. Core reads the same manifests only to refuse app-only packages and to
use the provider part of a mixed one. To build a first package, start with the
[tutorial](TUTORIAL.md).

Two kinds of app package exist:

- A **web package** (`tier: "web"`) holds pages. It can read files the person grants it
  ([Access](#access-for-web-packages)) and, if curated, call native tools.
- A **native tool** (`tier: "native"`) holds a program that the host runs on demand. It has
  **full access to your user account**: the host separates APIs and storage, it does not sandbox
  the program.

## Manifest

An app package has one `pluxy-extension.json` at the archive root, with `manifest_version: 1` and
the same `id` (`publisher/name`) and `version` rules as the
[provider manifest](../../protocol/README.md#package-manifest). The app part goes in `app`. A
mixed package keeps the provider `protocol`, `programs`, `ui` and `config` at the top level and adds
`app` beside them; an app-only package has no provider fields. A version 0 manifest ignores `app`.

| Field inside `app` | Tier | Meaning |
| --- | --- | --- |
| `version` | both | App contribution format: `0`. |
| `tier` | both | `web` or `native`. |
| `host_versions` | both | Nonempty list of host contract major versions it works with; must include `0`. |
| `ui` | both | Optional pages, in the [UI format](../../ui/extension-pages/README.md#ui). Paths are relative to the package root. |
| `config` | both | Optional settings, in the [config format](../../ui/extension-pages/README.md#config). The device catalog serves no settings: a page's settings calls answer `unavailable`. |
| `resources` | web | Files the package may ask for: each `{"id", "kind": "file" \| "subtree", "operations": [...]}`. Only `file` with `read`, and `subtree` with `list` or `read`, can be granted. Declaring one grants nothing. |
| `programs` | native | Platform (`GOOS/GOARCH`) to the program's relative path. At least one. |
| `environments` | native | Optional. The only value is `wsl`: on Windows its Linux program may also run in a WSL distribution ([Linux build for WSL](#linux-build-for-wsl)). |
| `operations` | native | At least one: `{"id", "description", "input_schema", "output_schema", "effect"?, "secrets"?}`. `description` is one line of at most 800 characters, which its call's confirmation quotes as the add-on's words. `effect` is `read`, `write`, `launch` or `disclose`; absent means `write`. `secrets` maps at most four input fields, each a `string` property of the input schema, to `"value"` (the tool gets the secret) or `"reference"` (it gets a reference); a `launch` operation takes references only, and its input schema must be an object that requires `executable`, `argv` and `cwd`. The ID `describe` is the host's: a tool that declares it installs but is incompatible when it starts. |
| `exports` | native | Optional `{"contract": <integer ≥ 0>, "operations": [<declared IDs>]}`: the operations other packages may call, under an export contract number. |
| `imports` | both | Optional, at most 16: `{"package", "contract": [<accepted contracts>], "operations": [...], "required": <bool, default true>, "secrets": [...]}`, one per package, never itself. `secrets` (native only) lists the imported operations it may pass secret references to. |
| `state` | native | Required: `{"version", "readable_versions": [...]}`. An update or rollback is refused unless the new release's `readable_versions` holds the active release's `version`. |
| `produces` | native | Optional, at most 8: `{"kind": "<package ID>/<stream>", "schema": <integer>, "operation"}`, each kind once ([Jobs](#jobs)). |
| `consumes` | native | Optional, at most 8: `{"kind": "<own ID>/<stream>", "schema": [<accepted>], "deliver"}`, each kind once. `deliver` names an operation with effect `read` or `write` that is not exported and not a `produces` operation. |

Rules:

- Resource and operation IDs are 1 to 32 lower-case letters, digits and hyphens, starting with a
  letter or digit, unique in their list. Versions are nonnegative integers.
- Paths stay inside the package: no empty, `.` or `..` parts.
- A web package cannot declare programs, operations, state, jobs or environments. A native tool
  cannot declare `resources`.
- Job operations (`produces[].operation`, `consumes[].deliver`) have no `secrets`.
- Core never serves `app.ui`, never reads `app.config` and never starts `app.programs`. It refuses a
  package with neither provider `protocol` nor provider `programs` with
  `app-only package: install it in the desktop app`. A partial provider part is invalid; it cannot
  borrow fields from `app`.
- Core checks only `version`, `tier`, `host_versions`, `ui`, `config`, `resources`, `programs`,
  `operations` and `state` (`next/internal/host/app_manifest.go`). The desktop app checks every
  field, so a mixed package core accepts can still be refused by the app.

## Archives, catalogs and provenance

Archives follow the provider [archive rules](../../protocol/README.md#release-archives): one
gzip-compressed tar per platform, regular files and folders only, at most 512 MiB unpacked and
10,000 entries. The desktop app also refuses an archive over 512 MiB compressed, a manifest over
1 MiB, entry names with `:` or a Windows device name, and a `ui` folder of more than 1,000 files,
32 MiB or 20 pages. A registry index is at most 8 MiB.

| Rule | What the host does |
| --- | --- |
| Storage | Installed releases are kept read-only in the app's own store (`device-packages` in its data folder), separate from core's packages. Installing in one never installs in the other. |
| Checks before use | A web package's page files are hashed at install and checked again before they are served. A native tool's whole installed tree is checked again every time it is admitted. |
| Reserved publishers | `pluxy` and every publisher a trust root names. A package from another source that claims one is refused. |
| Unverified | Every package without a signature that is curated under the app's current roots, such as one from a source no root names or an unsigned one. It can show pages; it never calls native tools. |
| Native tools | Install only from a curated registry (one whose index address a trust root names), signed by that root's key. They install disabled. |
| Enabling | A native confirmation that says the tool has full access to your user account and lists its operations, imports and jobs. The admission is bound to the release's digest, source, signer and key; a new release, a rollback, disabling or removal ends it. |
| File grants | Never for a native tool or a reserved publisher's package. A web package needs a signature, a signer the person approved for that source, and a native confirmation. |
| Source | A release from another source than the installed package's is refused until the package is removed. Removing it forgets its grants. |
| Install from a file | Linux only. Such a package is never signed, so it never gets file grants. |

The program host takes a native tool only as `device::Admitted` (`device_admission.rs`), built only
by the catalog: `Catalog::admitted` checks the confirmation, the trust root, the signature and every
installed file again each time it is called. The record carries the ID, version, archive SHA-256 and
size, install time, source, signature, root key, store folder, program path and the program's
SHA-256, operations, imports, exports, jobs, state versions, app contribution version and host
contract.

Without trust roots nothing is curated. `next/app/src-tauri/trust/production.json` holds none, so
only a build with test trust (the native tests, the local build) admits native tools; test roots
are never substituted in a normal build.

### Linux build for WSL

A native tool that declares `"environments": ["wsl"]` can also run its `linux/amd64` or
`linux/arm64` program in WSL. That archive, the Linux build, is installed only when the person asks
in the main window (`device_install_wsl`), for a tool that is enabled and confirmed. It must be:

- the active release's version, from the same registry and signer, with a signature for that
  platform;
- a native tool with the same ID, version, host contract, app contribution version, operations,
  state, imports, exports, jobs and environments, with an executable program for that platform.

The app checks it as any archive, then asks in a native confirmation naming the package, version,
platform and archive SHA-256. Only a confirmed build is kept, by its SHA-256. Installing it stops
the tool's instances in WSL. An update, rollback or removal of the active release drops every
Linux build of the package, so a call in WSL then fails with
`Linux build for <platform> is not installed: install it for WSL` until it is installed again.
Before each start in WSL, `device::admitted_linux_unlocked` checks the tool's admission and the
Linux build's signature and files again.

### Signed descriptor bytes

A signed release has a UTF-8 JSON descriptor, a detached Ed25519 signature over its exact bytes,
and the archive:

```json
{"id":"example/notes","version":"1.0.0","tier":"web","platform":"linux/amd64","size":1234,"sha256":"<64 lower-case hex digits>"}
```

| Item | Rule |
| --- | --- |
| Descriptor | At most 16 KiB; unknown fields refused; valid ID, version and `os/arch` platform; `size` is the archive's byte count and `sha256` its lower-case hex digest. Whitespace and key order are signed: the exact bytes are verified and kept, never re-serialized JSON. |
| Signature | 64 raw bytes; 128 lower-case hex digits in an index. |
| Public key | 32 bytes; 64 lower-case hex digits. |
| Index entry | Beside `url`, `sha256` and `size`: `key_id` (1 to 64 letters, digits, `.`, `_` or `-`), `public_key`, `descriptor` (the exact bytes as a JSON string) and `signature`. All four or none. |
| Verifier | `ed25519-dalek` 2.2.0 with default features off, `VerifyingKey::verify_strict`. Separate from Tauri updater signatures. |

Order of checks, all before anything is unpacked: the descriptor's own fields; the signature over
its bytes; its platform against this computer's; the archive's size and SHA-256 against the
descriptor; the descriptor's ID and version against the index (a web package's descriptor also
against the index's size and SHA-256 and tier `web`). After unpacking, the manifest's ID, version
and tier must match the descriptor.

Which key counts:

- In a curated registry, every listed signature must be made with the root's key for that
  publisher and `key_id`, and `public_key` must be that key; otherwise the package is refused.
- In any other registry, a signature is verified with the index's `public_key` and kept for
  [Access](#access-for-web-packages). The package stays Unverified, web only, and cannot have a
  reserved ID.
- Whether a signature is curated is worked out from the app's current roots each time, never from
  what a release carries.

Provider packages (manifest version 0) keep digest-only trust; this contract adds no signature to
them.

## Access for web packages

A page of a web package installed on this device reads granted files through three page channel
methods, answered by the app's native code. Grants come only from the person, in the app's native
UI; a page cannot ask for one. A page core serves gets `unknown_method`. Grants can be made on
Linux only (Linux 5.8 or later). On macOS the methods answer with nothing granted
(`resources.read` and `resources.list` get `not_granted`); on Windows every call gets
`unavailable`.

| Method | `params` | `result` |
| --- | --- | --- |
| `resources.status` | `null` | `{"resources": [{"id", "kind", "operations", "granted", "session_only"}]}`, one per declared resource |
| `resources.read` | `{"resource", "path"?, "offset"?}` | `{"data", "size", "offset", "eof"}`: up to 1 MiB of a regular file from `offset` (default 0), base64 in `data`; `size` is the file's size |
| `resources.list` | `{"resource", "path"?}` | `{"entries": [{"name", "kind", "size"?}], "truncated"}`: entries sorted by name; `kind` is `file` (with `size`), `folder`, `link` or `other` |

- A `file` resource takes no `path` and cannot be listed. A `subtree` resource takes `path`,
  slash-separated parts inside the granted folder; for `resources.list`, empty or absent is the
  folder itself; `resources.read` needs one.
- `resources.list` reads at most 1,000 entries. A bigger folder gives `truncated: true`; entries
  that cannot be read or have names that are not UTF-8 count toward the 1,000 but are left out.
- A file picked through the desktop portal gets a grant for this session only (`session_only`).
- Grants are bound to the package's source, release digest and approved signer: a new release asks
  again. No grant reaches the app's own folders (settings, data, vault, packages, caches) or a
  folder holding one, checked at grant and before every call.
- Every page channel call's limits apply (64 KiB per call, 30 calls a second per page and 60 per
  package, replies at most 2 MiB).

| Code | When |
| --- | --- |
| `bad_request` | Wrong or unknown parameters, an undeclared resource or operation, a `path` with a `file` resource, no `path` for a `subtree` read, a resource name over 64 bytes, a `path` over 4,096 bytes, a negative or non-integer `offset` |
| `not_granted` | No grant for this package, release, source and signer, or not for this operation; the package is disabled or the page is not the active release's; the grant store is damaged |
| `refused` | An absolute path, `..`, `.` or empty parts, a link leaving the folder, another mount than the granted folder's, not a regular file, a target that is or holds one of the app's folders; inside a `subtree`, any open error but a missing file |
| `changed` | The chosen link, where it leads, or the target's identity or mount changed since the grant; refused until granted again |
| `unavailable` | The granted target or a file inside is gone, the target cannot be opened for now, a read fails, or the portal no longer offers the file; the grant stays |
| `revoked` | The grant was revoked while the call ran |

## App program contract v0

The program host runs this contract on Linux, Windows and macOS. It adds no provider operation.

**Start.** The host runs the admitted program directly from its read-only package folder, with a
cleared environment, private anonymous pipes on stdin, stdout and stderr, and no other descriptor.
The environment holds:

| Variable | Value |
| --- | --- |
| `PLUXY_APP_CONTRACT_VERSIONS` | Supported contract major versions, comma-separated: `0` |
| `PLUXY_EXTENSION_ID` | The manifest ID |
| `PLUXY_APP_ARTIFACT_SHA256` | The installed archive's SHA-256 |
| `PLUXY_TOOL_DATA_DIR` | The tool's own data folder ([The Linux host](#the-linux-host)) |
| `HOME` | The app's home folder (on Windows, the profile folder) |

Windows adds a few system values ([Windows and macOS](#windows-and-macos)).

**Frames.** Each frame is a four-byte big-endian length and that many bytes of UTF-8 JSON, as in
the provider [framing](../../protocol/README.md#framing); unknown fields are ignored. A frame from
the tool is at most 1 MiB. Stdout carries frames only; stderr is a log and must never hold
credentials.

1. The tool sends
   `{"type":"hello","contract":"app","versions":[0],"id":"example/tool","sha256":"<archive digest>"}`.
   The host checks the ID and digest against the admitted release and that `versions` holds `0`.
   This checks the tool answers the expected host, not its signature.
2. The host sends
   `{"type":"call","id":"c-1","op":"describe","contract_version":0,"unknown":[...],"journals":[...]}`
   ([unknown outcomes](#the-linux-host), [journals](#journals-and-recovery)). The tool answers
   `{"type":"result","id":"c-1","operations":[{"id":...}, ...],"state":{"version":...}}`. The
   operation IDs must be exactly the manifest's and `state.version` the manifest's, or the tool is
   **incompatible** until its package changes.
3. Work is `{"type":"call","id":"c-2","op":"<declared ID>","input":{...},"chain":[...]}`, with
   `job` and `secrets` when they apply ([Frames](#frames), [Jobs](#jobs),
   [Secrets](#secrets-and-references)). The tool may send `{"type":"progress","id":"c-2","value":{...}}`
   frames, then exactly one `{"type":"result","id":"c-2","output":{...}}` or
   `{"type":"error","id":"c-2","error":{"code","message"}}`.
4. `{"type":"cancel","id":"c-2"}` cancels one call; the pipe stays open. The tool stops the work
   and answers. If it answers, its answer stands; if it does not within five seconds, the host
   stops it and the call ends with `cancelled`. Closing the pipe cancels every call.

Error objects use the provider [error object](../../protocol/README.md#errors); the host passes a
tool's code on and uses `internal` when it has none. A call to an undeclared operation gets
`unsupported`. When the tool exits or its stdout ends before a call's answer, the call fails; it
never succeeds empty, and the host never retries it.

### States and lifetime

The catalog's states are separate from the process: a package is **installed**, **enabled** (by
the person) and, for a native tool, **admitted** (confirmed for this source and digest). The
program host's states for a tool are:

| State | Meaning |
| --- | --- |
| idle | Not running: never started, stopped when idle, or a start refused before the process ran. |
| starting | Started; hello and describe are under way (at most 10 seconds, 60 in WSL). |
| ready | Described and serving; no lease. It stops after 30 idle seconds. |
| active | Ready, with calls or jobs holding leases. |
| draining | Being stopped, or an old release after a swap: no new calls, its calls finish. |
| stopped | The process exited and was reaped. |
| crashed | It exited on its own, broke its pipe, left a cancel unanswered, or a start failed after the process ran (hello or describe timed out or failed). |
| quarantined | Five crashes in a row: calls are refused until the package changes. |
| incompatible | Hello or describe did not match the manifest. |

| Timer or limit | Value |
| --- | --- |
| Startup (hello and describe) | 10 s; 60 s in WSL |
| Idle stop after the last lease | 30 s |
| Grace after stdin closes on a stop | 5 s, then the process group (job on Windows) is killed |
| Answer to a cancel | 5 s, then the tool is killed |
| Crash backoff | 1 s, doubling per crash in a row, at most 1 min |
| Quarantine | 5 crashes in a row |
| Crash count reset | a clean stop, or 10 min of running |
| Drain limit after a swap | 10 min, then the old tool is killed |

On a stop (idle, a package change, app quit) the host closes the tool's stdin; the tool must then
end every process it started and exit. A cancel left unanswered, a broken pipe, a hello timeout and
the drain limit kill it at once. A dedicated supervisor thread starts each tool and stays until it
is reaped. Disabling or removing a package stops admission, cancels its calls and stops its tools;
its vault records stay ([Secrets, references and the vault](#secrets-references-and-the-vault)).
Stopping the process is the only way to end a native tool's access.

### The Linux host

These rules hold on every platform; [Windows and macOS](#windows-and-macos) and
[Tools in WSL](#tools-in-wsl-windows) list the differences.

**Process.**

- The tool starts from `Catalog::admitted`'s record at that moment, never from anything a caller
  passes. The host opens the program file, checks its SHA-256 and runs that opened file
  (`/proc/self/fd/<n>`), so a replaced file never runs. Use a native executable, not a script.
- `argv[0]` is the program's path; the process name (`comm`) is the descriptor number.
- Descriptors above 2 close as it executes (`close_range` with `CLOSE_RANGE_CLOEXEC`: Linux 5.11 or
  later).
- It leads a process group of its own and gets SIGTERM if the app dies (parent-death signal).
- `PLUXY_TOOL_DATA_DIR` is `<app data>/tools/<publisher>/<name>` (usually
  `~/.local/share/<the app's identifier>/tools/...`). The host makes it from the verified package
  ID, opening each part without following links; a part that is not a folder of this user's fails
  the start, and a folder others may use is set to 0700. It is beside the vault, not in it, and
  stays across updates, rollbacks and removal.

**Frames and calls.**

- A frame over 1 MiB, invalid JSON, a frame with no `id`, an unknown frame type or anything before
  hello breaks the pipe and the tool is stopped; a start that breaks fails saying which.
- An answer to a call that is no longer waiting is dropped. Progress frames of a job's call are its
  batches ([Jobs](#jobs)); other progress frames are read and dropped.
- An input is at most 64 KiB of JSON. Inputs and outputs are checked against the operation's
  schemas, which may use only `type`, `properties`, `required`, `additionalProperties`, `items`,
  `enum`, `const`, `minimum`, `maximum`, `minLength`, `maxLength`, `minItems`, `maxItems` and the
  annotations `title`, `description`, `default`, `examples` and `$comment`, at most 32 levels deep.
  A schema with another keyword makes the tool incompatible. An output its schema refuses fails the
  call with `extension_protocol`.
- A call is checked against its operation and input schema before the tool is leased or started.
- Calls to one package run one at a time, reads included, across its releases. At most eight are
  running or waiting their turn, and at most eight more wait for it to start or stop; more are
  refused with `unavailable`. Calls that waited on a failed start get that start's error.
- Each call holds a lease. Closing the window cancels every call; once the app is quitting, nothing
  starts a tool.

**Stderr.** Lines are logged with the tool's ID as prefix: at most 8 KiB a line and 50 lines a
second (the rest are counted), control and format characters (such as U+202E) escaped, and every
vault secret and reference replaced by `[redacted]`. The last 20 lines and the dropped count show in
the tool's status. Error messages and hello or describe values from a tool are escaped the same way.

**Crashes.** Nothing restarts a crashed tool on its own: a call after the backoff starts it, and a
call during the backoff is refused. A crash of a draining release is not counted. A stop the host
asks for while the tool is crashed, quarantined or incompatible resets it to idle.

**Unknown outcomes.** A call the tool received and did not answer (it crashed, was stopped, left a
cancel unanswered or reached the drain limit) has an unknown outcome: its error says so, and it is
never sent again. The package's next start lists it in describe:

```json
{"type":"call","id":"c-9","op":"describe","contract_version":0,"unknown":[{"id":"c-7","op":"apply","generation":1700000000}],"journals":[]}
```

`generation` is the release's install time. A tool that writes reconciles these calls with its own
records before it answers. The describe that carries them waits for the package's turn, so no other
call runs meanwhile; if the turn does not come within the startup limit, that describe carries none.
The next start of any release gets them, except a swap's candidate; once a start serves, the host
forgets them. Delivery may repeat, so reconciliation must be idempotent. A call refused before it
was sent, an `error` answer and an answered cancel have known outcomes.

**Swaps.** Updating or rolling back an enabled native tool:

1. Stages the release and asks a native confirmation of its own. A second update of the package is
   refused meanwhile.
2. Checks again that the package was not disabled, removed or changed while the dialog was open.
3. Starts the new release's tool and runs hello and describe with no call (no unknown outcomes, no
   journals).
4. Commits it as active, enabled and admitted, only if the release it replaces is still active and
   admitted. New calls go to the new tool; the package's pages close.
5. The old tool drains: calls holding its leases finish on it, then its stdin closes. At the drain
   limit it is killed and its unanswered calls have unknown outcomes.

A cancelled confirmation or a failed candidate installs nothing; the old release keeps serving.

### Windows and macOS

The program host, router, launch service, vault, launcher and native confirmations are built and
wired on Windows and macOS too (`program.rs`, `route.rs`, `spawn.rs`, `launcher.rs`,
`src/owned/`), with these differences. Only the Linux x86_64 build is usable: on Windows and
macOS CI runs the app against a fixture tool, and the coding-tool add-ons run there only in their own
unit tests.

- On Windows a tool's `HOME` is the profile folder (`USERPROFILE`), and it also gets `SystemRoot`,
  `APPDATA` and `LOCALAPPDATA` as the app has them, each only when it is a full path. Its data
  folder is under the app's local data folder (`<app local data>/tools`), private by its access
  list.
- Installed package files are read-only: on Windows by their access lists (the user and SYSTEM may
  read and run them); on Linux and macOS programs are 0555, other files 0444 and folders 0500. A
  Windows program must be a `.exe` or `.com` file starting `MZ`.
- Not on Windows or macOS: saving files (`files` frames get `unsupported`), Access grants (on
  Windows its commands are refused; on macOS listing and revoking answer, with nothing granted) and
  installing a package from a file.

Every process the app starts and ends goes through `src/owned/`: an unreaped child on Linux and
macOS, a process handle and its job object on Windows.

| | Windows | macOS |
| --- | --- | --- |
| What runs | The file the host checked, by its current path (with `\\?\` removed), held open with only reading shared so it cannot be renamed, replaced or written before it runs | The file the host checked, by its current path (`F_GETPATH`), once the path still names the same file and no other user can change it or any folder above it (below) |
| Handles and descriptors | Only its three standard handles (`PROC_THREAD_ATTRIBUTE_HANDLE_LIST`) | Descriptors above 2 close as it executes; a terminal launch also gets its program as 3 |
| Containment of a tool, a managed runtime, the app's core | A job object of its own (`JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`): everything in it ends when it is stopped or the app exits or crashes | A process group of its own (a tool's, a runtime's); the app's core stays in the app's group |
| Stopping a tool | Stdin closes; after the grace the job ends | Stdin closes; after the grace SIGKILL to the group |
| Stopping a runtime or launch | `CTRL_BREAK` to its process group on its own hidden console, then the job ends after the grace; an app with a console of its own ends the job at once | SIGTERM to the group, then SIGKILL after the grace |
| Detached launch | Started outside every job of the app's. Refused when the app's own job does not allow breakaway | A session and process group of its own, without the systemd scope |
| If the app crashes | Tools, runtimes and the core end with their jobs; detached launches run on | A tool sees its stdin close; runtimes and the core run on (no parent-death signal) and are reported `unknown` at the next start |

**The macOS path check.** Each part from `/` down to the file must be owned by the person or root
and not changeable by another user: its mode, and its extended access list (inherited entries too),
where an allow entry for another user or group with any right beyond `read`, `execute`,
`readattr`, `readextattr`, `readsecurity`, `synchronize` and the generic read and execute rights is
refused. The refusal names the entry and how to remove it (`chmod -a#`, `chmod -N`). Deny entries
pass. Every part must be on a volume that keeps ownership (not `MNT_IGNORE_OWNERSHIP`); the refusal
says to run `sudo diskutil enableOwnership` or move the program. For a detached launch, a managed
runtime and a terminal launch's program, a folder above may also be writable by the `admin` group
(as `/Applications` and Homebrew's folders are); another admin account can then replace what runs.

**Variables.** Windows compares variable names ignoring case. Besides the names no launch gets
([What a launch gets](#what-a-launch-gets)), a launch never gets `DYLD_*` on macOS, nor `COR_*`,
`__COMPAT_LAYER` or `WSLENV` on Windows. For a launch of Windows' own `wsl.exe` (a launch in WSL,
prepared by `pluxy/wsl`), the host sets `WSLENV` itself to the variables the launch sets or gives a
secret in, each with `/u`.

### Tools in WSL (Windows)

On Windows a package that declares `"environments": ["wsl"]` can also run in a WSL distribution,
as one of its users: an instance of its own beside the one on Windows (`environments.rs`). Only the
app's window chooses where a call runs; the host carries that choice itself, never in an
operation's input, and refuses an environment it has no record of or a package that does not run
in WSL. The record is what `pluxy/wsl`'s `check` returned when the person added the environment
([app tools](../../app-tools/README.md#wsl)): distribution, user, user ID, home, processor and the
Windows drives mounted there. Records are kept in `environments.json` (owner-only, at most 64).

| | In WSL |
| --- | --- |
| What runs | The Linux build for the distribution's processor, from the Windows disk through the drive the distribution mounts (`/mnt/c/...`; no mounted drive, or a `noexec` one, is refused), as `wsl.exe -d <distribution> -u <user> --cd <its folder> --exec <its program>` in a job of its own with three pipes. The program stays pinned (no writer, rename or delete) for the whole run. |
| Environment of `wsl.exe` | `SystemRoot`, `WSLENV`, the contract's variables, `PLUXY_TOOL_DATA_DIR`, `PLUXY_ENVIRONMENT=wsl:<distribution>:<user>` and `PLUXY_ENVIRONMENT_ACCOUNT=<user ID>:<home>`, each passed into the distribution with `/u`. No `PATH`. The tool also sees what WSL sets itself (`HOME`, `USER`, `PATH`, `WSL_DISTRO_NAME`, `WSL_INTEROP` and others). |
| Identity check | Before hello, the tool checks it runs in that distribution as that user, with that user ID and home (the program library does this: `next/app-tools/lib/program`), so a stale record runs nothing. |
| Data folder | `<home>/.local/state/pluxy/tools/<publisher>/<name>`, made by the tool; the host never writes in a distribution. |
| State | Each instance has its own process, turn, unknown outcomes and vault records. Changing, disabling or removing the package stops it in every environment; removing an environment stops what runs there and forgets the launcher's launches and recent folders there. |
| Start | 60 s for hello and describe. When WSL refuses to start the program (`0xd0000225`), the start is tried twice more, two seconds apart, before one crash counts. Text `wsl.exe` prints instead of a frame (at most 512 characters of its first 4 KiB) is shown in the start's error. |
| Confirmations | A write in WSL shows `Runs in WSL: distribution "<d>", user "<u>" (<home>).` above the review, and a remembered approval names it. A launch there shows `Starts in WSL: …` and runs only if `pluxy/wsl`'s prepared launch is exactly the one asked for and names only `executable`, `argv`, `cwd` and `program`. |
| Refused | A tool in WSL may use its vault; its own `call`, `jobs`, `spawn`, `runtime` and `files` frames get `unsupported`. |

## Calls between packages

The host routes every call of a native tool's operation: from the main window (`program_call`),
from a page (page channel method `native.call`) or from another native tool (a `call` frame). It
checks each call against the catalog at that moment and names the caller itself. Programs never
talk to each other directly.

### What a package declares

A native tool lists what other packages may call in `exports`, under an export contract number it
raises on an incompatible change. A caller lists the target package, the contracts it accepts and
the exact operations in `imports`. Only the contract number is compared, never package versions.

| Effect | Asks |
| --- | --- |
| `read` | Nothing, unless the call fills secret fields |
| `write` | A native confirmation each call, naming the chain and the exact input, `(write)` |
| `disclose` | The same, `(shows content)`; the tool may also hand over one file ([Saving files](#saving-files)) |
| `launch` | A native confirmation of the exact launch ([Launches and managed runtimes](#launches-and-managed-runtimes)) |

Only a launch's confirmation can be remembered.

### Enabling a native tool with imports

A native tool is enabled only when each required import's target is an enabled, admitted native
tool exporting those operations under an accepted contract, and when no import, required or
optional, leads back to it through other enabled tools. The same checks run when an update or
rollback is staged and again when it commits. A missing optional target is allowed; a call to it
fails with `extension_unavailable`. The enable confirmation lists the imports, and enabling is the
person's consent to them.

### Web packages: native consent

Only a curated web package may call native tools; an Unverified one never can. Its imports are
refused until the person allows them on the Add-ons page (`device_imports {id, allowed: true}`, main
window only), which opens a native confirmation listing the caller's source, digest and signer and
each target operation with its effect. The consent is bound to the caller's release and to each
target's release, export contract and operations. Disabling, updating, rolling back or removing the
caller or a target (or swapping a target) clears it. `allowed: false` revokes it and cancels the
package's running page calls; their answers are never delivered.

### Frames

A call from the host carries its chain:

```json
{"type":"call","id":"c-2","op":"summary","input":{"items":["a"]},
 "chain":[{"kind":"main"},{"kind":"package","id":"pluxy/relay","sha256":"<digest>","generation":1700000000}]}
```

The chain starts with the requester, `{"kind":"main"}` or
`{"kind":"page","id":<package>,"sha256":<release digest>,"page":<page ID>}`, followed by each tool
that called on, with its release digest and generation (install time). A tool calls an import for
the host call it serves, named in `for`:

```json
{"type":"call","id":"o-1","for":"c-2","package":"pluxy/target","op":"summary","input":{"items":["a"]}}
```

The host answers on the tool's stdin with `{"type":"result","id":"o-1","output":{...}}` or an
`error` frame; the tool may send `{"type":"cancel","id":"o-1"}`. Its calls are cancelled when the
call they serve ends or is cancelled.

Rules for the requests a tool sends to the host:

| Refusal | Frames | Code |
| --- | --- | --- |
| An ID that is not 1 to 64 letters, digits, `-`, `_` or `.` | all | `invalid_request` |
| An ID in use; a missing `op` or `for` (and `package` for `call`) | `call`, `jobs`, `spawn`, `runtime`, `files` | `invalid_request` |
| A frame naming `chain`, `caller`, `requester`, `from`, `origin` or `context` | `call`, `jobs` | `permission` |
| A `for` the tool is not serving | all | `permission` |
| More than eight of its own requests at once | `call`, `jobs`, `spawn`, `runtime`, `files` | `unavailable` |

A tool that leaves 64 answers unread on its stdin is stopped.

A page calls with `native.call`, params exactly `{"package", "op", "input"}` with `input` an
object. The app answers in the page's own webview, never through the main window. Codes map to the
page channel's: `permission` and `cancelled` to `forbidden`, `invalid_request` to `bad_request`,
the rest to `unavailable`. A native tool's own page may call only its own tool; a web package's page
calls only what it imports, with consent. A page call is cancelled when its webview closes or its
consent is revoked; after a reload its answer is not delivered.

### How a call is decided

A call goes ahead only if all of these hold, checked for each call:

- The window may call any declared operation of an admitted tool. A tool may call only an
  operation it imports and the target exports under an accepted contract (otherwise `permission`),
  and only while it runs its current admitted release. A web package's page needs its consent.
- The target is enabled and admitted. The call is pinned to the release it was allowed for: if
  another release would serve it, it fails with `extension_unavailable` and starts nothing.
- The target is not already in the chain, and the chain reaches at most five hops: the requester
  and four tools, the target included (`permission` otherwise).
- The input is at most 64 KiB, matches the input schema, and fills its secret fields as
  [Secrets and references](#secrets-and-references) says (`invalid_request` otherwise).
- A `write` or `disclose` call, and any call that fills secret fields, has a fresh native
  confirmation. A `launch` call has a fresh confirmation of the exact launch. Only the window's own
  call from a native gesture (Start in the app's launcher, or the tray's Start… menu, within five
  seconds) may use or save a remembered approval. A chain a page started is confirmed as the page's;
  a chain with a tool hop is confirmed as an import; neither offers Remember.
- A page's or an import's confirmation is limited to one attempt a second and one pending per
  package (the chain's first hop), 16 such pending in all; the window's own, to 64 pending. The
  limits are checked before an executable is hashed (`permission`). A dialog waits 60 seconds.
  Declining, expiry, or a cancel while it is open (its ticket, the window or page closing, consent
  revoked) ends the call with `cancelled` and closes the dialog. Once confirmed, the call is granted
  again and runs only if the target release and operation are still the ones confirmed.
- In safe mode no import and no page call is allowed.

A routed call then runs as any call from the window: registered so a cancel reaches it, under the
same limits, per-package turn, backoff, quarantine and unknown-outcome rules.

## Jobs

A job is bounded asynchronous work that one native tool, the **consumer**, asks of another, the
**producer**, through the host. The producer streams batches of a declared kind; the host delivers
each batch to the consumer, with a source only the host fills in.

### Registrations

A kind is `<package ID>/<stream>`, in the namespace of the package that consumes it, which defines
its schema. A producer declares `produces` in its manifest; there is no registration message. A
registration is read from the catalog at each request, from an enabled, admitted native tool's
active release: package ID, version, archive SHA-256, generation (install time), schema, source (its
registry index) and signer. Disabling, updating, rolling back, swapping or removing the producer
ends it. A consumer declares `consumes` for kinds in its own namespace only. Neither adds an import.

### Asking the host

While serving a host call, a consumer sends `jobs` frames for it; the host answers on its stdin
with a `result` or `error` frame of that ID:

```json
{"type":"jobs","id":"q-1","for":"c-2","op":"registrations","input":{"kind":"example/hub/items"}}
{"type":"jobs","id":"q-2","for":"c-2","op":"start","input":{"kind":"example/hub/items","package":"example/parser","sha256":"<digest>","generation":1700000000,"input":{...}}}
{"type":"jobs","id":"q-3","for":"c-2","op":"cancel","input":{"job":"j-7"}}
```

| `op` | Answer |
| --- | --- |
| `registrations` | `{"registrations": [{"package", "version", "sha256", "generation", "schema", "source", "signer": {"publisher", "key_id"}}]}`: each registration of the kind at a schema the caller accepts, other than its own. A kind it does not consume: `permission`. |
| `start` | `{"job": "j-<n>"}`, at once. The start must name one registration exactly and is decided as a call of the producer's operation with the consumer as caller ([How a call is decided](#how-a-call-is-decided)), pinned to the producer's release. Refused: a producer the consumer reaches through its imports, a `launch` operation, a secret value or reference anywhere in `input`, more than four jobs for this consumer or sixteen in the host (`unavailable`), a change to either package before it runs (`cancelled`). |
| `cancel` | `{"cancelled": "<job>"}` for one of its own jobs under way; any other ID: `permission`. |

Any other `op` gets `unsupported`.

### Running a job

The host calls the producer's operation with `"job": {"id": "j-7", "kind": "example/hub/items", "window": 4}`
in the call frame. The producer sends each batch as `{"type":"progress","id":"c-9","value":{...}}`
and the host acknowledges each once the consumer took it with `{"type":"ack","id":"c-9","seq":1}`
(numbered from 1). At most `window` batches wait unacknowledged. These fail the job and cancel the
producer's call, dropping undelivered batches:

| Batch | Code |
| --- | --- |
| Past the window | `extension_protocol` |
| A value over 48 KiB | `invalid_request` |
| Any field but `type`, `id` and `value` | `permission` |

The producer's `result` ends the job as done; its `error` or a cancel ends it as failed or
cancelled.

The host delivers to the consumer's `deliver` operation, as host calls with the job's chain plus the
producer, pinned to the consumer's release, one at a time and in order:

```json
{"job":"j-7","kind":"example/hub/items","seq":1,"event":"batch","value":{...},"source":{"package":"example/parser","version":"1.2.0","sha256":"<digest>","generation":1700000000,"schema":1,"source":"<index>","signer":{"publisher":"example","key_id":"k1"}}}
```

Then one end: `{"job", "kind", "event": "done", "output", "batches", "source"}` after every batch,
or `"event": "failed"` or `"cancelled"` with an `error`. The deliver operation's input schema must
accept both shapes.

- Each batch is granted again before it is delivered; if either package changed, the batches stop
  and the end is `cancelled`.
- The end is `done` only while both packages are still those releases and the output is at most
  48 KiB. It is delivered once, best-effort, only while the consumer is still that release. After
  it, a `cancel` of the job is refused.
- A batch the consumer does not take fails the job. A batch holding a secret value or reference is
  not delivered and fails the job; an end holding one is delivered as `failed` without it.
- Each delivery waits at most 10 seconds for the consumer's turn. A consumer must answer the call
  that started a job before deliveries can arrive, and never wait for its own job inside a call.
- Only the host calls a deliver operation; the window, pages and tools are refused it.
- The consumer is leased while its job runs; the producer while its call runs. Closing the window
  cancels every call, jobs included.

## Launches and managed runtimes

The host starts only what a person confirmed; a tool never starts a program itself through the
host. A `launch` operation's input names `executable`, `argv` (the arguments after the program's
name) and `cwd`, and may name the fields below. The router fills and checks them; the tool then
asks for the spawn.

| Field | Meaning |
| --- | --- |
| `program` | For a terminal launch: the program the terminal will run. The confirmation opens and hashes it as it does the executable, and the spawn refuses one changed since. Where it must appear and who checks it: see below. Only `detached`. |
| `file`, `within` | For an open launch: the file to open (one of `argv`) and the folder it must be inside, named together. See [Opening a file](#opening-a-file). |
| `stdin` | One of the launch's secret fields: its secret is written to the launch's standard input, which then closes. |
| `lifetime` | `detached` (default) or `managed` ([Detached and managed](#detached-and-managed)). |
| `environment` | `{"set": {NAME: value}, "unset": [NAME], "inherit": [NAME], "secrets": {NAME: <secret field>}}`, only these keys, at most 64 names in each. |

**`program` for a terminal launch.**

- Linux: `program` must be the argument right after the terminal's exec flag (`-e`, `-x` or `--`),
  which must be the first argument. The spawn refuses the launch unless that path still leads to the
  confirmed file and only root and the person can change the file, each folder on the way and each
  link: writable by others only in a sticky folder whose entry is root's or the person's, and by a
  group only when it is the person's own private group (their primary group, named as they are,
  with no other member, per `/etc/passwd` and `/etc/group`). A path through `/proc`, `/dev/fd` or
  `/dev/std*` is refused. The refusal names the path and a `chmod g-w,o-w` that fixes it. A package
  that starts a terminal without naming `program` gets no such check.
- macOS: `program` must be the argument right after `--`. The terminal is the execution program,
  which gets the confirmed file as descriptor 3 and runs it under the macOS path check
  ([Windows and macOS](#windows-and-macos)); the spawn runs the same check first.
- Windows: `program` must be one of `argv`. The execution program, in the launch's console, holds
  the program and every folder above it against deletion (and the program against writing), checks
  the path names exactly what it holds (final path, volume serial number and file ID), that the
  program is a `.exe` or `.com` starting `MZ` (and no `.com.exe` sits beside a `.com`), and that only
  the person, SYSTEM, Administrators and TrustedInstaller can change it or its folders. A refusal
  shows in the terminal window ([execution](../../app-tools/README.md#execution)).

**`environment`.** Names are ASCII letters, digits and `_`, not starting with a digit, at most 128
characters; a `set` value is at most 32 KiB with no NUL. A name may not appear in more than one of
`set`, `inherit` and `secrets`, nor twice in `unset`. Names below are compared ignoring case:

- These are never set, inherited or given a secret: `PLUXY_*`, `LD_*`, `MALLOC_*`, `CORECLR_*`,
  `COR_PROFILER_PATH*`, `DOTNET_PROFILER*`, `COMPlus_*`, `APPIMAGE`, `APPDIR`, `ARGV0`, `OWD`,
  `GCONV_PATH`, `GLIBC_TUNABLES`, `LOCPATH`, `NLSPATH`, `HOSTALIASES`, `RES_OPTIONS`,
  `NODE_OPTIONS`, `BASH_ENV`, `ENV`, `SHELLOPTS`, `BASHOPTS`, `PROMPT_COMMAND`, `PS4`,
  `PYTHONSTARTUP`, `PYTHONINSPECT`, `PERL5OPT`, `PERL5DB`, `PERL5LIB`, `RUBYOPT`,
  `JAVA_TOOL_OPTIONS`, `_JAVA_OPTIONS`, `JDK_JAVA_OPTIONS`, `DOTNET_STARTUP_HOOKS`, `COR_PROFILER`,
  `DOTNET_ADDITIONAL_DEPS`, `PYTHONPATH`, `PYTHONHOME`, `NODE_PATH`, `RUBYLIB`, and the platform
  additions in [Windows and macOS](#windows-and-macos).
- A name that looks like a credential (containing `TOKEN`, `SECRET`, `PASSWORD`, `PASSWD`,
  `CREDENTIAL`, `APIKEY` or `_KEY_`, ending in `_KEY`, or starting `TAURI_SIGNING_`) cannot be
  inherited; such a value reaches a launch only from the vault, through `secrets`.
  `SSH_AUTH_SOCK` can be inherited.

**The confirmation** shows the lifetime and each variable: its name and a set value, "inherited
from the app's environment", "removed", or the secret's label and ID; never an inherited or secret
value. Its environment revision is a SHA-256 of the policy version, the lifetime and the request,
so a remembered approval does not stand for another lifetime or environment. A remembered approval
is bound to the executable, `argv`, `cwd` and the secrets' IDs.

**The spawn.** While serving the call that confirmed it, the tool sends:

```json
{"type":"spawn","id":"s-1","for":"c-2"}
```

The frame names nothing else: the host starts exactly the confirmed launch for that call and that
tool's release, once, and answers `{"lifetime":"detached"}` or
`{"lifetime":"managed","runtime":"rt-<32 hex>","pid":1234}`. What it started does not end with the
call.

| Refusal | Code |
| --- | --- |
| A second spawn for the call, a call that confirmed no launch, a call the tool is not serving | `permission` |
| The executable or cwd changed since the confirmation; a secret refused now (removed from the vault, or a [client key](#client-keys) that fails its checks) | `unavailable` |
| The call it serves was cancelled, or the tool cancelled the request | `cancelled` |
| A tool in WSL | `unsupported` |

A tool that is stopping, or no longer runs its package's enabled, admitted release, gets no spawn
or runtime request served.

### Opening a file

A launch with `file` and `within` opens one file with the desktop's app for it. The confirmation
resolves both and refuses, before any dialog:

- a launch of any other shape. Linux: the executable is `xdg-open` (argv exactly the file) or `gio`
  (argv `open` and the file) from `/usr/local/bin`, `/usr/bin`, `/bin` or
  `/run/current-system/sw/bin`, the cwd is the file's folder, detached, with no `program`,
  `environment`, `stdin` or secret. A `native-test` build also takes an opener with one of those
  names from the folder `PLUXY_APP_TEST_OPENERS` names. Windows: `pluxy/execution`'s own program
  (by identity) with argv exactly `open`, `--` and the file, started with no console. macOS:
  `/usr/bin/open` with argv exactly the file;
- a file that is not a regular file, has an execute bit, is named `.desktop`, or is not inside the
  folder;
- a file, folder or folder between them that is not the person's own, that someone else may write,
  or that is a link. On Windows: owned by the person or the token's default owner, no allow entry
  giving anyone but the person, SYSTEM or Administrators a right to change it, not a link or
  junction (`src/win_acl.rs`). On macOS: owned by the person, no access list entry at all;
- a folder that is `/`, a drive's root, the home folder or above it, or any folder when the home
  folder cannot be found;
- a type that is not plain text: `text/plain`, `application/json`, `application/x-ndjson`,
  `application/jsonl`, `text/markdown` or `text/x-log`. On Linux the type is what GIO guesses from
  the name and the first bytes. On Windows and macOS it is the extension's (`.txt`, `.json`,
  `.jsonl`, `.ndjson`, `.md`, `.log`) and the content is not read (`src/confirmation_open.rs`);
- on Windows, a file on a network path, with an alternate data stream, a path of 260 characters or
  more, or a `\\?\` path. On macOS, a file with `com.apple.FinderInfo`, `com.apple.ResourceFork` or
  `com.apple.LaunchServices.OpenWith`.

The dialog says "Opens the file <file> (<type>, inside <folder>) with <opener>." (on Windows and
macOS "… with the app Windows (or macOS) opens .<ext> files with, through <opener>."). Both
identities, the file's mode, size, times and type are bound: the spawn checks them again by
resolved path without following links, and refuses the launch if anything changed. The launch gets
the launch environment with only the absolute entries of `PATH`. A desktop whose opener guesses
types otherwise than GIO may still pick another app.

### What a launch gets

- The executable the confirmation opened, with the confirmed `argv` and the executable as named as
  `argv[0]`, in the confirmed cwd. Linux runs the opened file (`/proc/self/fd/<n>`; a script's
  interpreter gets that path); macOS runs it by its current path after the macOS path check; Windows
  by its pinned path.
- `/dev/null` on stdout and stderr, and on stdin unless the launch names `stdin` (then a pipe that
  gets the secret and closes). No other descriptor of the app's, except a macOS terminal launch's
  program as 3.
- The launch environment: these values from the app's environment as it started, those it has,
  minus what the launch removes:

  | Platform | Values |
  | --- | --- |
  | Linux | `HOME`, `USER`, `LOGNAME`, `SHELL`, `PATH`, `LANG`, `LANGUAGE`, `TZ`, `TERM`, `COLORTERM`, `DISPLAY`, `WAYLAND_DISPLAY`, `XAUTHORITY`, `DBUS_SESSION_BUS_ADDRESS`, `XDG_RUNTIME_DIR`, `XDG_CONFIG_HOME`, `XDG_DATA_HOME`, `XDG_CACHE_HOME`, `XDG_STATE_HOME`, `XDG_CONFIG_DIRS`, `XDG_DATA_DIRS`, `XDG_SESSION_TYPE`, `XDG_CURRENT_DESKTOP`, `XDG_SESSION_DESKTOP` |
  | macOS | `HOME`, `USER`, `LOGNAME`, `SHELL`, `PATH`, `LANG`, `LANGUAGE`, `TZ`, `TERM`, `COLORTERM`, `TMPDIR`, `__CF_USER_TEXT_ENCODING` |
  | Windows | `USERPROFILE`, `USERNAME`, `USERDOMAIN`, `HOMEDRIVE`, `HOMEPATH`, `APPDATA`, `LOCALAPPDATA`, `PROGRAMDATA`, `PROGRAMFILES`, `PROGRAMFILES(X86)`, `PROGRAMW6432`, `COMMONPROGRAMFILES`, `COMMONPROGRAMFILES(X86)`, `COMMONPROGRAMW6432`, `SYSTEMROOT`, `SYSTEMDRIVE`, `WINDIR`, `COMSPEC`, `PATHEXT`, `PATH`, `TEMP`, `TMP`, `OS`, `PROCESSOR_ARCHITECTURE`, `NUMBER_OF_PROCESSORS`, `COMPUTERNAME`, `LANG`, `TZ` |
  | All | `LC_*`; `http_proxy`, `https_proxy`, `no_proxy`, `all_proxy` (and in capitals) |

  In an AppImage, entries inside its own folder are taken out of `PATH`, `XDG_DATA_DIRS` and
  `XDG_CONFIG_DIRS`. On macOS `PATH` then gets, after its own entries, those of `~/.local/bin`,
  `~/.npm-global/bin`, `~/.bun/bin`, `~/.cargo/bin`, `~/.volta/bin`, `~/Library/pnpm`, `~/bin`,
  `/opt/homebrew/bin` and `/usr/local/bin` that exist and it lacks. Then come the inherited names,
  the set values and the secrets, which the host resolves from the vault at the spawn, each checked
  as a delivery to the tool whose launch it is. No shell is run to read a login environment.
- A process given a secret's value (by `environment` or `stdin`) gets core file size limits of 1,
  soft and hard, inherited by what it starts, so the kernel writes no core file and runs no core
  dump handler for it. On Windows it gets `SEM_NOGPFAULTERRORBOX`. Other launches get the limits the
  app started with.

### Detached and managed

A **detached** launch is started by a short-lived wrapper in a session and process group of its own,
with no parent-death signal; the app reaps the wrapper and never signals the launch again. It runs
on after its tool stops, crashes or is swapped, disabled or removed, and after the app quits.

Only a direct launch can be **managed**: `lifetime: "managed"` with a `program` is refused with
`invalid_request` before the confirmation (many terminals hand their window to a server process
and exit, so the host could neither watch nor stop the agent). A managed runtime:

- leads a process group of its own (a job object on Windows), held on a dedicated thread that
  started it and stays until it is reaped, so its process ID is never reused while the host may
  signal it;
- gets SIGTERM if the app dies, on Linux; on Windows it ends with its job; on macOS it runs on;
- belongs to its package, whatever release asks: a tool restarted or swapped controls the runtimes
  its package started.

While serving a call, a tool sends:

```json
{"type":"runtime","id":"u-1","for":"c-5","op":"status","runtime":"rt-<hex>"}
```

| `op` | `output` |
| --- | --- |
| `list` | `{"runtimes": [<status>...]}`: its package's runtimes |
| `status` | `{"runtime", "package", "state", "pid", "started", "restarts", "executable", "argv"}`, with `exit` once it ended and `note` when `unknown`; `state` is `running`, `stopping`, `exited` or `unknown` |
| `stop` | The status once reaped: SIGTERM to its group (Windows: `CTRL_BREAK`), then SIGKILL (Windows: the job ends) after five seconds or as soon as its leader exits. Fails if it has not stopped within 7 seconds. |
| `restart` | The status once it runs again: stopped as above, then the same confirmed launch started again. Refused if its executable or cwd changed, a secret it gets is refused now, or its launch was dropped. |

- Another package's runtime is `not_found`. An unknown `op` is `unsupported`. `stop` and `restart`
  of a runtime being started or restarted, or of an `unknown` one, are refused.
- A package keeps at most eight runtimes, running or ended (`unknown` ones do not count); the
  oldest ended one makes room. When a runtime's leader exits, the rest of its group is killed.
- The window's `program_status` lists the package's runtimes.
- The app's quit stops every managed runtime and starts none after.
- Disabling or removing the package stops its runtimes and drops their confirmed launches
  (`restart` is then refused; a new launch needs a new confirmation); none starts until it is
  enabled again. A tool's idle stop, crash or swap stops none.
- `runtimes.json` in the app's data folder (owner-only) lists the running and stopping runtimes.
  At the next start each is reported `unknown` for that run and never signalled, since a process ID
  alone does not prove it is still that runtime; a descendant that ignored SIGTERM or left the
  group may live on.

### What a detached launch does not leave

On Linux, when the session bus has a systemd user manager, the host asks it
(`org.freedesktop.systemd1.Manager.StartTransientUnit`) for a transient scope
`app-pluxy-launch-<16 hex>.scope` holding the launch's wrapper, which waits until it has been moved
there before it starts the launch. Connecting, the call and the move each wait at most two seconds.
The launch and what it starts are then outside the app's cgroup, so stopping a systemd unit that runs
the app, or `systemd-oomd` acting on the app's cgroup, does not end them; the scope goes when its
last process exits. With no user manager or session bus, or when it refuses (as for an app in a
cgroup it does not own), the launch runs in the app's cgroup and the app logs why: once for a
missing manager, each time for a refusal.

A process of the same user, a native tool among them, can read a launch's environment, secrets
included, in `/proc/<pid>/environ`. Same-user isolation is not claimed.

### The app's own core files

At its start the app lowers its own soft core file size limit to 0 and keeps the hard limit; its
tools inherit it. A developer can raise it for a running app
(`prlimit --core=unlimited --pid <app>`). The app does not mark itself undumpable. It keeps the
vault's secrets in memory, not in its environment, so a core dump handler's record of the
environment holds no secret.

## Saving files

On Linux, a native tool can save a file for the person through the host, at a place the person picks
in a native save dialog; neither a page nor a tool names the path. It may do so only while serving a
call of one of its `disclose` operations, and once per call. That is how a page offers an export: it
calls its tool's `disclose` operation, and the tool hands the bytes to the host. There is no page
channel method for it. On Windows, macOS and in WSL every `files` frame gets `unsupported`.

```json
{"type":"files","id":"f-1","for":"c-2","op":"append","input":{"offset":0,"data":"<base64>"}}
{"type":"files","id":"f-2","for":"c-2","op":"save","input":{"name":"session-019a.jsonl"}}
```

| `op` | Input | Answer |
| --- | --- | --- |
| `append` | `offset` (the bytes so far), `data` (standard base64) | `{"size": <bytes so far>}`. A frame is at most 1 MiB, so a larger file takes several. Past 64 MiB in all: `invalid_request`, and the bytes so far are dropped. |
| `save` | `name`: the name to suggest, nothing else | Shows the dialog titled `Save a file from <package>`; answers `{"outcome": "saved", "size": <bytes>}` or `{"outcome": "cancelled"}`. |

- A name holding `/`, `\` or NUL, or an input with another field: `invalid_request`. Control and
  format characters become `_`, leading dots and surrounding white space are dropped, an empty name
  becomes `file`, and it is cut to 255 bytes.
- Once a `save` passed those checks, every later `files` frame for the call gets `permission`, as
  does any `files` frame for a call whose operation is not `disclose`.
- A frame for a cancelled call: `cancelled`. An unknown `op`: `unsupported`. A `save` while another
  dialog shows: `unavailable`.

The dialog is GTK's file chooser (the desktop portal's with `GTK_USE_PORTAL=1` or in a sandbox), which
asks before replacing a file. Cancelling it writes nothing; so does the call ending, which closes it.

When the dialog answers, the host opens the chosen folder once. It refuses (`unavailable`) a folder
not owned by the person or root, or writable by group or others unless sticky, and a chosen name
that holds anything but nothing or a regular file of the person's (a link, a folder, another user's
file).

The write, in order:

1. The tool must still run its package's admitted release.
2. The opened folder must still be the one at the chosen path, and still the person's.
3. A temporary file is made in that folder without following links, mode 0600; the bytes are
   written and synced.
4. The name must still be what the dialog answered: nothing, or the same file (device, inode, size,
   modification and change times). Any change there, or to the folder in step 2, is refused
   (`permission`) and the temporary file removed.
5. A replaced file's permission bits are copied to the new one, which is renamed over the name; the
   folder is synced. A new file never replaces anything: `RENAME_NOREPLACE`, else a hard link at the
   name; with neither, the save is refused (`unavailable`). For an overwrite, a change between the
   check and the rename is not caught.

Neither the bytes nor the chosen path are logged or returned.

## Secrets, references and the vault

The app keeps a host-private **vault** (`vault` in its data folder; on Windows its local data
folder). It holds the secrets the person entered and each native tool's backups and journals.

- Linux and macOS: folders 0700 and files 0600 of the user; extended access list entries are
  cleared or refused on macOS.
- Windows: a protected access list whose allow entries name only the user and SYSTEM, and an owner
  that is the user, the token's default owner or SYSTEM, checked at each read.
- No package names or reads it through the host, and no window or page API gives its paths, a
  secret's value, a backup's bytes or a journal's intent. A native tool has the user's full access
  and could read it directly: this separates APIs and storage, not programs of the same user.
- A vault whose secrets cannot be read keeps none and refuses every change; vault frames then get
  `unavailable`.

### Secrets and references

A secret enters only through native key entry (`vault_secret_add`): GTK's hidden entry on Linux, a
password edit control on Windows, `NSSecureTextField` on macOS. The window gets back only its ID
(`s-` and 32 lower-case hex digits), never the value. Limits: at most 64 secrets, a label of 1 to 64
printable characters, a value of 8 to 4,096 printable bytes.

An operation's `secrets` fields carry secrets:

| Caller | A secret field holds |
| --- | --- |
| The window | A vault secret's ID |
| A native tool | A reference the host gave it in the call it serves, passed to an operation its import lists in `secrets` |
| A page | Nothing: a page cannot pass secrets |

A secret's value or a reference anywhere else in an input, at any depth, as a name or a value, is
refused with the same `invalid_request` any input off its schema gets, so the error does not say a
string was a secret.

A call that delivers a value (a `value` field), or one the window gives a secret to, asks a native
confirmation naming each secret, its label and ID, and the tool that gets it, even for a `read`
operation. A launch takes references only, so a remembered launch approval never stands for a
delivered value; it is remembered with the secrets' IDs. Passing a reference on to a `reference`
field asks nothing more.

The tool finds a new reference of its own (`r-` and 32 lower-case hex digits) in each secret field,
and each `value` field's secret in the call frame's `secrets`:

```json
{"type":"call","id":"c-4","op":"apply","input":{"key":"r-<hex>","file":"/home/me/.config/x.json"},
 "chain":[{"kind":"main"}],"secrets":{"r-<hex>":"<the secret>"}}
```

A reference is bound to its secret, to the release it was given to (ID, digest, generation) and to
that call. It is refused from another tool or release, in another call, after its call ended and
once its secret is removed. References live in memory only.

The host replaces every secret value and reference with `[redacted]` in a tool's logged stderr, its
status, errors leaving the host and every call's output. Overlapping values are replaced as one; a
removed secret's value is replaced until the app restarts. Only exact values are replaced.

### Client keys

A client key, the key a coding tool sends core with its requests, is a vault secret of its own,
apart from the management key. It enters only natively:

- `client_key_capture` (label, client): trims the label and refuses, before core is asked, a
  client that is not a native tool, a label of more than 64 characters or more than one line, and
  a full vault. It asks no native confirmation of its own: the window can already have core make a
  client key through management calls, and the key leaves the vault only in a call of its tool,
  whose native confirmation names it. The app asks core for a key (`POST /v0/management/client-keys`
  with `{"label"}`) and keeps the key and core's ID from the reply. Core refuses a label equal to
  one of its keys before making a key. If the vault cannot keep the key, the app deletes it on core
  again.
- `client_key_enter` (label, client): reads an existing key from native key entry and keeps it once
  core takes it. Core's ID is recorded only when exactly one of core's keys has its masked form.

The key is bound to the core the app is connected to (mode and origin), core's ID of it, its masked
form and the native tool it is for:

```json
{"id":"s-<hex>","label":"Claude Code","client_key":{"core":{"mode":"managed","origin":"http://127.0.0.1:<port>"},
 "key_id":"<24 hex digits>","masked":"sk******XY","client":"pluxy/claude-code"}}
```

The window gets that record, never the key. At every use, before the confirmation and again just
before a value goes into a call frame:

- It goes only to its own tool, also when another tool passes a reference on.
- The app must be connected to the key's core, checked before anything is sent.
- Before a value is delivered, core must still take it: the app calls `GET /v1/models` with the key;
  a 401 is `permission`. Then the app checks again that the connection did not change and the
  secret is still in the vault.

A launch's secret is a value delivered to the tool whose launch it is. Its checks run before the
confirmation and the call, and again each time the host turns the reference into a value: at the
spawn and at a managed runtime's start and every restart. Right before the process starts, the
host checks once more that each secret is in the vault and the connection is the one each key was
checked on.

### Vault frames

While serving a host call, a tool may send `vault` frames for it, answered on its stdin with
`result` or `error`:

```json
{"type":"vault","id":"v-1","for":"c-4","op":"journal.begin","intent":{"file":"/home/me/.config/x.json","sha256":"<hex>"}}
```

| `op` | Fields | `output` |
| --- | --- | --- |
| `backup.put` | `label` (at most 64 characters, no control characters, no secret or reference), `data` (base64, at most 512 KiB decoded) | `{"backup": "b-<hex>"}` |
| `backup.get` | `backup` | `{"backup", "label", "size", "created", "data"}` |
| `backup.list` | none | `{"backups": [{"backup", "label", "size", "created"}]}` |
| `backup.delete` | `backup` | `{}` |
| `journal.begin` | `intent` (a JSON object of at most 16 KiB, no secret or reference) | `{"journal": "j-<hex>"}` |
| `journal.commit` | `journal` | `{"journal", "outcome": "committed"}` |
| `journal.end` | `journal`, `outcome` (`committed` or `rolled_back`) | `{"journal", "outcome"}` |

- Backups and journals belong to the package, whatever its release, and stay when it is removed. A
  package keeps at most 256 backups; retention is the tool's.
- In a call, a tool ends only journals its own release began. While answering a describe that names
  journals or unknown outcomes, it may send vault frames for that describe too, only `backup.get`,
  `backup.list`, and `journal.commit` or `journal.end` of the journals that describe named.
- One journal is open at a time per package.
- Files in a package's vault folder that its records do not name are removed when next read.

| Code | When |
| --- | --- |
| `invalid_request` | Bad fields (including a label or intent holding a secret value or reference, which the error does not name); a journal already ended with another outcome |
| `not_found` | Another package's or an unknown ID; a journal the tool may not end |
| `unavailable` | The vault cannot be read or written; a second journal while one is open; 256 backups |
| `permission` | A `for` the tool is not serving; an `op` a describe may not send |
| `unsupported` | An unknown `op` in a call; a host with no vault |

### Journals and recovery

A tool that writes begins a journal before the write, naming what it means to write in `intent`
(such as each file and the SHA-256 of its new bytes), keeps the old bytes as a backup, and commits
the journal once the write is done. A journal still open when the tool or the app stopped stays on
disk, and the package's next start names it in describe, on the package's turn:

```json
{"type":"call","id":"c-9","op":"describe","contract_version":0,"unknown":[...],
 "journals":[{"id":"j-<hex>","op":"apply","generation":1700000000,"began":1700000001000,"intent":{...}}]}
```

- The tool reconciles each before it answers: it looks at what is on disk and ends the journal as
  `committed` (the write took effect) or `rolled_back` (it did not, or the tool put the old bytes
  back), never by writing again.
- A start whose describe leaves a journal open fails (`extension_protocol`). A start whose journals
  cannot be read fails (`unavailable`; `program_status` shows `journals_error`). A start that cannot
  take its package's turn within the startup limit while a journal is open fails
  (`extension_unavailable`) and is retried after the backoff. Unknown outcomes alone wait for a later
  start.
- Ending a journal again with the same outcome answers the same with `"again": true`, since delivery
  may repeat; another outcome is refused.
- A swap's candidate is told of no journal.
- The window's `program_status` lists the package's open and recently ended journals and whether
  each was reconciled after a crash.

Config writers use the app tools' transaction library
([`lib/transactions`](../../app-tools/README.md#libtransactions)): it pins a file at preview, writes
only its target, keeps backups (10 manual backups per set) and a journal in the vault, and
reconciles them from disk.

## Shared manifest fixtures

[`protocol/testdata/app-manifests`](../../protocol/testdata/app-manifests) holds manifest cases
with expected outcomes in `cases.json`. Go runs them through core's install route
(`next/e2e/packages_test.go`), and the desktop app's Rust tests check each one's `manifest_valid`
against its validator (`shared_manifest_fixtures_agree_with_core` in `device_tests.rs`). They are
manifests only: no signed artifacts.

# App package contract

This reference defines the app contribution to a pluxy package and app program contract v0.
The signature verifier is shared Rust code in `src-tauri/src/artifact.rs`. The desktop app
installs page-only web packages and curated signed native tools on this device
([extensions on this device](../README.md#extensions-on-this-device)), grants signed web
packages files through [Access](#access-for-web-packages), and runs an admitted tool's program
on demand through the program host (`src-tauri/src/program.rs`, Linux).
Core recognizes app contributions to refuse app-only packages and use only the provider part of
a mixed package. Start with the [tutorial](TUTORIAL.md).

## Manifest

An app package has one `pluxy-extension.json` at the archive root. It uses `manifest_version: 1`,
the same `publisher/name` ID and package version rules as the [provider manifest](../../protocol/README.md#package-manifest).
Version 0 provider manifests keep their existing meaning and ignore an unknown app field. Version 1 adds `app`. A mixed package
keeps provider `protocol`, `programs`, `ui` and `config` at the top level and adds `app` beside
them. App-only packages omit provider fields; no dummy provider is needed.

| Field inside `app` | Meaning |
| --- | --- |
| `version` | App contribution format, initially `0`. |
| `tier` | `web` for assets and broker resources, or `native` for a full-access program. |
| `host_versions` | Nonempty list of compatible app host contract major versions, initially `[0]`. These are contract versions, not desktop release numbers. |
| `ui` | Optional pages, using the existing [UI format](../../ui/extension-pages/README.md#ui), with its own version. Paths are relative to the package root. |
| `config` | Optional settings, using the existing [config format](../../ui/extension-pages/README.md#config), with its own version. App settings belong to this device. |
| `resources` | Web only: requested broker resources, each with an `id`, `kind` (`file` or `subtree`) and nonempty `operations` list. A declaration grants no access. |
| `programs` | Native only: platform (`GOOS/GOARCH`) to relative executable path, as for provider programs. At least one entry is required. |
| `operations` | Native only: declared operations, each with an `id`, plain `description`, `input_schema` and `output_schema` JSON objects, an optional `effect` (`read`, `write` or `launch`; absent means `write`) and optional `secrets`: at most four input fields that carry a secret, each `"value"` (the tool receives the secret) or `"reference"` (it receives only a reference to it; a `launch` operation's are references only); each must be a `string` property of the input schema. See [Secrets, references and the vault](#secrets-references-and-the-vault). At least one operation is required. |
| `exports` | Native only, optional: `{"contract": <nonnegative integer>, "operations": [<declared IDs>]}`, the operations other packages may import and the version of that export contract. |
| `imports` | Optional, both tiers: at most 16 entries `{"package": <ID>, "contract": [<accepted export contracts>], "operations": [<IDs>], "required": <bool, default true>, "secrets": [<IDs>]}`, one per package, never the package itself. `secrets` (native tools only) lists the imported operations it may pass secret references to. See [Calls between packages](#calls-between-packages). |
| `state` | Native only: `version` written by this release and nonempty `readable_versions` list. Check state compatibility before swap or rollback. |

Resource and operation IDs use 1 to 32 lower-case letters, digits and hyphens, starting with a
letter or digit, and are unique within their respective lists. State and host contract versions
are nonnegative integers. Paths must stay inside the
package. Web packages cannot declare programs, native operations or state; native packages cannot
request folder grants through `resources`. Resources describe exact files or bounded subtrees
selected later by the user, never automatic home-directory access. Jobs belong to a later host
contract; these fields do not introduce them.

Package release, manifest, provider protocol, UI, config and app contribution versions are
separate. App program negotiation is separate too. A mixed package can be compatible with core
while incompatible with this desktop host. Each target installs independently: core never serves
`app.ui`, reads `app.config` as provider settings or starts `app.programs`. Core refuses an app
contribution with neither provider `protocol` nor provider `programs` with
`app-only package: install it in the desktop app`. A partial provider contribution is invalid;
it cannot borrow fields from `app`.

## Archives, catalogs and provenance

Use the provider [archive and index limits](../../protocol/README.md#release-archives): one
platform archive, regular files and folders only, no links, devices, traversal or install hooks;
512 MiB unpacked, 10,000 entries and an 8 MiB index limit. Copy selected local archives into an
immutable store. Hash the installed tree and verify it again before serving assets or spawning.
The device and `Core <address>` catalogs have separate stores, grants and secrets. Installing in
one never installs in the other. An offline catalog preserves installed releases.

Reserve publisher `pluxy` and configured first-party publisher identities for curated entries.
Refuse local/private sources claiming them. Other non-curated web packages are labelled
**Unverified** and may display without resource access. Native programs require an exact
first-party release in the curated index and a curated first-party key. An untrusted index
listing the same ID or digest does not qualify.

Resource access requires a publisher signature and an explicitly approved signer key, followed
by native confirmation. Native imports require separate native confirmation and are limited to
curated first-party web packages. A signature proves the signer, not safety. A source change
clears prefilled grants and trust and requires fresh confirmation, even with matching ID, version
and content. Record source trust domain, key identity, digest and contract versions. Native
enablement displays **full access to your user account** in the main UI and native confirmation.
Its page still has no Tauri permissions. Process isolation contains crashes; it does not confine
a native program.

The program host takes a native tool only as `device::Admitted`
(`src-tauri/src/device_admission.rs`), from `Catalog::admitted`, which checks the confirmation,
the root, the signature and every installed file again each time it is called. Only the
catalog builds one; it offers, read-only: ID, version, archive SHA-256 and size, install time,
source, signature, the root's public key, store folder, program path and the program's SHA-256
(check the opened file against it before running it), operations, state versions, app
contribution version and host contract.

### Signed descriptor bytes

A signed artifact has a UTF-8 JSON descriptor, detached Ed25519 signature and archive:

```json
{"id":"example/notes","version":"0.1.0","tier":"web","platform":"linux/amd64","size":1234,"sha256":"<64 lower-case hex digits>"}
```

`size` is the exact archive byte count; `sha256` hashes the archive, covering its manifest, assets
and programs. The publisher fixes the descriptor bytes before signing. Verify the detached
signature over those exact bytes against the trusted publisher key, then verify archive size
and SHA-256 **before extraction**. Check the extracted manifest ID, version and tier against the
descriptor, and platform against the selected artifact. Store the exact descriptor bytes and
signature; never verify a receiver's reserialized JSON. Whitespace and key order are signed too.
The index pins size, digest, key identity and signature: a signed artifact adds `key_id` (1 to
64 letters, digits, `.`, `_` or `-`), `public_key`, `descriptor` (the exact descriptor bytes as a
JSON string) and `signature` beside `url`, `sha256` and `size`, all four or none; part of a
signature is refused. The desktop app verifies one only for a reserved publisher's package from a
curated registry, one whose index address equals a trust root's `source` exactly: the key is the
root's for that publisher and `key_id`, and `public_key` must be that key, or the package is
refused. In a curated registry every listed signature is checked so, whatever the package's
publisher: a key that is not the root's is refused. A signature a registry no root names lists
is verified with the `public_key` the index names and kept for
[Access](#access-for-web-packages); the package is Unverified, is never a native tool and never
has a reserved ID. Detached signature files contain exactly
64 raw Ed25519 bytes; JSON/index transport encodes them as 128 lower-case hexadecimal digits.
Public keys are 32 bytes, encoded as 64 lower-case hexadecimal digits. Verification uses pinned
`ed25519-dalek` 2.2.0 with `VerifyingKey::verify_strict` and default features disabled. This is
separate from Tauri updater signatures. Trust binds the publisher, key identity and exact source
trust domain; a changed source needs fresh trust. The feature-free `verify_descriptor` accepts
an approved key from native settings for web resources or curated native admission; both use
the same verifier. `PublisherKeys` supplies publisher/source/key lookup separately.
`VerifiedArchive<R>` retains the rewound checked reader. Later folder-grant, native-admission and core-update code uses `into_archive()`
instead of reopening a path, prevent concurrent writes, and check manifest identity after extraction.

For such a signature, the desktop app refuses a release whose descriptor does not match the
index's ID, version, `web` tier, this computer's platform, size and SHA-256, or the archive,
before unpacking it. Its key comes from the index: a person approves it natively, for that
source only, before any grant. Whether a signature is curated is derived from the app's current
roots each time, never from a release carrying one.

Version 2.2.0 is retained over stable 3.0.0 to avoid its curve25519-dalek 5, ed25519/signature 3
and sha2 0.11 dependency transition. The app already uses sha2 0.10; both Dalek versions expose
strict verification. This pin enables no optional features and makes no independent audit claim.
Public RFC 8032 vectors, noncanonical S + L refusal and generated-package signature controls
run in plain cargo tests.

Schema-0 provider packages retain digest-only trust. This contract adds no signature claim to
them. Production keys and feeds are release configuration. Generate test keys at runtime in
temporary storage; never track private keys. Test public roots require both a dedicated test
build and isolated temporary trust profile. Normal builds with no production roots refuse native
enablement; they never substitute test roots.

## Access for web packages

A web package's pages read granted files through its page channel (version 0), with three methods
the desktop app's native code answers for a page of a package on this device. A page core lists
gets `unknown_method` for each. Grants come only from the person, in native UI (the app README's
Access section); a page cannot ask for one.

| Method | `params` | `result` |
| --- | --- | --- |
| `resources.status` | `null` | `{"resources": [{"id", "kind", "operations", "granted", "session_only"}]}`: each resource this release declares, and whether it is granted to it now |
| `resources.read` | `{"resource", "path"?, "offset"?}` | `{"data", "size", "offset", "eof"}`: up to 1 MiB of a regular file from `offset` (default 0), as base64 in `data`; `size` is the file's size |
| `resources.list` | `{"resource", "path"?}` | `{"entries": [{"name", "kind", "size"?}], "truncated"}`: a folder's entries sorted by name, at most 1,000; `kind` is `file` (with `size`), `folder`, `link` or `other`. The app reads no further than 1,000 entries: for a bigger folder, `truncated` is true and the entries are the first 1,000 the folder gave |

A `file` resource is read with no `path`. A `subtree` resource takes `path`, slash-separated parts
inside the granted folder; for `resources.list`, empty or absent means the folder itself. A
`file` resource cannot be listed. Unknown parameters are refused. Error codes:

| Code | When |
| --- | --- |
| `bad_request` | Parameters of the wrong shape or unknown, an undeclared resource or operation, a `path` with a `file` resource |
| `not_granted` | No grant for this package, release, source and signer, or the operation is not granted |
| `refused` | An absolute path, `..`, `.` or empty parts, a link leaving the folder, a file or folder on another mount than the granted folder (such as one mounted inside it), or not a regular file |
| `changed` | The chosen link, where it resolves, or the target (its identity or its mount) changed since it was granted; it stays refused until granted again |
| `unavailable` | The file is gone, cannot be reached for now (permissions, an I/O error, too many open files), or the document portal no longer offers it; the grant stays, and a later call may read |
| `revoked` | The grant was revoked while the call was in flight |

The native ingress limits of every page channel call apply to these too. Grants are bound to the
package's source, release digest and approved signer: a new release asks again.

## App program contract v0

The Rust program host implements this contract on Linux; [The Linux host](#the-linux-host) says
what it adds and what is still to come. It adds no provider operation and does not change
provider wire version 0.7. Start a verified absolute executable directly in its immutable
package folder, with a cleared environment and only required platform values. Pass selected
home/config/context explicitly; do not inherit shell credentials.
The host supplies `PLUXY_APP_CONTRACT_VERSIONS` (comma-separated supported major versions,
initially `0`), `PLUXY_EXTENSION_ID` (the manifest ID), and `PLUXY_APP_ARTIFACT_SHA256` (the
verified archive digest) in that environment. The program echoes the ID and digest in hello;
this checks that it is answering the expected host context, not an independent signature check.

Use private anonymous stdin/stdout pipes, not core's socket or a browser/loopback API. Reuse
provider [framing](../../protocol/README.md#framing): a four-byte big-endian unsigned length
followed by that many UTF-8 JSON bytes, at most 64 MiB. Ignore unknown fields. Stdout is frames
only; stderr is bounded, redacted logging, never credentials.

1. The program sends `{"type":"hello","contract":"app","versions":[0],"id":"example/tool","sha256":"<archive digest>"}`.
   The host checks identity, installed artifact digest and a shared contract version before work.
2. The host sends `{"type":"call","id":"c-1","op":"describe","contract_version":0}`.
   The reply is `{"type":"result","id":"c-1","operations":[...],"state":{...}}`, using the
   manifest's operation descriptions and state compatibility. Readiness requires compatibility;
   describe cannot dynamically add raw Tauri commands.
3. Work uses `{"type":"call","id":"c-2","op":"<declared ID>","input":{...}}`.
   Replies may send bounded `{"type":"progress","id":"c-2","value":{...}}`, then exactly
   one `{"type":"result","id":"c-2","output":{...}}` or `error` frame. IDs are unique within
   the host process. Native code validates schema, IDs, size and in-flight limits before dispatch
   or delivery. Mutations are serialized by default; concurrent reads require declared bounds.
4. `{"type":"cancel","id":"c-2"}` cancels one call on the shared pipe. Reuse provider
   [cancellation](../../protocol/README.md#events): stop underlying work and progress. This pipe
   also carries other calls, so cancellation does not close it. A cancelled call ends with error
   code `cancelled`; a result already sent remains its sole terminal outcome. Pipe closure cancels
   all calls.

Error frames reuse the provider [error object and codes](../../protocol/README.md#errors), with
`type: "error"`, the call `id`, and `error`. Unknown operations return `unsupported`. EOF or
process exit before a terminal reply fails pending calls, never succeeds with an empty result.
Never automatically retry a mutation whose outcome is unknown.

### States and lifetime

| State | Meaning |
| --- | --- |
| installed | Verified files are present. |
| enabled | The user enabled the package. |
| authorized | Native authorization exists for this source and digest. |
| idle | After idle stop, no lease, program, watcher, scan or thread remains. |
| starting | Spawn and hello/describe readiness are in progress. |
| ready | Started and described, with no lease: it stops after the idle delay. |
| active | Calls or jobs own leases. |
| draining | Admission stopped; work is finishing or cancelling. |
| crashed | Unexpected exit failed pending calls. |
| quarantined | Repeated crashes prevent automatic starts. |
| incompatible | Host, operation or state compatibility failed. |
| stopped | The process exited and was reaped. |

Installation, enablement and authorization are distinct from process state. Start on demand with
a ten-second startup limit. Last lease release schedules idle stop, default 30 seconds. On stdin
EOF/stop, checkpoint, end every process the tool started and exit; do the same on SIGTERM, which
the tool gets if the app dies without stopping it, when nothing kills its group. After five seconds, kill/reap only the owned tool group. A dedicated
supervisor thread remains until exit/reap, including on Linux where the parent-death signal follows
the creating thread. Explicitly launched agents have separate lifetimes outside tool cleanup.

Crash backoff runs from one second to one minute, with quarantine after five consecutive crashes
and reset after ten stable minutes. Restart only while demand remains. For swap, verify and
describe the candidate without secrets or work; switch new calls only after readiness and native
authorization. Old calls retain their generation until drain ends. Candidate failure keeps the old
version. Serialize mutation commit/recovery across versions. Revoke old page channels and tokens
at switch. Rollback rechecks trust and state compatibility. Disable/removal stops admission,
cancels work, drains/reconciles commits, closes pipes and reaps processes. Preserve private backups
until explicit removal. Stop a full-access process to end its access; grants cannot revoke its OS
access while it runs.

### The Linux host

The program host starts a tool from `Catalog::admitted`'s record at that moment, never from
anything a caller passes. On top of the rules above:

- It opens the program file, checks its SHA-256 against the record and executes that opened file,
  so a replaced file never runs. A program must therefore be a native executable, not a script:
  an interpreter would open the path again.
- The tool's environment holds exactly the three variables above. Only stdin, stdout and stderr
  are open in it; every other descriptor closes as it executes. It leads a process group of its
  own and gets SIGTERM if the app dies without stopping it. Its command line (`argv[0]`) is its
  program's path; its short process name (`comm`) is the number of the descriptor it was run
  from. Its supervisor thread starts it and stays until it has been reaped. It needs
  Linux 5.11 or later (`close_range` with `CLOSE_RANGE_CLOEXEC`).
- Hello must come first and name the record's ID and digest and contract 0. The describe reply's
  `operations` are objects with an `id`; their IDs must be exactly the manifest's, and
  `state.version` the manifest's. A tool that does not match stays **incompatible** until its
  package changes. `describe` is the host's call: a manifest may not declare it.
- A frame from a tool may be at most 1 MiB; a larger one, an unknown frame type or a reply
  before hello breaks the pipe and the tool is stopped; a start it breaks fails saying which.
  A reply to a call that is no longer
  waiting is dropped. Progress frames are read and not delivered yet.
- Inputs are at most 64 KiB of JSON. Inputs and outputs are checked against the operation's
  schemas, which may use only `type`, `properties`, `required`, `additionalProperties`, `items`,
  `enum`, `const`, `minimum`, `maximum`, `minLength`, `maxLength`, `minItems`, `maxItems` and
  the annotations `title`, `description`, `default`, `examples` and `$comment`; a schema using
  any other keyword makes the tool incompatible rather than half checked. An output its schema
  refuses fails the call with `extension_protocol`.
- A call is checked against its operation and input schema before the tool is leased or
  started. Calls to one tool run one at a time, at most eight at once counting those waiting
  their turn, and at most eight more wait for it to start or stop. Calls that waited on a start
  that failed get that start's error rather than starting it again. A cancelled call has five
  seconds for its terminal reply; after that the tool is stopped. A cancel also ends a call
  still waiting for its tool to start or stop.
- Each call holds a lease. Idle stop, a change to the package and app quit close the tool's
  stdin, kill its whole process group after five seconds (once its leader exits, the rest of
  the group at once), and the tool itself if it left that group, and reap it. A child that
  leaves the group is not followed. Closing the window cancels every call, so the tools stop
  when idle; once the app is quitting, no call or swap starts a tool.
- Stderr lines are logged prefixed with the tool's ID, at most 8 KiB each (a longer one is left
  out with a note) and 50 a second (the rest are left out and counted), with control and format
  characters (such as U+202E) escaped and the values the host knows the tool must not show
  removed: every secret value in the vault and every reference the host gave (see
  [Secrets, references and the vault](#secrets-references-and-the-vault)). The last 20 lines,
  kept whether or not the rate let them into the log, and the count
  of those left out, say why it stopped. Error messages and hello or describe values a tool
  sends reach the window and the status escaped the same way.
- A tool that exits on its own, breaks its pipe or leaves a cancel unanswered has **crashed**.
  Nothing restarts it on its own: a call after the backoff (one second, doubling with each crash
  in a row, up to a minute) starts it, and a call during the backoff is refused. Five crashes in a
  row make it **quarantined**: calls are refused until its package changes (disabled and enabled
  again, updated, rolled back). A clean stop (when idle, or a stop the host asked for, such as a
  disable) or a run of ten minutes starts a new count: with the 30-second idle stop, crashes in a
  row are those with no clean stop between them. A crash of a release that is draining is not
  counted.
- A call the tool received and did not answer (it crashed, was stopped, left a cancel unanswered
  or reached the drain limit) has an **unknown outcome**: its error says so, and it is never sent
  again. The host keeps it until the package's next start, which carries the list in describe,
  as `{"type":"call","id":"c-9","op":"describe","contract_version":0,"unknown":[{"id":"c-7","op":"apply","generation":1700000000}]}`
  (`generation` is the release's install time). A tool that writes must reconcile those calls
  with its own records before it answers describe. The describe that carries them waits for the
  package's turn, so no call of the package runs while the tool reconciles; if the turn does not
  come within the startup limit, that describe carries none. Whichever release starts next gets
  them, except a swap's candidate, which gets none; once that start serves, the host forgets them.
  Delivery may repeat (a start that fails after its describe is told again at the next one), so
  reconciliation must be idempotent. A call refused before it was sent, an `error` frame and a
  cancel the tool answered all have known outcomes.
- **Swaps.** Updating or rolling back an enabled native tool stages the release and asks for
  its own native confirmation. Once confirmed, the host starts the release's tool and runs hello
  and describe with no call; it fails as any start does. It does not start if the package was
  disabled, removed or changed while the confirmation was open, and a second update of a package
  is refused while one is under way. Then the catalog commits it as active,
  enabled and admitted, only if the release it replaces is still the active, admitted one, and
  new calls and leases go to the new tool. The old one **drains**: calls holding its leases,
  including those waiting their turn, finish on it; then its stdin closes as for any stop. After
  ten minutes (the drain limit) it is stopped and its unanswered calls have unknown outcomes.
  Calls of one package run one at a time, whichever release they reach: the new release's first
  call waits for the call running on the old one, and calls still waiting their turn on either
  release go in no set order. The package's pages close at the switch. A cancelled confirmation
  or a candidate that fails installs nothing; the old release stays active and serving.

Still to come: jobs, and Windows and macOS.

## Calls between packages

The Linux host routes every call of a native tool's operation, whoever makes it: the main window
(`program_call`), a page (its `native.call` method) or another native tool (a `call` frame on its
pipe). Each call is checked against the catalog as it is at that moment, and the host, never the
call, says who the caller is: the window, the page's webview and its package release, or the
calling tool's running release. Programs never talk to each other directly.

### What a package declares

A native tool lists the operations it offers other packages in `exports`, under an export
contract number it raises when it changes them incompatibly. A package lists what it calls in
`imports`: the target package, the export contracts it works with and the exact operations.
Package versions are not compared; only the contract number is.

Each operation has an effect: `read` (runs with no question asked), `write` (asks a native
confirmation each call) or `launch` (asks a native confirmation of the exact launch). A launch
operation's input schema must require `executable`, `argv` and `cwd`.

### Enabling a native tool with imports

A native tool is enabled only when each required import's target is an enabled, admitted native
tool that exports those operations under a contract the import accepts, and when its imports,
required or optional, do not lead back to it through other enabled tools (no cycles). Its native
confirmation lists its imports. The same checks apply to an update or rollback of an enabled tool
(a swap), when it is staged and again when it commits. A missing optional target is not an error
at enable: a call to it fails as `extension_unavailable`. The host never installs or picks a replacement for a missing
target. Enabling the tool is the person's consent to its imports.

### Web packages: native consent

Only a curated web package may call native tools; an Unverified one is refused, always. Its
imports are refused until the person allows them on the Device extensions page, which calls
`device_imports {id, allowed: true}` (main window only) and opens a native confirmation listing
the caller's source, digest and signer and each target operation with its effect. The consent
is bound to the caller's release and to each target's release, export contract and operations.
Disabling, updating, rolling back or removing the caller clears it, and so does disabling,
updating, rolling back, swapping or removing any target it names: enabling that target again is a
new decision, so those calls are refused until the person allows them again. `allowed: false`
revokes it and cancels the package's page calls still running; their answers are never
delivered. A page call is registered before it is granted, so a revocation at any point after
the grant reaches it.

### Frames

A call from the host to a tool carries the call's chain:

```json
{"type":"call","id":"c-2","op":"summary","input":{"items":["a"]},
 "chain":[{"kind":"main"},{"kind":"package","id":"pluxy/relay","sha256":"<digest>","generation":1700000000}]}
```

The chain starts with the original requester, `{"kind":"main"}` or
`{"kind":"page","id":<package>,"sha256":<release digest>,"page":<page ID>}`, followed by each tool
that called on, at its release digest and generation (install time). A tool calls an import for
the host call it is serving, named in `for`:

```json
{"type":"call","id":"o-1","for":"c-2","package":"pluxy/target","op":"summary","input":{"items":["a"]}}
```

The host answers on the tool's stdin with `{"type":"result","id":"o-1","output":{...}}` or an
`error` frame; the tool may send `{"type":"cancel","id":"o-1"}`. Its calls are cancelled when the
call they serve is cancelled or ends. A frame naming `chain`, `caller`, `requester`, `from`,
`origin` or `context` is refused with `permission`, as is a `for` the tool is not serving or an
ID in use; a tool has at most eight calls of its own at once.

A page calls with the page channel method `native.call`, params `{"package", "op", "input"}`. The
app answers it in the page's own webview and never forwards it to the main window. Error codes
map to the page channel's: `permission` and `cancelled` to `forbidden`, `invalid_request` to
`bad_request`, the rest to `unavailable`. A native tool's own page may call only its own
operations; a web package's page calls only what it imports, with consent. A page call is
cancelled when its page closes or its consent is revoked, and its answer is delivered only if the
call is still allowed then.

### How a call is decided

A call goes ahead only if all of these hold, checked again for each call:

- The window may call any declared operation of an admitted tool. A tool may call only an
  operation it imports and its target exports, under an accepted contract, and only while it
  runs its current admitted release. A web package's page needs its consent, as above.
- The target is enabled and admitted. The call is pinned to the target release it was allowed
  for: if another release would serve it, it fails with `extension_unavailable` and starts
  nothing.
- The target is not already in the chain (no call back into the chain), and the chain is at most
  five hops long, the requester and four tools.
- The input is at most 64 KiB and matches the operation's input schema, and its secret fields
  are filled as [Secrets, references and the vault](#secrets-references-and-the-vault) says.
- A `write` call has a fresh native confirmation naming the whole chain and the exact input. A
  `launch` call has a fresh native confirmation of the exact executable, `argv` and `cwd`. Only
  the window's own direct call, made from a native gesture (a native button press), may use a
  remembered approval, and only of exactly that launch. A call with a tool hop in its chain is
  prepared as an import, whoever started it, and never uses or saves a remembered approval; a
  chain a page started always asks too. Neither dialog offers "remember". The host
  confirms the launch; the operation receives it as its input, as with any call (the host's own
  launcher comes later). The confirmation's rate and pending limits are checked before an
  executable is hashed, and it is hashed without holding the confirmations' lock. A call cancelled
  while its confirmation is open (its ticket, the window closing, its page closing or its consent
  revoked) withdraws it, and the dialog closes. Once confirmed, the call is granted again and runs
  only if the target release and operation are the ones confirmed.

A routed call is then a call like any other: it is registered so a cancel and the window's
closing reach it, and it goes through the same limits, per-package turn, backoff, quarantine and
unknown-outcome rules as a call from the window.

## Secrets, references and the vault

The app keeps a host-private **vault** in its data folder (`vault`, owner-only: 0700, files
0600): the secrets the person entered, and each native tool's backups and journals. It is never a
folder a package names or reads through the host, and no window or page API gives its paths, a
secret's value, a backup's bytes or a journal's intent. A native tool has the user's full access
and could read it directly: this separates APIs and storage, it is not an operating-system
sandbox, and it does not protect a secret from another program of the same user.

### Secrets and references

A secret enters only through native key entry (`vault_secret_add`, the app README's Commands):
GTK's hidden entry reads it, and the window gets back its opaque ID (`s-` and 32 hexadecimal
digits) and label, never the value. An operation's `secrets` fields carry secrets:

- From the window, a secret field holds a vault secret's ID.
- From a native tool, it holds a reference the host gave that tool in the call it is serving,
  passed on to an operation its import lists in `secrets`.
- From a page, never: a page cannot pass secrets.
- A secret's value or a reference anywhere else in an input is refused: in any other field's
  value or name, at any depth, or as the whole input. The refusal is the one any input that does
  not match the operation's schema gets, so it does not say whether a string was a secret. A
  field filled any other way is refused too.

A call that delivers a secret's value (a `value` field), or one the window gives a secret to,
asks a native confirmation naming each secret, its label and the tool that gets it, even for a
`read` operation. A launch's secrets are references only (a manifest with a `value` field on a
`launch` operation is invalid), so a remembered launch approval never stands for a value's
delivery; it is remembered only with the same secrets' IDs, which its dialog and its Remember
checkbox name. Passing a reference on to a
`reference` field asks nothing more. The tool then finds, in each secret field, a new reference
(`r-` and 32 hexadecimal digits) of its own, and a `value` field's secret in the call frame's
`secrets`, by that reference:

```json
{"type":"call","id":"c-4","op":"apply","input":{"key":"r-<hex>","file":"/home/me/.config/x.json"},
 "chain":[{"kind":"main"}],"secrets":{"r-<hex>":"<the secret>"}}
```

A reference is bound to its secret, to the release of the tool it was given to (ID, digest and
generation) and to the call it was given in, and so to that call's chain. It is refused when
another tool presents it, when another release of the tool does (after an update or rollback),
in any other call of the tool, after its call has ended, and once its secret is removed. A tool
that only receives references (an execution program) never receives the secret. References live
in the app's memory only.

The host never shows a secret's value or a reference: they are replaced by `[redacted]` in a
tool's logged stderr lines and its status, in the errors leaving the host, and in every call's
output, whether it goes to the window, a page or a calling tool. Overlapping values are replaced
as one, and a removed secret's value is still replaced until the app restarts. Only exact values
are replaced: a tool that encodes a secret otherwise can still show it.

### Vault frames

While a tool serves a host call, it may send `vault` frames on its stdout for it (`for`), answered
on its stdin like its own calls, with `result` or `error`. While it answers a describe on its
package's turn (one that names journals or unknown outcomes), it may send them for that describe
too, only to read backups (`backup.get`, `backup.list`) and to end the journals that describe named
(`journal.commit`, `journal.end`):

```json
{"type":"vault","id":"v-1","for":"c-4","op":"journal.begin","intent":{"file":"/home/me/.config/x.json","sha256":"<hex>"}}
```

| `op` | Fields | `output` |
| --- | --- | --- |
| `backup.put` | `label` (at most 64 characters), `data` (base64, at most 512 KiB) | `{"backup": "b-<hex>"}` |
| `backup.get` | `backup` | `{"backup", "label", "size", "created", "data"}` |
| `backup.list` | none | `{"backups": [{"backup", "label", "size", "created"}]}` |
| `backup.delete` | `backup` | `{}` |
| `journal.begin` | `intent` (a JSON object of at most 16 KiB) | `{"journal": "j-<hex>"}` |
| `journal.commit` | `journal` | `{"journal", "outcome": "committed"}` |
| `journal.end` | `journal`, `outcome` (`committed` or `rolled_back`) | `{"journal", "outcome"}` |

Backups and journals belong to the package, whatever its release, and stay when it is removed; a
package keeps at most 256 backups. In a call, a tool commits or ends only a journal its own release
began; in describe, any journal that describe named. The vault's folders are opened without
following links, and files in a package's folder that its records do not name (a write cut
short, a removal that failed) are removed the next time they are read. Errors, which never name
another journal: `invalid_request` for bad fields or a journal ended already with another outcome,
`not_found` for another package's or an unknown ID (or a journal the tool may not end),
`unavailable` when the vault cannot be read or written, a second journal is begun while one is
open, or the package keeps 256 backups, `permission` for a `for` the tool is not serving or an
`op` describe may not send, and `unsupported` for an unknown `op`.

### Journals and recovery

A tool that writes begins a journal before the write, naming what it means to write in `intent`
(such as each file and the SHA-256 of its new bytes), keeps the old bytes as a backup, and commits
the journal once the write is done. A journal still open when the tool or the app stopped is kept
on disk, and the package's next start names it in describe, with the calls of unknown outcome and
on the package's turn:

```json
{"type":"call","id":"c-9","op":"describe","contract_version":0,"unknown":[...],
 "journals":[{"id":"j-<hex>","op":"apply","generation":1700000000,"began":1700000001000,"intent":{...}}]}
```

The tool must reconcile each before it answers: look at what is on disk and end the journal as
`committed` (the write took effect) or `rolled_back` (it did not, or the tool put the old bytes
back), never by writing again. A start whose describe leaves one open fails, and so does one whose
journals cannot be read (the status says why, in `journals_error`). Ending a journal
again with the same outcome is answered the same, with `"again": true`, because delivery may
repeat; another outcome is refused. While one is open, no other journal begins. A swap's
candidate is told of none, as of unknown outcomes. The window's `program_status` lists the
package's open and recently ended journals and whether each was reconciled after a crash.

## Shared manifest fixtures

[`protocol/testdata/app-manifests`](../../protocol/testdata/app-manifests) holds JSON cases with
expected core outcomes in `cases.json`. Go exercises them through the installer; Rust can reuse
them later. These are manifest fixtures, not signed artifacts or proof of an implemented app host.

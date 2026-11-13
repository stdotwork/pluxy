# App package contract

This reference defines the app contribution to a pluxy package and app program contract v0.
The signature verifier is shared Rust code in `src-tauri/src/artifact.rs`. The desktop app
installs page-only web packages and curated signed native tools on this device
([extensions on this device](../README.md#extensions-on-this-device)), and runs an admitted
tool's program on demand through the program host (`src-tauri/src/program.rs`, Linux).
Resources are later implementation work.
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
| `operations` | Native only: declared operations, each with an `id`, plain `description`, and `input_schema` and `output_schema` JSON objects. At least one is required. |
| `state` | Native only: `version` written by this release and nonempty `readable_versions` list. Check state compatibility before swap or rollback. |

Resource and operation IDs use 1 to 32 lower-case letters, digits and hyphens, starting with a
letter or digit, and are unique within their respective lists. State and host contract versions
are nonnegative integers. Paths must stay inside the
package. Web packages cannot declare programs, native operations or state; native packages cannot
request folder grants through `resources`. Resources describe exact files or bounded subtrees
selected later by the user, never automatic home-directory access. Native import routing, jobs
and secret references belong to their later host contracts; these fields do not introduce them.

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
refused. Any other listed signature, from a registry no root names or for a publisher no root
names, is ignored for now and the package is Unverified; such a package is never a native tool
and never has a reserved ID. Detached signature files contain exactly
64 raw Ed25519 bytes; JSON/index transport encodes them as 128 lower-case hexadecimal digits.
Public keys are 32 bytes, encoded as 64 lower-case hexadecimal digits. Verification uses pinned
`ed25519-dalek` 2.2.0 with `VerifyingKey::verify_strict` and default features disabled. This is
separate from Tauri updater signatures. Trust binds the publisher, key identity and exact source
trust domain; a changed source needs fresh trust. The feature-free `verify_descriptor` accepts
an approved key from native settings for web resources or curated native admission; both use
the same verifier. `PublisherKeys` supplies publisher/source/key lookup separately.
`VerifiedArchive<R>` retains the rewound checked reader. Later folder-grant, native-admission and core-update code uses `into_archive()`
instead of reopening a path, prevent concurrent writes, and check manifest identity after extraction.

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
  when idle; once the app is quitting, no call starts a tool.
- Stderr lines are logged prefixed with the tool's ID, at most 8 KiB each (a longer one is left
  out with a note) and 50 a second (the rest are left out and counted), with control and format
  characters (such as U+202E) escaped and the values the host knows the tool must not show
  removed. The last 20 lines, kept whether or not the rate let them into the log, and the count
  of those left out, say why it stopped. Error messages and hello or describe values a tool
  sends reach the window and the status escaped the same way.

Still to come: crash backoff and quarantine (a crashed tool starts again at its next call), swaps
and draining old generations, calls routed from pages, jobs, secrets, and Windows and macOS.

## Shared manifest fixtures

[`protocol/testdata/app-manifests`](../../protocol/testdata/app-manifests) holds JSON cases with
expected core outcomes in `cases.json`. Go exercises them through the installer; Rust can reuse
them later. These are manifest fixtures, not signed artifacts or proof of an implemented app host.

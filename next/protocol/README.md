# pluxy extension protocol, version 0.11

This is the contract between the pluxy core and an extension program. Version 0 covers one kind
of extension, a **provider**: a program that takes a model request from core, calls an upstream
model service, and streams the reply back; a provider may also sign in to accounts and renew their
credentials (see [Login and refresh](#login-and-refresh)). Version 0.1 adds
[sessions](#sessions): a run of requests on one account whose upstream keeps state between them,
such as a Responses WebSocket. Version 0.2 adds [token counting](#count_tokens) and the
thinking suffix of a model name (`options.thinking_suffix`). Version 0.3 adds
[model lists](#models): the models an account serves, for the model lists clients ask core for.
Version 0.4 adds [account checks](#check): whether an account works, asked without generating.
Version 0.5 adds [quota](#quota), what an account's plan allows and how much of it is used, and
the identity of a login's grant, so core can check that a new login to an account signs in to
the same upstream account. Version 0.6 adds an error's `quota_window_minutes`: how long the
usage window that ran out is. Version 0.7 adds [endpoint calls](#endpoint): client requests to a
provider's endpoints beyond generation, such as Responses compaction, whose reply the extension
writes. Version 0.8 adds the request's [`caller`](#the-request): a value that stands for the
client key a request came with, so an extension can keep what it learns from replies per client.
Version 0.9 adds optional non-secret account settings to a login grant. Version 0.10 adds the
optional quota `plan_ends_at`, the end of the subscription in Unix seconds. Version 0.11 adds
the account's [`proxy_url`](#accounts): the proxy the call's upstream traffic goes through.
It is deliberately small; later versions add capabilities and more operations without changing
what is described here. An extension written for version 0 works unchanged with a core that
speaks 0.11 (see [Versioning](#versioning)).

If you want to write an extension, read [the tutorial](TUTORIAL.md) first and come back here for
the details. A Go SDK (`github.com/stdotwork/pluxy/next/sdk`) implements the extension side of
this document, and the conformance kit (`github.com/stdotwork/pluxy/next/conformance`) checks an
extension against it.

## Overview

```
client ──HTTP──▶ core ──one local socket connection per call──▶ extension ──▶ upstream
                      ◀── stream of typed events, ending in finish or error ──
```

- Core starts the extension program, the extension says which protocol version it speaks and
  where it listens, and core connects to it.
- Every call opens a new connection. Core sends one call frame; the extension replies with a
  stream of frames and closes the connection. There are no per-token round trips.
- Closing the connection is how core cancels a call.
- A call has exactly one terminal outcome: a `finish` event or an `error` event. A connection
  that ends without one is an error, never a success.
- A [session](#sessions) (0.1) is the one call that carries more than one request: its connection
  stays open while core sends one turn after another, each answered like a `generate` call.

## Package manifest

An extension ships as a folder with a manifest named `pluxy-extension.json`:

```json
{
  "manifest_version": 0,
  "id": "pluxy/openai-compatible",
  "version": "0.1.0",
  "protocol": [0],
  "programs": {
    "linux/amd64": "bin/openai-compatible-linux-amd64",
    "darwin/arm64": "bin/openai-compatible-darwin-arm64"
  }
}
```

| Field | Meaning |
| --- | --- |
| `manifest_version` | Version of this manifest format. Version 0 is described here. |
| `id` | Publisher-qualified ID, `publisher/name`: lower-case letters, digits, `-` and `.`. |
| `version` | The package version, chosen by the publisher: 1 to 64 letters, digits, `.`, `+`, `_` and `-`, starting with a letter or digit. Core compares versions only for equality. |
| `protocol` | Protocol versions the program speaks. Core picks one it also speaks. |
| `programs` | Program to run per platform, keyed by Go's `GOOS/GOARCH`. Paths are relative to the manifest's folder and may not leave it. |
| `ui` | Optional: pages the panel shows for the extension, with their own `version`. See [extension pages](../ui/extension-pages/README.md#ui). |
| `config` | Optional: the schema of the extension's settings, with its own `version`; the panel draws a form from it. See [extension pages](../ui/extension-pages/README.md#config). |

The package version, manifest version, protocol version and the versions of `ui` and `config`
are separate on purpose: each can change without the others. Later versions add more optional
parts (themes) with their own version fields.

App contributions use manifest version 1. See the [app package contract](../app/app-host/README.md)
and its [tutorial](../app/app-host/TUTORIAL.md). Core refuses app-only packages; mixed packages
use only their provider part in core. Provider operations and wire version 0.7 are unchanged.

### Release archives

A release of a package is published as one archive per platform: a gzip-compressed tar file
holding the package folder, with `pluxy-extension.json` at the top (no enclosing folder) and the
program the manifest names for that platform. An archive may hold only regular files and
folders: no links, devices or names that leave the folder. Core refuses an archive larger
than 512 MiB unpacked or with more than 10,000 entries. A file is installed executable when any
of its execute bits is set in the archive.

```bash
tar -czf pluxy-codex-0.1.0-linux-amd64.tar.gz -C package-folder .
sha256sum pluxy-codex-0.1.0-linux-amd64.tar.gz
```

### Registry index

Core installs packages from registries. A registry is one JSON file, the index, read from a
local path or an `https://` URL. It lists packages by ID and, for each, the releases it offers,
newest first, each pinned to its archives' SHA-256 digests and sizes:

```json
{
  "registry_version": 0,
  "packages": {
    "pluxy/codex": {
      "releases": [
        {
          "version": "0.1.0",
          "protocol": [0],
          "artifacts": {
            "linux/amd64": {
              "url": "pluxy-codex-0.1.0-linux-amd64.tar.gz",
              "sha256": "9f2c…",
              "size": 8123456
            }
          }
        }
      ]
    }
  }
}
```

| Field | Meaning |
| --- | --- |
| `registry_version` | Version of this index format. Version 0 is described here. |
| `packages` | Packages by ID (the manifest's `id`). |
| `releases` | Releases offered, newest first. A release's `version` appears once per package. |
| `protocol` | Protocol versions the release's program speaks, so core can pick a release it can run before downloading it. |
| `artifacts` | The release's archives by platform (`GOOS/GOARCH`). |
| `url` | Where the archive is: relative to the index (a path inside the index's folder for a local index, a URL reference for an HTTPS one) or an absolute `https://` URL. Never plain `http://`. |
| `sha256` | The archive's SHA-256 digest, in lower-case hex. |
| `size` | The archive's exact size in bytes. |

Core installs a release only when its archive has exactly `size` bytes and the digest it lists,
and when the manifest inside has the release's ID and version, a manifest version core reads, a
protocol version core speaks and a program for core's platform. Core reads at most 8 MiB of an
index. Unknown fields are ignored, so later versions of the format can add optional ones.

A registry earns trust by what it lists, not by where it is: the curated registry lists releases
its maintainers reviewed, each pinned by digest, and a local or private registry is used only when
core's config names it. A digest pins the archive the registry listed; it does not prove who built
it. Version 0 has no signatures, so nothing stops a registry, or whoever controls it, from
listing an older release or a changed digest; core refuses a changed digest only for a version it
has installed.

## Starting and the handshake

Core starts the program with a cleared environment. It sets only `PATH` (and `SYSTEMROOT` on
Windows) and these variables:

| Variable | Value |
| --- | --- |
| `PLUXY_PROTOCOL_VERSIONS` | Protocol versions core speaks, comma-separated, for example `0`. |
| `PLUXY_SOCKET` | Path of the Unix socket the extension must listen on. Its folder is private to this extension process. |
| `PLUXY_EXTENSION_ID` | The manifest `id`, for logs. |

The extension then:

1. Picks the highest version that is both in `PLUXY_PROTOCOL_VERSIONS` and one it speaks. If there
   is none, it writes the reason to stderr and exits with a non-zero status, without writing a
   handshake line.
2. Listens on `PLUXY_SOCKET`.
3. Writes one handshake line to stdout and keeps running:

   ```
   pluxy|0|unix|/path/from/PLUXY_SOCKET
   ```

   The fields are a fixed marker, the chosen protocol version, the network type and the address.

One more variable is passed on when core's own environment holds it: `PLUXY_TEST_LOGIN_HOSTS`.
Only tests set it. With it set to `1`, an extension may take the address of its provider's login
or token endpoint from an account's settings, so a test can point it at a fake provider; without
it the extension must use the provider's own hosts, because account settings are data a stored
account carries and must not decide where a login's codes or a refresh token are sent. The Go
SDK's `sdk.TestLoginHost` applies the rule.

If `PLUXY_PROTOCOL_VERSIONS` is not set, the program was not started by pluxy: it should say so on
stderr and exit with a non-zero status.

Core waits for the handshake line for a limited time (10 seconds by default); this startup check
is the only timeout in the protocol. After the line, core sends a `describe` call and checks that
the reply's `id` matches the manifest.

Anything the extension writes to stderr goes to core's log, prefixed with the extension ID. Never
write credentials there. Anything written to stdout after the handshake line is also logged.

## Shutdown

Core closes the extension's stdin to ask it to stop. The extension stops accepting calls, ends the
calls it is running and exits. Core kills the process if it is still running 5 seconds later.
Because stdin also closes when core itself dies, an extension that follows this rule never
outlives its core.

Core starts and stops extension programs as it needs them, so a program must not count on a
long life. It stops a program that has had no calls for a while and starts it again for the next
call. When a new version of an extension is installed, core starts the new program next to the
old one, sends new calls to the new one, and asks the old one to stop once its calls have ended
(or at a deadline). A program that exits on its own is restarted after a backoff, and one that
keeps crashing is no longer started. None of this changes the protocol: each program sees one
start, its calls and one stop.

## Framing

Every frame is a 4-byte big-endian unsigned length followed by that many bytes of UTF-8 JSON. A
frame may not exceed 64 MiB; a reader that sees a larger length treats the connection as broken.
Unknown JSON fields must be ignored, so later versions can add fields.

## Calls

Core opens a connection and writes one call frame:

```json
{"type": "call", "id": "c-17", "op": "generate", "account": {...}, "request": {...}}
```

| Field | Meaning |
| --- | --- |
| `id` | Call ID for logs and traces. Unique per core process. |
| `op` | The operation. Version 0 has `describe`, `generate`, `login` and `refresh`; version 0.1 adds `session`; version 0.2 adds `count_tokens`; version 0.3 adds `models`; version 0.4 adds `check`; version 0.5 adds `quota`; version 0.7 adds `endpoint`. |
| `account` | For `generate`, `count_tokens`, `endpoint`, `refresh`, `session`, `models`, `check` and `quota`: the account to use (below). For `login`: the account being signed in to, with its `id`, `settings` and `proxy_url` but no `credential`. |
| `request` | For `generate`, `count_tokens` and `endpoint`: the model request (below). |
| `login` | For `login`: what to sign in with (see [Login and refresh](#login-and-refresh)). |
| `session` | For `session`: what kind of session to open (see [Sessions](#sessions)). |

An extension that gets an `op` it does not know replies with an `error` event whose code is
`unsupported`.

### `describe`

The reply is one frame, then the extension closes the connection:

```json
{"type": "describe.result", "info": {"id": "pluxy/openai-compatible", "version": "0.1.0", "protocol": 0, "protocol_minor": 1, "operations": ["generate"]}}
```

`protocol` is the version from the handshake; `protocol_minor` is the revision of that version
the extension implements (absent means 0; see [Versioning](#versioning)). `operations` lists the
operations the extension serves: always `generate`, plus `login` and `refresh` when it signs in to
accounts, `session` when it holds [sessions](#sessions), `count_tokens` when it
[counts tokens](#count_tokens), `models` when it lists its accounts' [models](#models), and
`check` when it [checks](#check) its accounts, `quota` when it reads their [quota](#quota), and
`endpoint` when it serves [endpoint calls](#endpoint). An
extension that lists `login` also
lists its `login_methods` (see [Login and refresh](#login-and-refresh)).

### `generate`

The reply is a stream of event frames (next section), ending with exactly one `finish` or `error`
event, after which the extension closes the connection.

While it streams, the extension must keep reading from the connection. When the read ends (core
closed the connection), the call is cancelled: stop the upstream request and stop sending. Core
may cancel at any time, including before the first event.

Write each event as soon as it is ready and do not queue events in memory without limit. When
core cannot write to its client, it stops reading the connection; your writes then block, and
that blocking should reach your upstream read. This is how a slow client slows the upstream
instead of filling memory.

Extensions never retry on their own. Report what happened with a structured error and let core
decide.

### `count_tokens`

Version 0.2. Counts the input tokens a request would use on the account, as Anthropic's
`/v1/messages/count_tokens` and Gemini's `countTokens` do, without generating anything. The call
carries `account` and `request` as `generate` does (`stream` is false; the request's items, tools
and options are what to count), and the reply is a stream of events as for `generate`, without
items: `response.start`, then `finish` whose `usage` holds `input_tokens`, the count, and `raw`,
the upstream's count reply as it came, in `response.format`. Core answers a client in that format
with `raw` as it is, and any other client with the count in its own shape.

```json
{"type": "response.start", "response": {"id": "", "model": "claude-sonnet-4-5", "format": "anthropic.messages"}}
{"type": "finish", "finish": {"reason": "stop"}, "usage": {"input_tokens": 42, "raw": {"input_tokens": 42}}}
```

Count with the upstream's own count endpoint. An account that cannot count (its upstream has no
such endpoint, or not for this request's format) answers with code `unsupported`, scope
`credential` and `retryable` true, so core tries another account. Core sends `count_tokens` only
to an extension whose describe reports `protocol_minor` 2 or more and lists it in `operations`;
for any other, core treats the account as one that cannot count. Core routes a count like a
request: the same accounts, blocks and retries. An extension does not estimate: when the count
gets no answer and an account tried answered `unsupported`, core answers the client with its own
estimate (unless the config turns `token_count_estimate` off, then with the last error). When no
account tried answered `unsupported`, any other error, such as an upstream's 400 or 429, reaches
the client as it is.

### `models`

Version 0.3. Core asks which models an account serves, for the model lists clients ask core for
(`GET /v1/models` and Gemini's `GET /v1beta/models`). Core sends it only to an extension that
reports `protocol_minor` 3 or more and lists `models` in `operations`; for any other extension's
accounts, core lists only the models the account's config names.

```json
{"type": "call", "id": "c-21", "op": "models", "account": {"id": "work", "settings": {...}, "credential": {...}}}
```

The reply is one frame, then the extension closes the connection: a `models.result`, or an `error`
event as for `generate`.

```json
{"type": "models.result", "models": {"owned_by": "openai", "models": [
  {"id": "gpt-6.1-sol", "display_name": "GPT-6.1 Sol", "created": 1794268800, "context_length": 400000, "max_completion_tokens": 128000}
]}}
```

| Field | Meaning |
| --- | --- |
| `models` | The models the account serves, by the names its upstream knows them by. Empty when the extension has no list for the account. At most 10000. |
| `owned_by` | Who provides the account's models, shown as `owned_by` for models without one of their own, and for the models the account's config names. Default: the account's ID. |
| model `id` | The model's name upstream, as requests name it. Required, unique in the list; core keeps the first of any repeats. |
| model `name` | Its resource name in Gemini's list, such as `models/gemini-2.5-pro`. Default `id`. |
| model `display_name`, `description`, `version` | For people. |
| model `created` | When the model was released, in Unix seconds. |
| model `owned_by` | Who provides this model, when it differs from the list's `owned_by`. |
| model `context_length`, `max_completion_tokens` | Its context window and longest reply, in tokens. |
| model `input_token_limit`, `output_token_limit` | Gemini's names for the same limits, where the provider gives them apart. Default: the two above. |
| model `generation_methods`, `input_modalities`, `output_modalities` | Gemini's `supportedGenerationMethods`, `supportedInputModalities` and `supportedOutputModalities`. |

Every field but `id` is optional and shown only where a client's list has a place for it. Core
renames, leaves out and prefixes the listed models by the account's naming rules (its `models`,
`excluded_models`, `model_alias` and `prefix`), and asks again when the account's settings change or
it signs in again. List the models the account can use: from the upstream's own list when it has
one, or from a catalog the extension carries. Leave out models the account's requests cannot
reach through this extension.

Core also routes by the list: an account whose config names no `models` gets only requests for
the models its list names (after its naming rules), and a request for any other model is
refused without a `generate` call. An empty list, or an `error` reply, leaves the account
serving any model, so reply with an empty list when the account's models are not known.

### `check`

Version 0.4. Core asks whether an account works, without generating anything: when it starts,
when a reload adds an account or changes one (its settings, its credential or its extension), and
when a login adds an account or one comes back after a new login. Core shows the result in the
management API's account listing and its log. It never waits for a check before serving
requests, and gives a check 30 seconds. Core sends `check` only to an extension that reports
`protocol_minor` 4 or more and lists `check` in `operations`; any other extension's accounts show
as not checked.

```json
{"type": "call", "id": "c-22", "op": "check", "account": {"id": "work", "settings": {...}, "credential": {...}}}
```

The reply is one frame, then the extension closes the connection: a `check.result` when the
account works, or an `error` event as for `generate` when it does not.

```json
{"type": "check.result", "check": {"detail": "the upstream lists 42 models"}}
```

| Field | Meaning |
| --- | --- |
| `detail` | What was checked, for people. Optional. Not secret. |

Make at most one call upstream, and only one that costs the account nothing: its model list, or
the provider's own call that tells whether a token is valid. Never generate, and do not renew the
credential: core renews it before the account's next request. A refused credential is an `auth`
error, an upstream that cannot be reached `unavailable`, an upstream failure `upstream`. When the
account cannot be checked without a generation (the upstream has no such call), reply with code
`unsupported`: core shows the account as not checked, not as failing.

### `quota`

Version 0.5. Core asks what an account's plan allows and how much of it is used, for the
management API's quota view (`GET /v0/management/accounts/{id}/quota`). Core sends it only to an
extension that reports `protocol_minor` 5 or more and lists `quota` in `operations`, and only when
someone asks: core does not poll.

```json
{"type": "call", "id": "c-22", "op": "quota", "account": {"id": "work", "settings": {...}, "credential": {...}}}
```

The reply is one frame, then the extension closes the connection: a `quota.result`, or an `error`
event as for `generate`.

```json
{"type": "quota.result", "quota": {"plan": "plus", "limits": [
  {"id": "five_hour", "label": "5-hour limit", "used_percent": 42, "resets_at": 1794268800, "window_seconds": 18000},
  {"id": "credits", "label": "Credit balance", "remaining": 120, "unit": "credits"}
], "raw": {"usage": {...}}}}
```

| Field | Meaning |
| --- | --- |
| `plan` | The account's plan, as the provider names it, for people. |
| `plan_ends_at` | Optional, since 0.10: when the subscription ends, in Unix seconds. Omitted when unknown. |
| `limits` | The plan's limits: usage windows, allowances and balances. At most 1000. |
| limit `id` | Names the limit within the extension, the same on every call (`five_hour`, `weekly`). Required, unique in the list. |
| limit `label` | For people, in English (`5-hour limit`, `Weekly limit`). |
| limit `used_percent` | The share used, from 0 to 100. |
| limit `used`, `limit`, `remaining` | Amounts, in `unit`. |
| limit `unit` | What the amounts count: `requests`, `tokens`, `credits`, `usd`, or the provider's own word. |
| limit `unlimited` | True when the limit does not run out. |
| limit `resets_at` | When it resets, in Unix seconds. |
| limit `window_seconds` | The length of its usage window. |
| limit `models` | The models it applies to, when not all of the account's. |
| `raw` | The provider's replies the quota was read from, as one JSON object, for what `limits` has no place for. |

Every field of a limit but `id` is optional; report what the provider gives. Read the quota from the
provider's own usage endpoint for the account's subscription, with the same client identity
(user agent and the like) the extension uses for the account's requests, and never with a request
that spends from the plan (a generation). An account that has no plan to report (an API key, or
a provider with no usage endpoint) answers with code `unsupported` and scope `credential`. Core
removes the account's credential from every string and from `raw`, as from every other reply.

### `endpoint`

Version 0.7. A client request to one of the provider's endpoints beyond generation, which core
passes to an account's extension. Except for core-built bodies below, core reads nothing of it
but the model, and the extension writes the reply's body. Core sends it only to an extension whose describe reports
`protocol_minor` 7 or more and lists `endpoint` in `operations`; for any other, core treats the
account as one that does not serve the endpoint.

The call carries `account` and `request` as `generate` does. The request has no items: `model`
is the body's `model` (renamed for the account and without its thinking suffix, which is in
`options.thinking_suffix`, as for `generate`; empty for an endpoint that takes no model, when the
body names none), and `native` holds the body (the client's, or core-built as below) and headers,
with `native.format` naming the endpoint:

| `native.format` | Client route | Body |
| --- | --- | --- |
| `openai.responses.compact` | `POST /v1/responses/compact` (and `/backend-api/codex/responses/compact`) | A Responses request; the reply is one JSON object. Core refuses `"stream": true` itself. |
| `codex.models` | `GET /v1/models?client_version=<version>` | Core builds `{"client_version":"<version>","models":[{"name":"team/coding","upstream":"gpt-6.1-sol","display_name":"Coding","non_codex":true}]}`. Presence of the query key selects this endpoint, even with an empty value. Core lists accounts of `pluxy/codex` under their normal aliases, exclusions and prefixes, keeps the first mapping of a repeated public name, and sorts by public name. `display_name` is omitted when empty. `non_codex` is true when any other extension's account can also serve that public name, including an account with an unknown model list; the extension then clears the Codex-only options as described below. The GET body is ignored. One direct call goes to the first enabled, usable `pluxy/codex` account in account order, carrying only its `account.id` (no settings or credential), with `request.model` empty. No routing reservation, cooldown, concurrency limit, credential renewal, result, rotation or usage event is involved; there are no retries. Without such an account core returns 200 `{"models":[]}` itself. The extension returns one JSON object in the Codex catalog shape, omitting unknown templates and replacing each slug with `name`. No upstream request or remote refresh is made. |
| `codex.alpha_search` | `POST /v1/alpha/search` (and `/backend-api/codex/alpha/search`) | Codex's search request, in Codex's own shape; the reply is one JSON object. Its `model` is optional: a request without one is sent to any account, with `request.model` empty. |
| `openai.images.generations` | `POST /v1/images/generations` | An Images API request; the reply is one JSON object, or server-sent events when it asks for `"stream": true`. Core refuses a request without a `prompt` and names `gpt-image-2` as the model when it names none. |
| `openai.images.edits` | `POST /v1/images/edits` | As for generations. A `multipart/form-data` edit arrives in its JSON form: each field a member (`n`, `output_compression` and `partial_images` as numbers, `stream` as a boolean, `mask[file_id]` and `mask[image_url]` inside `mask`), the image files (`image[]`, else `image`) added to `images` as `{"image_url": "data:..."}` and the mask file as `mask.image_url`. Core refuses a form without an image file. |
| `gemini.interactions` | `POST /v1beta/interactions` | Gemini's Interactions API request, in Google's own shape; the reply is one JSON object, or server-sent events when it asks for `"stream": true`, which the extension sends with each event's name. The request names a model or an agent: core refuses one that names both or neither, a `stream` that is not a boolean, and a body that is not JSON, removes a `models/` prefix from the model, and sends a request with an agent and no model to any account, with `request.model` empty. The client's `Api-Revision` header is in `native.headers` (`api-revision`). Google's clients may send the client key in `x-goog-api-key` or `?key=`. |

For `codex.models`, `non_codex` clears `prefer_websockets`, `use_responses_lite`,
`supports_reasoning_effort_updates`, `supports_search_tool` and an existing
`supports_image_detail_original` to false, sets `service_tiers` to an empty array and `tool_mode`
to `"direct"`, and sets `apply_patch_tool_type`, `upgrade` and `availability_nux` to null.
Catalog errors are returned without updating routing state, and are never retried.

Adding an endpoint format uses the existing version 0.7 operation and frames; it does not
require a new protocol minor version (as with `codex.alpha_search` and `gemini.interactions`).

The reply is a stream of events as for `generate`, without items: `response.start`, whose
`response.format` is the endpoint's format and whose `response.raw` says how to send the body,
then any number of `native` events, each a JSON string holding the next piece of the body, in
order, then `finish`, with `usage` when the upstream reported it.

```json
{"type": "response.start", "response": {"id": "", "model": "", "format": "openai.responses.compact", "raw": {"content_type": "application/json"}}}
{"type": "native", "raw": "{\"object\":\"response.compaction\",\"output\":[...]}"}
{"type": "finish", "finish": {"reason": "stop"}, "usage": {"input_tokens": 120, "output_tokens": 30}}
```

| `response.raw` field | Meaning |
| --- | --- |
| `content_type` | The body's media type, which core sends as the reply's `Content-Type`. Required. With `text/event-stream`, core sends each piece to the client as soon as it arrives. |

The body is text: send it as it should reach the client. Except for the direct `codex.models`
call above, an `error` before `response.start` is
routed as for `generate`: core tries another account when it is retryable, and otherwise answers
the client with an error in OpenAI's shape. An `error` after `response.start` ends the body where
it is; a `text/event-stream` body then gets a last `error` event. An account that does not serve
the endpoint (or does not serve it for this account's credential) answers with code
`unsupported`, scope `credential` and `retryable` true, so core tries another account.

## Accounts

```json
{
  "id": "openai-main",
  "settings": {"base_url": "https://api.openai.com/v1"},
  "credential": {"kind": "api_key", "api_key": "sk-..."}
}
```

| Field | Meaning |
| --- | --- |
| `id` | The account's ID in core's configuration. |
| `settings` | Extension-specific settings, passed through as configured. |
| `credential` | The credential for this call (below). |
| `proxy_url` | Since 0.11, optional: the proxy every upstream request of this call goes through (below). Absent: no proxy. |

An extension only ever receives its own accounts: core stores each credential with the ID of the
extension that created it and never sends it to another.

A credential has a `kind` and the fields that kind uses:

| Field | Meaning |
| --- | --- |
| `kind` | `api_key`, `oauth`, or a kind of the extension's own. |
| `api_key` | For `api_key`: the key. |
| `access_token`, `refresh_token` | For `oauth`: the tokens. |
| `expires_at` | When the access token stops working, in Unix seconds; absent when it does not expire or is not known. |
| `refresh_at` | When core should renew the credential, in Unix seconds. Absent: 5 minutes before `expires_at`. A provider whose tokens are best renewed well ahead (days, for some) sets it. |
| `data` | A JSON object of anything else the provider needs (an ID token, a service-account key, an account ID). Core stores it and passes it on without reading it. |

Every string in a credential except `kind` counts as secret: core removes each of them, in their
JSON, URL and base64 forms and as quoted in core's own messages, from everything a call sends
back. Keep values that are not secret,
especially short ones, out of the credential. Core removes the secrets in the account's
`settings` the same way: every value below a key named like a secret (such as `api_key`,
`password` or `*_token`), header values about authentication in a `headers` object, and any URL
with a user name, password or query (a `base_url` with a token in it), as its management API
hides them. Below a key named like a secret, `true`, `false`, `null` and the empty string are
not secrets: they carry none. Unlike a credential's strings, a settings secret shorter than 8
bytes is left in text, since removing every occurrence of so short a string would mangle
unrelated text; the management API still shows it as a placeholder. When the settings change,
core keeps removing the secrets of every earlier settings from the extension's stderr and stdout
until the program exits, for calls that still run with them.

### Proxy

From version 0.11 an account may carry `proxy_url`, an `http://`, `https://`, `socks5://` or
`socks5h://` URL: `scheme://[user:password@]host[:port]`. The extension sends every upstream
request and connection of the call through it: model requests, WebSocket handshakes, token
renewals in `refresh`, and everything a `login` sends (token exchange, device polling). An
`http` or `https` proxy gets plain HTTP requests in absolute form and a `CONNECT` tunnel for
TLS; a SOCKS5 proxy gets a `CONNECT` to the target, and with `socks5h` it resolves the
target's host name (Go's `net/http` lets the proxy resolve it for `socks5` too). The user name and password, when present, authenticate to the proxy
(HTTP Basic in `Proxy-Authorization`, or SOCKS5 user/password) and are secret: an extension never
logs or shows them, and core removes them from everything a call sends back, as it does a
credential's strings. An extension that cannot use the proxy fails the call rather than send
it without one.

Core decides the proxy; the extension never chooses one. It sends the account's own
`proxy_url` setting, else core's global `proxy_url`; an account set to `direct` or `none` gets
none even when the global one is set, and then `proxy_url` is absent. A `login` that adds a new
account carries the `proxy_url` the request to add it named, else the global one, so even a
first sign-in goes through the proxy. Core sends a call that has a proxy only to an extension
whose describe reports `protocol_minor` 11 or more; for any other it fails the call itself with
`unsupported` (scope `credential`), so routing moves on, and the traffic never leaves without the
proxy.

In Go, `sdk.Upstream` does all of this for each client it wraps: requests made with the
handler's context go through the call's proxy. A proxy is honoured only by clients wrapped in
`sdk.Upstream`; a client that is not, such as `http.DefaultClient`, sends direct. So the SDK
answers a call that carries a proxy with `unsupported` (scope `credential`, retryable) and never
runs the handler when the extension process has not created any `sdk.Upstream` transport. An
extension that wraps some of its clients and not others still sends the others direct: wrap them
all.

## The request

A request is an ordered list of typed items plus options. It is modelled on the prompt types of
Vercel's AI SDK Language Model Specification and on the items of Open Responses, kept small for
version 0.

```json
{
  "model": "gpt-4.1-mini",
  "stream": true,
  "items": [
    {"type": "message", "role": "system", "content": [{"type": "text", "text": "Be brief."}]},
    {"type": "message", "role": "user", "content": [{"type": "text", "text": "Weather in Oslo?"}]},
    {"type": "tool_call", "call_id": "call_1", "name": "get_weather", "arguments": "{\"city\":\"Oslo\"}"},
    {"type": "tool_result", "call_id": "call_1", "output": "4°C, rain"}
  ],
  "tools": [
    {"name": "get_weather", "description": "Current weather", "parameters": {"type": "object"}}
  ],
  "options": {"max_output_tokens": 200, "temperature": 0.2, "top_p": 1, "stop": ["\n\n"]},
  "native": {"format": "openai.chat", "body": {...}}
}
```

| Field | Meaning |
| --- | --- |
| `model` | The model name the client asked for. |
| `stream` | Whether the client asked for a streamed reply. Core always receives events; this only tells the extension how the client is being served, in case the upstream call should match. |
| `items` | The conversation, in order. |
| `tools` | Function tools the model may call. `parameters` is a JSON Schema. |
| `options` | Generation options; any field may be absent: `max_output_tokens`, `temperature`, `top_p`, `stop`, `tool_choice` (`mode`: `auto`, `none`, `required` or `tool` with `name`), `parallel_tool_calls`, `response_format` (`type`: `text`, `json_object` or `json_schema` with `name`, `description`, `schema`, `strict`), `reasoning_effort` (as the client named it), `thinking_suffix` (version 0.2; see below). |
| `native` | The client's original request body and its format, when core received one; `headers` holds the client's request headers that belong to that format, by lower-case name (for `anthropic.messages`: `anthropic-version` and `anthropic-beta`, and the headers the client names itself with: `user-agent`, `x-app`, `anthropic-dangerous-direct-browser-access`, `x-claude-code-session-id`, `x-client-request-id`, the `x-stainless-*` headers of Anthropic's SDKs, `x-client-app` and `x-anthropic-additional-protection`; and Claude Code's subagent, request and remote-session headers (`x-claude-code-agent-id`, `x-claude-code-parent-agent-id`, `x-claude-code-agent-type`, `x-claude-code-request-class`, `x-claude-code-prev-tool-durations`, `x-claude-code-compaction`, `x-claude-code-context-compacted`, `x-claude-remote-container-id`, `x-claude-remote-session-id`), which an extension should send upstream only for a request that names itself as Claude Code (`anthropicmessages.IsClaudeCode`: `x-app: cli`, a `claude-cli/` User-Agent and the `claude-code-20250219` beta); for `openai.responses`: the headers the Codex CLI sends, such as `session-id`, `x-codex-window-id`, `x-codex-turn-metadata`, `version`, `originator` and `user-agent`). |
| `origin` | The request's top-level fields that have no place in the model (see Origin below). |
| `drop_policy` | What to do when building the upstream request would drop part of this one: `report` (default: send it and report the losses) or `reject` (refuse with `unsupported`, naming what could not be carried). |
| `caller` | Version 0.8. A value that stands for the client key the request came with: the same for every request with that key to this extension, different for another key or another extension, and not derivable from the key (core derives it with a secret it makes each time it starts, so it changes when core restarts). Absent when the client sent no key. Core sends it with `generate`, `count_tokens` and `endpoint` calls, to every extension. An extension that keeps state from replies across requests, such as reasoning to put back into a later turn, keys that state by `caller`, so one client never gets another's. |

Item types:

| Type | Fields |
| --- | --- |
| `message` | `role` (`system`, `developer`, `user`, `assistant`), `content`: a list of parts; `text_content` is true when the client gave the content as a plain string. |
| `tool_call` | `call_id`, `name`, `arguments` (the raw argument string, not parsed); `custom` (default false) marks a call to a custom (freeform) tool, whose `arguments` is the tool's raw input rather than JSON. |
| `tool_result` | `call_id`, `output` (string). |
| `reasoning` | `text`: reasoning text or summary the client replayed. Opaque reasoning data (such as an OpenAI encrypted blob or an Anthropic thinking signature) stays in `origin.raw`. |
| `native` | An item core could not interpret (for example a hosted tool call or Anthropic redacted thinking); `origin.raw` holds it whole, in its place in the conversation. |

Every item may also carry `id` (its identifier in the client's format) and `origin`.

Content part types:

| Type | Fields |
| --- | --- |
| `text` | `text`. |
| `image` | `url` (an `https:` or `data:` URL), optional `detail`; or `file_id`. |
| `file` | One of `file_id`, `file_data` (inline) or `url`; optional `filename`. |
| `native` | `format` and `data`: a part core could not interpret, kept as the client sent it. |

Tools: `type` (`function`, the default, or `native` for a tool core could not interpret, such as a
provider-hosted tool, held whole in `origin.raw`), `name`, `description`, `parameters` (JSON
Schema), `strict`, `origin`.

**Origin.** The request, every item, part and tool may carry `origin`: `format` (the format it
came from), `path` (where it was in the client's body, for example `messages[2]` or `input[0]`)
and `raw` (a JSON object of its fields that have no place in the model, such as a message's `name`
in Chat Completions). An encoder for `format` writes `raw` back, so those fields survive a round
trip; an encoder for another format cannot, and reports each of them as dropped.

**Native passthrough.** When `native.format` is the format the extension's upstream speaks, the
extension should send `native.body` upstream unchanged instead of rebuilding a body from `items`.
That keeps fields core does not model. It also sends `native.headers` with it (for `anthropic.messages`, at least `anthropic-version` and `anthropic-beta`; the client's identification headers when the upstream should see the real client). Formats in version
0: `openai.chat` (OpenAI Chat Completions request body), `openai.responses` (OpenAI Responses
request body), `anthropic.messages` (Anthropic Messages request body) and
`gemini.generate_content` (Gemini GenerateContent request body; its model and whether it streams
come from the request path, so they are in the request's `model` and `stream`).

**Thinking suffix.** A client may name a thinking setting after the model name, in brackets:
`gpt-5(high)`, `claude-sonnet-4-5(16000)`. Core takes the suffix off `model` (and off the model
in `native.body`) and puts it in `options.thinking_suffix`: a level (`minimal`, `low`, `medium`,
`high`, `xhigh`, `max`), `none`, `auto`, or a token budget (a whole number; `0` is `none`). The
extension applies it to its upstream request in place of any reasoning or thinking settings the
request has, also to a native body it sends unchanged: as the upstream's own effort or thinking
settings for the model, and a budget as a budget where the upstream takes one, otherwise as the
level it falls in (up to 512 tokens `minimal`, 1024 `low`, 8192 `medium`, 24576 `high`, more
`xhigh`). An extension that ignores it serves the model without the suffix's setting.

**Translation report.** When the extension rebuilds the body in another format, it reports in the
reply's `response.start` how it built it: `response.translation` with `from`, `to`, `passthrough`
(true when the client's body was sent unchanged) and `losses`, each with `kind` (`approximated`:
carried in a different form; `dropped`: left out), `path` (in the client's body) and `detail`.
Everything not listed was preserved. Core logs the losses, together with what the reply had that
the client's format cannot hold.

## Events

Events are modelled on the stream parts of Vercel's Language Model Specification and on the
streaming events of Open Responses: the reply is a sequence of output items, each opened, filled
with deltas and closed, then one terminal event.

| Type | Fields | Meaning |
| --- | --- | --- |
| `response.start` | `response`: `id`, `model`, `created` (Unix seconds), optional `format`, `raw` and `translation` | First event. `model` is the model that actually served the request. `format` names the upstream format the reply came from (for example `openai.chat`); `raw` is a JSON object of the upstream's reply-level fields that have no place in the model (for example `system_fingerprint`). `translation` is the translation report (above). |
| `item.start` | `index`, `item`: `kind` (`message`, `reasoning`, `tool_call`), and for tool calls `call_id` and `name` | Opens output item `index`. |
| `text.delta` | `index`, `delta` | Text for an open `message` item. |
| `reasoning.delta` | `index`, `delta` | Reasoning text for an open `reasoning` item. |
| `tool_input.delta` | `index`, `delta` | A fragment of the argument string for an open `tool_call` item. |
| `item.end` | `index`; for a reasoning item, optional `opaque`: `format` and `data` | Closes item `index`. `opaque` is the provider's opaque record of the reasoning, which it needs back to continue from it (for `anthropic.messages`, the thinking block's signature; for `openai.responses`, the reasoning item's `encrypted_content`). A client format with a place for it carries it: a Responses client gets a thinking signature as the reasoning item's `encrypted_content`, and a Messages client gets a Responses reasoning item's encrypted content as the thinking block's signature. When the client replays that item or block, the request builder for the provider's format turns it back into the provider's own. |
| `native` | `raw` | Upstream data that has no place in the model, in the reply's `format`. For `openai.chat` it is `{"choices": [...]}`: choices after the first, as the upstream sent them. For `openai.responses` and `anthropic.messages` it is an upstream stream event the model has no event for (for example `response.in_progress`, or `ping`, `message_delta` and a thinking block's `signature_delta`). For `gemini.generate_content` it is a stream chunk that produced no other event. |
| `finish` | `finish`: `reason`, `raw_reason`; optional `usage` | Terminal: success. |
| `error` | `error` (below) | Terminal: failure. |

Any event may carry `raw`: the upstream data it came from, for diagnostics. `raw` must be valid
JSON; upstream bytes that are not JSON go in as a JSON string. Core writes upstream data back
only to a client that speaks the reply's `format`: the `response.start` fields, `native` events
and the usage `raw` (so fields such as a vendor cost survive); other clients do not get it.

For `openai.responses` and `anthropic.messages`, every event carries the upstream stream event it
came from in `raw` (an `error` carries it in `error.raw`), and the `finish` of a non-streamed reply
carries the whole Response or Message object. A client in that format gets those upstream events
and that object unchanged; events an extension adds itself (such as closing an item the upstream
left open) carry no `raw` and are not written to it.

For `openai.chat`, a `tool_call`'s `item.start` carries the call's `extra_content` in `raw`, as
`{"extra_content": ...}`, when the upstream sent one with the call's first delta (its `item.end`
carries one sent later): Gemini's OpenAI-compatible API puts a call's
thought signature there (`extra_content.google.thought_signature`), and a Gemini client gets it back
as the call part's `thoughtSignature`.

For `gemini.generate_content`, each upstream chunk (a whole GenerateContentResponse) rides in the
`raw` of the first event it produces, or of a `native` event when it produces none; a non-streamed
reply's body rides on `response.start`. A Gemini client gets those chunks unchanged, thought
signatures included. Gemini streams end when the upstream closes them, so `finish` comes at the end
of the stream and carries no `raw`; `finish.raw_reason` keeps Gemini's finish reason (`SAFETY`,
`RECITATION`, `MAX_TOKENS` and the rest stay distinct), and a blocked prompt finishes with reason
`content_filter` and the block reason as `raw_reason`.

Ordering rules, which core enforces and the conformance kit checks:

- The first event is `response.start` or `error`.
- Item indexes start at 0 and go up by one with each `item.start`.
- A delta's `index` names an open item of the matching kind.
- A `native` event carries `raw`; it may come anywhere between the first and the terminal event.
- Every opened item is closed before `finish`. Items may interleave (for example, parallel tool
  calls).
- Nothing follows the terminal event.

An extension that breaks a rule fails the call: core ends it with an `extension_protocol` error.

Finish reasons: `stop`, `length`, `tool_calls`, `content_filter`, `other`. `raw_reason` keeps the
upstream's own value.

Usage, in `finish.usage`; a field that is absent was not reported, which is different from zero:

| Field | Meaning |
| --- | --- |
| `input_tokens` | All input tokens, including cached ones. Gemini's `promptTokenCount` already includes its cached tokens; its tool-use prompt tokens count here too. |
| `output_tokens` | All output tokens, including reasoning. Gemini counts `thoughtsTokenCount` apart from `candidatesTokenCount`; this is their sum. |
| `cached_input_tokens` | Input tokens read from a cache. |
| `cache_write_input_tokens` | Input tokens written to a cache. Anthropic counts cache reads and writes apart from its `input_tokens`; `input_tokens` here is their sum. |
| `reasoning_tokens` | Output tokens spent on reasoning. |
| `raw` | The upstream's usage object, unchanged. |

## Errors

```json
{
  "type": "error",
  "error": {
    "code": "rate_limit",
    "message": "Rate limit reached for requests",
    "status": 429,
    "provider_code": "rate_limit_exceeded",
    "retryable": true,
    "scope": "model",
    "retry_after_ms": 20000
  }
}
```

| Field | Meaning |
| --- | --- |
| `code` | One of the codes below. |
| `message` | Human-readable; safe to show to the client. Never include credentials. Core also removes the call's credential, and its JSON, URL and base64 forms, from `message`, `provider_code`, every `raw` field, every `opaque` `data`, the response's `id`, `model` and `translation`, every item's `call_id` and the finish's `raw_reason` of every event before passing them on, in case an upstream echoes it. In the error's `code` and `scope`, the response's `format`, the finish's `reason` and an opaque's `format`, a value core defines is passed on as it is and any other that holds the credential is replaced whole with `[redacted]`, so a very short credential cannot turn a value core acts on into another. |
| `status` | The upstream HTTP status, when there was one. |
| `provider_code` | The upstream's own error code, when there was one. |
| `retryable` | True only when the upstream certainly did not run the request (it was refused), so trying again, on this account later or on another account, is safe. For `login` and `refresh` calls, see [Errors in login and refresh calls](#errors-in-login-and-refresh-calls). |
| `scope` | What the failure is about: `request` (this request is bad), `credential` (this account), `model` (this model on this account), `endpoint` (the upstream endpoint). A rate limit or quota is `model` unless the provider says it covers the whole account, such as a subscription's used-up usage window; today's core also holds back only the model for a plain 429. When `scope` is absent, core takes `model` for `rate_limit`, `quota` and `not_found`, `credential` for `auth` and `permission`, and `endpoint` for `upstream` and `unavailable`. |
| `retry_after_ms` | When the upstream said to try again, in milliseconds from now. |
| `raw` | The upstream's error body, unchanged. |
| `usage` | Usage the upstream reported before the failure, in the same form as `finish.usage`; absent when it reported none. A reply that fails partway may still have cost tokens. |
| `quota_window_minutes` | Version 0.6. For code `quota`: the length, in minutes, of the usage window that is used up, when the upstream said (such as 300 for a five-hour window). Core passes it on in its [usage-limit reply](../README.md#accounts-and-routing). |

Codes an extension sends:

| Code | Use for |
| --- | --- |
| `invalid_request` | The upstream rejected the request itself. |
| `auth` | The credential was refused. |
| `permission` | The credential is valid but not allowed this request. |
| `not_found` | The model or resource does not exist upstream. |
| `rate_limit` | Too many requests; try again later. |
| `quota` | A usage allowance is used up. |
| `unsupported` | The extension does not support what was asked. |
| `upstream` | The upstream failed (for example a 5xx). |
| `unavailable` | The upstream could not be reached. |
| `cancelled` | The call was cancelled. |
| `internal` | A bug or unexpected state in the extension. |

Codes core produces itself: `extension_crashed` (the connection ended without a terminal event),
`extension_protocol` (the extension broke a rule above), `extension_unavailable` (the extension is
not running).

An error may come after other events, for example when the upstream stream breaks halfway. Core
then reports it to the client in the client's stream format; it never retries once bytes have
reached the client.

## Login and refresh

Core owns the record of every account added by login: it stores the credential, with a version,
and decides when to renew it. The provider owns the protocol: how to sign in, and how to turn an
old credential into a new one. An extension that signs in to accounts lists `login` and `refresh`
in `operations`, and its ways of signing in in `login_methods`:

```json
"login_methods": [
  {"method": "device", "kind": "device", "label": "Device code"},
  {"method": "browser", "kind": "browser", "label": "Browser"},
  {"method": "api_key", "kind": "api_key", "label": "API key", "inputs": [{"name": "api_key", "label": "API key", "secret": true}]}
]
```

| Field | Meaning |
| --- | --- |
| `method` | Names the method in a login call. Unique within the extension. |
| `kind` | `api_key` (the user enters a key), `browser` (the user opens a URL and signs in: OAuth authorization code with PKCE, [RFC 7636](https://www.rfc-editor.org/rfc/rfc7636)) or `device` (the user enters a code on another device: [RFC 8628](https://www.rfc-editor.org/rfc/rfc8628)). |
| `label` | For people. |
| `inputs` | What the user enters when starting the login: `name`, `label`, `secret` (hide it), `optional`. |

### `login`

```json
{"type": "call", "id": "c-9", "op": "login", "account": {"id": "work", "settings": {...}}, "login": {"method": "device", "input": {}}}
```

`login.input` holds what the user entered for the method's `inputs`. The reply is a stream:
any number of `login.prompt` frames, then exactly one terminal frame, `login.done` or `error`,
after which the extension closes the connection.

```json
{"type": "login.prompt", "prompt": {"kind": "device", "user_code": "WDJB-MJHT", "verification_uri": "https://example.com/device", "expires_in": 900}}
{"type": "login.done", "grant": {"credential": {"kind": "oauth", "access_token": "...", "refresh_token": "...", "expires_at": 1767225600}, "label": "user@example.com"}}
```

A prompt tells the user what to do; a later prompt replaces an earlier one. Its fields:

| Field | Meaning |
| --- | --- |
| `kind` | `browser` (open `url`), `device` (open `verification_uri` and enter `user_code`) or `input` (enter `inputs`). |
| `message` | For people. |
| `url` | For `browser`. |
| `user_code`, `verification_uri`, `verification_uri_complete` | For `device`, as RFC 8628 section 3.2 names them. |
| `expires_in` | Seconds the prompt stays valid. |
| `inputs` | What the user may send back. A browser prompt usually accepts the URL the browser was redirected to, for when the extension's own callback cannot be reached (core runs on another machine). |

While a login runs, core may send `login.input` frames on the same connection with what the user
entered for the last prompt; nothing else follows the call frame:

```json
{"type": "login.input", "input": {"redirect_url": "http://localhost:1455/callback?code=...&state=..."}}
```

From version 0.9, a login grant may also carry a `settings` object of non-secret,
extension-specific account settings. It must never hold a secret: secrets belong in
`credential`. The extension validates and normalises these settings before returning them.
When adding an account, core merges the grant's settings with those supplied by the caller:
caller-supplied properties win, including explicit `null`; nested objects are replaced as a
whole. On a login to an existing account, core ignores the grant's settings and keeps the
account's existing settings. A grant without settings keeps the previous behaviour.
Core validates grant settings at the extension boundary: `null` is treated as absent; any
other value must be an object with an encoded size of at most 8192 bytes (8 KiB, matching
the maximum total identity value bytes: 16 fields of 512 bytes). A non-object or oversized
value fails the login with `extension_protocol`. Caller settings must be an object or
absent (`null` also means absent); `POST /accounts` refuses a non-object with `invalid_request`
before starting a login. Like a grant's `identity`, core uses grant settings regardless of
the extension's reported minor version, an exception to the general minor-version rule.

`login.done` carries the `grant`: the new `credential`, a `label` naming the account for people
(not secret) and, from version 0.5, its `identity`: the upstream account the credential signs in
to, as an object of the fields the provider knows it by, such as
`{"email": "user@example.com", "account_id": "..."}`. Identity is not secret, has at most 16 fields,
and each name and value is a non-empty string (names up to 64 bytes, values up to 512). Core
stores it with the account. On a new login to an existing account, every field of the stored
identity must be in the new grant's identity with the same value, letter case aside; otherwise
core refuses the login and keeps the old credential, so a login never silently turns an account
into someone else's. Report the fields that tell two accounts apart: for a provider whose users
can belong to several workspaces, both the user and the workspace. An account stored without an
identity is checked by `label` instead, when both have one. A grant with an identity that breaks
these bounds fails the login with `extension_protocol`. A refusal (the user denied access, the code expired, the key was refused) is an
`error` with code `auth`. Closing the connection cancels the login: stop polling and release any
callback listener. A login may wait for a person for a long time; core ends it after 15 minutes.

The provider does the protocol work: generating and checking the PKCE verifier and `state`,
polling the token endpoint at the interval it gave (and slowing down on `slow_down`), and talking
to its own callback. Core never sees a verifier or an authorization code, only the credential.

### `refresh`

```json
{"type": "call", "id": "c-12", "op": "refresh", "account": {"id": "work", "settings": {...}, "credential": {...}}}
```

The reply is one frame: `refresh.done` with the renewed credential, whole (core replaces the
stored one with it; keep the old refresh token when the provider did not send a new one), or an
`error`:

```json
{"type": "refresh.done", "grant": {"credential": {"kind": "oauth", "access_token": "...", "refresh_token": "...", "expires_at": 1767229200}}}
```

| Failure | Report |
| --- | --- |
| The provider refused the grant itself (OAuth `invalid_grant`): only a new login helps. | Code `auth`, scope `credential`, not retryable. Core marks the account as needing a new login. |
| The request never reached the token endpoint (connection refused, host name not found, TLS handshake failed). | Code `unavailable`, scope `endpoint`, retryable. Core keeps the credential and tries again later. |
| The token endpoint answered 5xx, or the request was sent and then failed (the connection was reset or timed out, or the reply could not be read). | Code `unavailable` (or `upstream`), scope `endpoint`, not retryable, with `retry_after_ms` when known. Core keeps the credential and tries again later. |
| The token endpoint rate limited the call (429). | Code `rate_limit`, retryable, with `retry_after_ms` when known. Core keeps the credential and tries again later. |

Core sends at most one refresh per credential at a time, and every request that needs the same
credential renewed waits for that one call. It applies the result only if the stored credential
has not changed since the refresh began (a new login, a removal); otherwise the result is
discarded. A refresh call is never cancelled because a client stopped waiting, since a provider
that rotates refresh tokens ([RFC 9700 section 4.14](https://www.rfc-editor.org/rfc/rfc9700#section-4.14))
may already have retired the old one: only core shutting down cancels it. Core renews a credential
at its `refresh_at` (on the first request after it), and when the upstream refuses it with an
`auth` error (once per request). After a renewal that failed in a way that may pass, core waits 5
minutes (or the `retry_after_ms` given) before renewing automatically again; after one that gave a
credential already due for renewal, 30 seconds. An extension never refreshes on its own.

### Errors in login and refresh calls

For a `login` or `refresh` call, `retryable` is true only when the request never reached the
provider. Send `retryable: true` only when you are sure of that: the connection was refused, the
host name was not found, the TLS handshake failed, or anything else went wrong before the first
byte of the request was written. A request that was written may have run. A provider that rotates
refresh tokens may already have retired the old token, and a code exchange may already have used
the code. So these are not retryable: a 5xx answer, a connection that was reset or timed out after
the request was sent, and a reply that could not be read or understood. Keep the code and scope you
would use otherwise (for example `unavailable` with scope `endpoint`) and set `retryable` false. A
429 stays retryable, with `retry_after_ms` when the provider said how long: the provider refused to
run the request.

A login that polls for the user (the device grant) follows the same rule. After a poll fails, poll
again only when the request was not sent. Stop the login with the error when the poll got a 5xx or
failed after it was sent. `authorization_pending` and `slow_down` are answers, not failures, and
work as RFC 8628 says.

In Go, `sdk.TrackSend` wraps a request and tells you, after it failed, whether any of it was
written.

## Sessions

Version 0.1. Some upstreams keep state between requests on one connection. A Responses WebSocket
keeps the previous response in memory for that socket only (with `store: false`, nothing else
holds it), so the next request names it with `previous_response_id` and sends only the new input.
A run of such requests needs the same upstream connection, on the same account, for its whole
life. A **session** is how core hands that run to an extension: one local connection, one
account, one request (a **turn**) at a time.

Core opens sessions only with an extension whose describe lists `session` in `operations` and
reports `protocol_minor` 1 or more.

### Opening a session

```json
{"type": "call", "id": "c-21", "op": "session", "account": {...}, "session": {"format": "openai.responses"}}
```

| Field | Meaning |
| --- | --- |
| `session.format` | The kind of session: the request format of its turns, whose upstream state the session holds. Version 0.1 has `openai.responses` ([below](#openairesponses-sessions)). |

The extension answers with one frame:

- `{"type": "session.ready"}`: the session is open. The extension may connect to its upstream now
  or at the first turn.
- An `error` event, after which it closes the connection: it will not hold this session. Code
  `unsupported` with scope `credential` means this account does not take sessions (for example,
  its settings leave the upstream WebSocket off); core then sends the same requests to that account
  as separate `generate` calls and keeps the conversation itself. Any other error fails the
  request that needed the session.

### Turns

```json
{"type": "session.turn", "account": {...}, "request": {...}}
```

Core sends a turn and reads its reply as for a `generate` call: events under the same ordering
rules, ending in one `finish` or `error`. Core sends the next turn only after the last one's
terminal event. A turn's `error` ends that turn, not the session.

| Field | Meaning |
| --- | --- |
| `account` | Always the account the session opened with (the same `id`). Its `credential` is the newest one core holds, which may have been renewed since the session opened: use it for any new upstream connection. A connection already open stays as it is. |
| `request` | A model request, as for `generate`. A turn may continue the session's previous response; for `openai.responses`, `native.body.previous_response_id` names it and `items` hold only the new input. A turn that continues nothing carries the whole conversation. |

Core removes every credential the session has carried, in all the forms listed under
[Errors](#errors), from every event of every turn.

### Interrupts

```json
{"type": "session.interrupt", "raw": {"type": "response.interrupt", "response_id": "resp_1", "mode": "discard_partial_items"}}
```

While a turn runs, core may send `session.interrupt` with the client's own control message in
`raw`, in the session's format. Core sends it only when it targets the running turn's response
(the `id` in the turn's `response.start`). The extension passes it to the upstream and carries on
with the turn: whatever the upstream sends back (an acknowledgement, a `response.incomplete`)
arrives as the turn's events. Nothing answers the frame itself. An extension whose upstream cannot
be interrupted ignores it.

### Ending a session

- Core ends a session by closing the connection: when the client disconnects, when the extension
  generation is replaced (an idle session at once, one with a running turn after that turn), and
  at shutdown. A running turn is cancelled, as when core closes a `generate` call; the extension
  then closes its upstream connection.
- The extension may end a session by closing the connection between turns. Core then treats the
  session as gone: a later turn that continues it fails without reaching the extension, and one
  that continues nothing opens a new session.
- When the upstream connection is lost, the extension keeps the session. A turn running at the
  time ends with an error; a later turn that continues a response held on the lost connection
  fails before any other event (for `openai.responses`, as described below); a turn that
  continues nothing connects to the upstream again, on the same account.

A session holds its extension generation like a running request, so core never moves it to
another account, another extension or a newer generation: no credential changes account halfway
through a conversation whose state lives upstream. A crash or stop of the program ends the session.

Liveness: the extension checks its upstream connection itself (for example, a read deadline
between upstream messages); that check is one of the named liveness checks the rule against
network timeouts allows. The local session connection needs none: either end sees the other go
when the connection closes.

### `openai.responses` sessions

The upstream is a Responses WebSocket (`/v1/responses`, as in
[OpenAI's WebSocket mode](https://developers.openai.com/api/docs/guides/websocket-mode)). Each turn
is a Responses request: `native.format` is `openai.responses` and `native.body` is the client's
`response.create` message. The extension sends it upstream as one `response.create` text message.

- `previous_response_id` and `generate` pass through as the client sent them. A warmup
  (`generate: false`) is answered like any turn.
- Every upstream message the turn receives is one event with the message in `raw`, as for a
  streamed Responses reply ([Events](#events)); messages the model has no event for travel as
  `native` events.
- An upstream `error` message or `response.failed` ends the turn with an `error` event carrying
  the message in `error.raw`; codes and scopes are chosen as for the same failure over HTTP.
- A connection lost during a turn ends it with an `error`: code `unavailable`, status 502,
  `provider_code` `upstream_websocket_lost`, not retryable (the upstream may have run the turn).
- A turn that continues a response the session no longer holds (its connection was lost) fails
  before any other event with an `error`: code `invalid_request`, status 409, scope `request`,
  `provider_code` `previous_response_not_found`. The client must send the whole conversation again,
  without `previous_response_id`.

Core serves the client side, `GET /v1/responses` upgraded to a WebSocket, and pins one client
socket to one session. Turns on an account that takes no sessions go out as separate `generate`
calls, each carrying the whole conversation so far: core keeps the earlier input and output and
removes `previous_response_id`.

## Versioning

The version in the handshake (`0`) is the major version: core and the extension must share it.
Changes that keep every extension working (new optional fields; new operations, frames and event
types that core uses only with an extension that says it supports them) keep the major version and
raise the minor version: this document is version 0.11. An extension reports the minor version it
implements in describe's `protocol_minor`, and core uses what a minor version adds only with an
extension that reports it (and, for an operation, lists it in `operations`). Any other change needs
a new major version, and core keeps speaking old versions for a while.

| Version | Adds |
| --- | --- |
| 0 (0.0) | `describe`, `generate`, `login`, `refresh`. |
| 0.1 | [Sessions](#sessions): the `session` operation and its frames; `protocol_minor` in describe. |
| 0.2 | [Token counting](#count_tokens): the `count_tokens` operation. The request's `options.thinking_suffix`. |
| 0.3 | [Model lists](#models): the `models` operation and its `models.result` frame. |
| 0.4 | [Account checks](#check): the `check` operation and its `check.result` frame. |
| 0.5 | [Quota](#quota): the `quota` operation and its `quota.result` frame. A login grant's `identity`. |
| 0.6 | An error's `quota_window_minutes`. |
| 0.7 | [Endpoint calls](#endpoint): the `endpoint` operation. |
| 0.8 | The request's `caller`. |
| 0.9 | A login grant's optional `settings` object. |
| 0.10 | Quota's optional `plan_ends_at`. |
| 0.11 | The account's optional [`proxy_url`](#proxy). |

## What version 0.11 leaves out

These are planned and the protocol is shaped to take them: per-credential and per-model
capabilities in `describe`; operations beyond `generate`, sessions, token counting, model lists,
account checks, quota and endpoint calls; several turns at once in one session (OpenAI's `stream_id` lanes); realtime sessions
(audio, WebRTC); a private data folder per extension.

# pluxy extension protocol, version 0

This is the contract between the pluxy core and an extension program. Version 0 covers one kind
of extension, a **provider**: a program that takes a model request from core, calls an upstream
model service, and streams the reply back. It is deliberately small; later versions add
capabilities, credentials and login, more operations, and hot swap without changing what is
described here.

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
| `version` | The package version, chosen by the publisher. Core does not interpret it in v0. |
| `protocol` | Protocol versions the program speaks. Core picks one it also speaks. |
| `programs` | Program to run per platform, keyed by Go's `GOOS/GOARCH`. Paths are relative to the manifest's folder and may not leave it. |

The package version, manifest version and protocol version are separate on purpose: each can
change without the others. Later manifest versions add optional parts (UI pages, a config schema,
themes) and their own version fields.

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
| `op` | The operation. Version 0 has `describe` and `generate`. |
| `account` | For `generate`: the account to use (below). |
| `request` | For `generate`: the model request (below). |

An extension that gets an `op` it does not know replies with an `error` event whose code is
`unsupported`.

### `describe`

The reply is one frame, then the extension closes the connection:

```json
{"type": "describe.result", "info": {"id": "pluxy/openai-compatible", "version": "0.1.0", "protocol": 0, "operations": ["generate"]}}
```

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
| `credential` | The credential for this call. Version 0 has one kind, `api_key`. |

An extension only ever receives its own accounts.

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
| `options` | Generation options; any field may be absent: `max_output_tokens`, `temperature`, `top_p`, `stop`, `tool_choice` (`mode`: `auto`, `none`, `required` or `tool` with `name`), `parallel_tool_calls`, `response_format` (`type`: `text`, `json_object` or `json_schema` with `name`, `description`, `schema`, `strict`), `reasoning_effort` (as the client named it). |
| `native` | The client's original request body and its format, when core received one; `headers` holds the client's request headers that belong to that format, by lower-case name (for `anthropic.messages`: `anthropic-version` and `anthropic-beta`). |
| `origin` | The request's top-level fields that have no place in the model (see Origin below). |
| `drop_policy` | What to do when building the upstream request would drop part of this one: `report` (default: send it and report the losses) or `reject` (refuse with `unsupported`, naming what could not be carried). |

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
That keeps fields core does not model. It also sends `native.headers` with it. Formats in version
0: `openai.chat` (OpenAI Chat Completions request body), `openai.responses` (OpenAI Responses
request body) and `anthropic.messages` (Anthropic Messages request body).

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
| `item.end` | `index` | Closes item `index`. |
| `native` | `raw` | Upstream data that has no place in the model, in the reply's `format`. For `openai.chat` it is `{"choices": [...]}`: choices after the first, as the upstream sent them. For `openai.responses` and `anthropic.messages` it is an upstream stream event the model has no event for (for example `response.in_progress`, or `ping`, `message_delta` and a thinking block's `signature_delta`). |
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
| `input_tokens` | All input tokens, including cached ones. |
| `output_tokens` | All output tokens, including reasoning. |
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
    "scope": "credential",
    "retry_after_ms": 20000
  }
}
```

| Field | Meaning |
| --- | --- |
| `code` | One of the codes below. |
| `message` | Human-readable; safe to show to the client. Never include credentials. Core also removes the call's credential, and its JSON, URL and base64 forms, from `message`, `provider_code` and every `raw` field of every event before passing them on, in case an upstream echoes it. |
| `status` | The upstream HTTP status, when there was one. |
| `provider_code` | The upstream's own error code, when there was one. |
| `retryable` | True only when the upstream certainly did not run the request (it was refused), so trying again, on this account later or on another account, is safe. |
| `scope` | What the failure is about: `request` (this request is bad), `credential` (this account), `model` (this model on this account), `endpoint` (the upstream endpoint). |
| `retry_after_ms` | When the upstream said to try again, in milliseconds from now. |
| `raw` | The upstream's error body, unchanged. |
| `usage` | Usage the upstream reported before the failure, in the same form as `finish.usage`; absent when it reported none. A reply that fails partway may still have cost tokens. |

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

## Versioning

The version in the handshake covers everything in this document. Additive changes (new optional
fields, new event types that core only sends when the extension says it supports them) keep the
version; anything else needs a new version, and core keeps speaking old versions for a while.

## What version 0 leaves out

These are planned and the protocol is shaped to take them: per-credential and per-model
capabilities in `describe`; login and credential refresh; operations beyond `generate` (token
counting, model listing); session ownership and WebSocket sessions; draining and hot swap; a
private data folder per extension.

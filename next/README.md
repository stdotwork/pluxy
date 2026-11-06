# next: the new pluxy core

A new core for pluxy, built from scratch beside today's `core/`. Core receives client requests and
hands them to **provider extensions**: separate programs, one per kind of upstream, that core
starts and talks to over a local socket. Today it serves OpenAI Chat Completions, OpenAI
Responses and Anthropic Messages (streamed and not, over HTTP) through the OpenAI-compatible
API-key provider, which talks to an upstream that speaks any of them. A request in the upstream's
own format passes through unchanged; a Responses or Messages request to a Chat upstream, and a
Messages request to a Responses upstream, are translated, and what the translation could not carry
is logged.

## Layout

| Path | What it is |
| --- | --- |
| `cmd/pluxy` | The core program. |
| `protocol/` | The extension protocol: [README](protocol/README.md), [tutorial](protocol/TUTORIAL.md), wire types and framing. |
| `llm/` | The internal model of a request and its streamed reply, shared by core and extensions. |
| `sdk/` | Go SDK for writing extensions. |
| `conformance/` | The conformance kit extensions run in their tests. |
| `format/` | Client and upstream formats as libraries extensions build in: `sse`, `openaichat`, `openairesponses`, `anthropicmessages`. |
| `extensions/openai-compatible` | Provider extension for OpenAI-compatible APIs with an API key. |
| `examples/scripted` | The tutorial's example extension. |
| `internal/host`, `internal/server` | Core's extension host and HTTP API. |
| `e2e/` | Black-box tests that drive the built core over HTTP. |
| `corpus/` | Differential test corpus: cases replayed against this core and today's `core/`, with listed exceptions ([README](corpus/README.md)). |

## Run it

```bash
go build -o pluxy ./cmd/pluxy
go build -o extensions/openai-compatible/openai-compatible ./extensions/openai-compatible
```

`config.json`:

```json
{
  "listen": "127.0.0.1:8400",
  "client_keys": ["choose-a-client-key"],
  "extension": {
    "manifest": "extensions/openai-compatible/pluxy-extension.json",
    "accounts": [
      {
        "id": "openai",
        "settings": {"base_url": "https://api.openai.com/v1"},
        "credential": {"kind": "api_key", "api_key": "sk-..."}
      }
    ]
  }
}
```

```bash
./pluxy -config config.json
```

An account may list `models` it serves; without a list it serves any model. Core runs one
extension and spreads requests over its accounts: see [Accounts and routing](#accounts-and-routing).

An account's `settings` may set `"wire_api": "responses"` for an upstream that speaks OpenAI
Responses (`POST {base_url}/responses`), or `"wire_api": "messages"` for one that speaks Anthropic
Messages (`POST {base_url}/messages`, with the key in `x-api-key` and the client's
`anthropic-version` and `anthropic-beta` headers); the default is `"chat"`. A Responses account
serves Responses and Messages clients; a Messages account serves only Messages clients for now.

Clients send the client key as a bearer token or, as Anthropic clients do, in `x-api-key`.

`drop_policy` (top level) says what happens when translating a request to the upstream's format
would drop part of it: `"report"` (the default) sends it and logs what was dropped, `"reject"`
refuses it with a 400 that names what could not be carried.

## Accounts and routing

For each request core picks an account, sends the request to the extension with it, and reads
the outcome. A refusal the extension reports as retry-safe (the upstream certainly did not run
the request, for example a 429 or a 401) moves the request on to another account; any other
failure goes to the client. Once a reply has started, nothing is retried. Extensions never
retry on their own.

An account is eligible when it serves the model, no block covers it and it is under its
concurrency limit. Refusals block what they are about, for as long as the upstream said
(`Retry-After`) or else by backoff: a rate limit or quota on the credential or on one model,
a refused key for 30 minutes, a missing model for 12 hours, a failing endpoint with backoff up
to a minute. A success clears the blocks on its account, except blocks recorded after it
started. Among eligible accounts, core prefers the session's account (with session affinity
on), then the highest `priority`, then picks by strategy.

```json
{
  "routing": {
    "strategy": "round-robin",
    "max_attempts": 0,
    "max_accounts": 0,
    "max_wait_ms": 0,
    "session_affinity": {"ttl_ms": 3600000}
  },
  "extension": {
    "accounts": [
      {"id": "a", "priority": 1, "weight": 2, "max_concurrency": 4, "endpoint": "openai", "...": "..."}
    ]
  }
}
```

| Field | Meaning |
| --- | --- |
| `strategy` | `round-robin` (default; takes turns after the account picked last), `weighted` (by `weight`, interleaved), `fill-first` (first eligible account in order), `least-active` (fewest running requests). |
| `max_attempts` | Upstream attempts per request, counting the first. 0: no fixed limit; each account is tried once per round. |
| `max_accounts` | Distinct accounts one request may try. 0: no limit. |
| `max_wait_ms` | Longest a request waits, in total, for a blocked account. 0 (default): never; when every account is blocked the request fails at once with a 429 and `retry_after_ms`. A request never tries an account before the time the upstream gave. |
| `session_affinity` | When present, requests of one session go to the same account while it is eligible. The session comes from the `X-Claude-Code-Session-Id`, `Session-Id`, `X-Session-Id` or `X-Session-Affinity` header, or the body's `prompt_cache_key`; bindings are per client key and model. |
| account `priority` | Higher first; lower accounts serve only while every higher one is blocked. Default 0. |
| account `weight` | For `weighted`. Default 1; 0 leaves the account out. |
| account `max_concurrency` | Requests running at once on the account. 0: no limit. |
| account `endpoint` | Accounts naming the same endpoint share its outages. Default: the account's ID. |

A client can pin a request to one account with the `X-Pluxy-Account: <id>` header. A pinned
request never goes to another account.

# next: the new pluxy core

A new core for pluxy, built from scratch beside today's `core/`. Core receives client requests and
hands them to **provider extensions**: separate programs, one per kind of upstream, that core
starts and talks to over a local socket. Today it serves OpenAI Chat Completions, OpenAI
Responses, Anthropic Messages and Gemini GenerateContent (streamed and not, over HTTP) through the
OpenAI-compatible API-key provider, which talks to an upstream that speaks any of them. A request
in the upstream's own format passes through unchanged; a Responses, Messages or Gemini request to a
Chat upstream, a Messages or Gemini request to a Responses upstream, and a Gemini request to a
Messages upstream are translated, and what the translation could not carry is logged.

## Layout

| Path | What it is |
| --- | --- |
| `cmd/pluxy` | The core program. |
| `protocol/` | The extension protocol: [README](protocol/README.md), [tutorial](protocol/TUTORIAL.md), wire types and framing. |
| `llm/` | The internal model of a request and its streamed reply, shared by core and extensions. |
| `sdk/` | Go SDK for writing extensions. |
| `conformance/` | The conformance kit extensions run in their tests. |
| `format/` | Client and upstream formats as libraries extensions build in: `sse`, `openaichat`, `openairesponses`, `anthropicmessages`, `geminigenerate`. |
| `extensions/openai-compatible` | Provider extension for OpenAI-compatible APIs with an API key. |
| `examples/scripted` | The tutorial's example extension. |
| `examples/oauth` | An example provider that signs in by device code, browser (PKCE) or API key and renews its tokens, with a fake OAuth provider for tests (`oauthtest`). |
| `internal/host`, `internal/server` | Core's extension host and HTTP API (client formats and the management API). |
| `internal/cred` | Core's credential store and renewal of accounts added by login. |
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
  "management_key": "choose-a-management-key",
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
`anthropic-version` and `anthropic-beta` headers), or `"wire_api": "gemini"` for one that speaks
Gemini GenerateContent (`base_url` ending in `/v1beta`; `POST {base_url}/models/{model}:generateContent`
or `:streamGenerateContent?alt=sse`, with the key in `x-goog-api-key`); the default is `"chat"`. A
Responses account serves Responses, Messages and Gemini clients; a Messages account serves Messages
and Gemini clients; a Gemini account serves only Gemini clients for now.

Gemini clients call `POST /v1beta/models/{model}:generateContent` or `:streamGenerateContent`
(a stream is server-sent events, with or without `alt=sse`); `countTokens` is not served yet.

Clients send the client key as a bearer token or, as Anthropic clients do, in `x-api-key`; Gemini
clients may also send it in `x-goog-api-key` or the `key` query parameter.

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

An account that cannot serve a request (its upstream speaks another format and the request
cannot be translated to it, or the upstream says it does not have the model) passes the request
on to the next account within the same request.

## Accounts added by login

Besides the accounts in the config, a provider extension that signs in to accounts (OAuth in a
browser, a device code, or an API key it checks) can add accounts through the management API.
Core keeps each one in its credential store, a folder with one file per account readable only by
its owner (`credentials_dir`, default `credentials` beside the config file), and renews its
credential through the extension shortly before it expires. Only the extension that created an
account ever receives its credential. The protocol side is in the
[protocol README](protocol/README.md#login-and-refresh).

The management API is served under `/v0/management` when the config sets `management_key`;
send it as a bearer token. With a management key set, `extension.accounts` may be empty.

| Request | Does |
| --- | --- |
| `GET /accounts` | Lists every account (`source` `config` or `login`), its credential `kind`, `state` (`active`, or `reauth_required` when the provider refused the grant), `disabled`, `expires_at` and the last renewal error. Never a secret. |
| `POST /accounts` | Starts a login that adds an account: `{"id": "work", "method": "device", "input": {...}, ...}` plus any account fields from the config (`settings`, `models`, `priority`, ...). Replies 202 with the login, usually carrying its first prompt (a URL or a device code). |
| `GET /logins/{id}` | The login's state (`pending`, `done`, `failed`, `cancelled`), prompt or error. |
| `POST /logins/{id}/input` | `{"input": {...}}`: what the user entered for the prompt, such as the redirect URL of a browser login. |
| `DELETE /logins/{id}` | Cancels the login. |
| `POST /accounts/{id}/login` | Signs in to an existing account again (`{"method": ..., "input": ...}`), replacing its credential. |
| `POST /accounts/{id}/refresh` | Renews the account's credential now. |
| `PATCH /accounts/{id}` | `{"disabled": true}` or `false`. Disabled accounts get no requests. |
| `DELETE /accounts/{id}` | Removes the account and its credential. |

Accounts set in the config are listed but changed only in the config.

## Reloading config and swapping extensions

Core reloads its config file without a restart: when the file changes (it looks every second;
`-watch-interval` changes that, `0` turns it off), on `SIGHUP`, or on `POST
/v0/management/reload`. A reload reads the file, validates it, prepares what it needs and only
then switches to it in one step; requests that already started finish on the config and
extension version they started with. A file core has already seen, or an empty one (an editor
in the middle of saving), is skipped; the file watcher waits until the file has stopped changing.
A reload that fails at any step, from broken JSON to an extension that does not start, leaves
the running config in place, and management shows why. `listen` and `credentials_dir` cannot
change without a restart.

When the extension's manifest or program changes, a reload starts the new version and waits for
its handshake before switching. The old version then drains: it finishes the requests that
started on it and stops, at the latest after `drain_timeout_ms`. Routing state (blocks,
cooldowns, session bindings, the round-robin turn) carries over for every account whose ID,
extension and `settings` stay the same; an account whose `settings` change starts fresh.
Accounts added by login stay in the credential store through reloads and serve requests on every
version of the extension that logged them in.

An extension's program runs only while it is needed: core starts it at load to check it, stops
it after `idle_stop_ms` without requests, and starts it again for the next request. A program
that exits on its own is restarted after a backoff that doubles with each crash in a row; after
`quarantine_after` crashes in a row it is quarantined: requests fail with a 503 until a reload
starts it afresh.

```json
{
  "management_key": "choose-a-management-key",
  "extension": {
    "manifest": "...",
    "accounts": ["..."],
    "idle_stop_ms": 600000,
    "drain_timeout_ms": 600000,
    "restart": {"backoff_ms": 1000, "max_backoff_ms": 60000, "quarantine_after": 5, "stable_ms": 600000}
  }
}
```

| Field | Meaning |
| --- | --- |
| `management_key` | Bearer token for the management API. Without it the management API is off. |
| `idle_stop_ms` | Stop the program after this long without requests. Default 10 minutes; 0 keeps it running. |
| `drain_timeout_ms` | Longest a replaced version may keep finishing its requests. Default 10 minutes. |
| `restart.backoff_ms` | Wait before the first restart after a crash; doubles per crash in a row. Default 1 second. |
| `restart.max_backoff_ms` | Cap on that wait. Default 1 minute. |
| `restart.quarantine_after` | Crashes in a row that quarantine the extension. Default 5. |
| `restart.stable_ms` | A program that ran this long before crashing starts a new count. Default 10 minutes. |

`./pluxy -safe-mode` starts core with no extensions: client requests fail with a 503, and the
config can still be checked and reloaded through management.

### Management API

Beside the account calls [above](#accounts-added-by-login), management serves these. Every call
needs `Authorization: Bearer <management_key>`.

| Call | What it does |
| --- | --- |
| `GET /v0/management/status` | `desired` (the newest config version core has seen, with `error` when it was refused) and `active` (the one serving), each with `revision` and `hash`; `extensions`, one entry per running generation with `state` (`starting`, `active`, `idle`, `crashed`, `quarantined`, `draining`, `stopped`), `current`, `pid`, `restarts`, `last_error` and `active_requests`; `safe_mode`. |
| `POST /v0/management/reload` | Reload now. 200 with the status when the new revision is active; 422 with the status when it was refused. |

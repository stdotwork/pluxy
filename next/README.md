# next: the new pluxy core

A new core for pluxy, built from scratch beside today's `core/`. Core receives client requests and
hands them to **provider extensions**: separate programs, one per kind of upstream, that core
starts and talks to over a local socket. Today it serves OpenAI Chat Completions, OpenAI
Responses, Anthropic Messages and Gemini GenerateContent (streamed and not, over HTTP) through the
OpenAI-compatible API-key provider, which talks to an upstream that speaks any of them. A request
in the upstream's own format passes through unchanged; a Responses, Messages or Gemini request to a
Chat upstream, a Messages or Gemini request to a Responses upstream, and a Gemini request to a
Messages upstream are translated, and what the translation could not carry is logged. The
[Codex provider](#codex) serves OpenAI's Codex models from an API key or a ChatGPT subscription.

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
| `extensions/codex` | Provider extension for OpenAI's Codex models: an OpenAI API key or a ChatGPT subscription (sign-in in a browser or with a device code), with a fake OpenAI login server and Codex endpoint for tests (`codextest`). |
| `extensions/claude` | Provider extension for Anthropic's Claude, with an API key or a Claude subscription login; `claudetest` is a fake Anthropic for its tests. |
| `extensions/gemini` | Provider extension for Google's Gemini: the Gemini API with an API key, and Vertex AI with a service account or an express mode key; `geminitest` is a fake Google for its tests. |
| `extensions/kimi` | Provider extension for Kimi Code, Moonshot AI's coding subscription, signed in with a device code; `kimitest` is a fake Kimi for its tests. |
| `extensions/xai` | Provider extension for xAI's Grok models with an API key; `xaitest` is a fake xAI for its tests. |
| `extensions/meta` | Provider extension for Meta's Muse models: a Muse Code subscription signed in with a device code, or a Meta API key; `metatest` is a fake Meta for its tests. |
| `extensions/antigravity` | Provider extension for Google Antigravity: Gemini and Claude models through a Google account signed in to Antigravity; `agtest` is a fake Google login and Cloud Code for its tests. |
| `examples/scripted` | The tutorial's example extension. |
| `examples/oauth` | An example provider that signs in by device code, browser (PKCE) or API key and renews its tokens, with a fake OAuth provider for tests (`oauthtest`). |
| `internal/host`, `internal/server` | Core's extension host and HTTP API (client formats and the management API). |
| `internal/cred` | Core's credential store and renewal of accounts added by login. |
| `internal/packages` | Registries, release archives and the record of installed packages. |
| `internal/panel` | Serves the web panel at `/panel/`, embedded in core's binary. |
| `internal/pages` | Serves extension pages on a site of their own ([extension pages](ui/extension-pages/README.md)). |
| `internal/extconfig` | Extensions' settings: the schema from the manifest, checking values, and the store core keeps them in. |
| `ui/` | The UI kit the panel and the desktop app share, the panel's browser build, and the [theme contract](ui/themes/README.md) ([README](ui/README.md)). |
| `app/` | The desktop app: the kit's second build in a Tauri window, with a tray icon and three ways to reach core ([README](app/README.md)). |
| `e2e/` | Black-box tests that drive the built core over HTTP. |
| `corpus/` | Differential test corpus: cases replayed against this core and today's `core/`, with listed exceptions ([README](corpus/README.md)). |

## Run it

```bash
go generate ./internal/panel   # builds the web panel core embeds; needs Bun (skip it for a core without the panel)
go build -o pluxy ./cmd/pluxy
go build -o extensions/openai-compatible/openai-compatible ./extensions/openai-compatible
go build -o extensions/claude/claude ./extensions/claude
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

With a `management_key` set, the web panel is at `http://127.0.0.1:8400/panel/`: core's status,
accounts, extensions and themes, the pages extensions bring and their settings. Sign in with the
management key. Extension pages are served from a listener of their own, on another site than
the panel (`extension_pages` in the config); see [extension pages](ui/extension-pages/README.md).

An account may list `models` it serves; without a list it serves any model. Core runs the
extension the config names and every [installed package](#packages) that is enabled, and spreads
requests over all their accounts: see [Accounts and routing](#accounts-and-routing).

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

Responses clients may also open a WebSocket on `GET /v1/responses` ([OpenAI's WebSocket
mode](https://developers.openai.com/api/docs/guides/websocket-mode)): each `response.create`
message is a turn, answered with that turn's stream events, one message each. When the chosen
account's extension holds sessions (protocol 0.1, such as a Codex account with `"websockets":
true`), the socket's turns go to one session on that account, so a turn can continue the previous
response with `previous_response_id`, and later turns stay on that account. On any other account
each turn is a request that carries the whole conversation so far, which core keeps for the
socket. `response.append`,
`response.interrupt` and warmups (`"generate": false`) are served as today's core serves them.

Clients send the client key as a bearer token or, as Anthropic clients do, in `x-api-key`; Gemini
clients may also send it in `x-goog-api-key` or the `key` query parameter.

`drop_policy` (top level) says what happens when translating a request to the upstream's format
would drop part of it: `"report"` (the default) sends it and logs what was dropped, `"reject"`
refuses it with a 400 that names what could not be carried.

## Codex

The Codex provider extension (`extensions/codex`) serves OpenAI's Codex models from an OpenAI API
key or a ChatGPT subscription. Build it as `extensions/codex/codex` and name its manifest:

```json
{
  "management_key": "choose-a-management-key",
  "extension": {
    "manifest": "extensions/codex/pluxy-extension.json",
    "accounts": [
      {"id": "openai", "credential": {"kind": "api_key", "api_key": "sk-..."}}
    ]
  }
}
```

A subscription is added by signing in through the management API (see
[Accounts added by login](#accounts-added-by-login)), with method `browser` or `device`; `api_key`
adds an API key the same way. A browser sign-in listens for its redirect on `localhost:1455`
when that port is free on core's machine; otherwise, or when core runs elsewhere, send the
address the browser ended on as the login's `redirect_url` input. Core renews a subscription's
tokens four days before they expire (tokens that last less than that, five minutes before).

Every request goes to `{base_url}/responses` as Codex takes it: streamed, not stored, with the
reasoning's encrypted content included, sampling settings removed, and the Codex terminal
client's `User-Agent` and `originator` (turn off with `"codex_identity": false`). Chat
Completions, Messages and Gemini requests are translated to Responses. A Messages client (such as
Claude Code) gets each reasoning item's encrypted content as its thinking block's signature, and
replaying the block sends the reasoning back, so it carries over between turns. Account
`settings`, all optional:

| Field | Meaning |
| --- | --- |
| `base_url` | Default `https://api.openai.com/v1` for an API key, `https://chatgpt.com/backend-api/codex` for a subscription. |
| `codex_identity` | Default `true`: send the Codex client's `User-Agent` and `originator`. |
| `image_generation_tool` | Default `true`: offer the hosted `image_generation` tool, as today's core does (never to a free plan or a `-spark` model). |
| `callback_listen` | Default `true`: let a browser sign-in listen on `localhost:1455`. |
| `websockets` | Default `false`: send a Responses WebSocket client's turns to `{base_url}/responses` over one upstream WebSocket per client socket (Codex's WebSocket transport), as today's core does with `websockets: true`. A socket the upstream drops is not replaced in the middle of a turn: the client gets `upstream_websocket_lost` and sends the whole conversation again. |
| `auth_base_url` | OpenAI's login server; for tests. |

## Claude

The Claude extension (`extensions/claude`) talks to Anthropic's Messages API with an API key or a
Claude subscription. Point `extension.manifest` at `extensions/claude/pluxy-extension.json`. An
API-key account in the config:

```json
{"id": "anthropic", "credential": {"kind": "api_key", "api_key": "sk-ant-..."}}
```

A subscription account is added by login through the management API (below), with method
`subscription`: the reply's prompt carries a sign-in URL; after signing in, send the address the
browser was sent to (or the code Claude's page shows) as `{"input": {"redirect_url": "..."}}` to
`POST /logins/{id}/input`. Method `api_key` adds an account from a key, after checking it with
Anthropic. Core renews a subscription's token an hour before it expires.

| Setting | Meaning |
| --- | --- |
| `base_url` | Where the Messages API is. Default `https://api.anthropic.com`; requests go to `{base_url}/v1/messages`. |
| `authorize_url`, `token_url`, `client_id` | Claude's OAuth sign-in page, token endpoint and client, for a test server. Defaults are Claude's. |

A Messages request goes to Anthropic unchanged: its body, so thinking blocks, signatures and
`cache_control` markers arrive as the client wrote them, and its `anthropic-version`,
`anthropic-beta` and identification headers (`User-Agent`, `X-App`, the `X-Stainless-*` headers of
Anthropic's SDKs). An API key goes in `x-api-key`; a subscription's token goes as a bearer token,
with the `oauth-2025-04-20` beta added. Chat Completions, Responses and Gemini requests are
translated to Messages; their reasoning effort becomes adaptive thinking with that effort on
Claude 4.6 and later, and a thinking budget from Claude 3.7 to 4.5 (low 1024, medium 8192, high
24576 tokens); a model name the extension does not know as a Claude model gets no thinking
settings, and the effort is reported as dropped. The extension never changes how a client presents itself: a client that is
not Claude Code is not made to look like Claude Code.

A 429 from a subscription whose usage window is used up blocks the account until the window
resets; a 529 (overloaded) moves the request to another account.

## Gemini

The Gemini extension (`extensions/gemini`) talks to Google's Gemini models through the Gemini API
or Vertex AI. Point `extension.manifest` at `extensions/gemini/pluxy-extension.json`. A Gemini API
key:

```json
{"id": "gemini", "credential": {"kind": "api_key", "api_key": "AIza..."}}
```

A Vertex AI account has `vertex` in its settings and either a service account or an express mode
API key:

```json
{
  "id": "vertex",
  "settings": {"vertex": {"project": "my-project", "location": "us-central1"}},
  "credential": {"kind": "service_account", "data": {"client_email": "...@my-project.iam.gserviceaccount.com", "private_key": "-----BEGIN PRIVATE KEY-----\n..."}}
}
```

The service account's credential holds the secret part of the JSON key Google Cloud gives you
(`client_email`, `private_key`, and `token_uri` if the key names another token endpoint; it must
be https, or plain http only to this machine, since the signed assertion goes there). The
whole key file works too, but core removes every string of a credential from replies, so its
project ID and other plain fields would be cut out of model output; keep them in settings. The
extension signs a JWT with the key, trades it for an access token at Google's token endpoint and
keeps the token until five minutes before it expires, or until Vertex refuses it. Accounts can also be added by login
(management API, below) with method `api_key` (a Gemini API key, checked by listing a model, or a
Vertex express key) or `service_account` (the JSON key; the account's settings must name
`vertex.project`).

| Setting | Meaning |
| --- | --- |
| `base_url` | The Gemini API (default `https://generativelanguage.googleapis.com`; requests go to `{base_url}/v1beta/models/{model}:generateContent`), or for Vertex the host that replaces Google's. |
| `vertex.project` | The Google Cloud project; default the service account key's `project_id` when the credential holds the whole key. |
| `vertex.location` | Default `global` (`aiplatform.googleapis.com`); `us` and `eu` use the multi-region hosts, any other location `{location}-aiplatform.googleapis.com`. |

A Gemini request goes upstream unchanged. Chat Completions, Responses and Messages requests are
translated to GenerateContent with every safety filter off (as today's core sends them); their
reasoning effort becomes a thinking level on Gemini 3 and later and a thinking budget on Gemini 2.5
(low 1024, medium 8192, high 24576 tokens, within the model's range), with thought summaries
included. A Messages client's `max_tokens` is left to the model's own limit, since Messages makes
it mandatory. Each thought signature Gemini sends reaches a Messages client as a thinking block's
signature and a Responses client as a reasoning item's `encrypted_content`; when the client replays
it, the signature goes back on the part that follows it, so Gemini 3's function calls keep their
reasoning between turns. A function call with no signature gets the value Google documents for
calls it did not issue.

Today's core can also relay Gemini requests through a browser tab signed in to AI Studio, which
connects to core over a WebSocket. This extension does not: the extension protocol gives a
program no listener that outlives its calls, and core stops idle programs.

## Kimi

The Kimi extension (`extensions/kimi`) serves Kimi Code, Moonshot AI's coding subscription. Point
`extension.manifest` at `extensions/kimi/pluxy-extension.json`. Accounts are added by login
through the management API (below) with method `device`: the reply's prompt carries a code and
Kimi's page to enter it on, and the login finishes once it is approved there. The sign-in is the
OAuth device authorization grant (RFC 8628) at Kimi's login host, with the public client of Kimi's
own CLI, since Kimi registers no other clients. Core renews the token five minutes before it
expires. A Kimi Code API key works too, as an account in the config; it has no login method yet:

```json
{"id": "kimi", "credential": {"kind": "api_key", "api_key": "sk-kimi-..."}}
```

| Setting | Meaning |
| --- | --- |
| `domain` | `kimi.com` (default) or `kimi.ai`, the site the account belongs to. |
| `base_url` | Where Kimi Code's API is. Default `https://api.{domain}/coding`; a trailing `/v1` is taken as the same place. |
| `auth_base_url` | Kimi's login host, for a test server. Default `https://auth.{domain}`. |

Kimi Code takes Chat Completions at `{base_url}/v1/chat/completions`, Responses at
`/v1/responses` and Messages at `/v1/messages`; each client format goes to its own, and a Gemini
request is translated to Chat Completions. A Messages request goes with its body and Anthropic
headers as the client wrote them, less Claude Code's billing block in the system prompt, which Kimi
would read as prompt text. The other formats get what Kimi's API needs, as today's core sends
them: Kimi's names for its coding models (`kimi-k2.8` is `kimi-for-coding`, and other names lose
their `kimi-` prefix), a temperature only where Kimi takes it (1.0, or 0.6 with thinking off),
tool schemas with their `$ref`s inlined, usage asked for on Chat streams, the reasoning effort as
Kimi's `thinking` setting (low, high or max; none turns it off), the latest reasoning repeated on
each Chat tool call, and a Responses batch of function call outputs moved next to its calls. Each
request names pluxy in Kimi's device headers, with the device ID made at login (none for a key); the device's host
name, which Kimi's CLI also sends, is left out. A 429 that says the quota is used up is reported
as a used-up quota, not a rate limit.

## xAI

The xAI extension (`extensions/xai`) serves xAI's Grok models with an xAI API key. Point
`extension.manifest` at `extensions/xai/pluxy-extension.json`. Accounts come from the config, with
the key as the credential. The extension has no login method:

```json
{"id": "xai", "credential": {"kind": "api_key", "api_key": "xai-..."}}
```

It does not build Grok CLI's OAuth login or its `cli-chat-proxy.grok.com` endpoint. That path
reaches xAI only by sending the Grok CLI's client ID and identity headers, which this extension
does not send.

| Setting | Meaning |
| --- | --- |
| `base_url` | Where xAI's API lives. Default `https://api.x.ai/v1`; requests go to `{base_url}/responses`. |

Every client format goes to xAI's Responses API. A Responses client's request passes through with
the changes listed below. Chat Completions, Messages and Gemini clients are translated to
Responses, and what the translation cannot carry is reported with the reply. The User-Agent is
`pluxy-xai/<version>`, and no Grok CLI header is sent.

What changes in a request:

- The stream flag follows the client's: a request that is not streamed is sent to xAI with
  `"stream": false`, and its reply is read as one response.
- `stop`, `previous_response_id`, `prompt_cache_retention`, `safety_identifier` and
  `stream_options` are removed. Today's core removes them on this path too; it does not record why.
- A custom tool is sent as a function tool, keeping its other fields. A function's local `$ref`
  references are inlined and its `$defs` and `definitions` are removed. A function without
  parameters gets an empty object schema. A request with more than 200 tools keeps the first 200,
  the most xAI accepts in one request.
- A custom tool call and its output are sent as a function call and a function call output.
- `store` is false unless the client set it: xAI keeps responses by default, and with
  `previous_response_id` removed a kept response is never used again. `include` asks for
  `reasoning.encrypted_content`, which carries a reasoning model's thinking to the next turn
  without stored responses.
- The `x-grok-conv-id` header is set to the request's `prompt_cache_key`, when the request has one.

Errors: 401, and a 403 whose body says the credential is bad, are authentication errors. Other 403
replies are permission errors. A 429 that says the free usage is used up is a quota error, which
holds the account back for 24 hours. Other 429 replies are rate limits and keep xAI's `Retry-After`.
A 404 is a model error, and a 5xx is an upstream error.

Not built, and why:

- Namespace tools (from the Codex app) are sent as the client wrote them. Today's core folds them
  into functions and restores the names in the reply. That needs the reply side, which this
  extension does not have yet.
- An `apply_patch` custom tool is not wrapped in an `input` field, and its call is not restored
  on the way back. Today's core does both, so a Codex `apply_patch` tool does not work through this
  extension yet.
- Today's core sends an empty `instructions`, a string input as a list, and tool defaults
  (`tool_choice`, `parallel_tool_calls`, `strict`, `additionalProperties`) on every Responses
  request, through its Codex translator. These are not rules about xAI, so this extension does not
  send them. Whether xAI needs any of them is not verified: the tests do not reach xAI.
- A session ID for `prompt_cache_key` and `x-grok-conv-id` when the client sends none. Today's
  core derives one. This extension has no session identity to derive it from.
- Checking the reasoning effort against each model's levels, and a default effort. Both need
  today's model catalog.
- Image references, encrypted reasoning content, websockets, text to speech, image and video
  generation, `/responses/compact`, and the optional `x_search` tool.

## Meta

The Meta extension (`extensions/meta`) serves Meta's Muse models on `api.meta.ai`. Point
`extension.manifest` at `extensions/meta/pluxy-extension.json`. A Muse Code subscription is added
by login through the management API (below) with method `device`: the reply's prompt carries a
code and Meta's page to enter it on. The sign-in is the OAuth device authorization grant (RFC 8628)
at `auth.meta.com` with Muse Code's own public client, as today's core signs in, since Meta
registers no other clients. The device token it yields (`dca:...`) is traded at Meta's key
endpoint (`https://api.meta.ai/muse-code/key`) for an API key; the credential keeps the key as its
access token and the device token as its refresh token. The key does not expire on a timer: when
Meta refuses it, core renews the credential once, which makes a new key from the device token. A
Meta API key works too, by login with method `api_key` or in the config; a `dca:` token is refused
as a key:

```json
{"id": "meta", "credential": {"kind": "api_key", "api_key": "..."}}
```

| Setting | Meaning |
| --- | --- |
| `base_url` | Meta's API (requests go to `{base_url}/responses`). Default: where the key endpoint said, for a login; `https://api.meta.ai/v1` otherwise. |
| `auth_base_url` | Meta's login host, for a test server. Default `https://auth.meta.com`. |
| `mint_url` | Meta's key endpoint, for a test server. Default `https://api.meta.ai/muse-code/key`. |

Every client format goes to Meta's Responses API: a Responses body passes through, and the other
formats are translated to Responses. As today's core sends them, every request is streamed (a
client that wants one reply gets the stream gathered), `generate`, `prompt_cache_retention`,
`safety_identifier`, `stream_options` and `client_metadata` are removed, `store` is false unless
the client set it, `reasoning.encrypted_content` is asked for, a `web_search` tool loses
`search_content_types`, and a request with tools also carries Muse Code's own `muse` tool
namespace, which Meta wants on a subscription key's tool requests. Requests name Muse Code
(1.4.14) in their User-Agent and `X-Client-Id` and carry a new Muse session ID each. A 429 that
says the subscription quota is used up is reported as a used-up quota until Meta's `resets_at`;
a 404 holds the account back from the model until then, or for five minutes.

Not built, from today's core: the Codex translator's defaults it adds to every request (empty
instructions, a list input, a medium effort, `tool_choice`, `parallel_tool_calls`, `strict`
false), apply_patch rewriting for Codex Desktop, dropping encrypted reasoning Meta did not issue,
Responses compaction through Meta, and token counting.

## Antigravity

The Antigravity extension (`extensions/antigravity`) serves the Gemini and Claude models a Google
account gets in Google Antigravity, through Google's Cloud Code API. Point `extension.manifest` at
`extensions/antigravity/pluxy-extension.json`. Accounts are added by login through the management
API (below) with method `browser`: the reply's prompt carries Google's sign-in link, and the login
finishes when the browser comes back to `http://localhost:51121/oauth-callback`, which the
extension serves on this machine while the login runs. When core runs elsewhere, or the port is
taken, paste the address the browser ended on as the prompt's `redirect_url` input.

The sign-in is Google's OAuth for installed apps with the Antigravity app's own OAuth client and
scopes, as today's core signs in, plus PKCE. Google's token endpoint also takes that client's
secret, which pluxy does not ship: enter it as the login's `client_secret` input. It is the
secret of the OAuth client the Antigravity desktop app uses (today's `core/` carries it as
`ClientSecret` in `core/internal/auth/antigravity/constants.go`). The login keeps it in the
account's credential, never in settings, and renewals send it again. After signing in, the
extension asks Cloud Code for the account's Google Cloud project (`loadCodeAssist`), or has one
made on the account's default tier (`onboardUser`); the credential keeps the project too. Core
renews the token 30 minutes before it expires; a renewal also looks up a project the credential
lacks.

An account written in the config holds the same credential:

```json
{
  "id": "antigravity",
  "credential": {"kind": "oauth", "access_token": "ya29...", "refresh_token": "1//...",
    "data": {"project_id": "my-project-123", "client_secret": "..."}}
}
```

| Setting | Meaning |
| --- | --- |
| `base_url` | The Cloud Code host (default `https://daily-cloudcode-pa.googleapis.com`, the one today's core uses); the project calls go there too. |
| `user_agent` | Replaces the Antigravity app's User-Agent (`antigravity/hub/2.29.0 (aidev_client; ...)`). |
| `auth_base_url` | Moves Google's sign-in, token and user info endpoints to a test server. |
| `callback_listen` | `false` keeps the login from listening on port 51121; only a pasted address finishes it. |

A request is built as a Gemini GenerateContent body: a Gemini client's own, or a translation of
any other format with its reasoning effort as thinking settings, as the Gemini extension makes
them. It goes to `{base_url}/v1internal:generateContent` (`:streamGenerateContent?alt=sse` for a
stream) inside Cloud Code's envelope, as today's core sends it: the model, the project, request
type `agent` (`image_gen` for image models), a request ID, and a session ID hashed from the first
user text. Safety settings are left out; a Gemini model gets no output limit and has turns of
function responses sent as the model's, and a conversation that starts or ends with the model gets
an empty user turn at that end; a Claude model gets function calling in `VALIDATED` mode;
translated system instructions go as a user turn. Each function declaration's schema is sent as
`parameters`, and a response schema as `responseSchema`, in the subset Cloud Code takes, as today's
core cleans them: local `$ref`s inlined, `const` as an enum, constraints and short enums moved into
the description as hints (`minLength: 1`, `Allowed: a, b`), a type list such as
`["string","null"]` as one nullable type, and other JSON Schema keywords removed; for a Claude
model, a union becomes its strongest branch. Claude models, Gemini 3 Pro and the image model are always
asked on the stream method and gathered into one reply when the client wants one. Replies are
taken out of the envelope's `response` field. A 429 is a used-up quota when Google says
`QUOTA_EXHAUSTED` or asks for a wait of five minutes or more, and a rate limit otherwise, with the
wait from Google's retry details or message. It holds back only that model on the account, as
today's core cools only the model that answered, except a used-up credit balance
(`INSUFFICIENT_G1_CREDITS_BALANCE`), which holds back the account.

Not built, from today's core: the paid Google AI credits fallback, its own cache of Antigravity's
reasoning (thought signatures travel in band, as for Gemini), the per-account model probe
(`fetchAvailableModels`), Responses compaction through Antigravity, structured output for Claude
5.5 as prompt text, sensitive-word obfuscation (`antigravity.sensitive-words`), the connection
pool settings (`antigravity.connection-pool.*`), the app version lookup at every start, token
counting (core has no count-tokens route), and the placeholder property today's core adds to a
Claude function's objects that require nothing.

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
send it as a bearer token. With a management key set, the config may set no accounts.

| Request | Does |
| --- | --- |
| `GET /accounts` | Lists every account (`source` `config` or `login`), its credential `kind`, `state` (`active`, or `reauth_required` when the provider refused the grant), `disabled`, `expires_at` and the last renewal error. Never a secret. |
| `POST /accounts` | Starts a login that adds an account: `{"id": "work", "extension": "pluxy/codex", "method": "device", "input": {...}, ...}` plus any account fields from the config (`settings`, `models`, `priority`, ...). `extension` names the extension that signs in and owns the account; it may be left out while only one extension runs. Replies 202 with the login, usually carrying its first prompt (a URL or a device code). |
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
the running config in place, and management shows why. `listen`, `credentials_dir`,
`packages.dir` and `extension_pages.listen` cannot change without a restart.

When the extension's manifest or program changes, a reload starts the new version and waits for
its handshake before switching. The old version then drains: it finishes the requests that
started on it and stops, at the latest after `drain_timeout_ms`. A Responses WebSocket's session
lives in the old version, so it closes once its running turn ends: the client's next turn that
continues it gets `previous_response_not_found`, and one with the whole conversation opens a
session on the new version. Routing state (blocks,
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
| `GET /v0/management/ui` | The pages of the extensions that run and where they are served; see [extension pages](ui/extension-pages/README.md#how-core-serves-pages). |
| `GET`, `PATCH /v0/management/extensions/{publisher}/{name}/config` | An extension's settings, checked against the schema its manifest declares; see [extension pages](ui/extension-pages/README.md#how-core-serves-pages). |

## Packages

Extensions are installed as packages: a release archive from a registry, checked against the
digest the registry pins, unpacked and checked, then kept in its own folder and run like the
extension the config names. The package and registry formats are in the
[protocol README](protocol/README.md#release-archives). A package is **installed** (its files
are in the packages folder), **enabled** (core runs it), and **configured** when it has accounts:
in the config, or added by login. Removing a package keeps its accounts: they serve again if it
is installed again.

```json
{
  "management_key": "choose-a-management-key",
  "packages": {
    "dir": "packages",
    "registries": [{"name": "mine", "url": "https://example.com/pluxy/index.json"}]
  },
  "extensions": {
    "pluxy/codex": {
      "accounts": [{"id": "openai", "credential": {"kind": "api_key", "api_key": "sk-..."}}],
      "idle_stop_ms": 600000
    }
  }
}
```

| Field | Meaning |
| --- | --- |
| `packages.dir` | Where packages are kept, relative to the config file. Default `packages` beside it. Each release has its own folder, `{dir}/{publisher}/{name}/{version}`, and `{dir}/packages.json` records what is installed: each package's version, registry, archive URL and digest, the digest of its unpacked manifest and program, and whether it is enabled. |
| `packages.registries` | Registries to install from, searched in order and before `bundled`: `name` and `url` (an `https://` URL or a path relative to the config file). |
| `extensions` | Settings of installed packages, by package ID: `accounts`, `idle_stop_ms`, `drain_timeout_ms` and `restart`, as in `extension`. They apply while the package is enabled. |

`extension` (the extension run from a manifest path) is optional once packages are used. Account
IDs are unique across `extension` and every `extensions` entry.

An enabled package's program runs only while it is needed, as any extension does. Each time core
starts it, core checks its manifest and program still have the digest recorded at install; one
that changed is not run. An enabled package that cannot start when core starts (its files
changed, or it needs a protocol version this core does not speak) is skipped, and management
shows why; core serves with the rest. A reload that cannot prepare a package that runs now is
refused, and the running version stays. Installing the same release again replaces files that
changed. Management never shows a registry or archive URL's user name, password or query.

### Bundled packages

The packages a pluxy install ships with are a registry like any other, named `bundled`: by
default `bundled/index.json` beside the `pluxy` program, or the index `-bundled` names. On its
first start with a packages folder that has no `packages.json`, core installs and enables every
package the bundled registry lists, through the same steps as any install. Bundled packages are
removed like any other, and stay removed; installing one again takes it from `bundled`.

### Package management API

Every call needs `Authorization: Bearer <management_key>`. `{id}` is the package ID,
`publisher/name`.

| Call | What it does |
| --- | --- |
| `GET /v0/management/packages` | Installed packages: `id`, `version`, `registry`, `url`, `sha256`, `enabled`, `installed_at`, and `state`: `disabled`, the running extension's state (`active`, `idle`, `starting`, `crashed`, `quarantined`), `failed` with `error` when it is enabled but could not start, or `stopped` when it is enabled in safe mode. |
| `GET /v0/management/registries` | Each registry's `name`, `url` and what it offers: `packages`, each with its `releases` (`version`, `protocol`, `platforms`); or `error` when it could not be read. |
| `POST /v0/management/packages` | Installs a package: `{"id": "pluxy/codex", "version": "0.1.0", "registry": "mine", "enable": true}`. Only `id` is required: without `version`, the first release listed that runs here; without `registry`, the first registry listing the package. Installing another version of an installed package replaces it; when it is enabled, the new version starts before the old one drains, and if the new one fails to start the old one stays. 200 with the package; 404 when no registry has it; 422 when the archive or its manifest fails a check (nothing is installed) or it does not start (it stays installed, disabled); 502 when the registry or archive could not be fetched. |
| `POST /v0/management/packages/{id}/enable` | Starts the package and routes to its accounts. 200 with the package; 422 when it did not start (it stays disabled). |
| `POST /v0/management/packages/{id}/disable` | Stops routing to it; requests already running on it finish, up to its drain timeout. Its files stay. |
| `DELETE /v0/management/packages/{id}` | Disables the package and removes its files once its requests have finished. Its accounts stay. 204. |

Package changes go through the same reload transaction as a config change: core reads the
config file, prepares everything with the package change applied, and switches in one step, so
a config file that does not load also refuses the package change. One package change runs at a
time.

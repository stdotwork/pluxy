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
| `extensions/xai` | Provider extension for xAI's Grok models: an API key, or a Grok account signed in as the Grok CLI signs in; `xaitest` is a fake xAI and xAI sign-in for its tests. |
| `extensions/meta` | Provider extension for Meta's Muse models: a Muse Code subscription signed in with a device code, or a Meta API key; `metatest` is a fake Meta for its tests. |
| `extensions/antigravity` | Provider extension for Google Antigravity: Gemini and Claude models through a Google account signed in to Antigravity; `agtest` is a fake Google login and Cloud Code for its tests. |
| `extensions/devin` | Provider extension for Devin: the models a Devin account gets, signed in as Devin's CLI signs in or with a session token, over Devin's protobuf API; `devintest` is a fake Devin for its tests. |
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

An account may list `models` it serves; without a list it serves the models its extension lists
for it, as today's core serves an account the models of its provider's catalog, and any model
when the extension has no list for it (the OpenAI-compatible extension has none). Accounts can rename,
exclude and prefix models, and clients list the models on `GET /v1/models`: see
[Model lists and model names](#model-lists-and-model-names). Core runs the
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

A Chat or Responses account sends a reasoning effort the upstream may not take as the nearest one
it does, as today's core does: `"reasoning_levels"` in its `settings` lists the efforts the
upstream accepts (`none`, `minimal`, `low`, `medium`, `high`, `xhigh`, `max`), by default `low`,
`medium` and `high`, so `xhigh` and `max` go as `high`, `minimal` and `none` as `low`, and `auto`
as `medium`. Today's core refuses `minimal` with a 400 instead.

Gemini clients call `POST /v1beta/models/{model}:generateContent` or `:streamGenerateContent`
(a stream is server-sent events, with or without `alt=sse`), and `:countTokens`.

Token counts (Anthropic's `POST /v1/messages/count_tokens` and Gemini's `:countTokens`) come from
the upstream's own count endpoint: Anthropic's for a Claude account or an OpenAI-compatible account
with `"wire_api": "messages"`, Gemini's for a Gemini account (Gemini API or Vertex AI) or one with
`"wire_api": "gemini"`. A count from an upstream of the client's own kind comes back as the upstream sent it;
otherwise core answers with the count in the client's shape. An account whose upstream has no count
endpoint (Codex, xAI, Kimi, Meta, Devin, Antigravity, and Chat or Responses accounts) cannot count:
core tries the model's next account, and answers with an error when none can. Today's core
estimates a count itself with a tokenizer for those, and for any Claude account whose base URL is
not Anthropic's own; this core does not, as that needs a tokenizer and its vocabularies for a
number that is only approximate.

A model name may end in a thinking setting in brackets, as today's core reads it: a level
(`gpt-5(high)`; `minimal`, `low`, `medium`, `high`, `xhigh` or `max`), `none`, `auto`, or a token
budget (`claude-sonnet-4-5(16000)`; `0` is none, `-1` auto). Core takes it off the name before
routing, so the request goes to the model's own accounts, and the account's extension applies it to
the upstream request in place of any reasoning or thinking settings the request has: as a
reasoning effort (a budget becomes the level it falls in: up to 512 tokens minimal, 1024 low, 8192
medium, 24576 high, more xhigh), or for Claude and Gemini models that take a budget, as that
budget. Bracketed text that is none of these stays part of the model name; today's core refuses it.

Responses clients may also open a WebSocket on `GET /v1/responses` ([OpenAI's WebSocket
mode](https://developers.openai.com/api/docs/guides/websocket-mode)): each `response.create`
message is a turn, answered with that turn's stream events, one message each. When the chosen
account's extension holds sessions (protocol 0.1, such as a Codex account with `"websockets":
true`), the socket's turns go to one session on that account, so a turn can continue the previous
response with `previous_response_id`, and later turns stay on that account. On any other account
each turn is a request that carries the whole conversation so far, which core keeps for the
socket. `response.append`,
`response.interrupt` and warmups (`"generate": false`) are served as today's core serves them.

When an upstream ends a Responses stream with a `response.completed`, `response.incomplete` or
`response.failed` whose `output` is empty, core fills it with the items the stream's
`response.output_item.done` events finished, as today's core does.

A Messages client's usage carries `output_tokens_details.thinking_tokens` (at most `output_tokens`)
whenever the upstream reports its reasoning tokens; today's core writes it only for a Responses
upstream.

OpenAI's legacy text completions (`POST /v1/completions`) are served as Chat Completions, as
today's core serves them: the prompt (a string, or a list of one string) becomes one user message,
with `max_tokens`, `temperature`, `top_p`, `frequency_penalty`, `presence_penalty`, `stop`,
`stream`, `logprobs`, `top_logprobs` and `echo`, and the reply comes back as `text_completion`
choices with `text`. Several prompts in one request are refused; `n`, `suffix`, `best_of` and the
rest are left out.

Clients send the client key as a bearer token or, as Anthropic clients do, in `x-api-key`; Gemini
clients may also send it in `x-goog-api-key` or the `key` query parameter.

Web pages may call the client API from a browser only from the origins listed in
`cors.allowed_origins` (each `scheme://host[:port]`, or `"*"` for every origin): core then adds
the CORS headers to their replies and answers their preflight `OPTIONS` requests with 204 before
checking the client key. The list is empty by default, so no page can. Today's core allows every
origin on every route; here CORS covers the client API only, never the panel or the management API.

```json
{"cors": {"allowed_origins": ["http://localhost:5173"]}}
```

A reply to a request that went to an account carries `X-CPA-TRACE-ID`, as today's core sends it:
the time and a random part, such as `20261109061500-3f9a0c2e5b7d4e18`. Core's log lines about the
request (a failure, what translation lost) carry the same ID and the account, so a reply can be
matched with them. A request refused before that (no client key, a body that does not parse, no
account for the model) gets none.

`drop_policy` (top level) says what happens when translating a request to the upstream's format
would drop part of it: `"report"` (the default) sends it and logs what was dropped, `"reject"`
refuses it with a 400 that names what could not be carried.

A config written for today's core, with its key names, works too: see
[config.yaml with today's key names](#configyaml-with-todays-key-names).

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

**Quota.** For a ChatGPT sign-in account, `GET /v0/management/accounts/{id}/quota` reads `GET {backend}/wham/usage`, where `{backend}` is the account's `base_url` without a trailing `/codex` (default `https://chatgpt.com/backend-api`), with the access token, the `Chatgpt-Account-Id` header and the same `User-Agent` and `Originator` the account's requests carry (so `codex_identity` applies). It reports the plan (`plan_type`), the 5-hour and weekly or monthly windows (`five_hour`, `weekly`, `monthly`), code review's windows (`code_review_*`), each extra limit such as one model's (`<name>_five_hour` and so on) and the credit balance (`credits`), with the share used, reset time and window length. An API key answers unsupported. A sign-in's grant carries `identity` `{"email", "account_id"}`: members of one ChatGPT workspace share its account ID, so the e-mail address tells them apart.

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
| `client_id` | Claude's OAuth client. Default is Claude's. |

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

**Quota.** For a subscription account, quota reads `GET {base_url}/api/oauth/usage` and `GET {base_url}/api/oauth/profile` (default base `https://api.anthropic.com`) with the access token, the OAuth beta in `anthropic-beta` and `anthropic-version`. Like the account's requests, it presents no client identity of its own. It reports the 5-hour window (`five_hour`), the weekly windows (`seven_day`, `seven_day_oauth_apps`, `seven_day_opus`, `seven_day_sonnet`, `seven_day_cowork`, the Fable window), any window counted in dollars, and extra usage (`extra_usage`), with the share used and reset time; the plan (Free, Pro, Max, Max 5x, Max 20x or Team) comes from the profile, and usage is still reported when the profile cannot be read. An API key answers unsupported. A subscription login's grant carries `identity` `{"email", "account_id", "organization_id"}` from the token reply, the fields today's core compares on a re-login.

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

**Quota and identity.** Gemini has no usage endpoint, so quota answers unsupported. A `service_account` login's grant carries `identity` `{"client_email"}`, the key's service account; an API key login has none.

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

**Quota.** For a device login, quota reads `GET {base_url}/v1/usages` and the plan from `GET {base_url}/v1/me` (`user_level_name`) on the account's Kimi Code host, with the access token and the same device headers (`X-Msh-*`, `User-Agent`) the account's requests carry. It reports each entry of `limits` (labelled by its name, else by its window length, else "Limit #n"), the usage summary ("Weekly limit") and the monthly ratios ("Monthly limit", "Monthly coding limit"). An API key answers unsupported. Kimi's device login returns no user ID, so its grant has no identity and a re-login is not checked, as in today's core.

## xAI

The xAI extension (`extensions/xai`) serves xAI's Grok models with an xAI API key or a Grok
account. Point `extension.manifest` at `extensions/xai/pluxy-extension.json`. An API key comes from
the config, with the key as the credential:

```json
{"id": "xai", "credential": {"kind": "api_key", "api_key": "xai-..."}}
```

A Grok account is added by login through the management API (below) with method `device`, as the
Grok CLI and today's core sign in: the OAuth device authorization grant with the Grok CLI's own
public client (it has no secret), at the endpoints xAI's OpenID discovery document
(`https://auth.x.ai`) names, which must be HTTPS on `x.ai`. The reply's prompt carries a code and
xAI's page to enter it on. The credential keeps the access and refresh tokens and the token
endpoint, and the account is labelled with the ID token's e-mail address. Core renews the token 5
minutes before it expires; a renewal keeps the refresh token when xAI sends no new one, and an
`invalid_grant` or revoked token asks for a new login.

A Grok login's requests go to the Grok CLI's chat endpoint, `https://cli-chat-proxy.grok.com/v1`,
unless `base_url` names another address than xAI's API, as today's core routes them, and carry the
Grok CLI's identity, which that endpoint checks: `User-Agent: xai-grok-workspace/<version>`,
`X-XAI-Token-Auth: xai-grok-cli`, `x-grok-client-version`, `x-grok-client-identifier: grok-shell`
and `x-authenticateresponse: authenticate-response`. The version is today's core's fallback,
1.0.46. Today's core sends these headers only to the exact CLI endpoint address; the extension
sends them on every request of a login.

| Setting | Meaning |
| --- | --- |
| `base_url` | Where xAI's API lives. Default `https://api.x.ai/v1`; requests go to `{base_url}/responses`. For a Grok login, the default means the Grok CLI's endpoint. |
| `client_version` | Replaces the Grok CLI version a login's requests name (default 1.0.46). |

Every client format goes to xAI's Responses API. A Responses client's request passes through with
the changes listed below. Chat Completions, Messages and Gemini clients are translated to
Responses, and what the translation cannot carry is reported with the reply. An API key's
User-Agent is `pluxy-xai/<version>`, with no Grok CLI header.

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
- For a Grok login: reading the Grok CLI's latest version from npm every 3 hours (set
  `client_version` when xAI asks for a newer CLI), the file name and legacy-file adoption today's
  core uses for its auth files, and opening the browser at sign-in.

**Quota.** A Grok login's quota is what the Grok CLI shows: `GET {cli}/billing?format=credits` and `GET {cli}/billing` on the CLI's chat endpoint (the host the account's requests go to), and `GET {cli}/user?include=subscription` and `GET {cli}/settings` for the plan's name, with the login's token and the Grok CLI identity headers the requests carry. It reports the weekly credit window (`weekly`), the monthly credits (`monthly_credits`), the pay-as-you-go cap and use (`pay_as_you_go`) and the prepaid balance (`prepaid`), in `usd`. Nothing that spends from the account is sent (today's panel sent a one-token chat for paid accounts; that is not rebuilt). An API key answers unsupported. A Grok login's grant carries `identity` `{"sub", "email"}` from its ID token.

## Meta

The Meta extension (`extensions/meta`) serves Meta's Muse models on `api.meta.ai`. Point
`extension.manifest` at `extensions/meta/pluxy-extension.json`. A Muse Code subscription is added
by login through the management API (below) with method `device`: the reply's prompt carries a
code and Meta's page to enter it on. The sign-in is the OAuth device authorization grant (RFC 8628)
at `auth.meta.com` with Muse Code's own public client, as today's core signs in, since Meta
registers no other clients. The device token it yields (`dca:...`) is traded at Meta's key
endpoint (`https://api.meta.ai/muse-code/key`) for an API key; the credential keeps the key as its
access token and the device token as its refresh token. A token poll that gets an answer with no
OAuth error (an outage) is tried again while the code is good. The key does not expire on a timer: when
Meta refuses it, core renews the credential once, which makes a new key from the device token. A
Meta API key works too, by login with method `api_key` or in the config; a `dca:` token is refused
as a key:

```json
{"id": "meta", "credential": {"kind": "api_key", "api_key": "..."}}
```

| Setting | Meaning |
| --- | --- |
| `base_url` | Meta's API (requests go to `{base_url}/responses`). Default: where the key endpoint said, for a login; `https://api.meta.ai/v1` otherwise. |

Every client format goes to Meta's Responses API: a Responses body passes through, and the other
formats are translated to Responses. As today's core sends them, every request is streamed (a
client that wants one reply gets the stream gathered), `generate`, `prompt_cache_retention`,
`safety_identifier`, `stream_options` and `client_metadata` are removed, `store` is false unless
the client set it, `reasoning.encrypted_content` is asked for, a `web_search` tool loses
`search_content_types`, and a request with tools also carries Muse Code's own `muse` tool
namespace, which Meta wants on a subscription key's tool requests. Requests name Muse Code
(1.4.14) in their User-Agent and `X-Client-Id` and carry a new Muse session ID each. A 429 that
says the subscription quota is used up is reported as a used-up quota until Meta's `resets_at`;
a 404 holds the account back from the model until then, or for five minutes. A failure in the
stream (an `error` event or `response.failed`) is classified by the status it names, a number or a
string, and is not retry-safe once the reply began. A renewal keeps the API address the credential
holds when Meta's key endpoint names none, and a rate-limited renewal keeps Meta's `Retry-After`.

Not built, from today's core: the Codex translator's defaults it adds to every request (empty
instructions, a list input, a medium effort, `tool_choice`, `parallel_tool_calls`, `strict`
false), apply_patch rewriting for Codex Desktop, dropping encrypted reasoning Meta did not issue,
Responses compaction through Meta, and token counting.

**Quota.** Meta reports a Muse Code login's usage only from its key endpoint, so quota sends `POST https://api.meta.ai/muse-code/key` with an empty object, the device token, `x-api-version: 1.0.0` and Muse Code's client identity headers. The device token renews the account, so this goes only where a renewal goes, never to `base_url`. The call makes a new API key, which is dropped and never returned or stored; the account's own key is left alone, and if Meta retires it, core renews the credential on the next refused request (whether Meta does is not known). It reports `window` ("Usage limit", with its length) and `weekly` ("Weekly limit") from `subs_usage`, and the plan from `subs_tier_name`. An API key answers unsupported. A device login's grant carries `identity` `{"email"}`.

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

**Quota.** Quota reads `POST {base_url}/v1internal:retrieveUserQuotaSummary` with the credential's project, and the plan from `POST {base_url}/v1internal:loadCodeAssist`, with the access token and the account's Antigravity user agent. Each bucket of each model group becomes a limit, such as "Gemini models 5-hour limit", with the share used, the reset time, the window length where known and the group's models. A login's grant carries `identity` `{"email"}`.

## Devin

The Devin extension (`extensions/devin`) serves the models a Devin account gets (SWE-2, Claude,
GPT, Gemini and others), through Devin's API server, as Devin's CLI talks to it. Point
`extension.manifest` at `extensions/devin/pluxy-extension.json`. Accounts are added by login
through the management API (below):

- `browser`: Devin's sign-in, as its CLI and today's core sign in. The prompt carries the link to
  `app.devin.ai`; the browser comes back to a loopback address the extension serves on this
  machine while the login runs, or the user pastes the code Devin's page shows (or the address
  the browser ended on) as the prompt's `code` input. The code and a PKCE verifier are traded at
  `api.devin.ai` for a session token. Devin's login has no client ID or secret.
- `token`: a session token (`devin-session-token$...`) the user already has, entered as the
  `session_token` input and checked against Devin's profile endpoint.

Either way the credential is the session token, kept as an API key: it does not expire and has no
renewal, so when Devin refuses it, the user signs in again. The account is labelled with its Devin
user name. An account written in the config holds the token the same way:

```json
{"id": "devin", "credential": {"kind": "api_key", "api_key": "devin-session-token$..."}}
```

| Setting | Meaning |
| --- | --- |
| `base_url` | Devin's API server (default `https://server.codeium.com`, as today's core uses). |
| `device_seed` | Makes the device fingerprint every request carries the same each time, as today's core's `device_seed`; without it the fingerprint is random per request. |
| `callback_listen` | `false` keeps the login from listening for the browser; the user pastes the code. |

Every client format is sent as Devin's chat request, a protobuf message over Connect, to
`{base_url}/exa.api_server_pb.ApiServerService/GetChatMessage`, built as today's core builds it:
the CLI's client metadata with the token and a device fingerprint, the system prompt, the turns
(user, assistant with its reasoning, signature and tool calls, and tool results; a result for no
call goes as the user's text; images as base64 data, named in the user's text as Devin's CLI names
pasted images), the completion settings (the output limit, at most 64000; the temperature, 1 by
default), the function tools with their JSON Schema, a new session, and the model ID. The headers
are the CLI's: the token twice as Basic credentials, Connect's version, a Sentry trace, no
User-Agent and no compression. What Devin has no place for is reported as dropped (and refused
under `drop_policy` `reject`): a client's fields kept beside the model (such as a Chat message's
`name` or `seed`), parallel tool calls turned off, strict tool arguments, and parts other than
text and base64 images. A tool result given as content blocks keeps its text. A user turn left
with nothing once media Devin cannot take is dropped is refused, as today's core refuses it.

The reply's frames become the reply's events as they arrive: reasoning, text, tool calls and
usage. Reasoning stays open until the reply ends or new reasoning follows other output, so
Devin's signature, which may come after the answer began, reaches it as the reasoning's opaque
data; a signature with no reasoning text gets a reasoning item of its own. Each tool call is
found by its ID, so a call can continue after another one started, and starts once its name is
known. Arguments Devin could not make JSON (its `invalid_json_str`) are passed on as text. A
character split across frames is passed on whole.

Devin names a model and its reasoning effort as one model ID (`claude-opus-5-5-medium`). A client
names the model as today's core lists it, `devin/claude-opus-5-5` (or without the `devin/`
prefix), and the extension adds the request's reasoning effort, or the model's default, moved to
the nearest effort the model has, as today's core does; a name that already ends in an effort
(`claude-opus-5-5-high`) is sent as it is. The efforts of each model come from today's core's
Devin catalog (`models.go`); a model not in it is sent as named, with the request's effort when it
has one. A few models name their efforts as variants, as in today's core: SWE-1.7 is sent bare
unless `medium` is asked for, SWE-1.6 bare unless `fast` is, GLM-5.2 bare but for `none` and `max`
(`glm-5-2-max-1m` for the long-context model), and the Claude 4.6 models as `-thinking` with any
effort but `none`.

An error in Devin's end frame is classified by its Connect code, as today's core maps it. A 429
(or `resource_exhausted`, a demand refusal or a credit limit) holds back only that model on the
account, unless Devin says the account's daily or weekly quota is exhausted, which holds back the
account. A monthly or unnamed quota holds back only the model. Devin's `canceled` is a cancelled
request (499), as in today's core. An error after Devin sent any frame (output, usage or a
signature) is not retry-safe, and a reply that ends without its end frame is an error.

Not built, from today's core: removing Claude Code's and Codex's attribution lines from the system
prompt and `devin.sensitive-words`; rewriting tool descriptions; encoding tool call IDs (they pass
through as given); a session's turn count (each request is a new session, as today's core sends
for a client that names none); the account's plan and quota status (`GetUserStatus`) and a used-up
quota's reset time; the remote model catalog refresh; Devin's names for older models
(`claude-haiku-4-5`, `gpt-4-1`, Sonnet 4.5, Claude 4.5 Opus) and the `-fast` variants built from a
`<effort>-fast` request; the usage fallback from response dimensions; images in tool results;
signature types other than `sealed`; namespace tools and custom (freeform) tools such as
`apply_patch`, which are reported as dropped, and today's core's checks and recovery of a malformed
`apply_patch` call; and token counting (core has no count-tokens route). A replayed reasoning
signature is sent as given, whoever issued it.

Gaps outside the extension: a Messages or Gemini client gets Devin's reasoning without its
signature, because those renderers pass on only Responses and Gemini signatures, so such a client
cannot replay it. A Gemini client's `thoughtSignature` on a function call is reported as dropped;
today's core sends it to Devin as a sealed signature.

**Quota.** Quota calls Devin's `GetUserStatus` on the API host (`{base_url}/exa.seat_management_pb.SeatManagementService/GetUserStatus`, Connect with a protobuf body), as today's core does, with the session token and the client identity chat requests carry. It reports "Daily limit" and "Weekly limit" (share used, reset time) and the plan name. A login's grant carries `identity` `{"user_id"}` from `/v3/self`; a re-login whose `/v3/self` call fails has no identity and is refused for an account that stored one.

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
| `max_wait_ms` | Longest a request waits, in total, for a blocked account; with `request_retry`, before each round. 0 (default): never; when every account is blocked the request fails at once with a 429 and `retry_after_ms`. A request never tries an account before the time the upstream gave. |
| `request_retry` | When set, rounds run as today's `request-retry` runs them: at most this many after the first, each trying every eligible account again. A round starts at once when no account is blocked, or else when the first block ends if that is within `max_wait_ms`; an account refused for rate or quota in the request waits at least 10 seconds; a refused key (401) or a missing model (404) starts none. Unset: rounds are bounded by `max_attempts` and `max_wait_ms`, and each needs a wait. |
| `disable_cooling` | `true`: failures leave no block, so every account stays eligible. |
| `transient_error_cooldown_ms` | How long a failing upstream or endpoint (a 5xx or a broken connection) is blocked when it gave no `Retry-After`: 0 (default) backs off from a second to a minute, above 0 that long, below 0 not at all, `Retry-After` or not. |
| `session_affinity` | When present, requests of one session go to the same account while it is eligible. The session comes from the `X-Claude-Code-Session-Id`, `Session-Id`, `X-Session-Id` or `X-Session-Affinity` header, or the body's `prompt_cache_key`; bindings are per client key and model. |
| account `priority` | Higher first; lower accounts serve only while every higher one is blocked. Default 0. |
| account `weight` | For `weighted`. Default 1; 0 leaves the account out. |
| account `disable_cooling` | Overrides `disable_cooling` for the account. |
| account `max_concurrency` | Requests running at once on the account. 0: no limit. |
| account `endpoint` | Accounts naming the same endpoint share its outages. Default: the account's ID. |

A client can pin a request to one account with the `X-Pluxy-Account: <id>` header. A pinned
request never goes to another account.

### Account checks

When core starts, and when a reload adds or changes an account (its settings, its credential or
its extension) or a login adds one, core checks the account: it asks the account's extension
whether the account works, and the extension makes one call upstream that costs the account
nothing, such as its model list. Nothing is generated and the credential is not renewed for it.
Up to 4 checks run at once, each for at most 30 seconds, in the background: core serves requests
while they run, and the result does not change routing. The management API's `GET /accounts`
shows each account's last `check`:

| `check.state` | Meaning |
| --- | --- |
| `checking` | The check is running. |
| `ok` | The account works; `detail` says what was checked. |
| `failed` | It does not; `error` is why (code `auth` for a refused key, `unavailable` for an upstream that cannot be reached). |
| `not_checked` | The extension cannot check it without a generation, or has no check; `detail` says which. |

Core also logs each result (`account "work": check passed: ...` or `check failed: ...`). Each
extension checks with its upstream's model list, and only accounts with an API key:

| Extension | Check |
| --- | --- |
| `pluxy/openai-compatible` | `GET {base_url}/models`; an upstream without that list (404) leaves the account not checked. |
| `pluxy/claude` | `GET {base_url}/v1/models`. A subscription login is not checked. |
| `pluxy/gemini` | `GET {base_url}/v1beta/models`. A Vertex account is not checked. |
| `pluxy/xai` | `GET {base_url}/models`. A Grok login is not checked. |

The other bundled extensions do not check accounts yet. The protocol side is the
[`check` operation](protocol/README.md#check).

An account that cannot serve a request (its upstream speaks another format and the request
cannot be translated to it, or the upstream says it does not have the model) passes the request
on to the next account within the same request.

## Model lists and model names

Clients list the models they can ask for in their own API's shape, with the client key as for
any request:

| Request | Reply |
| --- | --- |
| `GET /v1/models` | OpenAI's list: `{"object": "list", "data": [{"id", "object", "created", "owned_by"}]}`. A request with an `anthropic-version` header or Claude Code's user agent (`claude-cli/...`) gets Anthropic's list instead: each model's `display_name`, `created_at`, `max_input_tokens` and `max_tokens` (200000 and 64000 when not known), sorted by display name, with `has_more`, `first_id` and `last_id`. |
| `GET /v1/models/{id}` | One model from that list, or a 404 with code `model_not_found`. |
| `GET /v1beta/models` | Gemini's list, `{"models": [...]}`: each model's `name` (`models/{id}`), `displayName`, `description`, token limits and `supportedGenerationMethods` (`["generateContent"]` when not known). |
| `GET /v1beta/models/{name}` | One model, named with or without `models/`, or a 404. |

A list holds each model the accounts serve, once under each name a client can ask for it by,
sorted by name. An account whose config lists `models` lists those. An account without `models`
lists the models its extension says the account serves (the protocol's `models` operation, from
version 0.3), under the account's naming rules below; an extension that cannot say adds nothing
for it. The same list decides which models such an account serves: a request for a model no
account serves is refused without calling an upstream, as today's core refuses it (404
`model_not_found`; today's core answers 400). An account whose extension has no list, an empty
one, or one it could not read within 15 seconds serves any model, and core asks again a minute
later. Core asks an
extension once per account and keeps the answer until the accounts change: a reload, or an
account added, removed or signed in again.

An account names its models with these fields, applied in this order:

```json
{
  "routing": {"force_model_prefix": false},
  "extension": {
    "accounts": [
      {
        "id": "team",
        "models": ["gpt-6.1-sol", {"name": "gpt-6-astra-2026-10", "alias": "gpt-6-astra"}],
        "excluded_models": ["*-mini"],
        "model_alias": [{"name": "gpt-6-luna", "alias": "luna", "fork": true, "display_name": "Luna"}],
        "prefix": "team",
        "...": "..."
      }
    ]
  }
}
```

| Field | Meaning |
| --- | --- |
| account `models` | The only models the account serves. Each is the upstream's name, or `{"name": ..., "alias": ...}` to serve the model `name` under the name `alias` only. |
| account `excluded_models` | Models the account does not serve: names, or patterns with `*` for any text (`gpt-5-*`, `*-mini`, `*preview*`), whatever their case. They match the name clients use for an entry of `models` (its `alias`, when it has one, as in today's core), and for any other model its name before `model_alias` renames it. |
| account `model_alias` | Renames: a request for `alias` (exactly as written) goes to the model `name` (any case). Without `fork` the model is served only as `alias`; with `"fork": true` under both names. `display_name` replaces the model's display name in lists. |
| account `prefix` | The account also serves each model as `{prefix}/{model}`, so a client can ask for a model on that account alone. No `/` in it. |
| `routing.force_model_prefix` | `true`: an account with a prefix serves its models only with the prefix, except a model whose name is the prefix itself. |

A request for a renamed or prefixed model reaches the upstream under the upstream's name, and
blocks on a model are kept under that name, so asking for the same model by another name does not
get around a block.

An extension's settings (`extension`, or its entry in `extensions`) may also hold `model_alias`
and `excluded_models` for the accounts added to it by a sign-in (not those added with an API
key, as today's `oauth-model-alias` and `oauth-excluded-models` skip API keys). Its `model_alias` rules come after
an account's own, leaving out a rule for an alias the account already has; its
`excluded_models` apply to an account that has none of its own.

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
| `GET /accounts` | Lists every account (`source` `config` or `login`), its credential `kind`, `state` (`active`, or `reauth_required` when the provider refused the grant), `disabled`, `expires_at`, the last renewal error and the last [account check](#account-checks); its routing fields (`priority`, `weight`, `disable_cooling`, `max_concurrency`, `prefix`, `models`, `excluded_models`, `model_alias`, `endpoint`); and what routing knows about it now: `cooldowns` (each live block's `scope`, `credential`, `model` with its `model`, or `endpoint`; `kind`, `rate_limit`, `quota` or `cooldown` for failures without a reset time; `until`; `backoff_level`; and the `error` that set it), `cooldown_until` (when the latest block on the whole account ends; model blocks do not count), `last_error` and `last_error_at` (the latest failure that set a block, until a request on the account that started after it succeeds; like a cooldown, it is not cleared by a request that started earlier) and `active_requests`. An account that gets no requests (disabled, or waiting for a login) has no routing state. Never a secret. |
| `POST /accounts` | Starts a login that adds an account: `{"id": "work", "extension": "pluxy/codex", "method": "device", "input": {...}, ...}` plus any account fields from the config (`settings`, `models`, `priority`, ...). `extension` names the extension that signs in and owns the account; it may be left out while only one extension runs. Replies 202 with the login, usually carrying its first prompt (a URL or a device code). |
| `GET /logins/{id}` | The login's state (`pending`, `done`, `failed`, `cancelled`), prompt or error. |
| `POST /logins/{id}/input` | `{"input": {...}}`: what the user entered for the prompt, such as the redirect URL of a browser login. |
| `DELETE /logins/{id}` | Cancels the login. |
| `POST /accounts/{id}/login` | Signs in to an existing account again (`{"method": ..., "input": ...}`), replacing its credential. The login must sign in to the same upstream account: when the provider reports who signed in (the grant's [`identity`](protocol/README.md#login), such as an e-mail address and an account ID), every field core stored for the account must match, letter case aside; an account stored without one is checked by its `label`. Otherwise the login ends `failed` with code `invalid_request` and the account keeps its credential, as today's core refuses with "Authenticated account does not match target credential". To use the other account, add it as a new one. |
| `POST /accounts/{id}/refresh` | Renews the account's credential now. |
| `PATCH /accounts/{id}` | `disabled` (`true` or `false`; disabled accounts get no requests) and any of the routing fields `priority`, `weight`, `disable_cooling`, `max_concurrency`, `prefix`, `models`, `excluded_models` and `model_alias`, checked as the config's are; `null` sets one back to its default. A bad field changes nothing (400). Replies with the account. |
| `POST /accounts/{id}/cooldown/reset` | Clears the account's cooldowns, on its credential, its models and its endpoint (which accounts naming the same endpoint share), with their backoff steps and its last error, so it serves again at once. Works for config accounts too. Replies with `cleared`, the number of live blocks it cleared, and the account. |
| `GET /accounts/{id}/models` | The models the account serves: its config's `models` when it lists them, else the ones its extension lists for it (the protocol's [`models`](protocol/README.md#models)). Each has its upstream `id`, what the extension says about it, `names` (the names clients use for it after the account's exclusions, aliases and prefix, and its extension's defaults for logins) and `excluded` when `excluded_models` leaves it out. |
| `POST /accounts/{id}/test` | Sends the account one small request (`Hi`, at most 16 tokens of reply), straight to its extension: its cooldowns are ignored and its routing state is left as it is. `{"model": ...}` names the model by its upstream name; without it, the first the account serves. Replies 200 with `result`: `ok`, `model`, `served_model`, `latency_ms`, the start of the `reply`, `finish_reason` and `usage`, or `error`. The request spends what a request that size costs. |
| `GET /accounts/{id}/quota` | The account's plan and usage, read from its provider when asked (the protocol's [`quota`](protocol/README.md#quota)): `quota` with `plan`, `limits` (each with `id`, `label`, and what the provider gives of `used_percent`, `used`, `limit`, `remaining`, `unit`, `unlimited`, `resets_at`, `window_seconds`, `models`) and `raw`, the provider's replies; and `fetched_at`. 422 with code `unsupported` for an account with no plan to report, such as an API key, or whose extension reads no quota; 502 with the provider's error code when the provider refused or failed. Each provider's section says what it reports. |
| `GET /login-methods` | For each running extension that signs in, its `extension` ID and `methods`: each `method`, `kind` (`browser`, `device`, `api_key`), `label` and the `inputs` it asks for (`name`, `label`, `secret`, `optional`), as its describe reports them; what a panel needs to offer "add account". |
| `DELETE /accounts/{id}` | Removes the account and its credential. |

Accounts set in the config are listed but changed only in the config; their cooldowns can be
reset and they can be tested and their models listed like any other.

A login's codes and a renewal's refresh token go only to the provider's own login hosts, over
HTTPS. No account setting moves them; `base_url`, where requests go, is a setting. Tests that
sign in to a fake provider start core with `PLUXY_TEST_LOGIN_HOSTS=1` in its environment, which
core passes on to its extensions and which makes them take the fake's address from the account's
settings (`auth_base_url` and the like) instead of ignoring it. Never set it in a real run.

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

### Changing the config through management

These calls read and change the config file core runs on, whether it is the JSON config or a
`config.yaml` with today's key names. A change goes through the same steps as a reload: core
checks the new contents and prepares them, and only when they are ready to serve does it write
the file and switch to them. The file is replaced in one step (a new file renamed over it, with
the same permissions, through a symbolic link if the path is one), so nothing ever reads half of
it. When a change is refused, the file and the running config stay as they were, and status
keeps showing the file as it is. When another program writes the file while a change is being
prepared, the change is dropped with a 409, so that write is not lost. Today's core writes its file in place and saves a change that
its reload may then refuse.

A change made with the structured calls (client keys, management key, settings) writes the file
out again. A `config.yaml` keeps its comments and the order of its keys, with two-space indents
and no blank lines, as today's core writes it when it saves; a JSON config keeps the order of
its keys and its indent. `PUT /config` writes the document as sent.

| Call | What it does |
| --- | --- |
| `GET /v0/management/config` | The config file: `format` (`json` or `yaml`), `hash` (as in status) and `content`, the file as written with each secret replaced by a placeholder such as `pluxy-redacted-4f1c...`. Secrets are found by key name, in both layouts of a `config.yaml` and in the JSON config: client keys, the management key, every `api-key` and `api_key`, everything in an account's `credential` except its `kind`, passwords, tokens and secrets, header values about authentication (`Authorization`, `X-Api-Key`, cookies), and any URL with a user name, password or query. Comments are shown as written. 422 when the file does not parse. Today's core returns its config, the management key's hash and provider keys included, as they are. |
| `POST /v0/management/config/validate` | `{"content": "..."}`: checks a document as a reload would, without starting extensions or writing anything. 200 with `valid: true` and the `notices` a `config.yaml` load logs, or `valid: false` and `error`. |
| `PUT /v0/management/config` | `{"content": "...", "base_hash": "..."}`: replaces the file with the document and applies it. 200 with the file as `GET /config` shows it; 409 when `base_hash` is set and the file has changed since; 422 with `error` when core refuses the document. |
| `GET /v0/management/client-keys` | The client keys: each one's `id` and `masked` form (first and last two characters, as today's panel shows them; a key shorter than 8 characters is hidden whole). |
| `POST /v0/management/client-keys` | Adds a new random client key (`sk-` and 26 random characters). 201 with the key's `id` and `masked` form and its `value`, which no later call shows. |
| `DELETE /v0/management/client-keys/{id}` | Removes the client key. 204; 404 when no key has the ID; 409 for the last key, since without client keys anyone who reaches core could use it. |
| `POST /v0/management/management-key/rotate` | Replaces the management key with a new random one and returns it as `value`. The old key stops working as the reply is sent. |
| `GET /v0/management/settings` | The basic and routing settings, by today's core's names (below). |
| `PATCH /v0/management/settings` | Changes the settings the body names (`{"routing": {"strategy": "fill-first"}}`) and replies with them all. 400 for a name or value it does not take. |

In the document, a placeholder stands for the secret it replaced, so a document read with `GET
/config`, edited and sent back keeps its secrets: core puts each one back from the file as it is
now. To change a secret, write the new value in its place. A placeholder is accepted only where a
secret goes, and only while the file still holds that secret and core has not restarted (the
placeholders are keyed hashes with a key core picks at start); otherwise the call answers 409
(read the config again) or refuses the document. Messages in replies never show a secret either.

A `config.yaml` keeps each setting under the name it already has there: the v8 name, or the
older spelling when only that is in the file. New client keys go to `access.api-keys`, or to the
older layout's `api-keys` list when the file has that and no `access.api-keys`; the management key
to `management.secret-key`, or `remote-management.secret-key` when only that is there.

| Setting | JSON config | Values |
| --- | --- | --- |
| `routing.strategy` | `routing.strategy` (`weighted` for `weighted-round-robin`) | `round-robin`, `fill-first`, `weighted-round-robin`, `least-active`; today's other spellings (`wrr`, `ff`, ...) are taken too. |
| `routing.session-affinity` | `routing.session_affinity` there or not | `true` or `false`. |
| `routing.session-affinity-ttl` | `routing.session_affinity.ttl_ms` | A duration such as `1h` (default) or `30m`. In a JSON config it needs session affinity on. |
| `routing.force-model-prefix` | `routing.force_model_prefix` | `true`: an account with a `prefix` serves its models only under the prefix. |
| `routing.retry.max-retry-credentials` | `routing.max_accounts` | Accounts one request may try; 0 is no limit. |
| `routing.retry.max-retry-interval` | `routing.max_wait_ms` (seconds × 1000) | Seconds one request may wait for a blocked account: before each round with `request-retry` set, else in all. |
| `routing.retry.request-retry` | `routing.request_retry` | Rounds a request may run after its first, as in today's core. `null` while a JSON config leaves rounds to this core's own rules; a `config.yaml` always has a number (default 0). |
| `routing.cooldown.disable-cooling` | `routing.disable_cooling` | `true`: failures leave no cooldown. An account's own `disable_cooling` wins. |
| `routing.cooldown.transient-error-cooldown-seconds` | `routing.transient_error_cooldown_ms` (seconds × 1000) | Seconds a failing upstream cools down: 0 for this core's backoff (1 second doubling to a minute), -1 for none. |

The settings shown are those of the running config. Today's other basic settings (`debug`,
logging to file, the request log, usage statistics, `proxy-url`, `ws-auth` and
`quota-response-mode`) do not exist in this core yet; `max_attempts`, which today's core has no
name for, is changed in the document.

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

## config.yaml with today's key names

Core also reads a `config.yaml` written for today's core (`core/`), so an existing config moves
over without renaming keys. A config file whose name ends in `.yaml` or `.yml` is read with
today's key names; any other file is read as the JSON config above. Both layouts today's core
reads work: the v8 layout of `core/config.example.yaml` (`server.port`, `access.api-keys`,
`api-keys.codex`, ...) and the older spellings (`port`, `api-keys` as a list,
`codex-api-key`, ...). When a file has both spellings of one setting, the v8 one wins, as in
today's core.

```bash
./pluxy -config config.yaml
```

The JSON config stays: the desktop app writes one, and it is the form core checks and runs on.
A `config.yaml` is turned into that form when it is loaded, so both files go through the same
checks and hot reload, and nothing behaves differently depending on which file a setting came
from.

Each time core loads a `config.yaml`, it logs one line naming the settings the file uses that
this core does not support yet, and one line naming the settings it ignores for good (the
"dropped" rows below). A setting that is off, zero, empty or at today's default asks for
nothing and is not named, nor is any setting of a feature whose `enabled` switch is off.
Neither line stops core from starting. Settings that did nothing in today's core either (marked
"dead") are accepted without a word. A key today's core does not know is named as unknown.

Settings only this core has keep their JSON names in a `config.yaml`: `packages`, `extensions`,
`extension`, `extension_pages`, `drop_policy`, `credentials_dir` and `cors` (today's core has no
CORS setting: it allows every origin). Provider accounts set in
the file go to the installed package of their provider (`pluxy/codex`, `pluxy/claude`,
`pluxy/gemini`, `pluxy/xai`, `pluxy/openai-compatible`), so those packages must be installed:
the bundled ones are, on the first start.

### Provider key sections

Each key in a provider section becomes one account of that provider's extension, with the key
as its `api_key` credential. A key with no `api-key` (today's core serves an upstream that
needs none) is not supported yet: it adds no account and is named in the log.

| Section (v8; older spelling) | Extension | Account settings |
| --- | --- | --- |
| `api-keys.codex` (`codex-api-key`) | `pluxy/codex` | `base-url` is `base_url`; `websockets` is `websockets`; `disable-codex-cloaking: true` is `codex_identity: false` |
| `api-keys.claude` (`claude-api-key`) | `pluxy/claude` | `base-url` is `base_url` |
| `api-keys.gemini` (`gemini-api-key`) | `pluxy/gemini` | `base-url` is `base_url` |
| `api-keys.vertex` (`vertex-api-key`) | `pluxy/gemini`, Vertex AI with the key as an express mode key | `base-url` is `base_url` |
| `api-keys.xai` (`xai-api-key`) | `pluxy/xai` | `base-url` is `base_url` |
| `api-keys.openai-compatibility` (`openai-compatibility`) | `pluxy/openai-compatible`, one account per entry in `keys` (`api-key-entries`) | `base-url` is `base_url`; a group with `disabled: true` adds no accounts |

In every section, `priority` is the account's `priority` and a key's `weight` is its `weight`.
A group's `models` list becomes the account's `models`: each entry's `name` is the model the
upstream knows, and its `alias` the name clients use (the first entry for a name wins, as in
today's core); the account serves only those models. A group's or key's `prefix` is the account's
`prefix`, and its `excluded-models` its `excluded_models` (today's core has no
`excluded-models` for `openai-compatibility`, so it is accepted there and does nothing). See
[Model lists and model names](#model-lists-and-model-names). An account's ID is the group's `name`
for a group with one key, and `<name>-<n>` (`n` from 1) for each key of a group with more;
entries without a name are `<section>-<n>`, counted across the section, such as
`codex-api-key-1`. Characters an account ID cannot hold (anything but letters, digits, `.`, `_`
and `-`) become `-`.

### What each setting does here

v8 name first, older spellings in brackets. **Supported** settings work as described.
**Later** settings are not supported yet and are named in the log when set. **Dropped** settings are ignored and named in
the log when set, for the reason given. **Dead** settings did nothing in today's core either and
are accepted silently.

| Setting | Here | Status |
| --- | --- | --- |
| `config-version` | Must be 8 when present, as in today's core. | Supported |
| `server.host` (`host`), `server.port` (`port`) | `listen` is `host:port`. An empty host listens on every interface and a port of 0 or none is 8317, as in today's core. | Supported |
| `access.api-keys` (`api-keys` as a list) | `client_keys`. | Supported |
| `management.secret-key` (`remote-management.secret-key`) | `management_key`, exactly as written: turns on the management API and the panel. A key already hashed with bcrypt (today's core can write one) is not supported yet: the management API stays off and the log says why. | Supported; hashed keys later |
| `management.allow-remote` (`remote-management.allow-remote`) | The management API answers anyone who can reach `listen` and sends the key. When the management API is on and this is not `true`, the log says so. | Later |
| `management.disable-control-panel` | The panel is on exactly when the management API is. | Dropped: the management key already turns the panel on and off |
| `management.disable-auto-update-panel`, `management.panel-github-repository`, `server.github-token` (`github-token`) | The panel is built into core; nothing is downloaded. | Dropped: no panel download |
| `management.base-url` | | Dropped: no terminal UI |
| `server.max-request-body-bytes` | Request bodies are capped at 24 MiB. | Dropped: core needs a fixed cap for its request frame |
| `server.response-compression.*` | | Later |
| `server.trusted-proxies` (`trusted-proxies`), `server.tls.*` (`tls`) | | Later |
| `server.commercial-mode` (`commercial-mode`) | | Dropped: it turns off features this core does not have |
| `server.discovery.*` (`discovery`) | | Later |
| `server.discovery.advertise-management` | | Dead |
| `routing.strategy` | `routing.strategy`: `round-robin`, `fill-first` (also `fillfirst`, `ff`), `weighted-round-robin` (also `weightedroundrobin`, `wrr`; this core's `weighted`); this core's `weighted` and `least-active` work too. A name today's core does not know is `round-robin`, as there, and the log says so. | Supported |
| `routing.session-affinity`, `routing.session-affinity-ttl` | `routing.session_affinity` with `ttl_ms` from the duration (default `1h`). | Supported |
| `routing.session-affinity-subagents`, `routing.session-affinity-rebind-on` | | Later |
| `routing.force-model-prefix` (`force-model-prefix`) | `routing.force_model_prefix`. | Supported |
| `routing.retry.max-retry-credentials` (`max-retry-credentials`) | `routing.max_accounts`. | Supported |
| `routing.retry.request-retry` (`request-retry`) | `routing.request_retry`: rounds after the first, run as today's core runs them. Default 0, as in today's core (its example config's 3 is only an example). | Supported |
| `routing.retry.max-retry-interval` (`max-retry-interval`) | `routing.max_wait_ms`, in seconds as in today's core: the longest wait before each round. Default 0: a round that needs a wait does not start. | Supported |
| `routing.cooldown.disable-cooling` (`disable-cooling`) | `routing.disable_cooling`. | Supported |
| `routing.cooldown.transient-error-cooldown-seconds` (`transient-error-cooldown-seconds`) | `routing.transient_error_cooldown_ms`: above 0 that many seconds, below 0 none. 0 is this core's backoff, from a second doubling to a minute, where today's core waits a fixed minute. | Supported |
| `routing.cooldown.quota-response-mode` (`quota-cooldown-response-mode`) | | Later |
| `routing.cooldown.save-cooldown-status` (`save-cooldown-status`) | Blocks live in memory and carry over reloads. | Dropped: no need found |
| `quota-exceeded.switch-project`, `quota-exceeded.switch-preview-model` | | Dead |
| `requests.proxy-url` (`proxy-url`) and every `proxy-url` | | Later |
| `requests.payload.*` (`payload`) | | Later |
| `requests.tls-handshake-timeout-seconds`, `requests.passthrough-headers`, `requests.nonstream-keepalive-interval`, `requests.streaming.*` (each also at the top level) | A request is retried on another account until its reply starts. | Dropped: off by default and no need found |
| `client.codex.enable-apply-patch`, `client.codex.optimize-multi-agent-v2` (`codex.optimize-multi-agent-v2`) | | Later |
| `client.codex.web-search-result-cache.*`, `client.claude.show-codex-commentary` | | Dropped: off by default and no need found |
| `upstream.codex.disable-codex-cloaking` (`codex.disable-codex-cloaking`) | `codex_identity: false` on the Codex subscription accounts `-import-auth` adds. It does not change accounts already imported. | Supported when importing |
| `upstream.codex.websocket-compression` | Always on; `false` is named in the log. | Supported (fixed on) |
| `upstream.codex.model-level-cooling`, `upstream.claude.model-level-cooling` | Blocks are always kept per model where the upstream says the limit is per model. | Supported (fixed on) |
| `upstream.codex.upstream-request-compression`, `-min-bytes`, `upstream.codex.stream-bootstrap-buffering`, `stream-bootstrap-timeout` | | Later |
| `upstream.codex.response-steering`, `upstream.codex.orphan-delegation-compatibility`, `oauth.providers.codex.header-defaults.*` (`codex-header-defaults`) | | Dropped: experimental or off by default, no need found |
| `upstream.claude.header-defaults.*`, `disable-claude-cloak-mode`, `disable-cloaking-model-list`, `session-date-pin-days` (also `claude`, `claude-code`, `claude-header-defaults`), and the Claude keys' `cloak` and `fingerprint-profile` | This core never changes how a client presents itself. | Dropped: making other clients look like Claude Code is out of scope |
| `upstream.claude.tool-search`, the Claude keys' `native-tool-search` and `rebuild-mid-system-message` | | Later |
| `upstream.xai.inject-x-search` (`xai`) | | Later |
| `experimental-cch-signing` (Claude keys) | | Dead |
| `oauth.auth-dir` (`auth-dir`) | The folder `-import-auth` reads (see below). Core never reads or writes it while it runs; at each load it logs how many credential files there are not imported yet. | Supported |
| `oauth.auth-auto-refresh-workers` (`auth-auto-refresh-workers`) | Each account is renewed shortly before it expires. | Dropped: no need for a pool size |
| `oauth.model-alias` (`oauth-model-alias`), `oauth.excluded-models` (`oauth-excluded-models`) | Each channel's rules become the `model_alias` and `excluded_models` of the package that serves its accounts, for the accounts added to it by a sign-in (not with an API key): `codex` is `pluxy/codex`, `claude` is `pluxy/claude`, `vertex` is `pluxy/gemini`, `kimi` (and `kimi-ai`, `kimi.ai`, `kimi.com`) is `pluxy/kimi`, `xai` is `pluxy/xai`, `meta` is `pluxy/meta`, `antigravity` is `pluxy/antigravity`, `devin` is `pluxy/devin`. Rules for another channel are named in the log and ignored. An alias's `force-mapping` and `openapi` are later. | Supported |
| `oauth.settings` (`oauth-settings`) | | Dropped: needs a model catalog, and no page uses it |
| `oauth.request-scoped-errors` (`oauth-request-scoped-errors`) and every `request-scoped-errors` | | Later |
| `oauth.providers.aistudio.ws-auth` (`ws-auth`) | | Later: the AI Studio relay is not built |
| `oauth.providers.codex.live-media-relay.*` | | Dropped: realtime and WebRTC are out of scope |
| `oauth.providers.codex.signin-with-chatgpt-identifier` | | Later |
| `oauth.providers.antigravity.*` (`antigravity`, `antigravity-signature-*`, `quota-exceeded.antigravity-credits`), `oauth.providers.devin.*` (`devin`) | | Later |
| `api-keys.meta` (`meta-api-key`), `api-keys.interactions` (`interactions-api-key`) | | Later |
| Every `headers` and `connection-pool` | | Later |
| Every `disable-cooling` on a group or key | The account's `disable_cooling`. | Supported |
| Every `request-retry` on a group or key | | Dropped: no need found for per-key round counts |
| The keys' `models[]` catalog fields (`display-name`, `max-context-length`, `force-mapping`, `is-compat`, `thinking`, `image`, `input-modalities`, `output-modalities`, `use-max-completion-tokens`, `support-configuration-update`, `openapi`) | A model's display name in lists is its upstream name. | Later |
| Codex keys' `alpha-search`; Vertex keys' `interactions`, `openapi`, `openapi-location`; openai-compatibility's `support-prompt-cache-key`; xAI keys' `websockets` | | Later |
| `multimedia.disable-image-generation` (`disable-image-generation`) | `true`, `"chat"` and `"passthrough"` are `image_generation_tool: false` on Codex accounts from the file and on those `-import-auth` adds. | Supported |
| `multimedia.gpt-image-2-base-model`, `multimedia.video-result-auth-cache-ttl` (each also at the top level) | No image or video routes yet. | Later |
| `observability.logs.*` (`debug`, `logging-to-file`, `logs-max-total-size-mb`, `error-logs-max-files`, `request-log`), `observability.usage.*` (`usage-statistics-enabled`, `redis-usage-queue-retention-seconds`) | | Later |
| `observability.pprof.*` (`pprof`) | | Dropped: a developer tool, off by default |
| `models.catalog`, `models.codex-catalog`, `models.devin-catalog` | | Later |
| `plugins.*` | Extensions are separate programs (packages), not libraries loaded into core. | Dropped: replaced by packages |
| `credentials.*` (`credential-concurrency`, `credential-in-flight`) | | Dropped: only the Home control plane used them |

Environment variables today's core reads (`MANAGEMENT_PASSWORD`, `PGSTORE_*`, `GITSTORE_*`,
`OBJECTSTORE_*`, ...) and its `.env` file are not read.

### Importing today's auth folder

```bash
./pluxy -config config.yaml -import-auth ~/.cli-proxy-api
```

`-import-auth` copies the credential files in today's auth folder (the JSON files at its top
level, as today's core reads them) into core's credential store (`credentials_dir`), prints what
it imported and skipped, and exits. Run it while core is stopped, then start core. Each file
becomes an account added by login, with the file's name (less `.json`) as its ID, so running it
again skips the files already imported. A file whose account ID the config or the store already
uses is skipped, as is a file larger than 1 MiB. The import never changes the auth folder, and
it refuses a `credentials_dir` that is the auth folder or inside it.

Both cores renewing the same login signs one of them out: the provider replaces the refresh
token at each renewal, and the other core's copy stops working. Once an account is imported,
use it from one core only, or sign in again in the other.

| File `type` | Becomes | Notes |
| --- | --- | --- |
| `codex` | `pluxy/codex` subscription account | `access_token`, `refresh_token`, `expired` (the expiry); `id_token` and `account_id` stay with the credential; `websockets` is the account's `websockets`. |
| `claude` | `pluxy/claude` subscription account | `access_token`, `refresh_token`, `expired`. |
| `kimi`, `kimi-ai` | `pluxy/kimi` account | `access_token`, `refresh_token`, `expired`, `device_id`; `base_url` is an account setting, and so is the site, `domain`, decided as today's core decides it: from `domain`, else `base_url`'s host, else the type (`kimi.ai` for `kimi-ai`). |
| `vertex` | `pluxy/gemini` Vertex AI account with a service account | `service_account` is the credential; `project_id` and `location` (default `us-central1`, as in today's core) are the account's `vertex` settings. |
| `antigravity` | `pluxy/antigravity` account | `access_token`, `refresh_token`, `expired`; `project_id` stays with the credential and `base_url` is an account setting. The file holds no OAuth client secret (today's core has it built in), so the account serves requests until its token expires; renewing it needs a new login (`POST /accounts/{id}/login`), which asks for the secret. |
| `xai` | `pluxy/xai` Grok login account | `access_token`, `refresh_token`, `expired`; `token_endpoint` stays with the credential and `base_url` is an account setting. |
| `meta` | `pluxy/meta` Muse Code login account | `api_key` (else `access_token`) is the minted key and `dca_token` (else a `dca:` `access_token`) the device token that mints a new one; `base_url` stays with the credential. A file with only the device token mints its key on the first request. |
| `devin` | `pluxy/devin` account with a session token | `session_token` (else `api_key`) is the credential; `base_url` is an account setting and `user_name` the label when the file has no `label` or `email`. |
| anything else | Skipped, named in the output | Today's core does not load a file with no `type` or with `gemini` either. |

From every file: `disabled: true` imports the account disabled, `priority` and `weight` are the
account's, `disable_cooling` (or `disable-cooling`) is its `disable_cooling`, and `label` (else
`email`) is its label. A file's `prefix` is the account's `prefix`,
its `excluded_models` (a list, or one comma-separated string) its `excluded_models`, and the
entries of its `model_aliases` (or `models`) with a `name` and an `alias` its `model_alias`, as
today's core reads them. A file's `proxy_url` and `headers` are not supported yet and are named
in the output.

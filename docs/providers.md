# Providers and sign-in methods

A **provider** is a company's AI service that pluxy sends requests to: Claude, ChatGPT (Codex),
Gemini and so on. pluxy talks to each one through an **extension**, a separate program that core
(pluxy's main program) starts when it needs it. An extension knows one kind of upstream: how to sign
in to it, which models it has, and how to send it a request. An **account** is one sign-in or one API
key with a provider. A provider can have many accounts; [routing](routing.md) spreads requests over
them.

This page lists every provider pluxy ships, how to sign in to each, and each provider's options. The
first part is a short how-to. The rest is reference.

## Add an account

pluxy installs no extension when it first starts. Install the ones you use, then add an account to
each.

1. **Install the extension.** In the app, Setup (step 1, "Your AI accounts") installs the kinds of
   account you tick. In the panel, use Extensions. From a terminal:

   ```bash
   pluxy ext install pluxy/claude
   ```

   The extension IDs are in the table below. A pluxy install carries its extensions as bundled
   packages, so installing one needs no network. See [Extensions](extensions.md).

2. **Add the account.** In the app or the panel, open Add an account, pick the provider and one of
   its ways to add it (each says "Opens your browser", "Shows a code to enter" or "Uses an API
   key"), and follow the prompts. From a terminal:

   ```bash
   pluxy account methods                          # each provider's sign-in methods and their inputs
   pluxy account add claude --method subscription # sign in; the short name works when it is unique
   pluxy account add pluxy/codex --method api_key # a key is asked for at a prompt, not as a flag
   ```

   A browser sign-in prints a link, a device-code sign-in prints a link and a code, and the command
   waits until you finish in the browser. `--no-wait` prints the link or code and returns, and the
   sign-in goes on in core; use it for a device code. Do not use it for a sign-in that asks you to
   paste something back (Claude's `subscription`): the command asks for it while it waits. See
   [the CLI](cli.md) for every account command and flag.

3. **Check it.** `pluxy account test <id>` sends one small request through the account;
   `pluxy account models <id>` lists the models it serves; `pluxy account quota <id>` shows its plan
   and limits where the provider reports them.

To change an account's settings afterwards, use Edit settings on its page in the app or the panel, or
`pluxy account set <id> 'settings={...}'`, which replaces all of the account's settings, not one key.
Accounts written in the config file are changed in the config file; see [Config](config.md).

An account can also be written in the config file with an API key as its credential:

```json
{
  "extensions": {
    "pluxy/codex": {
      "accounts": [{"id": "openai", "credential": {"kind": "api_key", "api_key": "sk-..."}}]
    }
  }
}
```

Accounts that sign in (a subscription, a device code) are added by signing in, not written in the
config.

## The providers

`--method` takes the value in the Method column. The panel shows the label.

| Provider | Extension ID | Method | Label in the panel | What you need |
| --- | --- | --- | --- | --- |
| Claude | `pluxy/claude` | `subscription` | Claude subscription | A Claude subscription. A browser sign-in; you paste back the address the browser ends on, or the code the page shows. |
| | | `api_key` | API key | An Anthropic API key. |
| | | `setup_token` | Setup token | The long-lived token `claude setup-token` prints. |
| Codex (ChatGPT) | `pluxy/codex` | `browser` | ChatGPT (browser) | A ChatGPT subscription. A browser sign-in. |
| | | `device` | ChatGPT (device code) | A ChatGPT subscription. A code to enter at OpenAI's page. |
| | | `siwc` | Sign in with ChatGPT (SIWC) | A ChatGPT plan, used for OpenAI API calls. A browser sign-in. |
| | | `api_key` | OpenAI API key | An OpenAI API key, or the key of a compatible service with its base URL (optional). |
| Gemini | `pluxy/gemini` | `api_key` | API key | A Gemini API key, or a Vertex AI express mode key (with a Vertex account's settings). |
| | | `service_account` | Vertex AI service account | The JSON key file of a Google Cloud service account, and the Google Cloud project in the account's `vertex.project` setting. |
| xAI (Grok) | `pluxy/xai` | `api_key` | xAI API key | An xAI API key. |
| | | `device` | Grok account (device code) | A Grok account. A code to enter at xAI's page. |
| Kimi | `pluxy/kimi` | `api_key` | Kimi Code API key | A Kimi Code API key. |
| | | `device` | Kimi Code (device code) | A Kimi Code subscription. A code to enter at Kimi's page. |
| Muse (Meta) | `pluxy/meta` | `device` | Muse (Meta) (device code) | A Muse Code subscription. A code to enter at Meta's page. |
| | | `api_key` | Meta API key | A Meta API key. |
| Devin | `pluxy/devin` | `browser` | Devin account (browser) | A Devin (formerly Windsurf) account. A browser sign-in. |
| | | `token` | Devin session token | A session token (`devin-session-token$...`). |
| OpenAI Compatible | `pluxy/openai-compatible` | `api_key` | OpenAI-compatible API key | The service's base URL and an API key. Optionally the wire API and the quota source. |

What each method does with what you give it:

| Check at sign-in | Providers |
| --- | --- |
| The key is checked with a call that costs nothing, and refused if the provider says it is wrong | Claude `api_key`, Codex `api_key`, Gemini `api_key` (not a Vertex express mode key), xAI `api_key`, Kimi `api_key`, OpenAI Compatible |
| The token is checked, and refused only if the provider rejects it | Devin `token` |
| The key or token is stored without asking the provider | Claude `setup_token` (it must contain `sk-ant-oat`), Meta `api_key`, Gemini `api_key` with Vertex settings, and a Codex `api_key` for a host other than OpenAI's that has no model list |
| The provider's page signs you in | every `browser`, `device`, `subscription` and `siwc` method |

Gemini's `service_account` method fetches an access token with the key, which is the check.

### Quota and account checks

| Provider | Quota (plan and limits) for | Account check |
| --- | --- | --- |
| Claude | a subscription or a setup token | API key only |
| Codex | a ChatGPT sign-in (`browser`, `device`) | none |
| Gemini | the last rate-limit reply seen, not a live reading | API key (not Vertex) |
| xAI | a Grok account | API key only |
| Kimi | a device-code sign-in | none |
| Muse (Meta) | a device-code sign-in | none |
| Devin | every account | none |
| OpenAI Compatible | OpenRouter and DeepSeek keys | yes, when the service has a model list |

An account check is one call that costs nothing, such as reading the model list. Core runs it when it
starts and when an account is added or changed. See [Routing](routing.md).

## Claude

Extension `pluxy/claude`. Claude models through Anthropic's Messages API, with an Anthropic API key
or a Claude subscription.

**Sign-in.**

- `subscription`: opens Claude's sign-in page. After you sign in, paste the address the browser was
  sent to (it starts with `http://localhost:54545/callback`), or the code the page shows. Nothing
  listens on that address; pasting is the only way back.
- `api_key`: an Anthropic API key, checked with `GET /v1/models`. A key that contains `sk-ant-oat` is
  a setup token and is treated as one.
- `setup_token`: the token `claude setup-token` prints. pluxy refuses a value that does not contain
  `sk-ant-oat`, and keeps one that does without asking Anthropic, because no call checks it without
  spending from the subscription. It is never renewed.

pluxy sends any key that contains `sk-ant-oat` as a subscription token, however it was added.

**Models.** Read from `GET {base_url}/v1/models` with the account's credential. When that fails or
lists nothing, pluxy uses the extension's built-in list.

**Options** (account `settings`, all optional):

| Key | Default | Meaning |
| --- | --- | --- |
| `base_url` | `https://api.anthropic.com` | Where the Messages API is. A trailing `/v1` or `/v1/messages` is dropped. |
| `cache_ttl` | `"5m"` | How long the `cache_control` markers pluxy adds to translated requests last: `"5m"` or `"1h"`. A Responses client's own `prompt_cache_retention` or `prompt_cache_options.ttl` takes precedence for that request. |
| `model_level_cooling` | `false` | `true`: a used-up 5-hour or weekly limit blocks only the model that hit it. `false`: it blocks the whole account. |
| `rebuild_mid_system_message` | `false` | `true`: system messages in the middle of a conversation are moved into the top-level system prompt, for an upstream that refuses them there. |
| `client_id` | Claude's own | The OAuth client the `subscription` sign-in uses. |

**Limits and quirks.**

- A token count (`POST /v1/messages/count_tokens`) is answered by Anthropic only when `base_url` is
  Anthropic's own address: `https`, the host `api.anthropic.com`, port 443 or none, and no user
  name or password. With any other base URL the account cannot count, and pluxy estimates the
  count.
- Quota for a subscription reads Anthropic's usage and profile endpoints (`/api/oauth/usage` and
  `/api/oauth/profile`, the ones Claude's own apps read; nothing is spent). It shows:
  - the 5-hour limit (`five_hour`) and the weekly limit (`seven_day`);
  - the weekly Opus, Sonnet, Fable, OAuth apps and Cowork limits where Anthropic gives them
    (`seven_day_opus`, `seven_day_sonnet`, `seven_day_fable`, `seven_day_oauth_apps`,
    `seven_day_cowork`), and any other credit pool counted in dollars;
  - extra usage (`extra_usage`, in dollars);
  - the plan: Free, Pro, Max, Max 5x, Max 20x or Team. When the profile cannot be read, the usage
    still shows, without the plan.

  A setup token shows usage without the plan. An API key has no quota.
- Anthropic's 429 replies say which limit was hit, and pluxy blocks accordingly:
  - A refused shared 5-hour or weekly window, or a refusal of the whole subscription, blocks the
    account (only the model, with `model_level_cooling`). The block lasts until the latest reset
    among the refused windows, or until `Retry-After` when that is at most 7 days and an hour away.
    When neither gives a usable time, core's usual backoff applies.
  - A model's own weekly window (`7d_oi`), overage, and any other 429 block only that model on the
    account.
  - A per-minute refusal (requests, input tokens or output tokens per minute) waits for its
    `Retry-After` alone, not for the hours until a window resets.
  - A 529 (Anthropic is overloaded) means the request did not run, so core tries another account.

## Codex (ChatGPT)

Extension `pluxy/codex`. OpenAI's Codex models, with a ChatGPT subscription, an OpenAI API key, or
Sign in with ChatGPT (SIWC).

**Sign-in.**

- `browser`: opens OpenAI's sign-in page. The browser comes back to `localhost:1455` on the machine
  core runs on, when that port is free. When it is not free, or core runs on another machine, paste
  the address the browser ended on.
- `device`: shows a link (`https://auth.openai.com/codex/device`) and a code to enter. The code is good for 15 minutes.
  Use it when the browser cannot reach core's machine.
- `siwc`: signs in as an agent other than the Codex client and adds an account whose token calls the
  OpenAI API on your ChatGPT plan. It runs like `browser`, with the same port. It signs in as the
  client `dynamic_agent_client` for `https://api.openai.com/v1`, with a new device ID, and gives
  `signin_with_chatgpt_identifier` as the agent's name. The grant must include the scope
  `chatgpt.tokens.use.direct`; without it the sign-in is refused.
- `api_key`: an OpenAI API key, checked with `GET {base_url}/models`. The check goes to the
  `base_url` input, else the account's `base_url` setting, else OpenAI's; the optional input points at
  a compatible service, including its `/v1`, and is kept as the account's `base_url`. Only a `200`
  whose body has a model list (a `data` array) accepts the key. No redirect is followed, and an
  unreachable service is reported as unreachable, not as a wrong key. A key the service refuses (401,
  403, or a body that names a bad key or a closed account) is not added. A host other than OpenAI's
  that has no model list (it answers 404 or 405 without refusing the key) cannot check a key, so the
  key is kept as entered.

**Models.** For a ChatGPT subscription, pluxy reads the model catalog of the ChatGPT backend. When
that fails or is empty it uses the built-in list for the account's plan (free, plus, team, pro;
business and go use the team list; any other plan gets the pro list). An API key and a SIWC account
always use the built-in list: an API key gets the pro list. Every list ends with the image models `gpt-image-1.5`, `gpt-image-2`,
`gpt-image-2.5`, `gpt-image-2.5-flare` and `gpt-image-2.5-sunburst`, except a SIWC account's, which
has none.

**Options** (account `settings`, all optional):

| Key | Default | Meaning |
| --- | --- | --- |
| `base_url` | `https://api.openai.com/v1` for an API key and SIWC; `https://chatgpt.com/backend-api/codex` for a subscription | Where requests go: `{base_url}/responses`. |
| `codex_identity` | `true` | Send the Codex terminal client's `User-Agent` and `originator`. |
| `codex_client_version` | `0.159.0` | The version in that `User-Agent`, and in the ChatGPT model list request. |
| `callback_listen` | `true` | Let a `browser` or `siwc` sign-in listen on port 1455. `false`: you always paste the address. |
| `images` | `true` | Serve the Images API (`/v1/images/generations` and `/v1/images/edits`) on the account. |
| `image_generation_tool` | `true` | Offer the hosted `image_generation` tool to requests. Never offered to a free plan, a SIWC account or a model whose name ends in `spark`. |
| `image_generation_off_models` | none | Model names, or patterns with `*` and `?` (any case), that are not offered the `image_generation` tool. Matched against the model name sent upstream. |
| `websockets` | `false` | Send a Responses WebSocket client's turns over one upstream WebSocket per client socket. When the upstream drops it mid-turn, the client gets `upstream_websocket_lost` and sends the conversation again. |
| `alpha_search` | `false` | Let an API key account serve Codex's Alpha Search. The account also needs a `base_url` of its own. A ChatGPT sign-in always serves it. |
| `prompt_cache_retention_passthrough` | `false` | Send the client's `prompt_cache_retention` on, for an upstream that takes it. A SIWC account never sends it. |
| `signin_with_chatgpt_identifier` | `pluxy` | The name a SIWC sign-in gives this program, shown on the sign-in page. |

**Limits and quirks.**

- A ChatGPT subscription's token is renewed four days before it expires (five minutes before, for a
  token that lasts less than that).
- A SIWC account serves Chat Completions, Responses, Messages and Gemini requests, but not requests
  from a Codex client (a `User-Agent` naming `codex-tui`, `codex_cli_rs`, `codex-rs`, `codex_exec` or
  Codex Desktop), and no Images API or Alpha Search; compaction is the only endpoint it serves. Core
  tries another account for those. SIWC has no quota.
- A SIWC account's requests go to the OpenAI API with a `User-Agent` of
  `<identifier> (<GOOS>; <GOARCH>)` (the `signin_with_chatgpt_identifier` setting, the operating
  system and the processor). They carry none of the Codex client's headers, no `originator` and no
  ChatGPT account ID. The settings `codex_identity`, `codex_client_version`, `image_generation_tool`,
  `image_generation_off_models` and `prompt_cache_retention_passthrough` do not apply. Compaction goes
  to `{base_url}/responses/compact`.
- A ChatGPT sign-in's quota reads the ChatGPT backend's `GET /wham/usage` (the backend is `base_url`
  without its trailing `/codex`, `https://chatgpt.com/backend-api` by default), and nothing it reads
  spends from the plan. It shows the plan, the 5-hour limit (`five_hour`), the weekly (`weekly`) or
  monthly (`monthly`) limit, code review's limits (`code_review_five_hour` and the like), any extra
  per-model limit (`<name>_five_hour` and the like), the credit balance (`credits`) and the number of
  manual resets (`reset_credits`, "Manual resets"; pluxy only reads this, it never uses one). The plan's end date comes
  from `GET /subscriptions?account_id=...` and the resets from `GET /wham/rate-limit-reset-credits`;
  when either read fails, only its field is left out. An API key has none.
- A refusal that says the model or turn is at capacity blocks that model on that account, not the
  account. Core reads one by the code `turn_rate_limit_exceeded`, `model_at_capacity` or
  `model_is_at_capacity`, or, with no code, by an "at capacity" message that comes with the error
  type `rate_limit_error` or names the model. Whatever status OpenAI gave it, the client sees a 429,
  and core tries another account, unless the refusal came inside a reply that had already started:
  that request is not tried again, but the model stays blocked.
- A reply that ends `response.incomplete` with nothing produced and `output_tokens` exactly `0` fails
  the request with a 502 (`upstream`). It blocks nothing and is not tried on another account.
- The free ChatGPT plan, and an account with `"images": false`, do not serve the Images API.
- Alpha Search (`POST /v1/alpha/search` and `/backend-api/codex/alpha/search`) goes to a Codex
  account: a ChatGPT sign-in (`browser` or `device`) always, an API key only with `alpha_search` and a `base_url` of its own
  (without either, that key is skipped for this endpoint only). A request that names no model may go
  to any such account. The body goes to `{base_url}/alpha/search` with the model renamed for the
  account and `prompt_cache_key` and `prompt_cache_retention` taken out. An upstream error comes back
  in OpenAI's shape, and a 404 blocks no model. When no account can serve it, the client gets an
  error.

## Gemini

Extension `pluxy/gemini`. Google's Gemini models through the Gemini API, or Vertex AI. There is no
browser sign-in: Google's APIs take keys and service accounts.

**Sign-in.**

- `api_key`: a Gemini API key, checked by listing one model. For a Vertex AI account, set the
  account's `vertex` setting first and enter a Vertex express mode key; it is kept without a check,
  because Vertex has no call that checks a key without running a model.
- `service_account`: the JSON key file Google Cloud gives for a service account. The account's
  settings must name `vertex.project`. pluxy signs a request with the key to fetch an access token;
  that is the check. The key's `token_uri` must be Google's token endpoint
  (`https://oauth2.googleapis.com/token`, or `https://accounts.google.com/o/oauth2/token`); a key that
  names another endpoint is refused.

`pluxy account import <key file>` does the second for you: it takes the key file (or `-` for standard
input), makes the account `vertex-<project_id>` with `vertex.project` set from the file and
`vertex.location` set to `us-central1`, and signs in. To use another location, change the account's
`vertex` setting afterwards. When the account `vertex-<project_id>` already exists, the command signs
in to it again, and only a key of the same service account (the same `client_email`) replaces its key.
An existing account of that ID that is not the same project's Vertex service account, or that is
written in the config file, refuses the import.

`pluxy account import` sends a service account key file to the management API's
`POST /vertex/import`. That call names the account `vertex-<project_id>` (a `/`, `\` or `:` in the
project ID becomes `_`, a space `-`), sets the settings `vertex.project` and `vertex.location`, labels
the account `<project_id> (<client_email>)`, and waits up to 45 seconds for the sign-in (504 when
that is not enough). See [Management API](management-api.md).

**Models.** A Gemini API account reads `GET {base_url}/v1beta/models` and falls back to the built-in
Gemini list when that fails or is empty. A Vertex AI account always uses the built-in Vertex list.

**Options** (account `settings`, all optional):

| Key | Default | Meaning |
| --- | --- | --- |
| `base_url` | `https://generativelanguage.googleapis.com` | The Gemini API host. For Vertex, the host that replaces Google's. |
| `vertex.project` | the key's `project_id`, when the credential holds the whole key file | The Google Cloud project. Required for `service_account`. |
| `vertex.location` | `global` | The Vertex AI location. `global` is `aiplatform.googleapis.com`; `us` and `eu` are the multi-region hosts; any other value `{location}-aiplatform.googleapis.com`. Only lowercase letters, digits and `-`. |
| `interactions` | `false` | `true`: serve Gemini's Interactions API (`POST /v1beta/interactions`) on this account. |

An account with a `vertex` setting is a Vertex AI account; one without is a Gemini API account.

In a config file, a service account's credential holds only the secret part of the key
(`client_email` and `private_key`), with the project in settings. The whole key file works there
too, but core removes every string a credential holds from replies, so the key's project ID and other
plain fields would be cut out of model output; a sign-in keeps only the secret part for that reason.

```json
{
  "id": "vertex",
  "settings": {"vertex": {"project": "my-project", "location": "us-central1"}},
  "credential": {"kind": "service_account", "data": {"client_email": "...@my-project.iam.gserviceaccount.com", "private_key": "-----BEGIN PRIVATE KEY-----\n..."}}
}
```

**Limits and quirks.**

- Google reports no remaining quota for these accounts. Quota shows the values from the account's
  last HTTP 429 reply, labelled with when it was seen. It is kept in the extension's memory only, so
  it is lost when core restarts or stops the idle extension (by default after 10 minutes without
  requests), and an account that never got a 429 shows none.
- The Interactions API is served only by accounts with `"interactions": true`. The setting only adds
  that endpoint: the account still serves the other client formats. A request that names an `agent`
  (such as Deep Research) goes to any such account. Thinking suffixes on the model name are refused
  there; set `generation_config.thinking_level` instead. Core also refuses, with a 400, a body that
  names both or neither of `model` and `agent`, a `stream` that is not `true` or `false`, and a body
  that is not JSON; it removes a `models/` prefix from the model. The request goes to:

  | Credential | Request goes to |
  | --- | --- |
  | Gemini API key | `{base_url}/v1beta/interactions` |
  | Vertex service account | `{host}/v1beta1/projects/{project}/locations/global/interactions`: the location is always `global` |
  | Vertex express mode key | `{host}/v1beta1/interactions`, or the project path above when `vertex.project` is set |

  A stream on Vertex is asked for with `alt=sse`. The `Api-Revision` header goes with the request
  (the client's, else `2026-05-20`). The reply comes back as it arrives, JSON or SSE, with Google's
  project and account names masked, and its usage is counted. An error from Google keeps Google's
  shape; one that pluxy raises has the OpenAI shape.
- Embeddings (`POST /v1/embeddings`), speech and transcription (`POST /v1/audio/speech` and
  `/v1/audio/transcriptions`) and Responses compaction (`POST /v1/responses/compact`) are served on
  every Gemini account. Speech returns `pcm` or `wav` only, not the default `mp3`.
- Keys for Google's sovereign-cloud universes name their own token endpoint and are not supported.

## xAI (Grok)

Extension `pluxy/xai`. xAI's Grok models, with an xAI API key or a Grok account.

**Sign-in.**

- `api_key`: an xAI API key, checked with `GET {base_url}/models`.
- `device`: shows xAI's page and a code to enter. It uses the Grok CLI's own public client at the
  endpoints in xAI's OpenID discovery document (`https://auth.x.ai`), which must be HTTPS on `x.ai`.
  The account is named after the e-mail address in the ID token. A token poll that fails before it was
  sent, or gets a 429, is tried again while the code is good; a 5xx ends the sign-in.

**Models.** The extension's built-in list, the same for every xAI account. It leaves out xAI's image,
video and speech models, which need endpoints pluxy does not send to.

**Options** (account `settings`, all optional):

| Key | Default | Meaning |
| --- | --- | --- |
| `base_url` | `https://api.x.ai/v1` | Where requests go: `{base_url}/responses`. For a Grok account, the default means the Grok CLI's chat endpoint, `https://cli-chat-proxy.grok.com/v1`. |
| `client_version` | `1.0.46` | The Grok CLI version a Grok account's requests name. Set it when xAI asks for a newer CLI. |
| `inject_x_search` | `false` | Add xAI's native `x_search` tool to requests that do not have one. |

**Limits and quirks.**

- A Grok account's token is renewed five minutes before it expires with the refresh token, at the
  token endpoint the sign-in found (the credential keeps it). When xAI sends no new refresh token,
  the old one stays. When xAI refuses the refresh token for good (`invalid_grant`, or an expired,
  reused or revoked token), the account asks for a new sign-in.
- A Grok account's requests, and its quota reads, carry the Grok CLI's identity headers, which xAI's
  chat endpoint checks: `User-Agent: xai-grok-workspace/<version>`, `X-XAI-Token-Auth: xai-grok-cli`,
  `x-grok-client-version`, `x-grok-client-identifier: grok-shell` and
  `x-authenticateresponse: authenticate-response`. The version is the `client_version` setting. An
  API key's requests name `pluxy-xai/<version>`.
- xAI's errors count as: a 401, or a 403 whose body says the credential is bad, an authentication
  error; any other 403, a permission error; a 404, a model error; a 5xx, an upstream error; a 429, a
  rate limit for that model that keeps xAI's `Retry-After`, unless it says the free usage is used up
  (below).
- Every client format goes to xAI's Responses API. A request with more than 200 tools keeps the first
  200, the most xAI accepts.
- A 429 that says the free usage is used up holds that model back on the account for up to 24 hours.
  Core checks a hold that long every 15 minutes and lifts it early when the account's quota shows
  room (see [Routing](routing.md)).
- Quota for a Grok account is read from the Grok CLI endpoint: `GET /billing?format=credits` for the
  weekly credit window (`weekly`) and `GET /billing` for monthly credits (`monthly_credits`), the
  pay-as-you-go cap and use (`pay_as_you_go`) and the prepaid balance (`prepaid`); the last three are
  in dollars. The plan comes from `GET /user?include=subscription` and `GET /settings`. Nothing
  that spends is sent. An API key has none.
- Responses compaction is served for API keys and Grok accounts. For a Grok account on the CLI chat
  endpoint, which has no compaction endpoint, it goes to xAI's official API instead.

## Kimi

Extension `pluxy/kimi`. Moonshot AI's Kimi Code subscription.

**Sign-in.**

- `device`: shows Kimi's page and a code to enter; the sign-in finishes when you approve it there. It
  uses the public client of Kimi's own CLI.
- `api_key`: a Kimi Code API key, checked with `GET {base_url}/v1/models`. A refused key reports Kimi's
  reason with secrets removed.

**Models.** The extension's built-in list, the same for every Kimi account.

**Options** (account `settings`, all optional):

| Key | Default | Meaning |
| --- | --- | --- |
| `domain` | `kimi.com` | The site the account belongs to: `kimi.com` or `kimi.ai`. |
| `base_url` | `https://api.{domain}/coding` | Where Kimi Code's API is. A trailing `/v1` is dropped. |

**Limits and quirks.**

- The token is renewed five minutes before it expires.
- Each client format goes to Kimi's matching endpoint (Chat Completions, Responses, Messages). A
  Gemini request is translated to Chat Completions. Kimi names its coding models differently: `kimi-k2.8`
  and the other `kimi-k2.x-code` names are sent as `kimi-for-coding` (or `kimi-for-coding-highspeed`),
  and any other name loses its `kimi-` prefix.
- Chat Completions and Responses requests name pluxy in Kimi's device headers, with a device ID made
  at sign-in (an API key has none). A Messages request carries the client's own Messages headers.
- A token count goes to Kimi's own count endpoint, `POST {base_url}/v1/messages/count_tokens?beta=true`,
  for every account; the beta `token-counting-2024-11-01` is added to the client's `anthropic-beta`.
- Quota for a device sign-in reads `GET {base_url}/v1/usages`, and the plan from `GET {base_url}/v1/me`
  (its `user_level_name`; the plan is left out when that read fails), with the device headers. It
  shows each limit Kimi reports (`limit_1`, `limit_2` and so on, named by Kimi's own name for it,
  else by its window length, else "Limit #n"), the weekly limit (`usage`), and the monthly
  (`month_total`) and monthly coding (`month_code`) limits. An API key has none.
- A 429 that says the quota is used up is reported as a used-up quota, not a rate limit.
- Thinking replay: when a Messages client from Claude Code resends an earlier tool-use turn without
  its signed thinking, pluxy puts the thinking back. It keeps each turn in the extension's memory for
  an hour after the turn was last used, and loses all of them when core stops the idle extension (by
  default after 10 minutes without requests) or restarts it. Turns are kept per client key, per
  Claude Code conversation (its session ID, from `X-Claude-Code-Session-Id`, else from the
  session in `metadata.user_id`) and per model family. A request with no client key or no session ID
  is sent as written. The memory holds at most 10,240 turns and 256 MiB, dropping the least recently
  used first, and a turn over 8 MiB or 512 blocks is not kept. When Kimi refuses a request that had
  thinking put back (a 400 or 422, or an error in the stream), that turn is dropped.

## Muse (Meta)

Extension `pluxy/meta`. Meta's Muse models on `api.meta.ai`.

**Sign-in.**

- `device`: shows Meta's page and a code to enter (`auth.meta.com`, Muse Code's own public client).
  The device token it gives (`dca:...`) is traded at Meta's key endpoint
  (`https://api.meta.ai/muse-code/key`) for an API key. The account keeps the key (as its access
  token) and the device token (as its refresh token). The wait for you is limited to 15 minutes. A
  token poll that fails before it was sent, or gets an answer with no OAuth error that is not a 5xx,
  is tried again while the code is good; a 5xx, or a failure after the poll was sent, ends the
  sign-in.
- `api_key`: a Meta API key, kept as entered. A `dca:` token is refused as a key: sign in with the
  device code instead.

**Models.** The extension's built-in list, the same for every Muse account. All are text models.

**Options** (account `settings`):

| Key | Default | Meaning |
| --- | --- | --- |
| `base_url` | For a sign-in, where Meta's key endpoint said. Otherwise `https://api.meta.ai/v1`. | Meta's API; requests go to `{base_url}/responses`. |

**Limits and quirks.**

- The key from a device sign-in has no timer. When Meta refuses it, core renews the credential once,
  which makes a new key from the device token. A renewal keeps the API address the credential
  holds when Meta's key endpoint names none, and a rate-limited renewal keeps Meta's `Retry-After`.
- Requests carry Muse Code's identity: a `User-Agent` and `X-Client-Id` (`tbh:exec`) that name Muse
  Code (build 1.4.14), and a new Muse session ID for each request.
- Every request is streamed (a client that wants one reply gets the stream gathered), and requests
  with tools also carry Muse Code's own `muse` tool namespace, which Meta wants.
- A 429 that says the subscription quota is used up is reported as a used-up quota until the reset
  time Meta gives (else its `Retry-After`, else core's usual backoff). A 404 holds the account back
  from the model until that time, or for five minutes. A stream failure (an `error` event or
  `response.failed`) is classified by the status it names, as a number or a string (a 502 when it
  names none).
- Quota for a device sign-in shows the usage limit (`window`, "Usage limit", with its length), the
  weekly limit (`weekly`, "Weekly limit") and the plan (Meta's `subs_tier_name`). To read it pluxy
  sends `POST https://api.meta.ai/muse-code/key` with an empty object and the device token, as a
  renewal does, and throws the new key away; the account's own key is left alone. The call goes only
  to Meta's key endpoint, never to `base_url`. An API key has none.
- Meta has no token count endpoint, and pluxy does not do compaction through Meta.

## Devin

Extension `pluxy/devin`. The models a Devin (formerly Windsurf) account gets, through Devin's API
server, as Devin's CLI talks to it.

**Sign-in.**

- `browser`: opens Devin's sign-in page (`app.devin.ai`). The browser comes back to a loopback address
  that core's machine serves while the sign-in runs, or you paste the code Devin's page shows (or the
  address the browser ended on). The wait is limited to 5 minutes. Devin's sign-in has no client ID or
  secret: the code and a PKCE verifier are traded at `api.devin.ai` for the session token. The pasted
  value is the prompt's `code` input.
- `token`: a session token (`devin-session-token$...`) you already have, checked against Devin's
  profile endpoint. Only a refusal of the token fails it. Its input is `session_token`.

The account is named after its Devin user name, or its user ID when Devin gives no user name.

**Models.** The extension's built-in catalog (SWE, Claude, GPT, Gemini, Kimi, GLM, DeepSeek, Grok and
other models), the same for every Devin account. Devin names a model and its reasoning effort as one
ID (`claude-opus-5-5-medium`). A client asks for `claude-opus-5-5` (or `devin/claude-opus-5-5`) and
pluxy adds the request's reasoning effort, or the model's default, moved to the nearest effort the
model has. A name that already ends in an effort is sent as it is, and so is a model that is not in
pluxy's catalog (with the request's effort added when it has one). A few models name their efforts as
variants instead: SWE-1.7 is sent bare unless `medium` is asked for, SWE-1.6 bare unless `fast` is,
GLM-5.2 bare except for `none` and `max` (`glm-5-2-max-1m` for the long-context model), and the Claude
4.6 models with `-thinking` for any effort but `none`.

**Options** (account `settings`, all optional):

| Key | Default | Meaning |
| --- | --- | --- |
| `base_url` | `https://server.codeium.com` | Devin's API server. |
| `device_seed` | none | Makes the device fingerprint every request carries the same each time. Without it the fingerprint is random per request. |
| `callback_listen` | `true` | `false` keeps the sign-in from listening for the browser; you paste the code. |

**Limits and quirks.**

- The session token does not expire and has no renewal. When Devin refuses it, sign in again.
- Devin has no place for some client fields (parallel tool calls off, strict tool arguments, files
  other than text and base64 images, namespace and freeform tools such as `apply_patch`). pluxy
  reports each as dropped, and refuses the request under the config's `drop_policy` `reject`.
- Devin has no token count endpoint, so core estimates counts.
- A 429, a demand refusal or a credit limit holds back that model on the account. When Devin says the
  account's daily or weekly quota is exhausted, it holds back the account; a monthly quota, or one
  that names no period, holds back only the model. A call Devin cancels is a cancelled request (499).
- Quota reads Devin's `GetUserStatus` call on `base_url` (a Connect call with a protobuf body, sent
  with the session token) and shows the daily (`daily`) and weekly (`weekly`) limits and the plan
  name; nothing is spent.
- An account's identity is the user ID from Devin's `/v3/self` profile. A sign-in whose profile read
  failed has no identity, and is refused for an account that stored one.

## OpenAI Compatible

Extension `pluxy/openai-compatible`. Any service with an OpenAI-style API and a key: OpenAI, OpenRouter,
DeepSeek, Mistral, Groq, local servers and others. Each key is a separate account; several keys for one
service use the same base URL.

**Sign-in.** `api_key` asks for:

| Input | Needed | Meaning |
| --- | --- | --- |
| `base_url` | yes | The service's API address including its path, such as `https://api.openai.com/v1`, or `https://generativelanguage.googleapis.com/v1beta` for a Gemini-style host. Must be HTTP or HTTPS, without user information, a query or a fragment. A trailing `/` is removed. |
| `api_key` | yes | The key. |
| `wire_api` | no | The API the service speaks: `chat` (default), `responses`, `messages` or `gemini`. |
| `quota` | no | `openrouter`, `deepseek` or `none`. Default: chosen by the base URL's host. |

pluxy checks the key with `GET {base_url}/models`, using the header that `wire_api` calls for, and
accepts any 2xx reply. A wrong base URL that answers 2xx is accepted too. The base URL and wire API
are kept as the account's settings. An account that signs in again needs only the key.

**Models.** An account that lists `models` of its own serves those, and pluxy reads nothing. Otherwise
pluxy reads `GET {base_url}/models` (in OpenAI's list shape or Gemini's). A later read that fails keeps
the last good list; an account whose first read failed serves any model until a read works.

**Options** (account `settings`):

| Key | Default | Meaning |
| --- | --- | --- |
| `base_url` | required | See above. |
| `wire_api` | `chat` | `chat`: `POST {base_url}/chat/completions`. `responses`: `/responses`. `messages`: `/messages`, with the key in `x-api-key`. `gemini`: `/models/{model}:generateContent`, with the key in `x-goog-api-key`. Every wire serves all four client formats: it passes its own format through and translates the other three. |
| `quota` | by host | `openrouter` (for `openrouter.ai`), `deepseek` (for `api.deepseek.com`) or `none`. Any other host has no quota. |
| `reasoning_levels` | `["low", "medium", "high"]` | The reasoning efforts the service accepts, from `none`, `minimal`, `low`, `medium`, `high`, `xhigh` and `max`. An effort outside the list is sent as the nearest one, and reported. Chat and Responses wires only. |
| `developer_role` | `developer` | `developer` or `system`: the role a developer message goes as, on the Chat wire. |
| `string_content` | `false` | `true`: a message whose content is only text parts is sent as one string, on the Chat wire. |
| `reasoning_passback` | unset | On the Chat wire: `true` sends earlier assistant turns' `reasoning_content` back, only for a request with tools and with thinking not turned off (a turn with none gets a blank one). `false` sends none. Unset leaves what the client sent. |

For some hosts pluxy fills the three Chat-wire shaping settings when you leave them unset, and a value
you set always wins: `api.deepseek.com` takes `developer_role` `system`, `string_content` `true` and
`reasoning_passback` `true`; `api.mistral.ai` takes `developer_role` `system` and `reasoning_passback`
`false`. Other hosts, OpenRouter and Groq included, have no preset.

**Limits and quirks.**

- Quota exists for OpenRouter and DeepSeek. Any other service has none.
  - OpenRouter: `GET /api/v1/key`. The plan is `free` or `paid`. The spending limit is `limit` ("Key
    limit", or "Budget limit" when the budget is not the key's own); spend is `usage_daily`,
    `usage_weekly` and `usage_monthly`, in dollars; a free model's daily request allowance is
    `free_model_daily_requests`.
  - DeepSeek: `GET /user/balance`. One limit for each currency, `balance_<currency>` (such as
    `balance_cny`), shows the balance left.
- Responses compaction is served on `responses` and `chat` accounts. The Images API is served on
  `chat` and `responses` accounts, for any model the account lists. Neither is served on `messages`
  or `gemini` accounts.
- Token counts (Messages and Gemini count requests) come from the upstream's own count endpoint on a
  `messages` or `gemini` wire. On a `chat` or `responses` wire core estimates the count. It does so
  too when the upstream answers 404 to the count (`/responses/input_tokens`, `/messages/count_tokens`
  or `/models/{model}:countTokens`) without naming a missing model; with `token_count_estimate` off,
  that 404 is returned instead.
- A service that lacks a side endpoint fails only that request when it answers 404 there without
  naming a missing model, and blocks no model on the account. This holds for compaction
  (`/responses/compact`), embeddings (`/embeddings`) and the Images API (`/images/generations` and
  `/images/edits`). A 404 that says the model is unknown still blocks as usual on compaction and the
  Images API, and so does any 404 from the generation path itself. On embeddings it does not: the
  account is taken as unable to embed with that model, and core tries the model's next account (a
  400 when none can), without blocking the model for generation.
- A service tier or verbosity from a client in another format is sent only to the hosts whose
  documentation names the field: `api.openai.com` (Chat and Responses wires), `api.anthropic.com`
  (Messages wire, service tier `auto` and `default`) and `generativelanguage.googleapis.com` (Gemini
  wire, service tier). Any other host gets neither, and the drop is reported.

## How accounts added by sign-in are kept and renewed

An account added by signing in (rather than written in the config file) has its credential kept in
core's credential store: the `credentials` folder beside the config file (the config's
`credentials_dir`), one file per account, readable only by core's user (and SYSTEM on Windows). Only
the extension that created an account ever receives its credential, and core removes the credential
from what an extension sends back.

Core's private folders and files (the credentials, the extensions' settings and the logs) can be read
only by core's user. On Linux and macOS the folders are `0700` and the files `0600`. On Windows, where
modes do not do this, each folder and file gets a protected access list (nothing inherited) that lets
in only core's user and SYSTEM.

What core does with the credentials folder:

- It checks the folder each time it loads or saves. On Windows it makes the folder private when its
  access list is not, and refuses to use `credentials_dir` when that is a link, a file or a folder
  owned by another account. A folder made by pluxy run as administrator is owned by the
  Administrators group, which only a run as administrator accepts: from an elevated prompt take
  ownership with `takeown /f <folder> /r`, or move the folder away and sign in again.
- A file in the folder that is a link, or (on Windows) is owned by another account, is skipped, with
  a notice in core's log. A file that is not a record (not JSON, or its `id` is not its file name) is
  skipped the same way, and its account shows as `broken` until the file is fixed or the account is
  removed. Neither stops core.
- On Windows, a record file whose access list lets in more than core's user and SYSTEM (one written
  before the folder was private, or moved in) is written again as a private file when it is loaded,
  with a notice in the log.

A login's codes and refresh tokens go only to the provider's own sign-in hosts, which each extension
carries as built-in HTTPS addresses. An account's settings cannot name another host. Only a test run
can, with `PLUXY_TEST_LOGIN_HOSTS=1` (see [Config](config.md)).

An account's credential is due for renewal a set time before it expires, and core renews it through
its extension the next time a request uses the account. An account nobody uses is not renewed in
between. The lead time is:

| Provider and method | Renewed |
| --- | --- |
| Claude `subscription` | an hour before it expires (halfway through a shorter life) |
| Claude `setup_token`, `api_key` | never |
| Codex `browser`, `device` | four days before it expires (five minutes before, for a token that lasts less than that) |
| Codex `siwc` | five minutes before it expires |
| xAI `device` | five minutes before it expires |
| Kimi `device` | five minutes before it expires |
| Muse (Meta) `device` | when Meta refuses the key, once, by making a new key from the device token |
| Devin | never; sign in again when Devin refuses the token |
| Gemini `service_account` | the extension fetches a new access token five minutes before the old one expires; the key itself does not expire |
| any API key | never |

`pluxy account refresh <id>` renews an account's credential now.

When the provider refuses the credential for good (the refresh token is revoked, the password was
changed), the account shows "Needs sign-in". Sign in again from its page, or with
`pluxy account login <id> --method <method>`. The new sign-in must be to the same upstream account:
every identity field the provider reported at the first sign-in (an e-mail address, an account ID, a
user ID) must match, ignoring case, or the sign-in fails and the account keeps its old credential.
An account whose provider reported no identity (Kimi's) is checked by the label its provider gave it,
when both sign-ins have one. To use another account, add it as a new one. The identity each provider
reports:

| Provider and method | Identity fields |
| --- | --- |
| Claude `subscription` | `email`, `account_id`, `organization_id` |
| Codex `browser`, `device`, `siwc` | `email`, `account_id` |
| Gemini `service_account` | `client_email` |
| xAI `device` | `sub`, `email` |
| Muse (Meta) `device` | `email` |
| Devin | `user_id` |

`pluxy account remove <id>` removes an account and its credential. Removing an extension keeps its
accounts: they serve again once the extension is installed again.

How core picks among accounts, how it blocks one that fails, and how it names and lists models is
in [Routing](routing.md). The management API behind all of this is in
[Management API](management-api.md).

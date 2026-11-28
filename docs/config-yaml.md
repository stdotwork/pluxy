# config.yaml keys

Core also reads a CLIProxyAPI `config.yaml`; this page maps its keys to pluxy's.
[Moving from CLIProxyAPI](moving-from-cpa.md) walks through a move step by step.

A config file whose name ends in `.yaml` or `.yml` (in any letter case) is read with CLIProxyAPI's key names; any other
file is read as the [JSON config](config.md). Both layouts of CLIProxyAPI's config work: its current layout
(`config-version: 8`, with `server.port`, `access.api-keys`, `api-keys.codex`, and so on), of which
`next/internal/legacy/testdata/config.example.yaml` is a copy of CLIProxyAPI's own example, and the
older spellings (`port`, `api-keys` as a list, `codex-api-key`, and so on). When a file has both
spellings of one setting, the current one wins, as in CLIProxyAPI.

```bash
pluxy serve -config config.yaml
```

The JSON config stays: the desktop app writes one, and it is the form core checks and runs on. A
`config.yaml` is turned into that form when it is loaded, so both files go through the same checks
and the same reload. A few defaults follow CLIProxyAPI's in a `config.yaml` (session affinity is off,
and `request_retry` is 0, unless the file says otherwise; see the rows below).

Each time core loads a `config.yaml`, it logs a line naming the settings the file uses that pluxy
does not support yet, a line naming the settings it ignores for good (the "Ignored" rows below), a
line naming unknown keys, and a line for each special case a row below describes. A setting that is off, zero, empty or at CLIProxyAPI's default asks for nothing and is not
named, nor is any setting of a feature whose `enabled` switch is off. Neither line stops core from
starting. Settings that did nothing in CLIProxyAPI either (marked "Accepted, no effect") are accepted
without a word. A key CLIProxyAPI does not know is named as unknown.

Keys are matched exactly, as CLIProxyAPI matches them: `Port:` is not `port:`, so it is not read, and
a key in another letter case in the `payload` rules is dropped and named in the log. The `payload`
rules that CLIProxyAPI skipped or dropped when it applied them (a `-raw` string that is not JSON, a
`.inf` or `.nan`) are dropped and named too; the row for `requests.payload.*` below lists them. A
setting CLIProxyAPI reads into a true-or-false field takes `yes`, `on`, `no` and `off` (and `y`, `n`,
in lower, title or upper case), as its YAML reader does; the exception is a key's or group's
`disable-cooling`, which must be `true` or `false`.

A management key needs setting once. CLIProxyAPI writes a bcrypt hash over `management.secret-key`
in the file after it reads the key, and pluxy cannot use a hash: it refuses the config and says so.
Replace the hash with the key itself, in plain text. A config with no management key at all is
refused too, with a message saying how to add one.

Settings only pluxy has keep their JSON names in a `config.yaml`: `packages`, `extensions`,
`extension`, `extension_pages`, `drop_policy`, `token_count_estimate`, `credentials_dir`, `cors` and
`client_key_labels` (CLIProxyAPI has no CORS setting: it allows every origin). Provider accounts set
in the file go to the installed package of their provider (`pluxy/codex`, `pluxy/claude`,
`pluxy/gemini`, `pluxy/xai`, `pluxy/openai-compatible`), so those packages must be installed first:
install them from the bundled registry (`pluxy ext install pluxy/codex`).

[`pluxy import cpa`](cli.md#pluxy-import-cpa) brings a CLIProxyAPI setup (its `config.yaml` and auth
folder) into a running core and reports each of these settings before anything is written.

## Provider key sections

Each key in a provider section becomes one account of that provider's extension, with the key
as its `api_key` credential. A key with no `api-key` (CLIProxyAPI serves an upstream that
needs none) is not supported yet: it adds no account and is named in the log.

| Section (current layout; older spelling) | Extension | Account settings |
| --- | --- | --- |
| `api-keys.codex` (`codex-api-key`) | `pluxy/codex` | `base-url` is `base_url`, and a key without one is dropped and named in the log, as CLIProxyAPI drops it; `websockets` is `websockets`; `alpha-search` is `alpha_search`; `disable-codex-cloaking: true` is `codex_identity: false` |
| `api-keys.claude` (`claude-api-key`) | `pluxy/claude` | `base-url` is `base_url`; `rebuild-mid-system-message: true` is `rebuild_mid_system_message: true` |
| `api-keys.gemini` (`gemini-api-key`) | `pluxy/gemini` | `base-url` is `base_url` |
| `api-keys.vertex` (`vertex-api-key`) | `pluxy/gemini`, Vertex AI with the key as an express mode key | `base-url` is `base_url`; `interactions: true` on a key is `interactions: true` in the account's settings, which serves the Interactions API |
| `api-keys.interactions` (`interactions-api-key`) | `pluxy/gemini` | `base-url` is `base_url`; every account gets `interactions: true`, which serves the Interactions API (see [Gemini](providers.md#gemini)) |
| `api-keys.xai` (`xai-api-key`) | `pluxy/xai` | `base-url` is `base_url` |
| `api-keys.openai-compatibility` (`openai-compatibility`) | `pluxy/openai-compatible`, one account per entry in `keys` (`api-key-entries`) | `base-url` is `base_url`; a group with `disabled: true` adds no accounts |

In every section, `priority` is the account's `priority` and a key's `weight` is its `weight`.
A key's own `models` list, else its group's, becomes the account's `models`: each entry's `name` is the model the
upstream knows, and its `alias` the name clients use; the account serves only those models. When
entries share a name clients use, the first wins in every section, as in CLIProxyAPI, except
`openai-compatibility`: there CLIProxyAPI treats them as a pool and rotates requests over them,
and falls back to the next on a failure. pluxy makes the pool a fallback chain
(`routing.chains`) named by the alias, with one step per upstream model in the order the group
lists them, each served by the group's accounts; the account lists each model under its own name
for the steps, and the log names the chain (a model listed twice adds no step). A name that
accounts of more than one provider serve, by their `models` lists or an `oauth.model-alias` alias,
which CLIProxyAPI shared among them, becomes a chain too. It has one step per extension that serves
the name (the `gemini`, Vertex and `interactions` keys are all `pluxy/gemini`, so they make one
step), in the order core reads the sections (`codex`, `claude`, `gemini`, Vertex, `xai`,
`openai-compatibility`, `interactions`), not the file's, with `oauth.model-alias` names after the key
sections. A chain has at most 8 steps: core cuts the rest and names them in the log. A chain
tries its steps in order rather than in turn: see [Fallback chains](routing.md#fallback-chains). A group's or key's `prefix` is the account's
`prefix`, and its `excluded-models` its `excluded_models` (CLIProxyAPI has no
`excluded-models` for `openai-compatibility`, so it is accepted there and does nothing). See
[Model lists and model names](routing.md#model-lists). An account's ID is the group's `name`
for a group with one key, and `<name>-<n>` (`n` from 1) for each key of a group with more;
entries without a name are `<section>-<n>` in the older section spelling, counted across every key of
the section, such as `codex-api-key-1`. Characters an account ID cannot hold (anything but letters,
digits, `.`, `_` and `-`) become `-`; leading `.`, `_` and `-` are dropped, the ID is cut to 128
characters, and an ID another account already has (compared without regard to letter case) gets `-2`,
`-3` and so on.

## Auth files

CLIProxyAPI keeps each signed-in account as a `.json` file in its auth folder.
[`pluxy import cpa`](cli.md#pluxy-import-cpa) and `POST /v0/management/accounts/import`
([Accounts](management-api.md#accounts)) turn each file into an account added by sign-in, by the
file's `type`:

| File `type` | Becomes | Fields |
| --- | --- | --- |
| `codex` | `pluxy/codex` subscription account | `access_token`, `refresh_token`, `expired` (the expiry); `id_token` and `account_id` stay with the credential; `websockets` is the account's `websockets`. A `siwc` section with a token (`access_token`, else `access`, or `refresh_token`) also becomes a Sign in with ChatGPT account, `<file id>-siwc` (with `-2`, `-3` and so on when another file in the folder imports as that ID), even when the file's own tokens are missing: its `access_token`, `refresh_token`, `expires_at` (else `expired`), and `client_id`, `device_id`, `scope` and `id_token` with the credential. It is disabled unless the file says `use_siwc: true`; it shares the file's label and routing fields below, and takes only `websockets` and the identifier setting of the account settings. |
| `claude` | `pluxy/claude` subscription account | `access_token`, `refresh_token`, `expired`. |
| `kimi`, `kimi-ai` | `pluxy/kimi` account | `access_token`, `refresh_token`, `expired`, `device_id`; `base_url` is an account setting, and so is the site, `domain`: from `domain`, else `base_url`'s host, else the type (`kimi.ai` for `kimi-ai`). |
| `vertex` | `pluxy/gemini` Vertex AI account with a service account | `service_account` is the credential; `project_id` and `location` (default `us-central1`, as in CLIProxyAPI) are the account's `vertex` settings. `interactions` or `native_interactions` set to JSON `true` at the top of the file is the account's `interactions` setting. |
| `xai` | `pluxy/xai` Grok sign-in account | `access_token`, `refresh_token`, `expired`; `token_endpoint` stays with the credential and `base_url` is an account setting. |
| `meta` | `pluxy/meta` Muse Code sign-in account | `api_key` (else `access_token`) is the minted key and `dca_token` (else a `dca:` `access_token`) the device token that mints a new one; `base_url` stays with the credential. A file with only the device token mints its key on the first request. |
| `devin` | `pluxy/devin` account with a session token | `session_token` (else `api_key`) is the credential; `base_url` is an account setting and `user_name` the label when the file has no `label` or `email`. |
| `antigravity` | `pluxy/antigravity` sign-in account | `access_token`, `refresh_token`, `expired`; `project_id` (the Google Cloud project requests are billed to) stays with the credential, and a file without one is renewed at once, which finds it; `base_url` is an account setting. pluxy does not ship this extension (it is [archived](../next/archive/antigravity/README.md)): the account is added whether or not the package is installed, and serves once the package is built and installed by hand. |
| anything else | Not brought | CLIProxyAPI does not load a file with no `type` or with `gemini` either. |

From every file: `disabled: true` imports the account disabled; `priority` and `weight` are the
account's; `disable_cooling` (or `disable-cooling`) is its `disable_cooling`; `request_retry` (or
`request-retry`, a number or numeric text; a negative one inherits routing's) is its
`request_retry`; and `label` is its label, a name a person gave it. `email` (in a Devin file without
one, `user_name`) is the provider's label, which the account shows when the file has no `label` and
a new sign-in is checked against. A file's `prefix` is the account's `prefix`, its `excluded_models`
(a list, or one comma-separated string) its `excluded_models`, and the entries of its
`model_aliases` (or `models`) with a `name` and an `alias` its `model_alias`. A file's `proxy_url`
(or `proxy-url`) is the account's `proxy_url`; a file whose value core cannot use is skipped rather
than imported without its proxy. A file's `headers` are the account's `headers`: text values only,
trimmed, empty ones dropped, with a copy of a client header (`$` and a header name) brought as in a
provider key's `headers`. A copy of a client's key, proxy password or cookie header and a value that
holds `$CPA-SESSION-ID` are not brought, and a file with a header core refuses is skipped rather
than imported without it.

A file larger than 1 MiB, a file whose account ID the config or the credential store already uses,
and anything but a `.json` file at the top of the folder are not brought.

## What each setting does

The current name comes first, older spellings are in brackets. The Status column says what core
does with the setting:

- **Supported**: it works as described.
- **Not supported yet**: core does not do it yet, and names the setting in the log when it is set.
- **Ignored**: core ignores it for good, for the reason given, and names it in the log when it is set.
- **Accepted, no effect**: it did nothing in CLIProxyAPI either, so core accepts it without a word.

| Setting | In pluxy | Status |
| --- | --- | --- |
| `config-version` | Must be the number 8 when present, as in CLIProxyAPI. | Supported |
| `server.host` (`host`), `server.port` (`port`) | `listen` is `host:port`. An empty host listens on every interface and a port of 0 or none is 8317, as in CLIProxyAPI. | Supported |
| `access.api-keys` (`api-keys` as a list) | `client_keys`. At least one key is required; CLIProxyAPI serves anyone when the list is empty. | Supported |
| `management.secret-key` (`remote-management.secret-key`) | `management_key`, exactly as written. Required: a config without one, or with a key already hashed with bcrypt (CLIProxyAPI can write one), is refused, saying what to set. | Supported; hashed keys refused |
| `management.allow-remote` (`remote-management.allow-remote`) | `management_allow_remote`: without it the management API answers only this machine, as in CLIProxyAPI. | Supported |
| `management.disable-control-panel` | The panel is always served; signing in takes the management key. | Ignored: the panel is always served, and signing in takes the management key |
| `management.disable-auto-update-panel`, `management.panel-github-repository`, `server.github-token` (`github-token`) | The panel is built into core; nothing is downloaded. | Ignored: no panel download |
| `management.base-url` | | Ignored: no terminal UI |
| `server.max-request-body-bytes` | Request bodies are capped at 24 MiB. | Ignored: core needs a fixed cap for its request frame |
| `server.response-compression.*` | | Ignored: a reverse proxy compresses replies |
| `server.tls.enable`, `.cert`, `.key` (`tls`) | `tls`, the same three keys. Relative paths are relative to the config file, where CLIProxyAPI reads them relative to the folder it was started in. | Supported |
| `server.trusted-proxies` (`trusted-proxies`) | `trusted_proxies`; a change applies on reload. | Supported |
| `server.commercial-mode` (`commercial-mode`) | | Ignored: it turns off features pluxy does not have |
| `server.discovery.*` (`discovery`) | | Ignored: no client here reads the advertisement |
| `server.discovery.advertise-management` | | Accepted, no effect |
| `routing.strategy` | `routing.strategy`: `round-robin`, `fill-first` (also `fillfirst`, `ff`), `weighted-round-robin` (also `weightedroundrobin`, `wrr`; pluxy's `weighted`); pluxy's `weighted`, `least-active` and `soonest-reset` work too. A name CLIProxyAPI does not know is `round-robin`, as there, and the log says so. | Supported |
| `routing.session-affinity`, `routing.session-affinity-ttl` | `routing.session_affinity` with `ttl_ms` from the duration (default `1h`). A `config.yaml` without `session-affinity: true` has it off, as CLIProxyAPI does, though a JSON config has it on by default. | Supported |
| `routing.session-affinity-subagents` | A Claude Code subagent sends its session's `X-Claude-Code-Session-Id`, so with `routing.session-affinity` it stays on the session's account, as CLIProxyAPI's default `true` keeps it. `false` (spread subagents over the pool) is not built, and the log names it. | Ignored: a subagent stays on its session's account, and no need was found to spread subagents over the pool |
| `routing.session-affinity-rebind-on` | | Ignored: no need found |
| `routing.force-model-prefix` (`force-model-prefix`) | `routing.force_model_prefix`. | Supported |
| `routing.retry.max-retry-credentials` (`max-retry-credentials`) | `routing.max_accounts`. | Supported |
| `routing.retry.request-retry` (`request-retry`) | `routing.request_retry`: rounds after the first, run as CLIProxyAPI runs them. Default 0, as in CLIProxyAPI (its example config's 3 is only an example). | Supported |
| `routing.retry.max-retry-interval` (`max-retry-interval`) | `routing.max_wait_ms`, in seconds as in CLIProxyAPI: the longest wait before each round. Default 0: a round that needs a wait does not start. | Supported |
| `routing.cooldown.disable-cooling` (`disable-cooling`) | `routing.disable_cooling`. | Supported |
| `routing.cooldown.transient-error-cooldown-seconds` (`transient-error-cooldown-seconds`) | `routing.transient_error_cooldown_ms`: above 0 that many seconds, below 0 none. 0 is pluxy's backoff, from a second doubling to a minute, where CLIProxyAPI waits a fixed minute. | Supported |
| `routing.cooldown.quota-response-mode` (`quota-cooldown-response-mode`) | `routing.quota_response_mode`: `retryable` (default) or `usage-limit`, letter case aside; another value fails the load, as in CLIProxyAPI. | Supported |
| `routing.cooldown.save-cooldown-status` (`save-cooldown-status`) | Core saves bans, and blocks with more than 30 seconds left on accounts and their models, to `state/routing.json` by itself; shorter blocks and endpoint blocks live in memory and carry over reloads. | Ignored: no need found |
| `quota-exceeded.switch-project`, `quota-exceeded.switch-preview-model` | | Accepted, no effect |
| `requests.proxy-url` (`proxy-url`) | `proxy_url`: the [outbound proxy](routing.md#outbound-proxy) of every account. A value that is not an `http`, `https`, `socks5` or `socks5h` URL, `direct` or `none` fails the load, where CLIProxyAPI logs it and goes without the proxy. | Supported |
| Every `proxy-url` on a group or key | The account's `proxy_url`: a key's own, else its group's. | Supported |
| `requests.payload.*` (`payload`) | `payload`: the [payload rules](routing.md#payload-rules), with CLIProxyAPI's lists and fields. A path outside the parts CLIProxyAPI's example config uses fails the load. CLIProxyAPI's `protocol` named the upstream's API format; here it names the extension. Each rule is carried as CLIProxyAPI applied it, and every difference below is named in the log. A key is matched exactly: one in another letter case (`Override:`, `Name:`) or not a payload key was never read there, so it is dropped. A `-raw` param goes across as CLIProxyAPI sent it: a date (`2024-01-15`) is the JSON string `"2024-01-15T00:00:00Z"`, and a param that is null, `.inf` or `.nan` (CLIProxyAPI skipped it, and applied the rule's other params) is dropped. A `-raw` rule with a string param that is empty or not JSON is dropped as a whole, as CLIProxyAPI dropped it with a warning. A `.inf` or `.nan` (alone or inside a list or mapping) in a `default` or `override` param is dropped: CLIProxyAPI skipped the param or wrote `+Inf`, `NaN` or `-Inf` into the body, which no upstream accepts. A `model` whose `match` holds one is dropped, as CLIProxyAPI never matched it, and a `not-match` condition that holds one is dropped, as it never excluded a request there. | Supported |
| `requests.tls-handshake-timeout-seconds`, `requests.passthrough-headers`, `requests.nonstream-keepalive-interval`, `requests.streaming.*` (each also at the top level) | A request is retried on another account until its reply starts. | Ignored: off by default and no need found |
| `client.codex.enable-apply-patch` | Models without a Codex template are left out of the [Codex client catalog](routing.md#codex-client-catalog), so there is no entry to add `apply_patch` to. | Not supported yet: built when someone uses a non-Codex model through the Codex client and needs `apply_patch` on it |
| `client.codex.optimize-multi-agent-v2` (`codex.optimize-multi-agent-v2`) | | Not supported yet: built when Codex multi-agent v2 sub-agents need to reach non-Codex providers |
| `client.codex.web-search-result-cache.*`, `client.claude.show-codex-commentary` | | Ignored: off by default and no need found |
| `upstream.codex.disable-codex-cloaking` (`codex.disable-codex-cloaking`) | `codex_identity: false` on the Codex subscription accounts `pluxy import cpa` adds. It does not change accounts already imported. | Supported when importing |
| `upstream.codex.websocket-compression` | Always on; `false` is named in the log. | Supported (fixed on) |
| `upstream.codex.model-level-cooling` | Blocks are kept per model where the upstream says the limit is per model; usage limits remain account-wide. | Supported (fixed on) |
| `upstream.claude.model-level-cooling` (`claude.model-level-cooling`) | `model_level_cooling: true` on the Claude keys' accounts and on the Claude subscription accounts `pluxy import cpa` adds (see [Claude](providers.md#claude)). It does not change accounts already imported. | Supported |
| `upstream.codex.upstream-request-compression`, `-min-bytes` | | Ignored: it needs a compression library pluxy does not carry, for an off-by-default saving no one has measured |
| `upstream.codex.stream-bootstrap-buffering`, `stream-bootstrap-timeout` | The Codex extension does not move a turn that ends in an overload error to another account. | Not supported yet: built when pooled Codex accounts are seen failing on overload errors that moving the turn to another account would hide |
| `upstream.codex.response-steering`, `upstream.codex.orphan-delegation-compatibility`, `oauth.providers.codex.header-defaults.*` (`codex-header-defaults`) | | Ignored, no need found: `response-steering` is experimental and `orphan-delegation-compatibility` is off by default |
| `upstream.claude.header-defaults.*`, `disable-claude-cloak-mode`, `disable-cloaking-model-list`, `session-date-pin-days` (also `claude`, `claude-code`, `claude-header-defaults`), and the Claude keys' `cloak` and `fingerprint-profile` | pluxy never changes how a client presents itself. | Ignored: making other clients look like Claude Code is out of scope |
| `upstream.claude.tool-search`, the Claude keys' `native-tool-search` | | Ignored: Codex's tool search on Claude accounts, off by default with no panel or app control |
| `rebuild-mid-system-message` (Claude keys) | The account's `rebuild_mid_system_message`. | Supported |
| `upstream.xai.inject-x-search` (`oauth.providers.xai.inject-x-search`, `xai.inject-x-search`) | xAI accounts get `inject_x_search`; applies to API keys and imported Grok logins | Supported |
| `experimental-cch-signing` (Claude keys) | | Accepted, no effect |
| `oauth.auth-dir` (`auth-dir`) | The folder `pluxy import cpa` reads (see below). Core never reads or writes it while it runs. | Supported |
| `oauth.auth-auto-refresh-workers` (`auth-auto-refresh-workers`) | Each account is renewed shortly before it expires. | Ignored: no need for a pool size |
| `oauth.model-alias` (`oauth-model-alias`), `oauth.excluded-models` (`oauth-excluded-models`) | Each channel's rules become the `model_alias` and `excluded_models` of the package that serves its accounts, for the accounts added to it by a sign-in (not with an API key): `codex` is `pluxy/codex`, `claude` is `pluxy/claude`, `vertex` is `pluxy/gemini`, `kimi` (and `kimi-ai`, `kimi.ai`, `kimi.com`) is `pluxy/kimi`, `xai` is `pluxy/xai`, `meta` is `pluxy/meta`, `devin` is `pluxy/devin`, `antigravity` is `pluxy/antigravity`. A channel name is matched without regard to spaces or letter case. Rules for another channel are named in the log and ignored. An alias's `fork` and `display-name` come across as `fork` and `display_name`; its `force-mapping` and `openapi` are not supported yet, as the same settings are on the keys' models below. | Supported |
| `oauth.settings` (`oauth-settings`) | | Ignored: needs a model catalog, and no page uses it |
| `oauth.request-scoped-errors` (`oauth-request-scoped-errors`) and every `request-scoped-errors` | | Ignored: a request-scoped error is already not the account's fault |
| `oauth.providers.aistudio.ws-auth` (`ws-auth`) | | Ignored: the AI Studio relay needs a long-lived listener the extension protocol does not have |
| `oauth.providers.codex.live-media-relay.*` | | Ignored: realtime and WebRTC are out of scope |
| `oauth.providers.codex.signin-with-chatgpt-identifier` (`codex.signin-with-chatgpt-identifier`) | `signin_with_chatgpt_identifier` on the Sign in with ChatGPT (SIWC) accounts `pluxy import cpa` adds. Left unset, they use the extension's default, `pluxy`. | Supported when importing |
| `oauth.providers.devin.*` (`devin`) | The whole section is ignored. Its only CLIProxyAPI setting is `sensitive-words`. | Ignored: rewriting words to get past a content filter is out of scope |
| `oauth.providers.antigravity.*`, `antigravity-signature-cache-enabled`, `antigravity-signature-bypass-strict`, `quota-exceeded.antigravity-credits` | | Ignored: the Antigravity extension does not read these settings |
| `api-keys.meta` (`meta-api-key`) | | Ignored: the login is built; no need for a bare key |
| Every `headers` on a group or key | The account's [`headers`](routing.md#upstream-headers): a key's own, else its group's (a key's `headers: {}` sends none), and an `openai-compatibility` group's for all its keys. Names and values are trimmed and empty ones dropped, as in CLIProxyAPI. A value that copies a header from the client's request (`$` and a header name) comes across as `$` and that name, without the spaces CLIProxyAPI allows after the `$`; one whose rest is not a header name (such as `$` alone or `$5/mo`), which CLIProxyAPI never sends, is dropped. A copy of `Authorization`, `Proxy-Authorization`, `X-Api-Key`, `X-Goog-Api-Key` or `Cookie`, which CLIProxyAPI sends on and pluxy never does, and a value that holds `$CPA-SESSION-ID` are named in the log and not imported. A header pluxy refuses (`Host`, `Content-Length`, `Transfer-Encoding`, `Connection`) fails the load, where CLIProxyAPI sends it. | Supported |
| Every `connection-pool` | | Ignored: extensions own their connections |
| Every `disable-cooling` on a group or key | The account's `disable_cooling`. | Supported |
| Every `request-retry` on a group or key | The account's `request_retry`: a key's own, else its group's (an `openai-compatibility` group's for all its keys, which have none of their own there). A negative one inherits `routing.retry.request-retry`, as in CLIProxyAPI. | Supported |
| The keys' `models[]` `display-name`, `max-context-length`, `input-modalities`, `output-modalities` | A model's display name in lists is its upstream name; use `model_alias` for a different one. | Ignored: list metadata only, no need found |
| The keys' `models[]` `thinking` | The thinking suffix and the extensions cover it. | Ignored: no need found |
| The keys' `models[]` `is-compat`, `use-max-completion-tokens` | | Ignored: single-provider quirks, no need found |
| The keys' `models[]` `force-mapping` | A reply carries the upstream's model name, not the alias the client asked for. | Not supported yet: built when a client needs a reply's model to be the alias it asked for |
| The keys' `models[]` `openapi`; Vertex keys' `openapi`, `openapi-location` | | Not supported yet: built when someone calls Vertex partner models (Claude, Llama, Mistral) through a key |
| The keys' `models[]` `image` (openai-compatibility) | A Chat or Responses OpenAI-compatible account serves the Images API for any model it lists; the old per-model flag is not imported. | Ignored: the model list already decides which Images requests route to the account |
| The keys' `models[]` `support-configuration-update` | | Ignored: an off-by-default prompt-cache saving for one kind of relay, no need found |
| xAI keys' `websockets` | xAI is served over HTTP. | Ignored: HTTP gives the same replies |
| openai-compatibility's `support-prompt-cache-key` | A [payload rule](routing.md#payload-rules) can set a fixed `prompt_cache_key`. | Ignored: no need found |
| `multimedia.disable-image-generation` (`disable-image-generation`) | `true`, `"chat"` and `"passthrough"` are `image_generation_tool: false` on Codex accounts from the file and on those `pluxy import cpa` adds; `true` is also `images: false` there, which turns the Images API off on them (CLIProxyAPI answers its image routes 404). | Supported |
| `multimedia.gpt-image-2-base-model`, `multimedia.video-result-auth-cache-ttl` (each also at the top level) | Images are served through the Codex upstream's Images API, the path CLIProxyAPI takes for its built-in image models; the Responses-based path this model sets is not built, and there are no video routes. | Ignored: the first names the model of a path that is not built, the second belongs to video routes that are not built |
| `observability.logs.logging-to-file` (`logging-to-file`), `observability.logs.logs-max-total-size-mb` (`logs-max-total-size-mb`) | `logs.to_file` and `logs.max_total_size_mb`: [log files](usage-and-logs.md#log-files). A negative cap is none, as in CLIProxyAPI. | Supported |
| `observability.logs.debug` (`debug`) | | Ignored: pluxy writes no debug-level lines |
| `observability.logs.request-log` (`request-log`), `observability.logs.error-logs-max-files` (`error-logs-max-files`) | `logs.request_log` and `logs.error_logs_max_files`: [request logs](usage-and-logs.md#request-logs). A negative count is the default, 10, as in CLIProxyAPI. | Supported |
| `observability.usage.usage-statistics-enabled` (`usage-statistics-enabled`) | `usage.enabled`: the [usage record](usage-and-logs.md#usage-statistics). CLIProxyAPI's example config sets it to `false`, which turns the record off here too. | Supported |
| `observability.usage.redis-usage-queue-retention-seconds` (`redis-usage-queue-retention-seconds`) | The record keeps requests by `usage.max_events` and `usage.max_age_ms`. | Ignored: it was how long a queued usage record waited for a reader that removed it; reading the record here removes nothing, and its default of 60 seconds would leave little to read |
| `observability.pprof.enable` (`pprof.enable`), `observability.pprof.addr` (`pprof.addr`) | `pprof.enable` and `pprof.addr`: the [profiling pages](usage-and-logs.md#profiling-pages). Only a build with `-tags pprof` serves them; the default build logs that the setting does nothing. With `enable` on, an address that is not loopback is refused. | Supported (in the `pprof` build) |
| `models.catalog`, `models.codex-catalog`, `models.devin-catalog` | Each extension carries its model list, and a developer refreshes it by hand (see [Codex client catalog](map.md#codex-client-catalog)). | Ignored: pluxy has no remote model catalog setting; each extension carries its model list |
| `plugins.configs.lamplighter.ignition.*`, `plugins.configs.lamplighter.providers.*` | The [Lamplighter plugin](https://github.com/PosvdM/cpa-plugin-lamplighter)'s window ignition, while the plugin is on (`plugins.enabled` and its own `enabled` both `true`): `routing.ignition`, where what the plugin leaves out takes pluxy's defaults, which are the plugin's. `start_hour` and `end_hour` keep their names; `failure_backoff_multiplier` becomes `backoff_multiplier`, `max_transient_failures` `max_failures`, `end_grace_minutes` becomes `end_grace_ms`, `grace_seconds` `grace_ms`, `failure_retry_seconds` `retry_ms` and `post_success_hold_seconds` `hold_ms`, held to the plugin's ranges (a retry of at least 60 seconds, a hold of at least 15). A provider that ignites (its `ignite`, by default on for `claude` and `codex`, and not turned off by `monitor: false`) becomes an entry of `routing.ignition.extensions`, `pluxy/claude`, `pluxy/codex` or `pluxy/antigravity` (archived: it serves once installed by hand), with its `model`. Antigravity's `groups` (by default `Gemini`) and `models` become `windows`: `Gemini` is `gemini_models_five_hour`, `Claude / GPT` `claude_and_gpt_models_five_hour`, any other group its name in lower case with each run of characters that is not a letter or digit as one `_` (trimmed of `_`), then `_five_hour`; an empty list is `Gemini` and `Claude / GPT`, and any group that has a model set. The hours are the machine's local time. | Supported |
| `plugins.configs.lamplighter.*`, other | The plugin's notifications and reminders (`bark_*`, `webhook`, the thresholds, `recovery_notify`, `reset_reminder`, `codex_reset_updates`), its time zone, poll interval, MCP server, page and records, and how it reached core (`cpa_base_url`, `models_api_key`). | Ignored: pluxy sends no usage notifications; it reads the hours in the machine's local time and usage when a window is due. While the plugin is off, all of its settings are dropped |
| `plugins.*`, other | Extensions are separate programs (packages), not libraries loaded into core. | Ignored: replaced by packages |
| `credentials.*` (`credential-concurrency`, `credential-in-flight`) | | Ignored: only the Home control plane used them |

Environment variables CLIProxyAPI reads (`MANAGEMENT_PASSWORD`, `PGSTORE_*`, `GITSTORE_*`,
`OBJECTSTORE_*`, ...) and its `.env` file are not read.

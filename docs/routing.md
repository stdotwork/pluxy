# Accounts and routing

This page explains how pluxy chooses an account for each request, and what it does when an upstream
refuses one. It covers blocks and how they end, session stickiness, fallback chains, window
ignition, the outbound proxy, upstream headers, payload rules, account checks, model lists and
model names. The settings it names are listed, with their types and defaults, in the
[config reference](config.md).

A few words used throughout:

- **Core** is the pluxy program that serves client requests.
- A **provider** is the extension that talks to one kind of upstream, such as `pluxy/claude` or
  `pluxy/codex`: a separate program core starts. See [Providers](providers.md).
- An **account** is one credential for a provider: an API key, or a sign-in to a subscription. An
  account comes from the config (the `extension` section, or an entry of the `extensions`
  sections) or from a sign-in (`pluxy account add`).
- The **upstream** is the service the provider sends requests to, such as Anthropic's API.
- A **block** is a period during which core sends an account no requests, after the upstream
  refused it.

## How a request picks an account

For each request core picks an account, sends the request to the account's extension, and reads
the outcome. Extensions never retry on their own: core decides every retry.

### Which provider serves a model name

A model name is served by the accounts of one provider. That provider is the one of the
highest-[`priority`](config.md) account that serves the name. When two accounts have the same
priority, the one that comes first decides, in this order: the accounts of the `extension`
section, then those of the `extensions` sections in order of extension ID, then the accounts added
by sign-in, in order of account ID.

- An account whose model list could not be read serves any model. It decides the provider only
  when no account lists the name.
- Accounts that cannot serve right now still count in this choice. A banned or blocked account, one
  turned off, and one waiting for a new sign-in keep the name on their provider. The error of a
  request they cannot serve names them, with their state, in `left_out`.
- Under the `weighted` strategy, an account with [`weight`](config.md) 0 takes no part: it neither
  serves the name nor decides its provider.

When another provider also serves the name, its accounts take none of these requests, however
blocked the first provider's accounts are. Only a change to the accounts or to what they serve
(their priorities, weights, model lists, excluded models and prefixes) or to the chains moves a
name to another provider. To send a name to another provider or model when the first runs out,
use a [fallback chain](#fallback-chains).

### Which accounts can take it

An account is **eligible** for a request when all of these hold:

- it serves the model;
- it is not banned, turned off or waiting for a new sign-in;
- no block covers it;
- it is under its [`max_concurrency`](config.md) limit (the number of requests it may run at
  once; 0 is no limit);
- this request has not tried it in the current round, and the round's
  [`max_accounts`](config.md) limit has not left it out.

Among eligible accounts, core prefers, in order:

1. the account the request's session is bound to (see [Session stickiness](#session-stickiness));
2. the accounts with the highest `priority`: an account with a lower priority serves only while no
   higher one is eligible;
3. the account the [`routing.strategy`](config.md) picks.

| Strategy | Picks |
| --- | --- |
| `round-robin` | The default. Takes turns, starting after the account picked last. |
| `weighted` | By each account's `weight` (default 1), interleaved. |
| `fill-first` | The first eligible account, in account order. |
| `least-active` | The account with the fewest requests running. |
| `soonest-reset` | The account whose longest usage window resets soonest (below). |

**`soonest-reset`** spends allowance that is about to reset before it is lost. Among the eligible
accounts of the top priority, a new request goes to the account whose longest usage window (for a
Codex or Claude account, its weekly window) resets soonest, by its provider's own usage reading.
Reset times are rounded to the hour, so accounts whose windows end minutes apart tie and take
turns. Accounts with no known reset come after those with one, and an account whose reading shows
a full window comes last until that window resets. A blocked account is never picked, whatever its
reset. Core reads every enabled account's usage for this every
[`routing.quota_refresh_ms`](config.md) (30 minutes by default), in the background and never while
a request waits; the first reading comes a minute after core starts. 0 turns these readings off:
accounts are then ranked only by the readings core takes when it checks a long block (see
[Checks while an account is blocked](#checks-while-an-account-is-blocked)). Accounts whose provider
gives no usage reading, such as API keys, have no reset to rank by.

`pluxy route <model>` shows the accounts a request for a model would try now, in order, and the
accounts it would leave out and why; under `soonest-reset` it also shows the reset each account
was ranked by.

### Pinning a request to one account

A client can send a request to one account with the `X-Pluxy-Account: <id>` header, naming the
account by its ID or by its public ID (the short ID replies give). A pinned request never goes to
another account.

### Session stickiness

Session affinity keeps the requests of one session on the same account while that account is
eligible, so a coding agent's conversation keeps its provider-side prompt cache. It is on unless
[`routing.session_affinity`](config.md) says `{"enabled": false}`. A session stays bound for an
hour after its last request; `ttl_ms` changes that.

The session comes from the first of these the request has: the `X-Claude-Code-Session-Id`,
`Session-Id`, `X-Session-Id` or `X-Session-Affinity` header, or the body's `prompt_cache_key`. A
request with none is routed by strategy as usual. Bindings are kept per client key and model. When
core runs from a `config.yaml` with CLIProxyAPI's key names, session affinity is off unless that
file turns it on (see the [config reference](config.md)).

### When a refused request moves to another account

When an attempt fails before the reply starts, the outcome updates the account's state (it may
set a block, below). Then:

- If the extension reports the refusal as **retry-safe**, meaning the upstream certainly did not
  run the request (for example a 429 or a 401), the request moves on to another eligible account.
- When the upstream refuses the credential of an account added by sign-in, core first renews the
  credential and tries the same account again, once per request. Only when the renewal gives no
  newer credential does the request move on.
- An account that cannot serve this request moves it on too: its upstream speaks another format and
  the request cannot be translated to it, or the upstream says it does not have the model.
- Any other failure goes to the client.

Once a reply has started, nothing is retried, and no request moves on for an error about the
request itself (a 400, or a refusal of its content), which would end the same way on another
account.

These settings bound the moves:

| Setting | What it limits |
| --- | --- |
| [`routing.max_attempts`](config.md) | Upstream attempts per request, counting the first. 0 (the default): no fixed limit; each account is tried once per round. |
| [`routing.max_accounts`](config.md) | Distinct accounts one request may try in a round. Each round gets its own count, and an account the limit left out takes part in a later round. 0: no limit. |
| [`routing.max_wait_ms`](config.md) | The longest a request waits, in total, for a blocked account to come back; with `request_retry`, the longest it waits before each round. 0 (the default): it never waits, and when every account is blocked it fails at once with a 429. A request never tries an account before the time the upstream gave. |
| [`routing.request_retry`](config.md) | When set, how many rounds run after the first, each trying every eligible account again. |
| account [`request_retry`](config.md) | The rounds after the first that this account takes part in, in place of `routing.request_retry`. |

With `request_retry` set, a round starts at once when no account is blocked, or else when the first
block ends, if that is within `max_wait_ms`. An account refused for rate or quota in this request
waits at least 10 seconds before its next round. When the request's last failure was a refused key
(401) or a missing model (404), no new round starts. An account's own `request_retry` takes it out of a request's rounds past its
count, and a count above the routing one runs more rounds with only the accounts whose count reaches
them. Setting `request_retry` on any account runs rounds even when `routing.request_retry` is not
set; accounts that set none then take part in the first round only. With `request_retry` unset
everywhere, rounds are bounded by `max_attempts` and `max_wait_ms`, and each new round needs a wait.

**Timeouts.** [`routing.timeouts`](config.md) sets the upstream HTTP limits in milliseconds, and an
account's own `timeouts` overrides them for that account; 0 turns a limit off.

| Limit | Default | Applies to |
| --- | --- | --- |
| `header_ms` | 60000 (1 minute) | Waiting for the upstream's response headers. |
| `idle_ms` | 300000 (5 minutes) | The gap between upstream bytes, on streamed requests. |
| `total_ms` | 600000 (10 minutes) | The whole request, on requests that are not streamed. |

A timeout before any reply byte moves the request to one more account, once. A second timeout ends
the request with that error, so a stuck upstream costs a client two limits, not one per account.
For an extension that speaks protocol 0.23 or later, core also ends an attempt on which the
extension sent nothing at all for as long as the extension may wait on the upstream plus a minute
(the longer of `header_ms` and `idle_ms` when streamed, the longest of the three when not), with
the error code `extension_timeout`. That moves the request to another account when no reply has
started. When a limit this deadline needs is 0, there is no such deadline.

### What the reply says about routing

A reply names the account (by its public ID), provider and model that served it in the `X-Pluxy-Account`,
`X-Pluxy-Provider`, `X-Pluxy-Model` and `X-Pluxy-Attempts` headers. An error carries a `pluxy`
member: the request's trace ID, the accounts it tried (`tried`, each with its result) and the
accounts it left out (`left_out`, each with why). Set [`routing.explain`](config.md) to `false` to
leave the headers and the `pluxy` member out, for a core whose client keys go to people who should
not see which accounts serve them. The trace ID stays, in the `X-CPA-TRACE-ID` header.

## Blocks

A refusal blocks what it is about: the whole credential, one model on the account, or the
account's **endpoint**. Accounts that name the same [`endpoint`](config.md) share its outages; by
default each account is its own endpoint. A block lasts as long as the upstream said (its
`Retry-After` or a reset time), or, when it said nothing, by these rules:

| Refusal | Blocks | When the upstream gives no time |
| --- | --- | --- |
| Rate limit or quota on one model (a plain 429) | That model on the account | Backs off from 1 second, doubling each time, up to 30 minutes |
| Rate limit or quota on the whole credential (the provider says the allowance is used up, such as a subscription's usage window) | The account | Backs off from 1 second, doubling, up to 30 minutes |
| Failing endpoint or other transient upstream failure (a 5xx, a broken connection) | The endpoint | Backs off from 1 second, doubling, up to 1 minute |
| Refused key (401) or refused permission (403) | The account | 30 minutes |
| Missing model (404) | That model on the account | 12 hours |

A backoff step is not exact: each block lasts a random time between half and all of the step, so
accounts that failed together do not all come back at the same moment. The first block of a
ladder lasts 0.5 to 1 second, and the top step 15 to 30 minutes (30 to 60 seconds for transient
failures).

Two settings change this. [`routing.disable_cooling`](config.md) `true` (or an account's own
`disable_cooling`) records no blocks after failures, so every account stays eligible; a ban (an
account the provider closed, below) is still recorded.
[`routing.transient_error_cooldown_ms`](config.md) sets how long a failing upstream or endpoint
(a 5xx or a broken connection) blocks: 0 (the default) uses the backoff above; a value above 0
blocks that long when the upstream gave no `Retry-After`; a value below 0 blocks not at all,
whether the upstream gave `Retry-After` or not. It does not cover an extension that stopped
answering (`extension_timeout`), which always uses the backoff.

A request for a renamed or prefixed model reaches the upstream under the upstream's name, and a
block on a model is kept under that name, so asking for the same model by another name does not
get around the block.

**Order of results.** Results count in the order their requests started on the account. A success
clears the blocks that failures of earlier-started requests left on its account (its credential,
its model and its endpoint). A failure that ends after a later-started request already succeeded on
the same scope leaves no block and no last error. Clearing a block resets its backoff step too. A
scope with no live block for an hour is forgotten, and its backoff step with it.

### Resets the upstream names

Core believes a reset the upstream names when it is between 1 second and 8 days away. When the
upstream also names the length of its usage window, and that length is 31 days or less, core
believes a reset up to that length plus an hour, so Codex's monthly window (28 to 31 days) can
reset a month off. A reset sooner than 1 second is held for 1 second. A reset further off than core
believes is not trusted: the account is held for 30 minutes, and a rate-limit or quota block is
checked after 15.

The newest attempt's word replaces the block's end, whether sooner or later. A backoff core chose
itself, or the word of an attempt that started before the one that set the block, only moves the
block later.

### Checks while an account is blocked

A rate-limit or quota block on an account, or on one of its models, with more than 15 minutes left
is checked every 15 minutes. The check reads the account's usage from its provider (the extension's
[`quota` call](../next/protocol/README.md#quota)):

- A usage window covers a block on the whole account when it counts every request on the account.
  It covers a block on one model when it does that, or when it counts that model's requests. A
  window that counts only other models, or a plan feature such as code review, covers neither.
- When no covering window is full, the block ends. Otherwise it lasts until the last-resetting
  full window that covers it resets.
- An account with no usage reading, such as an API key, lets one real request through at the check
  instead, and that request's outcome decides. Where a reading exists, a real request is never used
  as the check.
- A check that cannot be made leaves the block in place, and is tried again 15 minutes later
  while more than 15 minutes of the block are left.

Endpoint blocks and plain cooldowns are not checked: they end when their time runs out.

### Blocks kept across a restart

Blocks of every kind (quota, rate limit and cooldown) on an account or one of its models with more
than 30 seconds left are saved to `state/routing.json`, in a `state` folder beside the config file
(mode 0600). Each is saved with its kind and the failure that set it (code, status and message, the
message cut to 256 bytes). Core reads them back at start, with the account's last error when one of
them set it, so a restart sends no requests to accounts whose usage ran out, that a provider rate
limited, or that it refused. A quota block read back with more than 15 minutes left is checked at
once. A rate-limit block with more than 15 minutes left is checked 15 minutes on, as it would have
been had core kept running. Endpoint blocks are not saved.

A file core cannot read, or one in a form this core does not read, is not loaded and is left as it
is until a block or a ban must be saved; it is then moved to `routing.json.bad`.

### Bans

A provider can close an account for good (a ban). Core saves bans in `state/routing.json` too, each
with the account, the provider's reason and when it was banned, and reads them back at start. A
saved ban whose account core no longer has (in the config or among the accounts added by sign-in)
is dropped, at start and on a reload. A ban for an
account that is only out of routing for now, such as one whose extension is stopped or whose
sign-in is due, is kept.

A ban has no expiry. It ends when the account signs in again (`pluxy account login`), is removed, or
is turned back on (`pluxy account enable`).

### Clearing blocks by hand

`pluxy account clear <id>` (the management API's `POST /v0/management/accounts/{id}/cooldown/reset`)
clears an account's blocks at once: on its credential, its models and its endpoint (which the
accounts naming the same endpoint share), with their backoff steps and its last error. The account
serves again straight away, and `state/routing.json` is written again without them. It does not
lift a ban, and a banned account keeps its last error. It works for accounts from the config too.

Each account's `health` in `pluxy account list` (`GET /v0/management/accounts`) sums its state up in
one word; the [management API reference](management-api.md) lists the words.

### When every account is blocked

When no account that serves the model can take the request, the reply tells the client how long is
left, in whole seconds, rounded up and at least 1 while anything is left. The message's "try again
in 1s" (or `1m1s`), the `Retry-After` header, and the usage-limit reply's `reset_time`,
`reset_seconds` and "reset after" all come from the same rounding, so they agree.

**Telling a client to stop.** A request can end because no account that serves its model can take
it for more than 5 minutes, or because none can until a person acts: every one is turned off, waits
for a new sign-in, was closed by its provider, or has no running provider. Then the reply has
`X-Should-Retry: false` and no `Retry-After`, so a coding tool stops at once and shows the message
instead of retrying in silence or sleeping through a long wait. Its status, body and message are
the same as otherwise. A shorter wait keeps `Retry-After`, so a client still retries a brief
overload. A Responses WebSocket turn that fails this way closes the socket.

**The usage-limit reply.** [`routing.quota_response_mode`](config.md) decides what a client gets
when every account that serves its model is out of quota until a reset the upstream named, the
first of them more than 5 minutes away:

| Mode | Reply |
| --- | --- |
| `retryable` | The default. The 429 any block gets, which, the wait being over 5 minutes, has `X-Should-Retry: false` and no `Retry-After`. |
| `usage-limit` | The usage-limit reply below. |

In `usage-limit` mode, a request that ends with a rate or quota refusal, or finds every account
blocked, gets the usage-limit reply when each account that serves its model was blocked last by a
quota refusal whose reset the upstream named (Codex's `usage_limit_reached`, an OpenAI-compatible
`insufficient_quota` with `Retry-After`, a Claude subscription's used-up window, Google's
`QUOTA_EXHAUSTED`), and the first reset is more than 5 minutes away. For each account, core looks
at the account's own blocks first: the quota block on the whole account that ends last counts when
no block of another kind on the account ends as late, and its reset is the account's. Otherwise
the blocks on the model and the endpoint are looked at with the account's, by the same rule.

The reply is a 429 that tells the client to stop until the reset rather than retry: no
`Retry-After`, `X-Should-Retry: false`, and, when the upstream said how long the used-up window is,
Codex's `X-Codex-Primary-*` headers (`X-Codex-Secondary-*` for a window of a day or more) with
`Used-Percent` 100, `Window-Minutes` and `Reset-At`. A Responses WebSocket turn that fails this way
closes the socket without a message. OpenAI, Responses and Gemini clients get this body:

```json
{"error": {"code": "model_cooldown", "type": "usage_limit_reached", "retryable": false,
  "message": "All credentials for model gpt-5 are cooling down via provider codex (last error: usage_limit_reached: The usage limit has been reached)",
  "model": "gpt-5", "provider": "codex", "resets_at": 1767225600, "reset_seconds": 7200, "reset_time": "2h0m0s",
  "limit_window_minutes": 300, "last_upstream_error": "usage_limit_reached: The usage limit has been reached"}}
```

Anthropic clients get a `rate_limit_error` whose message ends with `(reset after 2h0m0s)` and that
carries `resets_at` and `retryable: false`. `provider` (the extension's name without `pluxy/`) is
there only when every account belongs to one extension, and `limit_window_minutes` only when the
window's length is known. `last_upstream_error` and the message's last error have credentials,
tokens and local paths removed. A stream that has already started ends with the same error, and a
Chat Completions stream then with `data: [DONE]`.

## Fallback chains

A fallback chain lists, under a model name clients ask for, the steps a request for that name goes
through in order. Each step is a model, and optionally the one provider (`provider`, an extension's
ID) and the accounts (`accounts`, their IDs) that serve it. A step that names neither is served the
way a name with no chain is: by one provider's accounts. Chains are kept in
[`routing.chains`](config.md):

```json
{"routing": {"chains": {
  "claude-opus-5-5": [{"model": "claude-opus-5-5", "provider": "pluxy/claude"}, {"model": "gpt-6.1-sol"}]
}}}
```

A request uses the first step's accounts, by priority and strategy as above. It moves to the next
step only when no account of the step can serve it now: each is blocked, at its concurrency limit,
or refused the request in a way that is safe to try elsewhere. Moving on needs no wait. When no
step can serve now, the request waits, within `max_wait_ms`, for the first account of any step to
come back. It never moves once its reply has started, nor for an error about the request itself. A
session bound to an account stays on that account and its step's model while it can serve, even
once an earlier step could serve again.

The rules for a chain:

- It has at most 8 steps.
- A step names a model, never another chain. A step may name the chain's own name: that is the
  model of that name.
- No step repeats another. Two steps that come to the same account and model (`m@pluxy/claude`,
  then `m` served by Claude) try it once.
- A chain's name may be one no account serves, such as `my-fast`. It is then in the model lists,
  as its first served step's model.

| Command | Management API | What it does |
| --- | --- | --- |
| `pluxy chain list` | `GET /v0/management/chains` | The chains, by name. |
| `pluxy chain set <name> <step>...` | `PUT /v0/management/chains/{name}` | Sets the chain, `{"steps": [{"model", "provider", "accounts"}]}`, and writes it to the config file. |
| `pluxy chain remove <name>` | `DELETE /v0/management/chains/{name}` | Removes the chain: the name goes back to one provider's accounts. 404 when there is none. |

On the command line a step is `model[@provider][:account,account]`. What follows the last `@` is
the provider only when it is an extension's ID (`publisher/name`), so `claude-opus-4-7@20260416` is
a model. A model whose name holds a colon ends with an `@`: `llama3:8b@`, or
`llama3:8b@pluxy/ollama:a1` with a provider and an account.

Setting a chain fails with 422 (the error names the step) for:

- no steps, or more than 8;
- a step with no model, a `provider` that is not an extension ID, or an empty account ID;
- a step that names another chain, or repeats another step;
- a step that no account in service serves now;
- a step that names an account core does not know, or one that does not serve the step.

A named account that serves the step but waits for a sign-in or is turned off is accepted, as long
as an account in service serves the step too. Under the `weighted` strategy an account with
`weight` 0 serves no step. A `config.yaml` cannot hold chains, so setting one there fails with 409.

The reply of a request served through a chain names the account, provider and model of the step
that served it (the `X-Pluxy-*` headers, when `routing.explain` is on), and its usage record names
the step (`chain_step`, counted from 1). An error's `left_out` entries then also name each
account's `provider` and `model`, since one account can serve two steps. `pluxy route` shows the
chain and the step of each account.

## Window ignition

A 5-hour usage window starts with the account's first request after the last window reset.
Window ignition starts an account's next 5-hour window right after the last one resets, instead of
at the first real request, so the window's reset comes sooner. Core sends the account one small
request for this: the one `pluxy account test` sends (`Hi`, at most 16 output tokens), straight to
the account's extension, leaving routing and its state alone. Ignition runs only between set
hours. It is off by default, and set in [`routing.ignition`](config.md):

```json
{"routing": {"ignition": {
  "enabled": true,
  "start_hour": 7, "end_hour": 22,
  "extensions": {
    "pluxy/claude": {},
    "pluxy/codex": {"model": "gpt-6-luna"}
  },
  "accounts": {"work-claude": {"enabled": false}}
}}}
```

- `start_hour` and `end_hour` are in the machine's local time zone. Ignition runs from
  `start_hour`:00 to `end_hour`:00 plus `end_grace_ms` (30 minutes by default). An `end_hour` not
  after `start_hour` is the next day's: 22 to 6 runs over midnight, and 0 to 0 runs all day. The
  defaults are 7 and 22.
- `extensions` lists the extensions whose accounts ignition runs for, by extension ID. Each entry
  takes `enabled` (`false` leaves it out), `model` (the model the request names) and `windows`
  (below).
- `accounts` holds one account's own switch and model, by account ID. An account's `enabled` wins
  over its extension's: `true` turns ignition on for the account even when its extension is not
  listed, and `false` turns it off.
- `grace_ms`, `retry_ms`, `backoff_multiplier`, `max_failures` and `hold_ms` set the waits and
  retries described below; the [config reference](config.md) has their defaults.

### Which windows are started

Core reads each such account's usage from its provider (the extension's `quota` call, as the
checks of long blocks do):

- when ignition turns on for it, and when the settings or the accounts change;
- a `grace_ms` (3 seconds by default) after each window's reset;
- a `hold_ms` (1 minute by default) after each ignition request;
- when a window's wait ends (a retry, the start of the hours, the end of the account's block);
- a `retry_ms` after a reading that failed;
- an hour after a reading that found nothing to wait for.

An account whose provider gives no usage reading is not read again until the settings or the
accounts change.

From each reading it takes the windows to start: every usage window of five hours, whether it
counts all the account's requests or those of one group of models. A provider with only one usage
window has that one started, whatever its length. A weekly window, or one model family's, is never
started. An extension's `windows` narrows this: it maps window IDs to models, and when it is set,
only the windows it lists among those are started. A window's ID is the `id` of its limit in
`pluxy account quota <id> --json` (for example `five_hour` for Claude and Codex).

A window is started only when all of these hold:

- It is not running: its reset has passed, or the reading gives none, or it is a window nothing has
  used, whose reset is always the reading's time plus its length (within 10 seconds), as some
  providers report a window that has not started.
- The time is within the hours. A window that resets outside them is started at the next
  `start_hour`:00.
- The account is in service and can serve now (its health is `ok`). An account that is turned off
  or waiting for a sign-in is not read at all, and one that is cooling, exhausted or banned waits
  until its block ends (a `retry_ms` when no end is known).
- No other usage window that covers the same requests (the account's, or the same group's) is
  full. A request would start nothing then, so the window waits for that one's reset.
- No retry's wait or pause holds it.

### Failures and pauses

A window still not running a `hold_ms` after the request counts as a failed ignition, as does a
request that failed. After a failure, core waits `retry_ms` (5 minutes by default) before the next
try, multiplied by `backoff_multiplier` (3 by default) for each further failure in a row: 5
minutes, then 15. After `max_failures` failures in a row (3 by default), the window's ignition is
paused until the next `start_hour`:00. A failure that retrying cannot mend (a refused key or
account, a missing model, a refused request, or no model to choose) pauses the window at once.
Every reading that shows the window running clears its failures and pause, and so does a change of
the settings (setting a model after "no model to choose", for example).

### The model it asks

The model a window's request names is the first that is set of: the model the extension's
`windows` gives that window, the account's `model`, and the extension's `model`.

With none set, core picks a small model from the models the account serves (as
`pluxy account models <id>` lists them):

1. It leaves out excluded models, and models with an ID part (split at `-`, `.`, `_` and spaces)
   that starts with `image`, `audio`, `tts`, `embed`, `thinking`, `review`, `realtime`,
   `transcribe` or `search`. For a window that counts a group of models, it keeps only models of
   the group's families, such as `gemini` or `claude`.
2. It takes the first of `haiku`, `luna`, `flash-lite`, `lite`, `mini`, `nano`, `flash`, `oss` and
   `small` that is a whole part of a model ID (so `gemini` is not a `mini` model), and among the
   models that word names, the newest by the numbers in their IDs (a run of 6 digits or more, a
   date, does not count).
3. With none of those words, it takes the first model left.

When the account's models cannot be listed, the ignition fails. When no model is left to choose,
the window is paused until a model is set.

### Seeing what ignition does

Each ignition request goes into the [usage record](usage-and-logs.md) with `endpoint` `ignition`,
which no client request has, and no `key_id`. Core logs each one (`ignition: account "work": sent
the request that starts window five_hour, with claude-haiku-5-5`, or why it failed).

`pluxy account list --json` (`GET /v0/management/accounts`) shows each account's `ignition`: for
each window its `id`, `state`, `next` (when core starts it next), `why`, `failures`, and the
`last` request's time, `model` and `error`; and an `error` when the usage cannot be read.

| `state` | Meaning |
| --- | --- |
| `running` | The window is running. |
| `waiting` | Waiting for the hours, the account, a full window or a retry. |
| `starting` | The request was sent and core is checking that the window started. |
| `paused` | Paused after failures, until the next `start_hour`:00 or a change. |

What ignition knows is kept in memory only. After a restart it reads each account at once, and a
window that has reset is started then, inside the hours. A config reload applies a change at once.

## Outbound proxy

[`proxy_url`](config.md) sends an extension's upstream traffic through a proxy: model requests,
WebSocket connections, token renewals and sign-ins alike. Set it at the top level for every
account, or on an account for that one:

```json
{
  "proxy_url": "socks5://user:password@proxy.example:1080",
  "extensions": {"pluxy/codex": {"accounts": [
    {"id": "work", "proxy_url": "http://proxy.example:3128", "...": "..."},
    {"id": "home", "proxy_url": "direct", "...": "..."}
  ]}}
}
```

- A value is an `http://`, `https://`, `socks5://` or `socks5h://` URL, with a user name and
  password when the proxy needs them, or `direct` or `none` for no proxy. Any other value fails the
  config load with a message.
- An account's own value wins. An account set to `direct` goes without a proxy even when the
  top-level one is set.
- A sign-in that adds an account uses the `proxy_url` it names, else the top-level one, so the
  first sign-in goes through the proxy too.
- With a SOCKS5 proxy, the proxy resolves host names, for `socks5` as for `socks5h`.
- Extensions run with a cleared environment, so `HTTP_PROXY` and `ALL_PROXY` never reach them:
  `proxy_url` is the only way to set a proxy.

The user name and password are secret. The management API shows the URL with `redacted` in their
place (`socks5://redacted@proxy.example:1080`), and they are removed from what extensions log and
send back. An extension older than protocol 0.11 cannot use a proxy, so core does not send it the
accounts that have one: their requests go to other accounts, and the error says why.

## Upstream headers

An account's [`headers`](config.md) adds HTTP headers to every upstream request of that account,
such as the key a gateway or relay in front of the provider asks for:

```json
{"extensions": {"pluxy/openai-compatible": {"accounts": [
  {"id": "relay", "headers": {"X-Gateway-Key": "...", "X-Title": "pluxy"}, "...": "..."}
]}}}
```

They go on the same traffic as the [outbound proxy](#outbound-proxy): model requests and their
streams, WebSocket handshakes, token renewals, sign-ins, model lists, checks and quota reads. A
configured header replaces the extension's own value for that name (letter case aside); the
extension's other headers stay. A sign-in that adds an account uses the `headers` it names.

The config load fails, with a message that names the header but never its value, for `Host`,
`Content-Length`, `Transfer-Encoding` or `Connection`, a name that is not a valid header name or is
given twice (letter case aside), an empty value, and a value with a line break.

A redirect to another host, or from `https` to `http`, carries the headers that are not secret
(below) but not the secret ones, even one the provider set itself under that name, as an HTTP
client leaves out `Authorization` there.

`pluxy account set <id>` and `PATCH /v0/management/accounts/{id}` with `headers` as an object
replace all the account's headers (one left out is removed); `null` or `{}` removes them all.

### Copying a header from the client

A value of `$` followed by a header name copies that header from the client's request. OpenCode
Go, for example, refuses a request that carries no session header; OpenCode sends one as
`x-opencode-session` and the Codex CLI as `session-id`:

```json
{"id": "opencode-go", "headers": {"x-opencode-session": "$x-opencode-session", "session-id": "$session-id"}, "...": "..."}
```

Core fills in such a value on the traffic of each client request: model requests and their
streams, token counts, endpoint calls, and a WebSocket session (whose client request is the
client's handshake). It takes the first value of the client's header of that name, letter case
aside. It leaves the header out when the client did not send it, sent it blank, or sent it with a
control character other than a tab; the extension's own value for that name then stays. Traffic
that serves no client request (token renewals, sign-ins, model lists, checks and quota reads) leaves
every copied header out. Only the headers an account names this way leave core.

The config load fails for a copy of `Authorization`, `Proxy-Authorization`, `X-Api-Key`,
`X-Goog-Api-Key` or `Cookie`, since they hold the client's key for pluxy, a proxy password or the
client's cookies. It fails too for a value that starts with `$` but is not `$` and a header name,
and for a value that holds `$CPA-SESSION-ID` (a CLIProxyAPI session ID, which pluxy does not have).
Any other value is sent as written, a `$` later in it included.

### Secret header values

The value of a header whose name is about authentication (it holds `auth`, `key`, `token`,
`secret`, `cookie`, `session`, `password`, `passphrase` or `signature`, letter case aside) is a
secret, by the rule config redaction uses. `GET /v0/management/config` and
`GET /v0/management/accounts` show a placeholder in its place. A value written as `true` or
`false` is the exception: `GET /config` shows it as written, while `GET /accounts` shows a
placeholder. A placeholder sent back keeps the stored value. A secret value is removed from what
extensions log and send back.

A copied header counts by the same rule: under such a name the `$` value shows as a placeholder,
and the value copied for a request is removed from what that request's call sends back. Core writes
no header value in its own logs or errors. An extension older than protocol 0.12 cannot send
headers, so core does not send it the accounts that have them: their requests go to other accounts,
and the error says why.

## Payload rules

[`payload`](config.md) sets, fills in or removes fields of the JSON body an extension sends
upstream, for the requests its rules name. The five lists and every rule field have the names
CLIProxyAPI uses, so a `config.yaml`'s `payload` (or `requests.payload`) works as written:

```json
{"payload": {
  "override": [{"models": [{"name": "gpt-5.4-fast", "protocol": "codex"}], "params": {"service_tier": "priority"}}],
  "filter": [{"models": [{"name": "gpt-*", "protocol": "codex"}], "params": ["tools.#(name==\"apply_patch\")"]}]
}}
```

| List | What it does |
| --- | --- |
| `default` | Writes a field the body does not have. The first rule that writes it wins. |
| `default-raw` | As `default`, with the value given as JSON text. |
| `override` | Writes a field over what is there. The last rule that writes it wins. |
| `override-raw` | As `override`, with the value given as JSON text. |
| `filter` | Removes fields. |

They run in that order, on the body as the extension finally sends it, once per upstream request:
a request tried again on another account gets them again on its new body, never twice on one. Each
extension applies them as it sends; core sends it only the rules that apply to the request.

A rule applies when any entry of its `models` matches:

| Field | Matches |
| --- | --- |
| `name` | The model the request reaches the upstream as, or the one the client asked for (before an alias or prefix, with its thinking suffix or without it). `*` matches any text. |
| `protocol` | The extension: `openai` (`pluxy/openai-compatible`), `claude`, `codex`, `gemini` (Gemini API keys and Vertex), `kimi`, `xai`, `meta`; for another package, the last part of its ID. Kimi is `kimi`, not `openai`, though CLIProxyAPI counts it as `openai`. |
| `from-protocol` | The API the client called: `openai` (Chat Completions, legacy Completions), `responses` (also compaction), `claude`, `gemini`, `interactions`, `codex-alpha-search`, `openai-image`. |
| `headers` | The client's request headers, each value a pattern with `*`. Core checks them; they are not sent to the extension. |
| `match`, `not-match`, `exist`, `not-exist` | The body about to go upstream. |

Paths are dotted keys (`\.` for a dot in a key), array indexes, and the queries `#(...)` (the first
match) and `#(...)#` (every match). A path with anything else (wildcards, `|`, `@`, `#` counts,
`-1`) or a raw value that is not JSON fails the config load, a reload or a settings change, with a
message that names the rule and the path.

A default checks the body the extension built, so a field the extension fills in itself counts as
present. An example is the Claude extension's default `max_tokens` (the model's output limit from
its built-in model list, or 32000 for an unlisted model) for a client that sets none. To replace
such a value, use `override`.

A turn on an upstream WebSocket (a Codex Responses WebSocket) gets the rules too: they apply once to
the `response.create` message the extension sends for the turn, selected for that turn as for any
request. A message that is not a turn, such as the client's interrupt, goes upstream as it is. A
client WebSocket turn that goes upstream as an HTTP request gets them as an HTTP request does.

Rules do not reach Devin, whose upstream body is protobuf. An extension older than protocol 0.14
cannot apply rules (0.15 for a turn on an upstream WebSocket), so a request that rules apply to is
not sent to it: it goes to other accounts, and the error says why. The protocol side is in the
[extension protocol](../next/protocol/README.md#payload-rules).

## Account checks

When core starts, when a reload adds or changes an account (its settings, `proxy_url`, `headers`,
credential or extension), and when a sign-in adds one, core checks the account. Banned accounts are
not checked. Core asks the account's extension
whether the account works, and the extension makes one call upstream that costs the account
nothing, such as reading its model list. Nothing is generated, and the credential is not renewed
for it. Up to 4 checks run at once, each for at most 30 seconds, in the background: core serves
requests while they run, and the result does not change routing.

`pluxy account list --json` (`GET /v0/management/accounts`) shows each account's last `check`:

| `check.state` | Meaning |
| --- | --- |
| `checking` | The check is running. |
| `ok` | The account works; `detail` says what was checked. |
| `failed` | It does not; `error` says why (code `auth` for a refused key, `unavailable` for an upstream that cannot be reached). |
| `not_checked` | The extension cannot check it without generating, has no check, or the credential has expired (core renews it for the next request, not for a check); `detail` says which. |

Each extension checks with its upstream's model list, and only accounts with an API key:

| Extension | Check |
| --- | --- |
| `pluxy/openai-compatible` | `GET {base_url}/models`. An upstream without that list (404) leaves the account not checked. |
| `pluxy/claude` | `GET {base_url}/v1/models`. A subscription sign-in is not checked. |
| `pluxy/gemini` | `GET {base_url}/v1beta/models`. A Vertex account is not checked. |
| `pluxy/xai` | `GET {base_url}/models`. A Grok sign-in is not checked. |

The other bundled extensions do not check accounts. The protocol side is the
[`check` operation](../next/protocol/README.md#check).

Core also logs the result of each check the extension ran (`account "work": check passed: ...`,
`check failed: ...` or `not checked: ...`). An account left unchecked because its extension has no
check, or because its credential has expired, gets no log line.

### Secrets in errors and logs

The check's result and its log line, like everything else a call on an account sends back and the
extension's own output, show `[redacted]` in place of the account's credential and of each secret in
its settings (the values `GET /config` and `GET /accounts` show as placeholders), such as a
`base_url` with a token in its query that an unreachable upstream's error quotes. A settings secret
shorter than 8 bytes is left in the text, since removing every occurrence of so short a string would
mangle unrelated text.

Upstreams that refuse a key often echo its start (`invalid x-api-key: sk-ant-api03-AbCdEf...`),
which removing whole secrets misses. So an error's message and the string values of its raw body,
from every extension and for every call (requests, checks, quota and model reads, renewals,
sessions and sign-ins), and the values of its rate-limit headers, also lose every run of token
characters that holds 4 or more consecutive characters of one of the account's secrets, or 8 or
more of a long token or of a PEM private key's body:

- **Token characters** are letters, digits and `_-.+/=~`, and any other character such a secret
  holds, except whitespace, control characters, quotes, square and curly brackets, commas and
  backslashes. The text
  is read as written, and with JSON and percent escapes decoded.
- A **long token** is one trimmed run of 32 or more token characters that is not base64 of text.
- **The account's secrets** are the credential's key and tokens, secret header values, the proxy's
  user name and password, and the secrets core finds in the settings (in a URL, only its user name,
  password and secret query or fragment values). On a sign-in they include the inputs it marks
  secret (the protocol's [Accounts](../next/protocol/README.md#accounts) section says how).
- A typed plain address that a sign-in does not mark secret (an `http` or `https` URL with no query,
  fragment or whitespace, whose host and path hold only plain URL characters, such as a base URL)
  counts only by its user name and password, so its scheme, host,
  port and path show in an error, and a short secret placed in its path would show too. Any other
  value the sign-in does not mark secret, and an address with a query or fragment, counts whole.

A value counts by its parts: only a scheme, a public prefix, a real cookie list's names, and Basic's
user name beside a password stay out of the pieces. A value that is one `name=value`, or holds one
behind a scheme or prefix, counts that pair whole as well as by its value, except in the `Cookie`
header. A list with anything in it but pairs with values counts whole too. A header, proxy or
settings secret of 8 bytes or more with no part of 8 bytes or more that nothing else splits counts
whole, so a word that shares 4 characters with its scheme or cookie name goes too. A known vendor's
public key prefix (`sk-proj-`, `sk-kimi-`, `xai-` and the others the code lists) gives no piece, so
`project` or `kimi` stays in an error for such a key; a secret of under 16 bytes after its prefix
keeps every piece. Any other word of the error that shares 4 characters with a secret (8 with a long
token) goes too, such as `token` for a header `Bearer gw-token-12345`. A reply that succeeded is
not changed by this rule, except the values of the rate-limit headers it starts with (a plain time
or duration among them stays).

## Model lists

Clients list the models they can ask for in their own API's shape, with a client key as for any
request:

| Request | Reply |
| --- | --- |
| `GET /v1/models` | OpenAI's list: `{"object": "list", "data": [{"id", "object", "created", "owned_by", "context_length", "max_output_tokens", "capabilities", "from"}]}`. A request with an `anthropic-version` header or Claude Code's user agent (`claude-cli/...`) gets Anthropic's list instead: each model's `display_name`, `created_at`, `max_input_tokens` and `max_tokens` (200000 and 64000 when not known), sorted by display name, with `has_more`, `first_id` and `last_id`. A request with a `client_version` query gets the [Codex client catalog](#codex-client-catalog). |
| `GET /v1/models/{id}` | One model from that list, or a 404 with code `model_not_found`. |
| `GET /v1beta/models` | Gemini's list, `{"models": [...]}`: each model's `name` (`models/{id}`), `displayName`, `description`, token limits and `supportedGenerationMethods` (`["generateContent"]` when not known). |
| `GET /v1beta/models/{name}` | One model, named with or without `models/`, or a 404. |

A list holds each model the accounts serve, once under each name a client can ask for it by, and
the name of each fallback chain no account serves, sorted by name. `pluxy model list` shows the
same models with the accounts that serve each.

### Where an account's models come from

- An account whose config lists [`models`](config.md) serves and lists those.
- An account without `models` serves and lists the models its extension says the account serves
  (the protocol's `models` operation), under the account's naming rules
  ([below](#model-names-and-aliases)). An extension that cannot say adds nothing for it.
- An account whose extension has no list, gives an empty one, or could not give one within 15
  seconds serves any model.

The same list decides routing: a request for a model no account serves is refused without calling
an upstream, with a 404 and code `model_not_found`.

### When lists are read again

| When | What happens |
| --- | --- |
| A list could not be read | It is asked for again a minute later. |
| Every 30 minutes, plus up to 3 minutes of random spread | Each known list is asked for again, so accounts do not all refresh at once. An empty list counts as known. |
| An account's credential is renewed, or it signs in again | That account's list is asked for again. |
| `pluxy model refresh` (`POST /v0/management/models/refresh`) | Every list is asked for again now; `--account` limits it to one account. |

Each read has 15 seconds. A refresh that fails, takes longer or comes back empty keeps the last good
list, and so does one that falls back to the extension's built-in list when the account's list was
read from the provider.

### Token limits and other details

For each model, core merges what three sources say: the account's upstream list, core's model
catalogue (from `models.dev`) and the extension's built-in list.

- **Token limits** (`context_length`, `max_output_tokens`): the smallest positive limit wins. The
  sources are read in the order above, so an equal limit stays with the earlier source.
- **Capabilities**: from the first of the account's list, the catalogue and the extension's list
  that has any; a later source adds only the reasoning levels and deprecation time an earlier one
  lacked.
- **Every other field** (display name, description, owner, version, modalities): from the first
  source that sets it, in the order the account's list, the extension's list, the catalogue.
- **`from`** names the source that gave the context limit, else the output limit, else the
  capabilities: `"account"`, `"catalogue"` or `"extension"`.

## Model names and aliases

An account names its models with these settings, applied in this order:

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

| Setting | Meaning |
| --- | --- |
| account [`models`](config.md) | The only models the account serves. Each is the upstream's name, or `{"name": ..., "alias": ...}` to serve the model `name` under the name `alias` only. One name clients use maps to one upstream model: the config check refuses an entry whose name or alias another entry already uses for a different upstream model, naming the alias and the account (in the config file, and as a 400 from `PATCH /v0/management/accounts/{id}`, which the panel uses). Listing the same model twice passes. |
| account [`excluded_models`](config.md) | Models the account does not serve: names, or patterns with `*` for any text (`gpt-5-*`, `*-mini`, `*preview*`), whatever their case. They match the name clients use for an entry of `models` (its `alias`, when it has one), and for any other model its name before `model_alias` renames it. |
| account [`model_alias`](config.md) | Renames: a request for `alias` (exactly as written) goes to the model `name` (any case). Without `fork` the model is served only as `alias`; with `"fork": true`, under both names. `display_name` replaces the model's display name in lists. |
| account [`prefix`](config.md) | The account also serves each model as `{prefix}/{model}`, so a client can ask for a model on that account alone. No `/` in it. |
| [`routing.force_model_prefix`](config.md) | `true`: an account with a prefix serves its models only with the prefix, except a model whose name is the prefix itself. |

An extension's settings (`extension`, or its entry in `extensions`) may also hold `model_alias` and
`excluded_models` for the accounts added to it by a sign-in, not those added with an API key. Its
`model_alias` rules come after an account's own, leaving out a rule for an alias the account
already has. Its `excluded_models` apply to an account that has none of its own.

## Codex client catalog

The Codex client asks for its own model catalog: `GET /v1/models?client_version=0.153.0` returns
`{"models": [...]}` instead of the normal model list. The `client_version` query, even with an
empty value, selects this reply, before the check for an Anthropic client. Without it the lists
above keep their usual shape.

Which models it holds:

- The models served by `pluxy/codex` accounts, with the same exclusions, aliases and prefixes as
  the normal list. A name outside a fallback chain is listed only when routing chooses `pluxy/codex`
  as the name's provider.
- Each fallback chain's name, when a step of it can go to a `pluxy/codex` account, as that step's
  model.
- Only models the Codex extension has a template for. A model without one is left out: a routing
  entry alone does not give the capabilities the Codex client needs.

Core sends one `codex.models` endpoint call with the client version and each model's public and
upstream names. The Codex extension loads its built-in catalog on the first such call and picks
templates by upstream name. It removes `cpa_capabilities`, replaces `slug` with the public name (and
`display_name` when one is given), and clears Codex-only options for chain names whose chain has a
step a non-Codex account can serve. The catalog keeps each template's `minimal_client_version`.
Clients below 0.144.0 get reasoning levels only up to `xhigh`; an empty or unreadable version keeps
the newer levels. The catalog data lives in the extension program: core neither embeds it nor keeps
a copy per account, and no upstream request or remote refresh is made for this list.

The call goes straight to the first enabled, usable Codex account in account order, with only its
ID and no credential. It ignores blocks and concurrency limits, renews no credentials, records no
routing result or usage record, and leaves the account rotation alone. With no enabled, usable
Codex account (including a config with only other extensions), the reply is 200 `{"models":[]}`.

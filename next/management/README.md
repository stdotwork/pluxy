# Management API

Everything that controls a running core goes through one HTTP API, `/v0/management`. The web panel, the
desktop app and the `pluxy` command are clients of it and can do nothing it cannot. The client API
(`/v1/...`) is separate and is not described here.

This file is the API's contract. Rows and fields marked **new** or **changed**, and commands that
`pluxy help` does not list yet, arrive in later changes; everything else is served today. The Go types
of every request and reply, and a client, are in this package (`types.go`, `client.go`); core's handlers
write the same types.

## Rules that hold for every call

- **Address.** The core's `listen` address, path prefix `/v0/management`.
- **Who may call.** `Authorization: Bearer <management_key>`. Calls from another machine are refused with
  403 `remote_disabled` unless `management_allow_remote` is on.
- **A core always has a management key.** A core started with no config file writes one (`pluxy init`
  does the same without starting): it listens on `127.0.0.1`, with one new client key and a new
  management key. A config that sets no management key is refused at start with a message that says how
  to add one; the API is never silently off.
- **Wrong keys.** Five wrong keys in a row from one address block that address for 30 minutes (403
  `blocked`, `Retry-After`). A call with no key at all is a 401 and does not count. A call from this
  machine is never blocked, so the person at the keyboard can always get back in.
- **Bodies.** JSON in, JSON out, UTF-8. Unknown fields in a request are a 400. Times are RFC 3339 UTC;
  durations are whole milliseconds in fields ending `_ms`.
- **Errors.** Every failure is `{"error": {"code": "...", "message": "..."}}` with the HTTP status that
  fits; `code` is stable and `message` is for a person. An error about an upstream account may add
  `status`, `provider_code`, `retryable`, `retry_after_ms`.

  | Status | Codes | Meaning |
  | --- | --- | --- |
  | 400 | `invalid_request` | The request does not parse or names a field or value the call does not take |
  | 401 | `unauthorized` | No key |
  | 403 | `forbidden`, `remote_disabled`, `blocked` | Wrong key, another machine, or blocked |
  | 404 | `not_found` | No such account, package, chain, key or log |
  | 409 | `conflict` | The thing changed since it was read, or the change would leave core unusable (the last client key) |
  | 422 | `refused` | Core checked the change and will not apply it; `message` says why; nothing was changed |
  | 502 | `upstream` | A registry or provider could not be reached |
  | 503 | `unavailable` | The extension that would answer is not running |

- **Secrets.** No reply holds a credential, a client key or the management key, except the one reply that
  creates a key. Account IDs and labels are shown.
- **Changes are whole.** A call that changes something is applied completely or not at all, through the
  same steps as a config reload; requests already running finish on what they started with.
- **Compatibility.** `GET /status` has `management_api`, a number that goes up when a call changes in a
  way an older client would misread. Fields are only added between such changes.

## The `pluxy` command

`pluxy` is one program: `pluxy serve` runs the core, and every other subcommand is a client of the
management API of a core that is already running. `pluxy -config ...` with no subcommand is `pluxy serve`.

- **Which core.** `--config <path>` (default `config.json`) is read for the address and the management
  key; or `--url` and `--key` (or `PLUXY_URL`, `PLUXY_MANAGEMENT_KEY`) name a core on another machine.
- **Output.** For people by default: aligned columns, one thing per line, no colour when not a terminal.
  With `--json`: the API's reply body, unchanged, on standard output and nothing else. Errors go to
  standard error: a sentence, or with `--json` the API's error object.
- **Nothing asks a question** unless standard input is a terminal; `--yes` answers yes.
- **Exit codes.**

  | Code | Meaning |
  | --- | --- |
  | 0 | Done |
  | 1 | The call failed (any error not listed below) |
  | 2 | The command line is wrong (unknown subcommand or flag, missing argument) |
  | 3 | No core answered at the address |
  | 4 | Refused: no key, wrong key, another machine, or blocked (401, 403) |
  | 5 | Not found (404) |
  | 6 | Core refused the change (409, 422); nothing was changed |
  | 7 | Waiting for a person: a sign-in needs a browser or a code, and `--no-wait` was given |

## Resources

`{id}` of a package is `publisher/name`.

### Core

| Call | Command | What it does |
| --- | --- | --- |
| `GET /status` | `pluxy status` | Config versions (wanted and serving), each extension's state, build version, `management_api`, and `first_run` |
| `POST /reload` | `pluxy reload` | Reload the config now. 422 with the status when refused |
| `GET /config`, `POST /config/validate`, `PUT /config` | `pluxy config show`, `pluxy config validate <file>`, `pluxy config apply <file>` | The config file with secrets replaced by placeholders; check a document; replace the file |
| `GET /settings`, `PATCH /settings` | `pluxy settings`, `pluxy settings set <name> <value>` | The basic and routing settings by name |
| `GET /client-keys`, `POST /client-keys`, `DELETE /client-keys/{id}` | `pluxy key list`, `pluxy key add`, `pluxy key remove <id>` | Client keys. `id` is stable across restarts. `add` prints the key once |
| `POST /management-key/rotate` | `pluxy key rotate-management` | A new management key, printed once |
| `GET /logs`, `DELETE /logs`, `GET /request-error-logs[/{name}]`, `GET /request-log-by-id/{id}` | `pluxy logs [--after n]`, `pluxy logs clear`, `pluxy logs errors [name]`, `pluxy logs request <id>` | Core's log and request logs |

### Setup

Setup is what a new install shows first. Core looks at the machine it runs on, says what it found, and
proposes what to install. Nothing is installed until asked.

`first_run` is true only while there is no `state/setup.json` and core has no installed packages and no accounts; once `setup.json` exists, its `first_run` is kept. Core scans on its own only at a first run's start with no kept scan; otherwise only on `POST /setup/scan`.

| Call | Command | What it does |
| --- | --- | --- |
| `GET /setup` | `pluxy setup` | The last scan, at once (it is kept on disk): `first_run`; `scanning`; `scanned_at`; `found` (below); `picks` (below). The command prints it and, on a terminal, offers to apply the picks marked `suggested` (`--yes` applies without asking) |
| `POST /setup/scan` | `pluxy setup scan` | Looks again. Returns at once with `scanning: true`; read `GET /setup` until it is false (each probe has 4 seconds) |
| `POST /setup/apply` | `pluxy setup apply [--pick id]... [--yes]` | `{"picks": ["pluxy/claude", ...]}`: installs and enables each. Returns `{"results": [{"id", "status", "package", "error"}]}` with one result per pick (`done`, or `error`); one failure does not undo the others |
| **new** `POST /setup/import` | `pluxy import cpa <config.yaml> [--auth-dir <dir>] [--dry-run]` | Brings a CLIProxyAPI setup across and returns the import report (below). With `dry_run` nothing is written |
| `POST /setup/done` | `pluxy setup done` | Ends first run (`{"first_run": false}`): the panel and app stop opening on setup |
| — | `pluxy client show <tool> [--key-id id]` | Prints what to paste into that client tool's config to point it at this core (address and a client key placeholder; never prints the management key) |

`found`:

```json
{
  "clients": [
    {"id": "claude-code", "name": "Claude Code", "installed": true, "version": "2.4.1",
     "config": "~/.claude/settings.json", "state": "not-configured"}
  ],
  "sign_ins": [
    {"provider": "pluxy/codex", "from": "codex", "label": "Codex is signed in on this machine"}
  ],
  "imports": [
    {"kind": "cliproxyapi", "config": "~/.cli-proxy-api/config.yaml", "auth_dir": "~/.cli-proxy-api", "accounts": 7}
  ],
  "core": {"listen": "127.0.0.1:8317", "reachable_from_network": false}
}
```

A client's `state` is `not-configured`, `configured` (it points at this core), `needs-update`, or **new** `refused` (a file the desktop app's writer for that tool would refuse to change; no pick is suggested to apply to it). `sign_ins`
only says a sign-in exists; core reads no token from it.

`picks`, in the order to show them:

```json
[
  {"id": "pluxy/claude", "kind": "provider", "title": "Claude", "summary": "Claude subscriptions and API keys",
   "installed": false, "suggested": true, "because": "Claude Code is installed on this machine"}
]
```

`suggested` is true only for a pick that something found on the machine points to. A machine where nothing
was found gets the same list with nothing suggested.

The import report:

```json
{
  "dry_run": true,
  "brought": [{"what": "account", "name": "codex-work@example.com", "as": "pluxy/codex account codex-work"}],
  "changed": [{"what": "server.host", "from": "(not set: every interface)", "to": "127.0.0.1",
               "why": "pluxy listens on this machine only unless told otherwise", "keep_with": "server.host: 0.0.0.0"}],
  "not_brought": [{"what": "auth file", "name": "gemini-user.json", "why": "pluxy has no Gemini CLI sign-in"}],
  "needs": [{"pick": "pluxy/codex", "why": "3 accounts need it"}]
}
```

Every key of the config and every file of the auth folder is in exactly one of `brought`, `changed` and
`not_brought`. An import whose report could not account for an item fails instead of dropping it.

### Extensions

| Call | Command | What it does |
| --- | --- | --- |
| `GET /packages` | `pluxy ext list` | Installed packages and their state |
| `GET /registries` | `pluxy ext available` | What each registry offers, with each package's `title`, `summary`, `kind` (`provider`, `client`) and `tier` (`pick`, `verified`, `unverified`) when its registry says them; core enforces nothing by tier. A first start installs nothing: every package, the bundled ones too, is installed by `POST /packages` |
| `POST /packages` | `pluxy ext install <id> [--version v] [--registry r]` | Install and enable |
| `POST /packages/{id}/enable`, `.../disable` | `pluxy ext enable <id>`, `pluxy ext disable <id>` | Start or stop routing to it |
| `DELETE /packages/{id}` | `pluxy ext remove <id>` | Remove its files; its accounts stay |
| `GET`, `PATCH /extensions/{id}/config` | `pluxy ext config <id> [name=value]...` | An extension's own settings |

### Accounts

| Call | Command | What it does |
| --- | --- | --- |
| `GET /accounts` | `pluxy account list` | Every account, with **new** `health` (below) and `public_id`: the short stable ID client replies name the account by, never an email address or a label (below) |
| `GET /login-methods` | `pluxy account methods` | How each running extension can add an account |
| `POST /accounts`, `GET /logins/{id}`, `POST /logins/{id}/input`, `DELETE /logins/{id}` | `pluxy account add <provider> [--method m] [--no-wait]` | Start a sign-in and follow it to the end; prints the address to open or the code to type |
| `POST /accounts/{id}/login` | `pluxy account login <id>` | Sign in again |
| `PATCH /accounts/{id}` | `pluxy account enable <id>`, `pluxy account disable <id>`, `pluxy account set <id> name=value...` | Turn on or off; change routing settings |
| `DELETE /accounts/{id}` | `pluxy account remove <id>` | Remove |
| `POST /accounts/{id}/refresh` | `pluxy account refresh <id>` | Renew its credential now |
| `POST /accounts/{id}/cooldown/reset` | `pluxy account clear <id>` | Forget what routing holds against it |
| `POST /accounts/{id}/test` | `pluxy account test <id> [--model m]` | One small request through it |
| `GET /accounts/{id}/quota` | `pluxy account quota <id>` | Its usage windows as its provider reports them |
| `GET /accounts/{id}/models` | `pluxy account models <id>` | The models it serves |
| `POST /accounts/import`, `POST /vertex/import` | `pluxy account import <file>` | Add an account from a credential file |

`health` on every account:

```json
{"state": "exhausted", "until": "2026-11-19T17:00:00Z", "why": "usage window used up",
 "source": "provider", "next_check": "2026-11-19T12:15:00Z"}
```

| `state` | Meaning | Comes back |
| --- | --- | --- |
| `ok` | Serving | |
| `cooling` | A short block: rate limit or a failing endpoint | When `until` passes, or a request succeeds |
| `exhausted` | A usage window ran out | At its reset (`until`), or sooner when a check finds it has room |
| `needs_login` | The provider refused the sign-in | `pluxy account login` |
| `banned` | The provider closed or suspended the account | Only when a person enables it or signs in again |
| `disabled` | Turned off by a person | `pluxy account enable` |

`source` is `provider` when `until` is the provider's own word, `backoff` when core chose it, and
`unverified` when the provider named a time core does not believe (more than 8 days away); such a block is
held 30 minutes and checked within 15. A block on one of the account's models counts too, and `why` then
names the model, except a model the account does not have (a 404), which says nothing about the account;
the blocks themselves, that one too, stay in `cooldowns`. When several blocks hold the account, an
`exhausted` one decides, then the one that ends last. `next_check` is when core next reads the account's
usage from its provider; it is absent when no check is due, and while a check of an account without a usage
reading waits for the one request it lets through. `pluxy account list` shows `state` (with
`(unverified)`), `until` and `why` as its HEALTH, UNTIL and WHY columns.

`public_id` is `acc_` and 8 hex digits, a keyed digest of the account's ID: an ID can be an email address or a label a person typed (an import of today's auth files names an account by its file), and an ID must not leave core in a client reply. The key is made at the first start and kept in `state/account-key` beside the config, so an account keeps its public ID across restarts. `X-Pluxy-Account`, the header a request pins an account by, takes either ID.

### Models

| Call | Command | What it does |
| --- | --- | --- |
| `GET /models` | `pluxy model list` | Every model clients can ask for, the accounts that serve it, and **new**: `context_length`, `max_output_tokens`, `capabilities`, and `from` (`account`, `catalogue` or `extension`: where the numbers came from), `listed_at` |
| `POST /models/refresh` | `pluxy model refresh [--account id]` | Ask the providers again now. Lists refresh by themselves every 30 minutes; a list that cannot be read keeps the last good one |

### Routing

| Call | Command | What it does |
| --- | --- | --- |
| **new** `GET /chains` | `pluxy chain list` | The fallback chains: `{"chains": [{"name", "steps"}]}`, sorted by name |
| **new** `PUT /chains/{name}` | `pluxy chain set <name> <step>...` | `{"steps": [{"model": "...", "provider": "pluxy/claude", "accounts": ["..."]}]}`. A step on the command line is `model[@provider][:account,account]`. What follows the last `@` is the provider only when it is an extension's ID (`publisher/name`), so `claude-opus-4-7@20260416` is a model; a model whose name holds a colon ends with an `@` (`llama3:8b@`, or `llama3:8b@pluxy/ollama:a1` with a provider and an account). Replies with the chain, `{"name", "steps"}`. 422 for a step no account can serve (under `weighted`, an account with `weight: 0` serves nothing), a step naming an account core does not know or one that does not serve it, a provider that is not an extension's ID, more than 8 steps, a step naming another chain, or a step that repeats another; 409 for a config.yaml, which cannot hold chains |
| **new** `DELETE /chains/{name}` | `pluxy chain remove <name>` | Back to the default: accounts of one provider, the same model. 404 when there is no such chain |
| **new** `GET /route?model=<name>` | `pluxy route <model>` | Sends nothing. Says what a request for the model would do now: each account in the order it would be tried, and for each account left out, why and until when. `{"model", "order": [{"id", "account", "provider", "model"}], "left_out": [{"id", "account", "state", "until"}]}`: `id` is the account's ID (only this API shows it) and `account` its public ID, which client replies use; the rest are the names of the `pluxy` member below. Asking changes no routing state. For a model with a chain, `chain` holds its steps and each account its `step` (from 1), and `left_out` entries name their `provider` and `model`. A session's bound account is not shown yet |

### Usage

| Call | Command | What it does |
| --- | --- | --- |
| `GET /usage` | `pluxy usage [--since 7d] [--until t] [--by model\|account\|key\|provider] [--model m] [--account a] [--client-key k]` | Totals, breakdowns (`by` + `breakdown` alongside `models`, `providers`, `accounts`, `keys`) and a time series (`1h` buckets for ranges up to 2 days / 48 hours, `24h` beyond, unless `bucket` is given; `1m` / 60 steps when no range is given). **Changed**: reaches back across all usage files kept on disk (`usage.keep_days`, default 90), reading closed UTC days from hourly `usage/summary/YYYY-MM-DD.json` files (stays when `.jsonl` files are pruned) and today from memory / `usage/YYYY-MM-DD.jsonl`. Also accepts `key` as an alias for `key_id` |
| `GET /usage/events` | `pluxy usage events [--after seq \| --before [seq]] [--limit n] [--since t] [--until t] [--model m] [--account a] [--provider p] [--client-key k]` | The recorded requests, oldest first with `after` or newest first with `before`. **new** fields per event: `attempts`, `chain_step` |

Closed days are counted by whole UTC hours, so a `since` or `until` inside an hour on a closed day counts that whole hour.

### Themes

| Call | Command | What it does |
| --- | --- | --- |
| `GET /themes` | `pluxy theme list` | The selected theme ID (`current`) and the stored themes (`id`, `name`, `bytes`) |
| `GET /themes/{publisher}/{name}` | `pluxy theme show <id>` | One stored theme's JSON unchanged |
| `PUT /themes/{publisher}/{name}` | `pluxy theme add <file> [--id publisher/name]` | Store a theme JSON object (up to 256 KiB; at most 64 stored) |
| `DELETE /themes/{publisher}/{name}` | `pluxy theme remove <id>` | Remove a stored theme; clears `current` if it was selected |
| `PUT /theme` | `pluxy theme set <id>`, `pluxy theme clear` | Select a theme ID (even one the UI holds) or clear the selection |

## What a client request gets back (**new**)

Not part of the management API, listed here because the same names appear in `GET /route`:

- Every reply to a request that may use the client API has the trace ID (`X-CPA-TRACE-ID`, the name
  clients already know; today it is missing when no account was tried), and when an account served it
  `X-Pluxy-Account` (its public ID), `X-Pluxy-Provider`, `X-Pluxy-Model` (the name sent upstream) and
  `X-Pluxy-Attempts`. An error from an account's refusal carries them too, for the last account tried.
- A request that ends in an error before a reply starts gets its usual error, with one more member, in
  the error object of each client format (OpenAI, Anthropic, Gemini):

```json
{"error": {"code": "model_cooldown", "message": "...", "type": "rate_limit_error",
  "pluxy": {"trace": "20261119120000-9f2c0a5b7d4e1830",
    "tried": [{"account": "acc_4e26e0c2", "provider": "pluxy/codex", "model": "gpt-6.1-sol", "result": "quota"}],
    "left_out": [{"account": "acc_306dcbdb", "state": "exhausted", "until": "2026-11-19T17:00:00Z"},
                 {"account": "acc_0702c008", "state": "needs_login"}]}}}
```

`result` is the code of the error the attempt ended with, one of core's own codes (anything else reads
`error`). `state` is the account's `health` state, `busy` for an account at its `max_concurrency`, or
`skipped` for one the request's limits (`max_attempts`, `max_accounts`) ended it before reaching. The
member holds codes and times, never an upstream's words. An account turned off or waiting for a login is
named when its own `models` name the model, or when it shares its extension with an account that serves it.

`routing.explain: false` leaves out the headers and `pluxy`, for a core whose client keys are given to
people who should not see account IDs.

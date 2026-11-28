# Configuration reference

pluxy's settings are in one file, `config.json`. This page lists where that file is and how pluxy
finds it, every key core reads, every `PLUXY_` environment variable, and which changes apply
without a restart. Core also reads CLIProxyAPI's `config.yaml` ([config-yaml.md](config-yaml.md)).

Most settings can also be changed in the web panel, the desktop app and the
[`pluxy` command](cli.md), which write this file for you. Provider-specific account settings are
in [providers.md](providers.md); how requests pick accounts is in [routing.md](routing.md).

## Where the config is

### pluxy's home

pluxy's home is the one folder where a pluxy run by your user account keeps its files: `config.json`
and, beside it, the folders core makes (see [Folders beside the config](#folders-beside-the-config)).
It is:

| System | Folder |
| --- | --- |
| Linux and other Unix systems | `$XDG_DATA_HOME/pluxy`, which is `~/.local/share/pluxy` when `XDG_DATA_HOME` is unset or not an absolute path |
| macOS | `~/Library/Application Support/pluxy` |
| Windows | `%LOCALAPPDATA%\pluxy` |

`PLUXY_HOME` names another folder in its place, as an absolute path. `pluxy help` prints the folder
it finds.

### Which `config.json` is used

Without `-config` (or `--config`), every `pluxy` command uses the first of these:

1. `config.json` in the folder the command runs in, when there is one.
2. `config.json` in pluxy's home.

What each command does with it:

- `pluxy serve` serves it. When neither file exists, its first start writes one in pluxy's home (see
  [A first config](#a-first-config)), so the next `pluxy serve`, from any folder, finds the same
  one. A start that found its config this way logs which: `config: <path> (pluxy's home)` or
  `config: <path> (config.json in the current folder)`.
- `pluxy init` writes the config in pluxy's home and never replaces a file. In a folder that has a
  `config.json`, it says that file exists.
- The client commands (every other command; see [cli.md](cli.md)) take core's address and management
  key, each, from the first of: `--url` and `--key`; `PLUXY_URL` and `PLUXY_MANAGEMENT_KEY`; the
  config file. `--verbose` says on standard error which core a command called and where its address
  and key came from. With no config anywhere, the error names each place it looked in and what to
  run.

A management key given with `--key` or `PLUXY_MANAGEMENT_KEY` is not sent to an address that only
the current folder's `config.json` names, unless that file holds exactly the same key. When the file
loads and has an address, the command refuses, exits 2 and calls nothing, so a stray file in a folder
cannot steer your key to another machine. To use the pluxy that file names, leave out the key the
message names, so the key in that `config.json` is used; to use another core, add `--url`.

pluxy moves and copies nothing into its home. A core started with `-config` (the desktop app's own
core, or one in a [container](setup.md)) keeps its files beside that config.

The desktop app runs a core of its own: a separate pluxy with its own keys and accounts, whose
config is `core/config.json` in the app's data folder (on Linux
`~/.local/share/dev.pluxy.desktop/core/config.json`). While pluxy's home has no config and the app's
exists, a plain `pluxy serve` writes no first config, since that would make a second pluxy beside the
app's. It says so and names the two ways on: `pluxy serve -config <the app's config>` (while the app
is not running that core) and `pluxy init` (a new pluxy in pluxy's home). The client commands never
read the app core's config unless given it with `--config`.

### A first config

With no config file at the path it would use, `pluxy serve` writes one and starts on it. `pluxy init
[-config file] [-port n]` writes the same file without starting core, and never replaces an existing
one. The file holds:

```json
{
  "listen": "127.0.0.1:8317",
  "client_keys": ["<a new random key>"],
  "management_key": "<a new random key>"
}
```

It listens on `127.0.0.1` port 8317, or on a free port when 8317 is taken (`pluxy init -port 0`
always picks a free port). Only your user can read the file, and the folder is made readable only by
your user when pluxy makes it. Core logs the file's path and the address, never the keys: read them
in the file. A first start installs no package: install the providers you use from the panel's Setup
page or with `pluxy setup apply --pick <id>` (see [setup.md](setup.md)).

The first config is written as JSON only. A `config.yaml` is written by a person.

The first config holds the management key, so pluxy checks the folder it goes into before writing
it, whether that is pluxy's home or the folder of a `-config` path. A missing folder is made so only
your user can read it. A folder that is already there, such as one `PLUXY_HOME` names, is refused
when it is a link, belongs to another user, or (on Linux and macOS) lets group or others write in
it: nothing is written, and the command exits 1 saying why and what to do (for the last case,
`chmod go-w <folder>`, or another folder). On Windows a folder other accounts can open is given the
private access list of the [credentials folder](providers.md#how-accounts-added-by-sign-in-are-kept-and-renewed)
instead. Only that folder is checked, not the folders above it; on macOS only by its permission
bits, not an access list added with `chmod +a`; and only when a first config is written: a config
that is already there is used without the check.

### Folders beside the config

Core makes these folders next to the config file it runs on:

| Folder | What it holds |
| --- | --- |
| `credentials` | Accounts added by sign-in and their credentials. Named by `credentials_dir`. |
| `packages` | Installed extension packages, and in `packages/.settings` each extension's own settings. Named by `packages.dir`. |
| `logs` | Core's log files, request logs and error logs ([usage-and-logs.md](usage-and-logs.md)). |
| `usage` | The usage record's day files and their summaries. |
| `state` | What core keeps between runs: routing blocks (`routing.json`), the keys that make account public IDs and client key IDs (`account-key`, `key-id.key`), stored themes (`themes`), setup progress (`setup.json`) and the model catalogue (`catalogue.json`). |

## Format

The config is a JSON object. The file's name decides how it is read: a name ending in `.yaml` or
`.yml` is read as CLIProxyAPI's `config.yaml` ([config-yaml.md](config-yaml.md));
any other name is read as the JSON described here.

- A relative path in the file (`credentials_dir`, `packages.dir`, `tls.cert`, `tls.key`,
  `extension.manifest`, a registry's local `url`) is relative to the config file's folder.
- A key core does not read is not an error. Each load logs one `warning:` line naming the file and
  the keys, such as `routing.explian (did you mean "explain"?)`, and core ignores them.
- A wrong type or a syntax error stops the load and names the key path or the line and column.
- Core does not start on a config with no `listen`, no `client_keys` or no `management_key`.
- A change made while core runs, through the panel or the `pluxy` command, rewrites the file with
  the order of its keys kept. The management API shows the file with every secret replaced by a
  placeholder ([management-api.md](management-api.md)).

A small config that serves one API-key account:

```json
{
  "listen": "127.0.0.1:8317",
  "client_keys": ["choose-a-client-key"],
  "management_key": "choose-a-management-key",
  "extensions": {
    "pluxy/openai-compatible": {
      "accounts": [
        {
          "id": "openrouter",
          "credential": {"kind": "api_key", "api_key": "<the provider's key>"},
          "settings": {"base_url": "https://openrouter.ai/api/v1"}
        }
      ]
    }
  }
}
```

## Keys

Times ending in `_ms` are milliseconds. A key with no default is left out of the file until you
need it.

### Top level

| Key | Type | Default | What it does |
| --- | --- | --- | --- |
| `listen` | string | none: required | The `host:port` core serves the client API, the management API and the web panel on. Needs a restart to change. |
| `client_keys` | list of strings | none: at least one required | The keys clients send as a bearer token (or in `x-api-key`; Gemini clients may also use `x-goog-api-key` or the `key` query parameter). The example keys `your-api-key-1`, `your-api-key-2` and `your-api-key-3` are public: with one of them in the list, core starts but answers every request that carries a configured key with a 403 (`unsafe_example_api_key`) until none is left. The management API stays open, so you can replace them. A reload or a change through the management API that would leave the list empty is refused, and the running config stays. A request with a missing or wrong key gets a 401 with `X-Should-Retry: false` in every API format, so a coding tool stops instead of retrying. |
| `client_key_labels` | object | none | A name for each client key, by the key's ID as `pluxy key list` shows it: `{"3fa9...": "work laptop"}`. A label is one line of at most 64 characters and is not a key. |
| `management_key` | string | none: required | The key for the management API and the web panel. The panel and every `pluxy` client command send it. |
| `management_allow_remote` | bool | `false` | Lets clients on other machines call the management API. Without it, only a caller on this machine (a loopback address) is served and any other gets a 403 `remote_disabled`. |
| `credentials_dir` | string | `credentials` | The folder that keeps accounts added by sign-in. Needs a restart to change. |
| `extension` | object | none | One extension run from a manifest file. See [Extensions](#extensions). |
| `extensions` | object | none | Settings and accounts of installed packages, by package ID (`publisher/name`). See [Extensions](#extensions). |
| `packages` | object | none | Where installed packages are kept and the registries they are installed from. See [packages](#packages). |
| `routing` | object | none | How requests pick accounts and how they are retried. See [routing](#routing). |
| `drop_policy` | string | `report` | What happens when translating a request to the upstream's format would drop part of it: `report` sends the request and logs what was dropped; `reject` refuses it with a 400 that names what could not be carried. [Translation](translation.md) lists what each provider drops. |
| `token_count_estimate` | bool | `true` | What a token count gets when no account for the model can count: `true` answers with core's own estimate, `false` answers with an error. Which accounts count, and what the estimate counts, is in [Token counts](routing.md#token-counts). |
| `extension_pages` | object | none | Where extension pages are served. See [extension_pages](#extension_pages). |
| `cors` | object | none | Which web pages may call the client API from a browser. See [cors](#cors). |
| `tls` | object | none | Serves `listen` over HTTPS. See [tls](#tls). |
| `trusted_proxies` | list of strings | none | IP addresses and CIDR ranges of the proxies in front of core. A request from one of them is logged with the client its `X-Forwarded-For` (else `X-Real-IP`) header names; from any other address, with its connection's address. |
| `proxy_url` | string | none | The outbound proxy of every account that has none of its own: an `http`, `https`, `socks5` or `socks5h` URL, or `direct` or `none` for no proxy. A user name and password in it are kept secret. |
| `payload` | object | none | Rules that set, fill in or remove fields in the JSON bodies extensions send upstream: lists named `default`, `default-raw`, `override`, `override-raw` and `filter`. See [routing.md](routing.md). |
| `usage` | object | none | The usage record. See [usage](#usage). |
| `logs` | object | none | Log files. See [logs](#logs). |
| `pprof` | object | none | Profiling pages. See [pprof](#pprof). |

**How `trusted_proxies` finds the client.** When a request's connection comes from a trusted proxy,
core reads all `X-Forwarded-For` lines as one list and takes the last address in it that is not a
trusted proxy, reading from the right (the first address when all are trusted proxies). With no
usable `X-Forwarded-For`, it takes the first `X-Real-IP` line. A header with an entry that is not an
IP address is not used. When neither names a client, core takes the connection's address.

### `routing`

How a request picks an account is explained in [routing.md](routing.md); this table lists the keys.

| Key | Type | Default | What it does |
| --- | --- | --- | --- |
| `strategy` | string | `round-robin` | How an account is chosen among the eligible ones: `round-robin`, `weighted`, `fill-first`, `least-active` or `soonest-reset`. |
| `max_attempts` | whole number | `0` | Most upstream attempts one request makes, counting the first. `0` is no fixed limit: each account is tried at most once per round, and a new round needs a wait that fits `max_wait_ms`. |
| `max_accounts` | whole number | `0` | Most distinct accounts one request tries in a round. `0` is no limit. |
| `max_wait_ms` | whole number | `0` | The longest one request waits in total for a blocked account (with `request_retry` set: before each extra round). `0` means it never waits: when every account is blocked, the request fails at once. |
| `request_retry` | whole number | unset | Rounds a request may run after its first, each trying every eligible account again. Unset, rounds are bounded only by `max_attempts` and the wait. |
| `disable_cooling` | bool | `false` | Records no blocks after failures, so every account stays eligible. An account's own `disable_cooling` wins. |
| `transient_error_cooldown_ms` | whole number | `0` | How long an upstream that failed with a 5xx or a timeout, and gave no `Retry-After`, is blocked. `0` backs off from 1 second to a minute, doubling; above `0` blocks that long; below `0` blocks nothing. |
| `session_affinity` | object | on | `{"enabled": false}` turns it off; `ttl_ms` (default `3600000`, one hour) is how long a session stays on its account after its last request. |
| `force_model_prefix` | bool | `false` | An account with a `prefix` serves its models only as `<prefix>/<model>`, except a model whose name is the prefix itself, which is served under both names. |
| `quota_response_mode` | string | `retryable` | What a client gets when every account is out of quota until a reset more than 5 minutes away: `retryable` (a 429 with `Retry-After`) or `usage-limit` (the final usage-limit reply that tells the client to stop until the reset). |
| `explain` | bool | `true` | `false` leaves out of replies the `X-Pluxy-*` headers and the `pluxy` member of errors, which name the accounts tried. The trace ID header stays. |
| `timeouts` | object | `{"header_ms": 60000, "idle_ms": 300000, "total_ms": 600000}` | Upstream timeouts of every account: time to response headers, longest gap between body bytes, and total time. `0` turns a limit off; a field left out keeps its default. An account's own `timeouts` win. |
| `chains` | object | none | Fallback chains by the model name a client asks for: a list of steps, each `{"model": "...", "provider": "pluxy/claude", "accounts": ["id"]}` (`provider` and `accounts` are optional). A chain has 1 to 8 steps. |
| `quota_refresh_ms` | whole number | `1800000` | How often `soonest-reset` reads every account's usage. `0` turns the readings off. |
| `ignition` | object | off | Starts an account's next usage window right after the last one resets. See the next table. |

`routing.ignition` (off unless `enabled` is `true`):

| Key | Type | Default | What it does |
| --- | --- | --- | --- |
| `enabled` | bool | `false` | Turns window ignition on. |
| `start_hour`, `end_hour` | whole number, 0 to 23 | `7`, `22` | The hours ignition runs between, in the machine's local time. |
| `end_grace_ms` | whole number | `1800000` | How long ignition keeps running past `end_hour`. |
| `grace_ms` | whole number | `3000` | How long after a window resets core reads it and starts the next. |
| `retry_ms` | whole number, at least 1000 | `300000` | The wait after a failed ignition, multiplied by `backoff_multiplier` for each further failure in a row. |
| `backoff_multiplier` | number, 1 to 10 | `3` | See `retry_ms`. |
| `max_failures` | whole number, 1 to 10 | `3` | Failures in a row that pause a window's ignition until the next `start_hour`. |
| `hold_ms` | whole number, at least 15000 | `60000` | How long after a successful ignition core reads the window again; so, the least time between two ignitions of a window. |
| `extensions` | object | none | Turns ignition on for each listed extension's accounts, by extension ID: `{"pluxy/claude": {"model": "...", "windows": {"<window ID>": "<model>"}}}`. `"enabled": false` leaves an extension out. |
| `accounts` | object | none | Turns ignition on or off for one account, by account ID: `{"<id>": {"enabled": true, "model": "..."}}`. It wins over the account's extension. |

The `*_ms` values of `ignition` range up to one day.

### Extensions

`extension` runs one extension from a manifest file on disk. `extensions` holds the settings and the
accounts of installed packages ([extensions.md](extensions.md)), keyed by package ID. Both take the
same keys, except `manifest`, which only `extension` takes (and `extension.accounts` needs it).

| Key | Type | Default | What it does |
| --- | --- | --- | --- |
| `manifest` | string | none | `extension` only: the path to the extension's manifest, relative to the config file. |
| `accounts` | list of accounts | none | The accounts this extension serves. See [Accounts](#accounts). |
| `idle_stop_ms` | whole number | `600000` | Stops the extension's program after this long without requests; the next request starts it again. `0` keeps it running. |
| `drain_timeout_ms` | whole number | `600000` | The longest a replaced version of the extension may keep finishing its requests. |
| `restart.backoff_ms` | whole number | `1000` | The wait before the first restart after a crash; it doubles with each crash in a row. |
| `restart.max_backoff_ms` | whole number | `60000` | The most that wait grows to. |
| `restart.quarantine_after` | whole number | `5` | Crashes in a row after which the extension is quarantined: its requests fail with a 503 until a reload starts it again. |
| `restart.stable_ms` | whole number | `600000` | A program that ran this long before crashing starts a new count. |
| `model_alias` | list | none | Renames models for every account of the extension added by sign-in (not by API key), after the account's own `model_alias`. Same entries as an account's. |
| `excluded_models` | list of strings | none | Model name patterns no sign-in account of the extension serves, unless the account has its own `excluded_models`. |

Timings range from 0 to 30 days. For the `restart` values, `0` means the default. An extension's own settings (the ones its page in the panel
edits) are not in `config.json`: core stores them in `packages/.settings/<publisher>/<name>.json`,
readable only by your user.

### Accounts

An account is an entry of an extension's `accounts` list. Accounts added by signing in are not in
the config: they are in the credential store, and the panel, the app and `pluxy account add` manage
them. Account IDs must be unique across the whole config. `pluxy account set` changes every key
below except `id`, `credential` and `endpoint`.

| Key | Type | Default | What it does |
| --- | --- | --- | --- |
| `id` | string | none: required | The account's name in the config, in `pluxy` commands and in logs. |
| `credential` | object | none | The secret the account presents: `{"kind": "api_key", "api_key": "..."}`, or `{"kind": "oauth", ...}` with `access_token`, `refresh_token`, `expires_at` (Unix seconds), `refresh_at` and `data`. |
| `settings` | object | none | Options the account's provider takes ([providers.md](providers.md)). |
| `models` | list | any model | The models the account serves: a name, or `{"name": "<upstream name>", "alias": "<name clients use>"}`. |
| `excluded_models` | list of strings | none | Model name patterns the account does not serve. |
| `model_alias` | list | none | Renames models the account serves: `{"name": "...", "alias": "...", "fork": false, "display_name": "..."}`. With `fork` the model is served under both names. |
| `prefix` | string | none | A namespace: clients name the account's models `<prefix>/<model>`, and also without the prefix unless `routing.force_model_prefix` is on. It holds no `/` and no leading or trailing space. |
| `priority` | whole number | `0` | Only the eligible accounts with the highest priority are used; lower ones take over while every higher one is blocked. |
| `weight` | whole number | `1` | The share under the `weighted` strategy. `0` leaves the account out of weighted routing. |
| `disable_cooling` | bool | the `routing` value | Overrides `routing.disable_cooling` for this account. |
| `max_concurrency` | whole number | `0` | Most requests running on the account at once. `0` is no limit. |
| `request_retry` | whole number | the `routing` value | Rounds the account takes part in after the first, in place of `routing.request_retry`. |
| `endpoint` | string | the account's `id` | Accounts that name the same endpoint share endpoint-wide blocks (an outage of that endpoint blocks them all). |
| `proxy_url` | string | the top-level `proxy_url` | The account's outbound proxy, as the top-level key. |
| `headers` | object | none | Extra HTTP headers sent on every upstream request of the account, over the extension's own of the same names. A value `$Name` copies that header from the client's request. |
| `timeouts` | object | the `routing` value | Overrides `routing.timeouts` for this account. |

### `packages`

| Key | Type | Default | What it does |
| --- | --- | --- | --- |
| `dir` | string | `packages` | The folder installed packages are kept in. Needs a restart to change. |
| `registries` | list | none | Registries searched, in order, when an install names none. Each is `{"name": "...", "url": "..."}`: `name` is unique and not `bundled` (the registry of the packages shipped with this install, which is always there); `url` is an `https://` URL or a path to the index file. |

### `extension_pages`

Extension pages are served on a site of their own, never the one that serves the panel
([extension pages](../next/ui/extension-pages/README.md)).

| Key | Type | Default | What it does |
| --- | --- | --- | --- |
| `listen` | string | the host of `listen`, port `0` | The pages listener's address. Port `0` is a free port core picks at start. Needs a restart to change. |
| `url` | string | derived | The pages site's origin as a browser reaches it, such as `https://pages.example.com`: `http://` or `https://`, a host and maybe a port, nothing else. Set it when the panel is reached by a name other than a loopback one. |

### `cors`

| Key | Type | Default | What it does |
| --- | --- | --- | --- |
| `allowed_origins` | list of strings | none: no page may call | Origins that may call the client API from a browser, each `scheme://host[:port]`, or `"*"` for every origin. Core then adds the CORS headers to replies and answers preflight requests. It covers the client API only, never the panel or the management API. |

```json
{"cors": {"allowed_origins": ["http://localhost:5173"]}}
```

A browser sends no credentials with a preflight request, so core answers the preflight `OPTIONS`
request of an allowed origin itself, with a 204, before it checks the client key.

### `tls`

| Key | Type | Default | What it does |
| --- | --- | --- | --- |
| `enable` | bool | `false` | Serves `listen` over HTTPS. |
| `cert` | string | none | The certificate file (PEM, the certificate first, then its chain). Required with `enable`. |
| `key` | string | none | The private key file (PEM). Required with `enable`. |

Core does not start when the files do not load. Turning TLS on or off, or pointing it at other
files, needs a restart; a renewed certificate in the same files needs none (see
[Reloading](#reloading)). The extension pages listener stays plain HTTP, whatever `tls` says. By
default its address is `http://127.0.0.1:<port>` or `http://localhost:<port>` (whichever the panel is
not opened at), and a browser lets a page served over HTTPS embed an `http://` page at a loopback
address, though not at any other. A panel reached by another name needs `extension_pages.url`: the
origin of a site of its own that reaches the pages listener, behind your own HTTPS when the panel
is served over HTTPS.

### `usage`

The usage record ([usage-and-logs.md](usage-and-logs.md)).

| Key | Type | Default | What it does |
| --- | --- | --- | --- |
| `enabled` | bool | `true` | Whether core keeps the usage record. |
| `max_events` | whole number | `10000` | The most requests the record keeps; the oldest go first. At most `200000`. |
| `max_age_ms` | whole number | `86400000` (24 hours) | How long the record keeps a request. At most 30 days. |
| `keep_days` | whole number | `90` | How many UTC days of day files in the `usage` folder are kept. `0` keeps everything. |

### `logs`

Log files ([usage-and-logs.md](usage-and-logs.md)).

| Key | Type | Default | What it does |
| --- | --- | --- | --- |
| `to_file` | bool | `false` | Also writes core's log to `logs/main.log`, rotated at 10 MB. |
| `max_total_size_mb` | whole number | `0` | The most the `.log` files in the logs folder may take; the oldest go first, `main.log` never. `0` is no cap. At most `1048576`. |
| `request_log` | bool | `false` | Writes every client request and its reply to a file in the logs folder. Off, only requests that fail are written, as `error-*.log`. |
| `error_logs_max_files` | whole number | `10` | How many error logs are kept, the newest. `0` keeps all. |

### `pprof`

| Key | Type | Default | What it does |
| --- | --- | --- | --- |
| `enable` | bool | `false` | Serves Go's profiling pages (`/debug/pprof/`) on `addr`. Only a build with the `pprof` build tag has them; any other build logs that the setting does nothing. |
| `addr` | string | `127.0.0.1:8316` | The profiling listener's address. It must be a loopback address (`127.0.0.0/8`, `::1` or `localhost`). |

## Environment variables

`pluxy` reads these. Core's settings are in `config.json`, not in the environment. The one other
thing core's environment changes: downloads from registries and the model catalogue refresh follow the
standard proxy variables `HTTP_PROXY`, `HTTPS_PROXY` and `NO_PROXY`.

| Variable | Default | What it does |
| --- | --- | --- |
| `PLUXY_HOME` | the folder in [pluxy's home](#pluxys-home) for your system | The folder whose `config.json` `pluxy serve`, `pluxy init` and the client commands use when given no config file and the current folder has no `config.json`. An absolute path; a relative one is an error. Example: `/srv/pluxy`. |
| `PLUXY_URL` | read from the config file | Core's base URL for the client commands; `--url` overrides it. Example: `http://127.0.0.1:8317`. |
| `PLUXY_MANAGEMENT_KEY` | read from the config file | The management key for the client commands; `--key` overrides it. |
| `PLUXY_INIT_PORT` | unset: `8317`, or a free port when it is taken | For tests: the port a first start writes into the new config (`0` picks a free port). A value that is not a port stops core. A real run never sets it. |
| `PLUXY_TEST_LOGIN_HOSTS` | unset | For tests: core passes it on to every extension program it starts, and when it is exactly `1`, extensions let account settings such as `auth_base_url` or `token_url` point sign-in, token and quota endpoints at a test server. Never set it in a real run. |

Where your system keeps application data decides pluxy's home, so `pluxy` also reads `XDG_DATA_HOME`
and `HOME` (Linux and other Unix systems), `HOME` (macOS) and `LOCALAPPDATA` (Windows).

An extension program starts with a cleared environment: only `PATH` (and `SYSTEMROOT` on Windows),
`PLUXY_TEST_LOGIN_HOSTS` when core has it set, and these three, which core sets. An extension author
reads them ([extension protocol](../next/protocol/README.md)); you never set them:

| Variable | What it holds |
| --- | --- |
| `PLUXY_PROTOCOL_VERSIONS` | The protocol versions core speaks, comma-separated. |
| `PLUXY_SOCKET` | The path of the socket the extension listens on. |
| `PLUXY_EXTENSION_ID` | The extension's ID from its manifest. |

The desktop app, its helper programs, the tests and CI use other `PLUXY_` variables. They are not
settings of core and are described where they are used.

## Reloading

Core reloads its config file without a restart. A reload happens:

- when the file changes. Core looks at the file's size and modification time every second
  (`pluxy serve -watch-interval`, `0` turns the watch off) and reloads once it looks the same on two
  looks in a row, so a save written in pieces is read whole;
- on `SIGHUP` (Linux and macOS);
- on `pluxy reload` or `POST /v0/management/reload`;
- when a change is made through the management API, the panel, the app or the `pluxy` command.

A reload reads and checks the file, prepares what it needs (starting a new version of an extension
and waiting for its handshake) and only then switches to it in one step. Requests already running
finish on the config and extension version they started with. When the file watch sees contents
core has already loaded, or an empty file (an editor in the middle of saving), it does nothing;
`SIGHUP`, `pluxy reload` and `POST /v0/management/reload` read the file again anyway, and refuse an
empty one. A reload that fails at any step, from broken JSON to an extension that does not start,
leaves the running config in place, and `pluxy status` shows why; secrets in that reason are
replaced by placeholders. One exception: an enabled package that is not running now (for example,
because it failed at the last start) and fails to start again is skipped and shown as failed, and
the rest of the config loads.

Everything applies on reload except these, which need a restart (a reload that changes one is
refused, and the running config stays):

- `extension_pages.listen`,
- `tls` (turning it on or off, or changing `cert` or `key`),
- `credentials_dir`,
- `packages.dir`.

A new `listen` is not refused: the rest of the file applies, core keeps serving on the address it
has (and logs that the new one waits), and takes the new address when it next starts.

A renewed certificate in the same `tls.cert` and `tls.key` files needs no restart: core looks at the
files at most once a second while connections arrive and serves the new pair from the next
connection on. While the files do not make a pair (one written, the other not yet), it keeps the
one it has.

When an extension's manifest or program changes, the new version starts first. The old one finishes
its running requests and stops, at the latest after the `drain_timeout_ms` it was started under
(a new value applies to the next replacement). A Responses WebSocket session
lives in the old version, so it closes once its running turn ends. Routing state (blocks, cooldowns,
session bindings, the round-robin turn) carries over for every account whose ID, extension and
`settings` stay the same; an account whose `settings` change starts fresh. Accounts added by sign-in
stay in the credential store through reloads.

`pluxy serve -safe-mode` starts core with no extensions: client requests fail with a 503, and the
config can still be checked and reloaded through the management API.

## config.yaml with CLIProxyAPI key names

Core also reads a CLIProxyAPI `config.yaml` (any config file whose name ends in `.yaml` or `.yml`).
[config-yaml.md](config-yaml.md) maps its key names and auth-file fields to pluxy's.

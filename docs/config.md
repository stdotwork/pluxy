# Configuration reference

pluxy's settings are in one file, `config.json`. This page lists where that file is and how pluxy
finds it, every key core reads, every `PLUXY_` environment variable, and which changes apply
without a restart. It also lists the key names of CLIProxyAPI's `config.yaml`, which core reads
too.

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
`.yml` is read as CLIProxyAPI's `config.yaml` ([below](#configyaml-with-cliproxyapi-key-names));
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
| `drop_policy` | string | `report` | What happens when translating a request to the upstream's format would drop part of it: `report` sends the request and logs what was dropped; `reject` refuses it with a 400 that names what could not be carried. |
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
| `force_model_prefix` | bool | `false` | An account with a `prefix` serves its models only as `<prefix>/<model>`. |
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

- `listen` and `extension_pages.listen`,
- `tls` (turning it on or off, or changing `cert` or `key`),
- `credentials_dir`,
- `packages.dir`.

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

Core also reads a `config.yaml` written for CLIProxyAPI, so an existing config moves over without
renaming keys. [Moving from CLIProxyAPI](moving-from-cpa.md) walks through a move step by step; this
section is the reference for the key names.

A config file whose name ends in `.yaml` or `.yml` (in any letter case) is read with CLIProxyAPI's key names; any other
file is read as the JSON config above. Both layouts of CLIProxyAPI's config work: its current layout
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

### Provider key sections

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

### Auth files

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

### What each setting does

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
| `management.disable-control-panel` | The panel is always on, with the management API. | Ignored: the panel needs the management key |
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
| `upstream.codex.response-steering`, `upstream.codex.orphan-delegation-compatibility`, `oauth.providers.codex.header-defaults.*` (`codex-header-defaults`) | | Ignored: experimental or off by default, no need found |
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

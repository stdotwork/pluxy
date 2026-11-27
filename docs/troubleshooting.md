# Troubleshooting

This page is a list of problems by what you see: find the message pluxy prints or returns, read
what causes it, and run the fix. Messages are quoted as pluxy writes them; the parts in `<angle
brackets>` change from case to case.

- [pluxy does not start](#pluxy-does-not-start)
- [A `pluxy` command cannot reach core](#a-pluxy-command-cannot-reach-core)
- [A client gets an error](#a-client-gets-an-error)
- [An account is not used](#an-account-is-not-used)
- [An extension is not running](#an-extension-is-not-running)
- [The panel](#the-panel)
- [The desktop app](#the-desktop-app)
- [Finding out what happened](#finding-out-what-happened)

## pluxy does not start

pluxy writes these to standard error and exits with code 1.

### The address is already in use

```text
pluxy: listen tcp 127.0.0.1:8317: bind: address already in use
```

Another program listens on the address in `listen` in `config.json`. A first start with no config
picks a free port when 8317 is taken, but once `config.json` exists, core keeps the address in it.

Stop the other program, or change `"listen"` in `config.json` to a free port and start pluxy again.

### The config has no management key

```text
pluxy: config <path>: no management key: core always has one, so the panel and the pluxy command can manage it. Add "management_key": "<a long random secret>" to the file, or move the file aside and run pluxy init to write a new config with both keys
```

Core does not start without a management key (`management_key` in `config.json`; `remote-management.secret-key`
or `management.secret-key` in a `config.yaml`). Add the line the message shows, or move the file aside and run `pluxy init`.

A `config.yaml` that CLIProxyAPI wrote can hold the key as a bcrypt hash, which pluxy cannot use:

```text
pluxy: config <path>: remote-management.secret-key is a bcrypt hash: replace it with a new key (pluxy cannot use a bcrypt hash); core always has a management key, so the panel and the pluxy command can manage it
```

Replace the hash with a plain key.

### The config has no client keys

```text
pluxy: config: client_keys is empty (access.api-keys in a config.yaml): set at least one client key; core serves no client without one
```

Add a random string to `"client_keys"` in `config.json`.

### The config is not valid JSON

```text
pluxy: config <path>: not valid JSON at line 1, column 32: invalid character 'b' looking for beginning of object key string
```

Fix the file at the line and column the message names. The file's name decides how pluxy reads it: a
name ending in `.yaml` or `.yml` is read as a CLIProxyAPI `config.yaml`, any other name as JSON.

### `pluxy init` finds a config

```text
pluxy: <path>/config.json already exists. Start core with: pluxy serve -config <path>/config.json, or pick another file with -config.
```

`pluxy init` never overwrites a config. Start core with the command in the message, or write to
another file with `pluxy init -config <other.json>`. For the config in pluxy's home, the message
reads `pluxy: <path> already exists: pluxy's home has a config. Start core with: pluxy serve, or pick another file with -config.`

### Core stops while starting, and the message ends with "-safe-mode"

```text
pluxy: <error> (start with -safe-mode to run without extensions)
```

Core could not finish starting, for a reason that is not its config file or its credential store
(those errors do not carry the hint). `pluxy serve -safe-mode` starts core without running any
extension: client requests fail, and the management API works, so you can disable or remove an
extension that holds the start up. See [An extension is not running](#an-extension-is-not-running).

## A `pluxy` command cannot reach core

A `pluxy` command other than `serve`, `init`, `help` and `key management` calls a running core. It finds core's address
and its management key, each in the first of these that applies: `--url` and `--key` (or `PLUXY_URL` and
`PLUXY_MANAGEMENT_KEY`), `--config <file>`, `config.json` in the current folder, `config.json` in
[pluxy's home](setup.md). `--verbose` on any of these commands prints which one it used.

### No core is running

```text
pluxy: no core is listening at http://127.0.0.1:8317, the address in <path>/config.json (pluxy's home). Start it with: pluxy serve, or check --url
```

Exit code 3. Nothing answers at the address. Two other forms name the source of the address:
`pluxy: no core is listening at <address> (from --url). Start one with: pluxy serve, or check --url` (`from PLUXY_URL` when the address came from the environment), and, for a
`config.json` in the current folder or a `--config` file,
`pluxy: no core is listening at <address>. Start one with: pluxy serve -config <file>, or check --url`.

Start core with the command the message shows, or pass the address of the core that runs.

### There is no config file to read

```text
pluxy: no core to call: there is no config.json in this folder, and none in pluxy's home, <path>/config.json. Start pluxy with pluxy serve, which writes its first config in pluxy's home (pluxy init writes it without starting), or pass --config <file>, or --url and --key
```

Exit code 1. The command found no address and no key. Do what the message says. When the desktop app
runs its own core, the message names that core's config file instead: pass it with `--config` to call that core.

For a `--config` path that does not exist: `pluxy: no config file at <path>. Check the --config path, run pluxy init -config <path>, or pass --url and --key`.

### The management key is wrong or missing

```text
pluxy: wrong management key for http://127.0.0.1:8317. Check --key, PLUXY_MANAGEMENT_KEY, or --config <file>
```

```text
pluxy: no management key for http://127.0.0.1:8317. Pass --key, set PLUXY_MANAGEMENT_KEY, or use --config <file>
```

Exit code 4. Core answered 401. Pass the `management_key` from core's `config.json` with `--key`, set
`PLUXY_MANAGEMENT_KEY`, or point `--config` at that `config.json`.

### The command will not send your key to this folder's config

```text
pluxy: not sending the management key from PLUXY_MANAGEMENT_KEY to http://127.0.0.1:8317, the address in this folder's config.json, which holds a different key. To use that pluxy, leave out PLUXY_MANAGEMENT_KEY so the key in its config.json is used. To use another, add --url <its address>.
```

Exit code 2; nothing was sent. You gave a key with `--key` or `PLUXY_MANAGEMENT_KEY`, and the only
address the command had was the `config.json` in the current folder, which holds another key. A file
in a folder could name any machine, so pluxy does not send your key there. To call the pluxy that
`config.json` describes, leave out your key (the message names the source). To call another pluxy,
add `--url <its address>`.

### The address is malformed

```text
pluxy: --url "127.0.0.1:8317" must start with http:// or https://, such as http://127.0.0.1:8317
```

Exit code 2. Write the address with `http://` or `https://`, for example
`--url http://127.0.0.1:8317`. The same check applies to `PLUXY_URL`.

### `pluxy client show` does not know the tool

```text
pluxy: unknown client tool "<name>" (supported: claude-code, codex, opencode, pi, omp)
```

Exit code 2. See [Point a coding tool at pluxy](clients.md).

## A client gets an error

A client is the tool that sends requests to pluxy. The bodies below are what an OpenAI-style
request (`/v1/chat/completions`, `/v1/responses`) gets, with the `error.code` field to look for.
An Anthropic request (`/v1/messages`) gets `{"type":"error","error":{"type":...,"message":...}}`, and a
Gemini request gets `{"error":{"code":<status>,"message":...,"status":...}}`; the status and the
message are the same. In an OpenAI-style body, the `pluxy` member of an error body, when there is one, names the accounts
tried (`tried`) and the ones left out (`left_out`), and `trace` holds the request's trace ID (see
[Finding out what happened](#finding-out-what-happened)).

### 401 `invalid_api_key`

```json
{"error":{"code":"invalid_api_key","message":"missing or invalid API key","type":"authentication_error"}}
```

The client sent no client key, or one that is not in `client_keys`. The key may be in
`Authorization: Bearer <key>` or in `x-api-key`; a Gemini client may also send it in `x-goog-api-key` or in the `key` query
parameter. Check the key against `client_keys` in `config.json`, or make a new one with
`pluxy key add`. A management key is not a client key. For what to paste into a tool, see
[Point a coding tool at pluxy](clients.md).

The reply carries `X-Should-Retry: false`, in every API format and in the error message of an open
Responses WebSocket, so a coding tool shows the error at once instead of retrying a key that cannot
succeed. `client_keys` always holds at least one key: pluxy does not start on an empty list, and a
reload that would leave it empty is refused.

### 403 `unsafe_example_api_key`

```json
{"error":{"code":"unsafe_example_api_key","message":"the client API is disabled because client_keys holds the example config's key your-api-key-1; replace it with your own key, then retry","type":"permission_error"}}
```

`client_keys` still holds one of the example config's public keys (`your-api-key-1` and its
neighbours), which anyone can read. While it does, pluxy refuses every client request that carries a
configured key, including your own. This reply has no trace ID header. Core says so at start: `pluxy: client_keys still holds the example config's public keys (your-api-key-1): every client request is refused (403 unsafe_example_api_key) until they are replaced`.
Remove the example keys from `client_keys` and run `pluxy reload`.

### 404 `model_not_found`

```json
{"error":{"code":"model_not_found","message":"no account serves model \"nonexistent-model\"","pluxy":{"trace":"20261127123005-dab0474c06d38d37","tried":[],"left_out":[]},"type":"invalid_request_error"}}
```

No account serves that model name. `pluxy route nonexistent-model` shows the same: it fails with exit code 5 and prints:

```text
pluxy: no account serves nonexistent-model. See pluxy model list.
```

Run `pluxy model list` for the names clients can ask for. A model name is not served when the account
that offers it is turned off or needs a new sign-in (see [An account is not used](#an-account-is-not-used)),
or when the extension it needs is not installed or not enabled. When the accounts that serve a model are all
turned off or waiting for a sign-in, the message says so instead:

```text
no account can serve model "<model>": every account that serves it is turned off or needs a new sign-in
```

(`is turned off` or `needs a new sign-in` alone, when all of them share one state.) A request that
pins one account with the `X-Pluxy-Account` header gets `account "<id>" cannot serve model "<model>": it is turned off or needs a new sign-in`, or
`account "<id>" does not exist or does not serve model "<model>"`.

### The model list is empty or lacks a model

`GET /v1/models` answers `{"data":[],"object":"list"}` while no account serves any model, for
example when no extension is installed or no account is added. `pluxy model list` shows the same
list. A provider's model list is read again every 30 minutes (with up to 3 minutes added), and when an
account signs in or renews; `pluxy model refresh` asks every provider now. For a model that is still missing, `pluxy account models <id>` lists what an account
serves; the account's `excluded_models` setting can leave a model out.

### 429 `model_cooldown`

```text
every account for model "<model>" is cooling down; try again in <wait>
```

`<wait>` is written as a duration, such as `45s`, `1m30s` or `5m0s`.

HTTP 429 with a `Retry-After` header. When no account can serve the request for more than 5
minutes (a usage limit that resets later, or an account that needs a person), the reply carries
`X-Should-Retry: false` and no `Retry-After`: the client should stop instead of retrying. Any
routing error of that kind carries it. Every account that serves the model is blocked for now, after
a rate limit or a failing upstream. By default pluxy does not wait for a block to end: it fails at
once with the time the first block ends (`max_wait_ms` under `routing` in `config.json` makes it wait up to that many milliseconds).
Wait for the time in the message, or end a block yourself with `pluxy account clear <id>`.

### 503 `extension_unavailable`

```text
extension <id> is not running
```

The account's extension is installed and enabled but is not serving: it crashed, was stopped or is
quarantined. Other forms: `extension <id> does not run`, and, when core runs with `-safe-mode`,
`core is in safe mode: no extension runs`. See [An extension is not running](#an-extension-is-not-running).

### 429 `accounts_busy`

```text
every account for model "<model>" is at its concurrency limit
```

Every account that serves the model already has as many requests running as its `max_concurrency`
allows. Retry later, or raise `max_concurrency` on the accounts (`pluxy account set <id> max_concurrency=<n>`).

## An account is not used

`pluxy account list` shows each account's state in the `HEALTH` column, with the time it ends
(`UNTIL`) and the reason (`WHY`). `pluxy route <model>` shows which accounts a request for a model
would try, in order, and which it would leave out. The panel's **Accounts** page shows the same states
in words.

| `HEALTH` | The panel says | Meaning | What to do |
| --- | --- | --- | --- |
| `ok` | Serving | The account takes requests. | Nothing. |
| `cooling` | Cooling down | A rate limit or a failing upstream blocked it, until `UNTIL`. | Wait, or run `pluxy account clear <id>` (**Clear cooldown** on the Accounts page). |
| `exhausted` | Limit used up | A usage window of the provider ran out. A usage block shorter than 5 minutes shows as `cooling` instead. | Wait for the reset, check what is left with `pluxy account quota <id>`, or add another account. |
| `needs_login` | Needs sign-in | The provider refused the account's sign-in. | `pluxy account login <id>`, or **Sign in again** on the Accounts page (for an account set in `config.json`, the button reads **See what to do**). |
| `banned` | Closed by provider | The provider closed or suspended the account. | Check the account with its provider; once it is open again, run `pluxy account enable <id>` (**Turn on** on the Accounts page), which also forgets the ban. |
| `disabled` | Turned off | A person turned it off (`WHY` says `turned off`). | `pluxy account enable <id>`, or **Turn on** on the Accounts page. |
| `broken` | Cannot be read | pluxy cannot read the account's saved credential file; `WHY` gives the file's path and the error. | Fix or remove that file. |

A rate-limit or usage block with more than 15 minutes left is checked every 15 minutes: core reads
the account's usage from the provider and ends the block early when the limit is no longer full. An
account with no usage reading, such as an API key, gets one request let through instead.

The panel says **Provider stopped** for an account whose provider's extension is not running: see
[An extension is not running](#an-extension-is-not-running). An account that `pluxy route <model>`
leaves out for none of these reasons may not serve that model: `pluxy account models <id>` lists
what it serves.

## An extension is not running

An extension is the program that serves one kind of provider. `pluxy ext list` shows each installed
package with `ENABLED` and `STATE`:

```text
ID             VERSION  ENABLED  STATE     REGISTRY
test/beta      1.0.0    no       disabled  bundled
```

| `STATE` | Meaning | What to do |
| --- | --- | --- |
| `disabled` | The package is installed and turned off. | `pluxy ext enable <id>`. |
| `starting`, `active`, `idle` | Normal. `idle`: the program was stopped for lack of use and starts at the next request. | Nothing. |
| `crashed` | The program exited or did not start; pluxy restarts it after a wait that grows from 1 second to 1 minute. | `pluxy status` shows the last error next to the state; `pluxy logs` shows more when file logging is on. |
| `quarantined` | It crashed 5 times in a row; pluxy does not restart it until the config is reloaded. | Fix the cause, then `pluxy reload`. |
| `failed: <reason>` | The package is enabled but could not start. | Fix what the reason names, then `pluxy reload`. |
| `stopped` | The package is enabled but nothing runs it: core is in safe mode (`-safe-mode`), or it has not been started. | Start core without `-safe-mode`, then `pluxy reload`. |

The waits, the 5 crashes and the run that resets the count (10 minutes) are defaults; the `restart`
setting of an extension in `config.json` changes them.

An account is served only when its provider's package is installed and enabled. `pluxy account list`
names the package an account needs in the `EXTENSION` column.

### `pluxy ext install` finds no registry

```text
pluxy: not found: no registries are configured (not_found)
```

`pluxy ext available` says the same in other words:

```text
This build has no extension registry.
From next/ in the source tree, run: go run ./cmd/pluxy-pack -out <dir>
Then put the bundled folder beside pluxy, or start core with -bundled <dir>/bundled/index.json.
```

A `pluxy` built with a plain `go build` has no registry to install from. Build the bundled
extensions with the command in the message, then put the `bundled` folder beside the `pluxy`
program, or start core with `-bundled <dir>/bundled/index.json`.

## The panel

### The panel is missing from a source build

```text
This core was built without its panel. Build the panel with go generate ./internal/panel, then build core again.
```

HTTP 503 at `/panel/`. The panel is built apart and embedded into the `pluxy` program, so a plain
`go build` makes a core without it. From `next/`:

```bash
go generate ./internal/panel
go build -o ~/pluxy/pluxy ./cmd/pluxy
```

Then start core again. `go generate` needs Bun.

### The sign-in form says "This management key was not accepted."

Open `http://127.0.0.1:8317/panel/` (or the address core printed at start). The form asks for the
**Management key**. Paste `management_key` from `config.json`, not a client key. `pluxy key management`
says where core's management key is kept.

The same refusal after you were signed in reads `Core refused the management key. Sign in again.`:
the management key changed since you signed in (`pluxy key rotate-management` or `POST /v0/management/management-key/rotate`
does that). Sign in with the new key.

### The sign-in form says "This pluxy answers only the computer it runs on"

Remote management is off. The panel's page loads from any machine, but the management API answers
only the machine core runs on unless `management_allow_remote` is `true` in `config.json`. A browser
on another machine, or one reaching core through a proxy or tunnel that forwards the client's address
(`X-Forwarded-For`, `X-Real-IP` or `Forwarded`), gets a 403 with the code `remote_disabled`. Open the
panel on core's own machine, or set `"management_allow_remote": true` there (`pluxy help` shows where
`config.json` is) and save it: core reloads the file by itself, so sign in again. Docker needs it,
because requests through a published port arrive from Docker's network address: see [Setup](setup.md).

### The sign-in form says "The reply from pluxy was 403"

The form shows "The reply from pluxy was", the status and core's reason for a status it has no other
words for. A 403 with `too many failed management keys from this address; try again in 30m0s` (the
code `blocked`) means the address is blocked: five wrong management keys in a row from one address
block it for 30 minutes (a call with no key does not count, and a right key resets the count). The
reply carries a `Retry-After` header, and the block holds even for the right key. Wait it out, or
restart core. An IPv6 address is counted by its /64 block. A call from the same machine as core is
never blocked.

### The sign-in form says "No pluxy answered at this address." or "Could not reach pluxy."

The first: the address answered 404 for `/v0/management/status`, so it is not a pluxy core, or a
proxy in front of it does not pass `/v0/management`. The second: nothing answered. Check that core
runs (`pluxy status`) and that the address in the browser is core's.

## The desktop app

Problems with the desktop app are on the app page, `app.md`.

## Finding out what happened

Core reads the config named by `-config`, else `config.json` in the folder it starts in, else the one
in pluxy's home; a start without `-config` logs which one (`config: <path>`). Log files are in the
`logs` folder beside the config: `main.log` (rotated to `main-<time>.log`), `error-<name>.log` for failed
requests, and a log per request when request logging is on. [Usage and logs](usage-and-logs.md) covers them.

### File logging is off

```text
pluxy: logging to file is off (invalid_request)
```

Exit code 2, from `pluxy logs`. Core always writes its log to standard error, and writes `main.log` too when file logging is on;
`pluxy logs` reads `main.log`. Set
`"logs": {"to_file": true}` in `config.json` and run `pluxy reload`, or turn on **File logging** on
the panel's **Config Panel** page, **Logging** tab.

### Request logging is off

```text
pluxy: no request log has this trace ID; request logging is off, so only requests that failed are logged. Set "logs": {"request_log": true} in core's config file, run pluxy reload, then send the request again (not_found)
```

Exit code 5, from `pluxy logs request <id>`. With request logging off, core writes a request log only for
a request that failed (status 400 or higher, or a stream that broke). Set `"logs": {"request_log": true}`,
run `pluxy reload`, send the request again, and use its new trace ID.

### Why a request went where it did

Every reply to a request that passed the client key check, except the 403 for example keys, has the
header `X-CPA-TRACE-ID`. A reply from
an account also has `X-Pluxy-Account` (its public ID), `X-Pluxy-Provider`, `X-Pluxy-Model` and
`X-Pluxy-Attempts`, unless `routing.explain` is `false`.

`pluxy route <model>` shows where a request would go now, without sending one. `pluxy usage events`
lists recent requests with their account, status and number of tries.

### Still stuck

Collect these before you ask for help. Read them first: pluxy replaces the keys and tokens it recognises,
but a request log holds the text of the request and of its reply.

1. `pluxy status`: core's state.
2. The failing request's trace ID, from the `X-CPA-TRACE-ID` header or `error.pluxy.trace`.
3. The error log: `pluxy logs errors` lists the files, `pluxy logs errors <name>` prints one, and
   `pluxy logs request <id>` prints the request's log.

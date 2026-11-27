# Troubleshooting

Find the message pluxy prints or returns, read what it means, and run the step that fixes it.

## pluxy does not start

### Port is already in use

```text
pluxy: listen tcp 127.0.0.1:8317: bind: address already in use
```

Exit code 1. Another process already listens on the address in `config.json`. On a first start with no config file, pluxy picks a free port when 8317 is taken; once `config.json` exists, core keeps the `listen` address written there.

Stop the other process, or change `"listen"` in `config.json` to a free port and start core again.

### Config has no management key

```text
pluxy: config config.json: no management key: core always has one, so the panel and the pluxy command can manage it. Add "management_key": "<a long random secret>" to the file, or move the file aside and run pluxy init to write a new config with both keys
```

Exit code 1. Core refuses to start without a `management_key` (or `remote-management.secret-key` in a `config.yaml`).

Add `"management_key": "<a long random secret>"` to `config.json`, or move the file aside and run `pluxy init`.

### Config has no client keys

```text
pluxy: config: client_keys is empty (access.api-keys in a config.yaml): set at least one client key; core serves no client without one
```

Exit code 1. Core requires at least one entry in `client_keys` so it never serves unauthenticated clients.

Add a random string to `"client_keys"` in `config.json` and start core again.

### `pluxy init` finds an existing config

```text
pluxy: <path>/config.json already exists. Start core with: pluxy serve -config <path>/config.json, or pick another file with -config.
```

Exit code 1. `pluxy init` never overwrites a config file.

Start core with `pluxy serve -config <path>/config.json`, or write to a different path with `pluxy init -config <other.json>`.

### First-run setup line at startup

```text
pluxy: first-run setup is not finished. Next: open http://127.0.0.1:8317/panel/ in a browser and sign in with the management key, or run pluxy setup
```

Core started with no installed provider extensions and no accounts. While first-run mode is active, the panel opens on **Set up pluxy**.

Open the panel URL and finish **Set up pluxy**, or run `pluxy setup` in a second terminal. To dismiss first-run mode without installing anything, run `pluxy setup done`.

## The command line cannot reach core

### No core is running

```text
pluxy: no core is listening at http://127.0.0.1:8317. Start one with: pluxy serve -config config.json, or check --url
```

Exit code 3. Nothing answered at the address from `config.json`, `--url`, or `PLUXY_URL`.

Start core with `pluxy serve -config config.json`, or pass the running core's address with `--url http://127.0.0.1:<port>`.

### No config file in this folder or in pluxy's home

```text
pluxy: no core to call: there is no config.json in this folder, and none in pluxy's home, /home/you/.local/share/pluxy/config.json. Start pluxy with pluxy serve, which writes its first config in pluxy's home (pluxy init writes it without starting), or pass --config <file>, or --url and --key
```

Exit code 1. Client subcommands read `config.json` in the current folder when there is one, and otherwise the one in [pluxy's home](../next/README.md#pluxys-home), to find core's address and management key.

Start pluxy with `pluxy serve`, change to the folder holding your `config.json`, pass `--config <file>`, or pass both `--url` and `--key` (or set `PLUXY_URL` and `PLUXY_MANAGEMENT_KEY`). When the desktop app runs its own core, the message names that core's config: pass it with `--config` to reach that core. `--verbose` on any client command says which config it read.

### Not sending the management key to this folder's config.json

```text
pluxy: not sending the management key from PLUXY_MANAGEMENT_KEY to http://127.0.0.1:8317, the address in this folder's config.json, which holds a different key. To use that pluxy, leave out PLUXY_MANAGEMENT_KEY so the key in its config.json is used. To use another, add --url <its address>.
```

Exit code 2, and nothing was sent. You gave a key with `--key` or `PLUXY_MANAGEMENT_KEY`, and the only address the command had was the `config.json` in the current folder (pluxy's home, when that is the current folder), which loads, has an address, and holds another key. A file in a folder could name any machine, so pluxy does not send your key there. If that is the pluxy you mean, leave out the key the message names (unset `PLUXY_MANAGEMENT_KEY`, or drop `--key`): the command then uses the key in that `config.json`. To call another pluxy, add `--url <its address>`. A `config.json` that holds exactly the same key is not refused.

A `config.json` in the folder with no `management_key`, or with `listen` on port 0, does not get this message: the command stops with that file's own error, exit code 1, and sends nothing either.

### Wrong or missing management key

```text
pluxy: wrong management key for http://127.0.0.1:8317. Check --key, PLUXY_MANAGEMENT_KEY, or --config <file>
```

```text
pluxy: no management key for http://127.0.0.1:8317. Pass --key, set PLUXY_MANAGEMENT_KEY, or use --config <file>
```

Exit code 4. Core returned 401 because the management key was wrong or omitted.

Pass the `management_key` from `config.json` with `--key`, set `PLUXY_MANAGEMENT_KEY`, or point `--config` at core's `config.json`.

### Malformed `--url`

```text
pluxy: --url "127.0.0.1:8317" must start with http:// or https://, such as http://127.0.0.1:8317
```

Exit code 2. The value passed to `--url` or `PLUXY_URL` is missing its scheme or host.

Include `http://` or `https://`, for example `--url http://127.0.0.1:8317`.

## A client gets an error

### Wrong or missing client key

```json
{"error":{"code":"invalid_api_key","message":"missing or invalid API key","type":"authentication_error"}}
```

HTTP 401 with `X-Should-Retry: false`. The client sent a key that is not in `client_keys` in `config.json` (or sent no key).

Copy a key from `client_keys` in `config.json`, or create a new one with `pluxy key add`. Run `pluxy client show <tool>` to print the config block for your coding tool (see [docs/clients.md](clients.md)).

### No account serves the requested model

```json
{"error":{"code":"model_not_found","message":"no account serves model \"nonexistent-model\"","pluxy":{"trace":"20261124091635-ce7ef4b16d5019cf","tried":[],"left_out":[]},"type":"invalid_request_error"}}
```

HTTP 404. No enabled account serves that model name. `pluxy route nonexistent-model` fails with exit code 5 and prints:

```text
pluxy: no account serves nonexistent-model. See pluxy model list.
```

Run `pluxy model list` to see the model names your accounts serve. If a request failed after trying accounts or skipping blocked ones, the `pluxy` object lists `tried` and `left_out` with each account's public ID and state.

## An account is not used

Run `pluxy account list` to check every account's `HEALTH`, `UNTIL`, and `WHY` columns, or `pluxy route <model>` to see which accounts a model would try and which appear under `left out:`.

### Account needs a new sign-in

```text
HEALTH       UNTIL  WHY
needs_login  -      the test provider revoked this sign-in
```

The provider refused or revoked the stored credential, so core stops routing requests to this account.

Run `pluxy account login <id>` to sign in again, or open the account on the panel's **Accounts** page and click **Sign in again**.

### Account is cooling down

```text
HEALTH   UNTIL                WHY
cooling  2026-11-24 09:21:35  model rate-limited: rate limited
```

The account hit a rate limit or transient upstream error and is on a temporary cooldown until the shown time.

Wait until `UNTIL` passes, or clear the block immediately with `pluxy account clear <id>` (or **Clear cooldown** in the account's panel details).

### Account limit is used up

```text
HEALTH     UNTIL                WHY
exhausted  2026-11-24 12:16:33  usage window used up
```

A provider quota limit ran out. During blocks longer than 15 minutes, core checks the account every 15 minutes and ends the block early if the limit resets.

Check what remains with `pluxy account quota <id>`, wait for the reset, or add another account with `pluxy account add <provider>`.

### Account is disabled

```text
HEALTH    UNTIL  WHY
disabled  -      turned off
```

The account was turned off manually and is skipped by routing.

Re-enable it with `pluxy account enable <id>`, or turn it back on from the panel's **Accounts** page.

### Extension is installed but not running

```text
ID             VERSION  ENABLED  STATE     REGISTRY
test/beta      1.0.0    no       disabled  bundled
```

`pluxy ext list` shows installed packages and their `ENABLED` and `STATE` (`disabled`, `active`, `idle`, `starting`, `crashed`, `quarantined`, `failed`, or `stopped` in safe mode). `pluxy status` lists only running extensions.

Enable a disabled package with `pluxy ext enable <id>`. If `STATE` is `failed` or `crashed`, check `pluxy status` and `pluxy logs` for the error; if you started core with `-safe-mode`, restart without `-safe-mode`.

## The panel

### Panel is missing from a source build

```text
This core was built without its panel. Build the panel with go generate ./internal/panel, then build core again.
```

HTTP 503 at `/panel/`. The web panel is built separately and embedded into the `pluxy` binary; `go build` without `go generate` produces a core with no panel.

From `next/`, run:

```bash
go generate ./internal/panel
go build -o ~/pluxy/pluxy ./cmd/pluxy
```

Then restart core.

### Signing in to the panel

Open `http://127.0.0.1:8317/panel/` (or the `listen` address core printed). The sign-in screen asks for the **Management key**: paste `management_key` from `config.json` and click **Sign in**.

If sign-in from another computer says "This pluxy answers only the computer it runs on", remote management is off: pluxy answers management calls only from the computer it runs on (its reply is 403 `remote_disabled`). On that computer, set `"management_allow_remote": true` in pluxy's `config.json` (`pluxy help` there shows where the file is) and save it. pluxy picks the change up by itself, so sign in again. Any other refusal at sign-in is shown as "The reply from pluxy was", the status and pluxy's own reason.

If the panel says "This management key was not accepted." at sign-in, or prints `Core refused the management key. Sign in again.` once you were signed in, verify you used `management_key` rather than a client key from `client_keys`. Five wrong keys in a row from another machine block that address for 30 minutes (403 `blocked`); requests from core's own machine are never blocked.

## Seeing what happened

Core reads the path given to `-config`, else `config.json` in the folder it starts in, else the one in [pluxy's home](../next/README.md#pluxys-home); a start without `-config` logs which (`config: <path>`). Log files live in the `logs` folder beside `config.json`: `main.log` (rotated to `main-<time>.log`), `error-<name>.log` for failed requests, and per-request logs when request logging is on.

### File logging is off

```text
pluxy: logging to file is off (invalid_request)
```

Exit code 2 from `pluxy logs`. Core writes to standard error by default and only writes `main.log` when file logging is enabled.

Add `"logs": {"to_file": true}` to `config.json` and run `pluxy reload` (or turn on **Write pluxy's log to a file** in the **Logging** section of the panel's **Config** page).

### Request logging is off when reading a request log

```text
pluxy: no request log has this trace ID; request logging is off, so only requests that failed are logged. Set "logs": {"request_log": true} in core's config file, run pluxy reload, then send the request again (not_found)
```

Exit code 5 from `pluxy logs request <id>`. With request logging off, core writes request logs only for failed requests (status 400 or higher, or a stream that breaks mid-reply).

Set `"logs": {"request_log": true}` in `config.json`, run `pluxy reload`, send the request again, and run `pluxy logs request <id>` with the new trace ID.

### Checking why a request went where it did

Every client reply that passes key checks includes `X-CPA-TRACE-ID`. Replies routed to an account also carry `X-Pluxy-Account` (the account's public ID), `X-Pluxy-Provider`, `X-Pluxy-Model`, and `X-Pluxy-Attempts` (unless `routing.explain` is `false`).

To inspect routing without sending a request, run `pluxy route <model>`. To inspect recent requests, their accounts, status codes, and attempt counts, run `pluxy usage events`.

## Still stuck

Before asking for help, collect these three items. Read them before you share them: pluxy replaces keys and tokens it recognises, but a request log holds the text of the request and its reply.

1. Core and extension state: `pluxy status`
2. The failing request's trace ID from the `X-CPA-TRACE-ID` response header or `error.pluxy.trace`
3. The error log: list files with `pluxy logs errors`, then print the matching file with `pluxy logs errors <name>` (or `pluxy logs request <id>` using the trace ID)

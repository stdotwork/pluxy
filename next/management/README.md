# Management API

Everything that controls a running core goes through one HTTP API, `/v0/management`. The web panel, the
desktop app and the `pluxy` command are clients of it and can do nothing it cannot. The client API
(`/v1/...`) is separate and is not described here.

This file is the API's contract. Every API call and field below is served today. The CLI commands
marked `(not built yet)` are listed together in [Commands not built yet](#commands-not-built-yet);
every other command is in `pluxy help`. The Go types of every request and reply, and a client, are
in this package (`types.go`, `client.go`); core's handlers write the same types.

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
- **Bodies.** JSON in, JSON out, UTF-8. These calls refuse unknown fields, and anything after the one JSON object, with 400 `invalid_request`:
  `POST /models/refresh`, `POST /config/validate`, `PUT /config`, `PATCH /settings`,
  `POST /setup/apply`, `POST /setup/import`, `PUT /chains/{name}`, `PUT /theme`, `POST /client-keys`,
  `PATCH /client-keys/{id}` and `PATCH /accounts/{id}`, which takes `disabled`, `label`, `settings` and an account's routing fields (with `timeouts`). The
  other calls that read a body ignore the fields they do not know: `POST /packages`,
  `PATCH /extensions/{id}/config`, `POST /accounts`, `POST /accounts/{id}/login`,
  `POST /logins/{id}/input`, `POST /accounts/{id}/test` and `POST /accounts/import`.
  `POST /vertex/import` reads multipart form data and ignores the parts it does not use, and
  `PUT /themes/{publisher}/{name}` stores any valid JSON object. A call that takes no body ignores
  one. A body is read up to 1 MiB, 4 MiB for `POST /accounts/import` and 256 KiB for a stored theme;
  a larger one is 413 `invalid_request` from every call that reads a body. Times are RFC 3339 UTC;
  durations are whole milliseconds in fields ending `_ms`.
- **Errors.** Every failure is `{"error": {"code": "...", "message": "..."}}` with the HTTP status that
  fits; `code` is stable and `message` is for a person. An error about an upstream account may add
  `status`, `provider_code`, `retryable`, `retry_after_ms`.

  | Status | Codes | Meaning |
  | --- | --- | --- |
  | 400 | `invalid_request` | The request does not parse, lacks a required member, or names a field or value the call does not take |
  | 400 | `file_required`, `invalid_json`, `invalid_service_account`, `project_id_missing` | `POST /vertex/import` did not get a usable service account key |
  | 401 | `unauthorized` | No key, or the wrong key |
  | 403 | `remote_disabled` | The call came from another machine while `management_allow_remote` is off |
  | 403 | `blocked` | Too many wrong keys came from the caller's address; `Retry-After` says for how long |
  | 404 | `not_found` | No such account, package, chain, key, log, theme or login |
  | 409 | `conflict` | The thing changed since it was read, or the change would leave core unusable (the last client key) |
  | 413 | `invalid_request` | The body is larger than core reads |
  | 413 | `log_too_large` | A log file is larger than 2 MiB and `format=json` was asked for |
  | 422 | `invalid_config` | A config document or an extension's settings do not parse or fail their checks |
  | 422 | `invalid_request` | An account test names no model, and the account lists none |
  | 422 | `refused` | Core checked the change and will not apply it; `message` says why; nothing was changed |
  | 422 | `unsupported` | The extension that would answer does not take that account call |
  | 500 | `internal` | Core could not finish the call, because of its own error rather than the request's |
  | 502 | `fetch_failed` | A package registry could not be fetched |
  | 502 | `account_closed`, `auth`, `cancelled`, `extension_crashed`, `extension_protocol`, `extension_timeout`, `extension_unavailable`, `internal`, `invalid_request`, `not_found`, `permission`, `quota`, `rate_limit`, `unavailable`, `unsupported`, `upstream` | An account, login or import call ended on an extension's or a provider's error, with that error's code; the body may add `status`, `provider_code`, `retryable`, `retry_after_ms` |
  | 503 | `extension_unavailable` | The extension that would answer an account models, quota or test call is not running |
  | 504 | `unavailable` | The service account login in `POST /vertex/import` did not finish in time |

- **Secrets.** No reply holds a credential, a client key or the management key, except the one reply that
  creates a key. Account IDs and labels are shown.
- **Changes are whole.** A call that changes something is applied completely or not at all, through the
  same steps as a config reload; requests already running finish on what they started with.
- **Compatibility.** `GET /status` has `management_api`, a number that goes up when a call changes in a
  way an older client would misread. Fields are only added between such changes.

## The `pluxy` command

`pluxy` is one program: `pluxy serve` runs the core (as does `pluxy -config ...` with no subcommand),
`pluxy init [-config file] [-port n]` writes a first config without starting core, `pluxy -version`
prints the build's version, commit and date, and `pluxy help` lists every subcommand. Every other
subcommand is a client of the management API of a core that is already running, except
`pluxy key management`, which calls no core.

- **Which core.** The address and the management key each come from the first of: `--url` and `--key`;
  `PLUXY_URL` and `PLUXY_MANAGEMENT_KEY`; the config file, which is `--config <path>`, else `config.json`
  in the current folder, else `config.json` in [pluxy's home](../README.md#pluxys-home). `--verbose` says
  on standard error which core the command called and where its address and key came from. With no
  config file anywhere, the error names each place looked in and what to run (exit code 1; 5 for
  `pluxy key management`).
- **A key is not sent where a folder says.** When the key comes from `--key` or
  `PLUXY_MANAGEMENT_KEY` and the address only from `config.json` in the current folder, the command
  compares that file's `management_key` with the key it was given, byte for byte (in constant time).
  When the file loads and has a usable address and the keys differ, it sends nothing and exits 2
  (`pluxy: not sending the management key from <where> to <address>, the address in this folder's
  config.json, which holds a different key`), so a stray file cannot steer a key to another machine.
  A file with no `management_key`, or whose `listen` has port 0, fails first with its own error (exit
  1) and sends nothing either. When the current folder is pluxy's home, its `config.json` is the
  folder's and is checked. The same key in the file, `--config` (a deliberate choice of file, so the
  key given goes to its address), `--url` and `PLUXY_URL` are not refused; pluxy's home's
  `config.json`, read when the current folder has none, is not checked, and neither is
  `pluxy key management`, which calls no core. The message says what to do: leave out the key it
  names, so the key in the folder's `config.json` is used, or add `--url <its address>` to call another.
- **Environment variables.** Client subcommands read two variables:

  | Variable | What it does | Default | Example |
  | --- | --- | --- | --- |
  | `PLUXY_URL` | Core's base URL for `pluxy` client commands; `--url` overrides it. | Read from the config file (above) | `http://127.0.0.1:8317` |
  | `PLUXY_MANAGEMENT_KEY` | Management API key for `pluxy` client commands; `--key` overrides it. | Read from the config file (above) | `<management-key>` |

  `PLUXY_HOME` names pluxy's home ([next/README.md](../README.md#pluxys-home)).
- **Output.** For people by default: aligned columns, one thing per line, no colour when not a terminal.
  Help asked for (`help`, `-h`, `--help`) goes to standard output and exits 0. With `--json`: the API's
  reply body, unchanged, on standard output and nothing else (`{}` when core replied with an empty body).
  Errors go to standard error: a sentence, or with `--json` the API's error object (including flag and
  usage errors).
- **Nothing asks a question** unless standard input is a terminal; `--yes` answers yes.
- **The management key.** `pluxy key management [--show-key]` calls no core: it says where the key the
  commands use came from, the `management_key` field of the config file read, `PLUXY_MANAGEMENT_KEY` or
  `--key`, and with `--show-key` prints the key alone on standard output. When there is no key to find it
  says why: each place it looked or the `--config` file that is not there (exit code 5), or a config
  file core refuses too, one that does not parse or has no `management_key`, with core's reason
  (exit code 1). When the key or its place cannot be written to standard output, it exits 1. Its `--json` is `{"from": "config", "config": "<path>"}` (`from` is `config`, `--key` or
  `PLUXY_MANAGEMENT_KEY`, and `config` is there for `config`), with `key` only with `--show-key`.
  `pluxy key rotate-management [--show-key] [--yes]` is in the [Core](#core) table.
- **Signing in.** `pluxy account add` and `pluxy account login` start a sign-in and follow it: the
  address to open or the code to type is printed (on standard error), each value the sign-in asks
  for is sent to it, and core is read again every 2 seconds until it ends. A value a sign-in wants
  that `--input name=value` did not give is read from standard input, one line for each value, in
  the order the sign-in asks for them; on a terminal each value is asked for by its label. A blank
  line leaves an optional field out, and `account login` also leaves out a non-secret value the
  account's settings already hold. A field that takes a JSON file, Vertex's service account key, is
  read until the document is whole, and one that never ends or passes 1 MiB is refused rather than
  sent cut short; any other value is one line, whatever it starts with. At the end of standard
  input the values read are sent if every required field has one. A secret's value is never taken
  from `--input`, where the shell's history and the process list keep it, and no value is printed
  back; a terminal keeps a secret or a JSON file off the screen until the whole value is read, and
  drops lines typed for a prompt the sign-in has moved past. At a terminal whose input is still open, a
  prompt that is still up after its answer and a further read of the sign-in may be answered again,
  as may one core refused an answer for. Once input has ended (Ctrl-D) nothing is asked again: a
  prompt still up waits as a piped one does, and an answer core refuses cancels the sign-in. Piped
  input answers each prompt once, since its next line may be the next prompt's value: the command
  waits a minute for an input prompt to move on, then cancels. With
  `--json` nothing is read from a terminal: the values come from `--input` or from a pipe, or the
  command says what it needs with exit code 2. A sign-in that needs a value it did not get is
  cancelled in core, with exit code 2, and so is one `--no-wait` leaves waiting on a prompt. A
  browser sign-in is the exception, since the browser's return may still reach its extension: a
  piped address that is refused does not end it, and when standard input ends at a prompt with a
  value to read, the command says once that nothing more can be pasted. It goes on following the
  sign-in until it finishes, core ends it 15 minutes after it started (exit code 1), or Ctrl-C
  cancels it. An extension with no callback can only reach that limit. A read that fails is tried
  three times, and then the sign-in is given up on and cancelled. Ctrl-C, SIGTERM and SIGHUP cancel
  the sign-in and put the terminal back as it was.
- **Exit codes.**

  | Code | Meaning |
  | --- | --- |
  | 0 | Done |
  | 1 | The call failed (any error not listed below) |
  | 2 | The command line or request is invalid (unknown subcommand or flag, missing argument, malformed `--url`, or 400 from core) |
  | 3 | No core answered at the address |
  | 4 | Refused: no key, wrong key, another machine, or blocked (401, 403) |
  | 5 | Not found (404, or `route` for a model no account serves) |
  | 6 | Core refused the change (409 or 422 from a command that changes something); nothing was changed |
  | 7 | Waiting for a person: a sign-in needs a browser or a code, and `--no-wait` was given |

## Resources

`{id}` of a package is `publisher/name`.

### Core

| Call | Command | What it does |
| --- | --- | --- |
| `GET /status` | `pluxy status` | Config versions (wanted and serving), each extension's state, build version, `management_api`, and `first_run` |
| `POST /reload` | `pluxy reload` | Reload the config now. 422 with the status when refused |
| `GET /requests`, `POST /requests/{trace}/cancel` | `pluxy requests`, `pluxy request cancel <trace>` | The client requests core is serving now, oldest first, each with its trace ID (`id`, the `X-CPA-TRACE-ID` reply header), when it arrived, endpoint, format, whether it streams, model, the client key's ID, the client's address and, once routing has chosen, the account, provider and upstream model of the newest attempt and the number of attempts. A Responses WebSocket is listed while one of its turns runs, under the socket's trace ID. `cancel` ends one as its client going away does: the call to the extension is closed, the client gets the `cancelled` error (a WebSocket is closed), the usage record has the request as `cancelled`, and core's log says an operator cancelled it. The reply is the request as it was. Cancel is best effort: it answers 200 whenever the request is listed, and a reply core had already gathered may still reach the client whole. A request cancelled before it was routed (its body still arriving, say) is answered with the same error and makes no attempt. 404 `not_found` when no request with that trace ID is being served, also one that has just ended (exit code 5) |
| `GET /config`, `POST /config/validate`, `PUT /config` | `pluxy config show`, `pluxy config validate <file>` (not built yet), `pluxy config apply <file>` (not built yet) | The config file with secrets replaced by placeholders; check a document; replace the file |
| `GET /settings`, `PATCH /settings` | `pluxy settings` | The basic and routing settings by name, [window ignition](../README.md#window-ignition)'s (`routing.ignition`) among them. `pluxy settings` only reads them (a command for `PATCH /settings` is not built yet; Config's Settings tab changes them) |
| `GET /client-keys`, `POST /client-keys`, `PATCH /client-keys/{id}`, `DELETE /client-keys/{id}` | `pluxy key list`, `pluxy key add`, `pluxy key remove <id>` (`PATCH` has no command yet) | Client keys: `{ "id", "masked", "label"? }`. `id` is stable across restarts. `add` prints the key once. `POST` takes an optional `{ "label" }` and `PATCH` `{ "label" }`, a name such as the tool the key is for; `""` or `null` clears it. A label is trimmed, one line, at most 64 characters (the same as an account's), and not a client key or the management key, else 400 (a hand-edited file with such a label is refused too); `PATCH` answers 404 for an ID no key has, and 422 `refused`, writing nothing, when a `config.yaml` sets the key's label through a YAML alias or merge key (`<<`), which core cannot change in place: edit the file. A `PATCH` that succeeds answers with the key as stored. Deleting a key deletes its label. Usage events record the key's `label` at request time. The names are kept beside the keys in the config file (`client_key_labels`, by key ID) |
| `POST /management-key/rotate/stage`, `POST /management-key/rotate/commit` | `pluxy key rotate-management [--show-key] [--yes]` | A new management key in two steps, so that, one rotation at a time, a lost reply does not leave the caller without a key that works. `stage` makes the key and keeps it in memory only: one at a time (another stage replaces it), for 10 minutes, and not across a restart. The old key keeps working and the config file is not touched. The reply is `{"value": "<new key>", "config": "<config file>"}`, `config` being the absolute path of the config file core runs on. `commit` with `{"value": "<staged key>"}` writes the staged key to that file and puts it into use, and the old key stops working: 204. Core decides a commit under the lock every config write takes, a stage sent meanwhile waits for it, and a commit of the key already in use changes nothing and answers 204 too, so a commit sent again after a lost reply is safe. Any other value (wrong, replaced, older than 10 minutes, or staged before a restart) answers 409 `conflict` and changes nothing. An older core answers 404 to both. The command asks first on a terminal and needs `--yes` without one; without `--show-key` it needs pluxy's address and key from a config file, and that address to be a loopback one (otherwise exit code 2, before any call: over another address the config file it read can be another machine's). It stages, and then makes sure the new key can be had: without `--show-key` only when the config file it read is the file `config` names, the same directory entry (a symbolic link to it counts), not a copy with the same contents or a hard link elsewhere, which keeps the old key once pluxy replaces its file (otherwise exit code 2, nothing changed); with `--show-key` by printing the key alone on standard output (with `--json`, the stage reply), and only when that write works. Then it commits. When the commit's reply is lost it commits once more with the old key: 204 is done; 401 is done when the new key answers `GET /status`; 409 is nothing changed. A stage reply that is lost or holds no key, a key that cannot be printed, and a commit core answers 409 change nothing: exit code 1, saying the old key still works. A 404 from `stage` (an older core) changes nothing either, exit code 1. When core stops answering during the commit, the command exits 1 and says which key to try: the one it printed, or the one in the config file. It exits 0 only when the new key is in use, and ends by naming what must be given the new key: the web panel, the desktop app, `PLUXY_MANAGEMENT_KEY` and `--key` |
| `POST /management-key/rotate` | None: the desktop app and the web panel use it | A new management key in one step, which core writes to its config file; the old one stops working at once. The reply is `{"value": "<new key>"}`. A reply lost on its way leaves the caller with neither key, which is why `pluxy key rotate-management` uses the two steps above |
| `GET /logs`, `DELETE /logs`, `GET /request-error-logs[/{name}]`, `GET /request-log-by-id/{id}` | `pluxy logs [--after n] [--limit n]`, `pluxy logs clear`, `pluxy logs errors [name]`, `pluxy logs request <id>` | Core's log and request logs. `logs errors` lists the error logs (name, size in bytes, modified); with a name, and `logs request`, it copies the file's text to standard output as it arrives, however large, and on a terminal shows control and Unicode format characters escaped (`\x1b`) so a logged escape sequence cannot act on it; `pluxy logs` escapes its lines the same way. `--json` is the reply (`?format=json`, which core refuses above 2 MiB with 413 `log_too_large`). `logs clear` says what core reports: `main.log` emptied, how many rotated files it deleted, request and error logs kept. If any file could not be deleted or emptied, `DELETE /logs` still tries the rest and answers 500 `internal` naming how many rotated files it deleted and what is left |

### Setup

Setup is what a new install shows first. Core looks at the machine it runs on, says what it found, and
proposes what to install. Nothing is installed until asked.

`first_run` is true only while there is no `state/setup.json` and core has no installed packages and no accounts; once `setup.json` exists, its `first_run` is kept. Core scans on its own only at a first run's start with no kept scan; otherwise only on `POST /setup/scan`.

| Call | Command | What it does |
| --- | --- | --- |
| `GET /setup` | `pluxy setup` | The last scan, at once (it is kept on disk): `first_run`; `scanning`; `scanned_at`; `found` (below); `picks` (below); `registries` (how many extension registries core reads from; `0` when core has neither a bundled registry nor any in `packages.registries`). The command prints it and, on a terminal, offers to apply the picks marked `suggested` (`--yes` applies without asking). While `first_run` is true and without `--json`, it ends with one line on standard error that names the next step: the picks to apply, the account to add, the client to point at core, or `pluxy setup done` |
| `POST /setup/scan` | `pluxy setup scan` | Looks again. Returns at once with `scanning: true`; read `GET /setup` until it is false (each probe has 4 seconds) |
| `POST /setup/apply` | `pluxy setup apply [--pick id]... [--yes]` | `{"picks": ["pluxy/claude", ...]}`: installs and enables each. Returns `{"results": [{"id", "status", "package", "error"}]}` with one result per pick (`done`, or `error`); one failure does not undo the others. The command then names the next step as `pluxy setup` does |
| `POST /setup/import` | `pluxy import cpa <config.yaml> [--auth-dir <dir>] [--dry-run]` | `{"config": "<path>", "auth_dir": "<path>", "dry_run": true}`: brings a CLIProxyAPI setup on core's machine across and returns the import report (below). `config` is an absolute path (or starts with `~`); `auth_dir` defaults to the config's `oauth.auth-dir`; the command makes a relative path absolute from the current folder. The config's settings, client keys, API-key accounts, aliases and chains go into core's config file in one change, and the auth folder's accounts into its credential store; `listen` stays, since it cannot change while core runs, and so does the management key, which is not brought (a plain one or a bcrypt hash): core keeps its own. An account or chain core already has one by that ID or name stays as it is. With `dry_run` nothing is written. 400 for a path that is not absolute or a file that cannot be read (the command prints that message alone, with no error code after it); 422 for a file core refuses (the message has the file's secrets taken out) or a credential store inside the auth folder; 409 for an import (not a dry run) into a core that runs on a config.yaml |
| `POST /setup/done` | `pluxy setup done` | Ends first run (`{"first_run": false}`): the panel and app stop opening on setup |
| `GET /setup/clients/{id}/paste` | `pluxy client show <tool> [--key-id id] [--model name]` | What to paste into that client tool's config to point it at this core: its files, the address, a client key placeholder and the model the tool starts with. `id` is the tool's ID (`claude-code`, `codex`, `opencode`, `pi`, `omp`) or another name for it (`oh-my-pi`, `codex-cli`, a setup package's ID such as `pluxy/pi-setup`, with the `/` escaped as `%2F`). Optional `key_id` names a client key from `GET /client-keys`; the placeholder then names it. Optional `model` names the model the tool starts with (at most 240 characters, no control characters, else 400 `invalid_request`); without it the text names the best match among the models core serves, and `<model-id>` when it serves none. 404 for a tool core has no text for, and for a `key_id` core does not hold |
| `POST /setup/clients/{id}/connect` | `pluxy client connect <tool> [--model name]` | Points the tool, on core's machine, at core: core writes into the tool's settings file what `GET /setup/clients/{id}/paste` shows, with core's own address, a client key made for the tool, and the model the tool starts with, and keeps the file as it was. An optional JSON body takes `label` (names that key, as `POST /client-keys` does; default: the tool's name) and `model` (checked like `paste`'s: at most 240 characters, no control characters, else 400 `invalid_request`; without it core picks the best match among the models it serves, and when core serves none it writes no placeholder and leaves any existing model setting alone). Only `claude-code` (`~/.claude/settings.json`) and `codex` (`~/.codex/config.toml`), in the home folder of the user core runs as. Every other setting in the file stays as written; Claude Code's `ANTHROPIC_API_KEY` and the pluxy provider's `env_key` and `requires_openai_auth` in Codex's config are taken out, since they would send another credential. A missing folder is made (0700) and a missing file too (0600); a file that holds a key afterwards is readable by its owner alone. When the file already holds a client key core holds, that key stays and none is made. 404 for a tool core does not connect; 422 (`refused`) for a file the tool's writer in the desktop app would refuse, a link where the folder or the file is, anything but a file, a folder or file another user owns, or a file over 2 MiB, and then nothing is written and no key made |
| `POST /setup/clients/{id}/disconnect` | `pluxy client disconnect <tool>` | Puts back what the connect changed: the file as it was before the first connect when it still holds what the connect wrote, else (it changed since) each setting the connect changed and nobody changed since; a file or folder the connect made goes. The client key the connect made goes too, unless another connected tool sends it or it is the last client key. 409 when core did not connect the tool, so it keeps nothing to put back |

The reply to `GET /setup/clients/{id}/paste`: `tool` (the canonical ID), `name`, `base_url` (the address a program uses to reach core: core's own, as a program on core's machine reaches it, for a caller on that machine, and the address the caller reached core by, from its `Host` and whether the connection is TLS, for a caller on another), `key` (`<client-key>`, or with `key_id` `<client-key:ID (MASKED)>`: never a key's value, never the management key), `files` (`path` from the home folder, and `content`), `note` (what the files need besides themselves; left out when empty), `model` (the model the files name; left out when there is none) and `models` (the names core serves now, for a person to choose `model` from). A coding tool asks for a model of its own unless told which one, and an account of another service serves none of those, so every tool's text names one: Claude Code's `model` and its three `ANTHROPIC_DEFAULT_*_MODEL` settings, Codex's `model`, and the model of OpenCode, Pi and Oh My Pi. The best match is a model made for the tool (a Claude model for Claude Code, a `codex` or `gpt-` one for Codex), else the first that is not an embedding, image or audio model. The command prints the files as they are, and with `--json` the reply body.

The reply to connect and disconnect: `client` (the tool as `found.clients` lists it, read again), `files` (`path` from the home folder, and `change`: `created`, `changed` or `unchanged` for a connect; `restored`, `merged`, `removed` or `unchanged` for a disconnect), `key` (a connect's: the client key the tool sends, `id`, `masked` and `label`, never its value), `key_made`, `key_removed` and `model` (a connect's: the model written for the tool to start with; left out when core served no model and none was asked for, and on disconnect). Core keeps each connect's record, the files as they were among it, in `state/tools/<id>.json`, readable by core's user alone, and logs which files it changed, never a key.

`found`:

```json
{
  "clients": [
    {"id": "claude-code", "name": "Claude Code", "installed": true, "version": "2.4.1",
     "config": "~/.claude/settings.json", "state": "not-configured"}
  ],
  "sign_ins": [
    {"provider": "pluxy/codex", "from": "codex", "label": "Codex is signed in on this computer"}
  ],
  "imports": [
    {"kind": "cliproxyapi", "config": "~/.cli-proxy-api/config.yaml", "auth_dir": "~/.cli-proxy-api", "accounts": 7,
     "last_import": {"at": "2026-11-20T08:30:05Z", "brought": 31, "changed": 3, "not_brought": 6}}
  ],
  "core": {"listen": "127.0.0.1:8317", "reachable_from_network": false}
}
```

`GET /setup` adds to each client `can_connect` (core can write its settings itself: `claude-code` and `codex`, unless `refused`) and `connected_by_pluxy` (core connected it and keeps the file as it was); both are left out when false.

A client's `state` is `not-configured`, `configured` (it points at this core: at core's address with its scheme and port, or, for a core listening on every interface such as `0.0.0.0`, at a loopback address or an address of core's machine on that port, such as the one the panel was reached by, of the listener's family (IPv4 for `0.0.0.0`, IPv6 for `::`) or of either family when the listener takes both, as Go's does where the system allows it; `localhost` counts only then; when core's own address is not known, no address counts), `needs-update`, `refused` (a file the desktop app's writer for that tool would refuse to change; no pick is suggested to apply to it), or `unchecked` (core does not read this tool's settings, so it cannot say whether they point at this core; see the last three tools below).

`clients` lists eight tools: `claude-code`, `codex`, `opencode`, `pi`, `omp`, `claude-desktop`, `grok-build` and `hermes`. The last three are looked for where the desktop app's tools for them look: Claude Desktop is the program `claude-desktop` on the `PATH` on Linux and `Claude.app` in `/Applications` or `~/Applications` on macOS (core knows of nothing to look for on Windows); Grok Build is `~/.grok/bin/grok`, else `grok` on the `PATH`; Hermes Agent is `~/.local/bin/hermes` (`bin\hermes.exe` in the `hermes` folder of `%LOCALAPPDATA%` on Windows), else `hermes` on the `PATH`. Core does not run Claude Desktop, so it has no `version`, and does not read the settings of any of the three: their `state` is always `unchecked`, found or not (`installed` says whether core found the tool; `config` names the file the desktop app edits), and they have no paste text (`GET /setup/clients/{id}/paste` answers 404 for them). `sign_ins`
only says a sign-in exists; core reads no token from it.

An import's `last_import` is there once core has brought that setup across (`POST /setup/import`
without `dry_run`, from the command or the panel): `at`, the time in UTC the last such import ended, and
`brought`, `changed` and `not_brought`, the number of rows in each list of its report (below). A row is a
key of the config or a file of the auth folder, so the numbers do not count accounts. Core keeps the
record in `state/setup.json`, by the config file's path, and a later scan does not drop it. The panel's
Setup page then says the setup was brought across and offers the import by the command only; `pluxy setup`
shows the time in its Imports table.

`picks`, in the order to show them:

```json
[
  {"id": "pluxy/claude", "kind": "provider", "title": "Claude", "summary": "Claude subscriptions and API keys",
   "installed": false, "suggested": true, "because": "Claude Code is installed on this computer"}
]
```

`suggested` is true only for a pick that something found on the machine points to. A machine where nothing
was found gets the same list with nothing suggested.

The import report:

```json
{
  "dry_run": true,
  "brought": [{"what": "auth file", "name": "codex-work@example.com.json", "as": "pluxy/codex account codex-work-example.com"},
              {"what": "routing.strategy"}],
  "changed": [{"what": "server.host", "from": "(not set: every interface)", "to": "127.0.0.1:8317",
               "why": "core keeps listening on 127.0.0.1:8317: listen cannot change while core runs",
               "keep_with": "\"listen\": \"0.0.0.0:8317\" in pluxy's config file, then restart core"}],
  "not_brought": [{"what": "auth file", "name": "gemini-user.json", "why": "today's core does not load it either"}],
  "needs": [{"pick": "pluxy/codex", "why": "3 accounts need it"}]
}
```

Every key of the config and every file of the auth folder is in exactly one of `brought`, `changed` and
`not_brought`. An import whose report could not account for an item fails instead of dropping it. A key is
named by its path in the file (`server.port`, or `port` in the older spelling); a list is one key, and a
provider section's entry is one key (`api-keys.codex[0]`) whose row names the accounts it became. Every
`not_brought` row says why. A row may also name a key the file does not set: a file with no `server.host`
gets a row for it (today's core then listened on every interface), and every listen row, that one included,
says core keeps listening on its own address while it runs, with the file's reach in `from` and core's in
`to`. `needs` lists the provider packages
the imported accounts need that core has not installed; core installs nothing by itself. A `management.secret-key`
is not brought, plain or bcrypt: core keeps its own management key, and its row says so and names
`POST /management-key/rotate` to change it.

### Extensions

| Call | Command | What it does |
| --- | --- | --- |
| `GET /packages` | `pluxy ext list` | Installed packages and their state |
| `GET /registries` | `pluxy ext available` | What each registry offers, with each package's `title`, `summary`, `kind` (`provider`, `client`) and `tier` (`pick`, `verified`, `unverified`) when its registry says them; core enforces nothing by tier. A first start installs nothing: every package, the bundled ones too, is installed by `POST /packages` |
| `POST /packages` | `pluxy ext install <id> [--version v] [--registry r]` | Install and enable |
| `POST /packages/{id}/enable`, `.../disable` | `pluxy ext enable <id>`, `pluxy ext disable <id>` | Start or stop routing to it |
| `DELETE /packages/{id}` | `pluxy ext remove <id>` | Remove its files; its accounts stay |
| `GET`, `PATCH /extensions/{id}/config` | `pluxy ext config <id> [name=value]...` (not built yet) | An extension's own settings |

### Accounts

| Call | Command | What it does |
| --- | --- | --- |
| `GET /accounts` | `pluxy account list` | Every account, with its `timeouts` when it sets them, `health` (below) and `public_id`: the short stable ID client replies name the account by, never an email address or a label (below). An account that [window ignition](../README.md#window-ignition) runs for also has `ignition`: `{"windows": [{"id", "state", "next", "why", "failures", "last", "model", "error"}], "error"}`, each usage window core starts with its state (`running`, `waiting`, `starting` or `paused`) and when core starts it next; `pluxy account list --json` shows it. Also `quota`, when core has read the account's quota since it started: `{"fetched_at", "quota"}`, the last reply of `GET /accounts/{id}/quota` as it was, whoever asked for it (that call or routing's own readings for `soonest-reset`); it is kept in memory until the account is removed, and absent for an account never read. The list reads nothing from providers to fill it |
| `GET /login-methods` | `pluxy account methods` | How each running extension can add an account |
| `POST /accounts`, `GET /logins/{id}`, `POST /logins/{id}/input`, `DELETE /logins/{id}` | `pluxy account add <provider> [--id id] [--method m] [--input name=value]... [--no-wait]` | Start a sign-in and follow it to the end; prints the address to open or the code to type. `--id` names the account (default: `account-` and 12 hex digits). The call also takes `label`, a name for people that wins over the provider's (as `PATCH` takes it), and makes the account's ID itself when `id` is left out (`acc_` and 8 hex digits, its own `public_id`; an `id` that, or whose public ID, is another account's ID or public ID is a 409); the command sends neither yet; `--no-wait` stops once it has started, with exit code 7, unless the sign-in waits on a prompt's value, which is exit code 2 and cancels it |
| `POST /accounts/{id}/login` | `pluxy account login <id> [--method m] [--input name=value]... [--no-wait]` | Sign in again, followed as `account add` follows a sign-in |
| `PATCH /accounts/{id}` | `pluxy account enable <id>`, `pluxy account disable <id>`, `pluxy account set <id> name=value...` | Turn on or off; change routing settings. `name` is `disabled`, `timeouts` (an object of `header_ms`, `idle_ms` and `total_ms`) or one of the fields below; its value is JSON, except `prefix` and `proxy_url`, which take their text as it is; `null` sets a field other than `disabled` back to its default. The call also takes `label`, the account's name for people (trimmed, one line of at most 64 characters, else 400; `""` or `null` takes it away, leaving the last label its provider gave it, or none when it never gave one; an account stored before core kept the two apart keeps its old label), which `account set` does not send yet; an account set in the config takes none (409) |
| `DELETE /accounts/{id}` | `pluxy account remove <id>` | Remove |
| `POST /accounts/{id}/refresh` | `pluxy account refresh <id>` | Renew its credential now |
| `POST /accounts/{id}/cooldown/reset` | `pluxy account clear <id>` | Forget the cooldowns routing holds against the account; a ban stays |
| `POST /accounts/{id}/test` | `pluxy account test <id> [--model m]` | One small request through it |
| `GET /accounts/{id}/quota` | `pluxy account quota <id>` | Its usage windows as its provider reports them, written as the panel writes them: the plan, each limit's percent, amounts, reset time and window (`--json` stays the reply) |
| `GET /accounts/{id}/models` | `pluxy account models <id>` | The models it serves, from the list routing keeps (not a fresh read), with `from`: `account`, `extension`, or left out when its config lists them |
| `POST /accounts/import`, `POST /vertex/import` | `pluxy account import <file>` | Add an account from a credential file. A JSON object holding a `private_key` and a `project_id` is a Google service account key and goes to `POST /vertex/import`; every other file is one of today's credential files, whose name gives the account's ID, and goes to `POST /accounts/import`. `-` reads a service account key from standard input; a file that is not a regular one, or is over the 1 MiB core reads, is refused before it is read |

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
| `broken` | Its credential file cannot be read; `why` names the file | When the file is fixed or replaced and core restarts, or the account is removed |

`source` is `provider` when `until` is the provider's own word, `backoff` when core chose it, and
`unverified` when the provider named a time core does not believe (more than 8 days away, or more
than an hour past the usage window's own length when the provider named it); such a block is
held 30 minutes and checked within 15. A block on one of the account's models counts too, and `why` then
names the model, except a model the account does not have (a 404), which says nothing about the account;
the blocks themselves, that one too, stay in `cooldowns`. When several blocks hold the account, an
`exhausted` one decides, then the one that ends last. `next_check` is when core next reads the account's
usage from its provider; it is absent when no check is due, and while a check of an account without a usage
reading waits for the one request it lets through. `pluxy account list` shows `state` (with
`(unverified)`), `until` and `why` as its HEALTH, UNTIL and WHY columns. Blocks on the account and
its models with more than 30 seconds left survive a restart, each with its `error`, and so do the
account's `last_error` and `last_error_at` when one of them set it (see `next/README.md`); endpoint
blocks and shorter ones do not.

A `broken` account is one whose credential file cannot be read: core knows it only by its file's
name, so `extension`, `kind` and `state` are empty and `why` names the file and the error. It never
serves a request, `PATCH` on it is a 404, and `DELETE` removes the file.

`public_id` is `acc_` and 8 hex digits, a keyed digest of the account's ID: an ID can be an email address or a label a person typed (an import of today's auth files names an account by its file), and an ID must not leave core in a client reply. An ID of the shape `acc_` and 8 lower-case hex digits is its own `public_id`, so such an account has one ID everywhere: an account added with `POST /accounts` without an `id` gets one core makes (8 random digits), and an `id` sent to `POST /accounts` or named by `POST /accounts/import` is refused (409) while it or its public ID is another account's ID or public ID. The key is made at the first start and kept in `state/account-key` beside the config, so an account keeps its public ID across restarts. `X-Pluxy-Account`, the header a request pins an account by, takes either ID.

### Models

| Call | Command | What it does |
| --- | --- | --- |
| `GET /models` | `pluxy model list` | Every model clients can ask for, the accounts that serve it, `context_length`, `max_output_tokens`, `capabilities`, and `from` (`account`, `catalogue` or `extension`: where the numbers came from) |
| `POST /models/refresh` | `pluxy model refresh [--account id]` | Ask the providers again now. Lists refresh by themselves every 30 minutes; a list that cannot be read keeps the last good one |

`GET /models` has no `listed_at` field in this version.

### Routing

| Call | Command | What it does |
| --- | --- | --- |
| `GET /chains` | `pluxy chain list` | The fallback chains: `{"chains": [{"name", "steps"}]}`, sorted by name |
| `PUT /chains/{name}` | `pluxy chain set <name> <step>...` | `{"steps": [{"model": "...", "provider": "pluxy/claude", "accounts": ["..."]}]}`. A step on the command line is `model[@provider][:account,account]`. What follows the last `@` is the provider only when it is an extension's ID (`publisher/name`), so `claude-opus-4-7@20260416` is a model; a model whose name holds a colon ends with an `@` (`llama3:8b@`, or `llama3:8b@pluxy/ollama:a1` with a provider and an account). Replies with the chain, `{"name", "steps"}`. 422 for a step no account can serve (under `weighted`, an account with `weight: 0` serves nothing), a step naming an account core does not know or one that does not serve it, a provider that is not an extension's ID, more than 8 steps, a step naming another chain, or a step that repeats another; 409 for a config.yaml, which cannot hold chains |
| `DELETE /chains/{name}` | `pluxy chain remove <name>` | Back to the default: accounts of one provider, the same model. 404 when there is no such chain |
| `GET /route?model=<name>` | `pluxy route <model>` | Sends nothing. Says what a request for the model would do now: each account in the order it would be tried, and for each account left out, why and until when. `{"model", "order": [{"id", "account", "provider", "model"}], "left_out": [{"id", "account", "state", "until"}]}`: `id` is the account's ID (only this API shows it) and `account` its public ID, which client replies use; the rest are the names of the `pluxy` member below. Asking changes no routing state. For a model with a chain, `chain` holds its steps and each account its `step` (from 1), and `left_out` entries name their `provider` and `model`. Under the `soonest-reset` strategy each `order` entry also has `rank`, what it was ranked by: `reset` (its longest usage window's reset, rounded to the hour) and `window_seconds` when a reading names one, and `full_until` when a usage window is full; `{}` when no reading is known. A session's bound account is not shown yet |

### Usage

| Call | Command | What it does |
| --- | --- | --- |
| `GET /usage` | `pluxy usage [--since 7d] [--until t] [--by model\|account\|key\|provider] [--model m] [--account a] [--provider p] [--client-key k]` | Totals, breakdowns (`by` + `breakdown` alongside `models`, `providers`, `accounts`, `keys`) and a time series (`1h` buckets for ranges up to 2 days / 48 hours, `24h` beyond, unless `bucket` is given; `1m` / 60 steps when no range is given). Reaches back across all usage files kept on disk (`usage.keep_days`, default 90), reading closed UTC days from hourly `usage/summary/YYYY-MM-DD.json` files (stays when `.jsonl` files are pruned) and today from memory / `usage/YYYY-MM-DD.jsonl`. Also accepts `key` as an alias for `key_id` |
| `GET /usage/events` | `pluxy usage events [--after seq \| --before [seq]] [--limit n] [--since t] [--until t] [--model m] [--account a] [--provider p] [--client-key k] [--failed true\|false]`, `pluxy usage export [--since t] [--until t] [--model m] [--account a] [--provider p] [--client-key k] [--failed true\|false]` | The recorded requests, oldest first with `after` or newest first with `before`. `usage export` writes those core still holds as CSV (the panel's Export columns and encoding, newest first), paging with `after` and `limit=1000`; on a terminal it shows control and Unicode format characters escaped and ends rows in a plain newline. `usage events` has a TRIES column (`attempts`, `-` when absent) and a WHY column (`why`, `-` when absent); `usage export`'s columns are unchanged. `usage events` on a terminal shows them escaped in the model, account, key and error code cells, tabs and newlines included. Piped and `--json` output is exact. It says on standard error when older requests are no longer available (`lost`) or usage is not recorded. Each event names the requested `model`, the routed account's `upstream_model` when it differs, and the `served_model` the upstream reply names, when it names one. Each event may also carry `chain_step`, `why`, `attempts` and `tried`. `why` is why routing chose the last attempt's account (`pinned`, `session`, `only`, `first`, `only-free`, `backup`, or the strategy's name), as the handbook's "Usage statistics" says. `attempts` is how many accounts the request went to (the reply's `X-Pluxy-Attempts`), absent when it reached none. `tried` lists each attempt in order as the error body's `pluxy.tried` does (`account` public ID, `provider`, `model`, `result`), the attempt that ended the request last: `result` is `ok` when it answered, else the error's code when it is one of core's own and `error` otherwise (a reply that broke after it began, or `cancelled`). It is absent for a request with one attempt that succeeded, whose `account` and `provider` say it; a failed request or one that fell back has the list, so a request with `attempts` above 1 and `failed` false fell back. At most 16 entries are kept, the first 15 and the last, so `attempts` can exceed the length of `tried`. A failed request also has `left_out` when routing left accounts out of it, as the error body's `pluxy.left_out` names them (`account` public ID, `state`, `until` when known, and `provider` and `model` through a fallback chain), the first 8 of them; a request that reached no account has `left_out` and no `account`. They are recorded whatever `routing.explain` says, which hides them from client replies only |

Closed days are counted by whole UTC hours, so a `since` or `until` inside an hour on a closed day counts that whole hour.

### Themes

| Call | Command | What it does |
| --- | --- | --- |
| `GET /themes` | `pluxy theme list` | The selected theme ID (`current`) and the stored themes (`id`, `name`, `bytes`) |
| `GET /themes/{publisher}/{name}` | `pluxy theme show <id>` | One stored theme's JSON unchanged |
| `PUT /themes/{publisher}/{name}` | `pluxy theme add <file> [--id publisher/name]` | Store a theme JSON object (up to 256 KiB; at most 64 stored). 422 `refused` for a built-in theme's ID (`pluxy/default`, `pluxy/paper`, `pluxy/terminal`), which the UI lists as its own; a file stored under one before this rule stays readable and removable. The command refuses an ID that is not `publisher/name` before it calls core. It prints the ID alone on standard output and, unless that theme is already selected, one line on standard error saying it is not turned on |
| `DELETE /themes/{publisher}/{name}` | `pluxy theme remove <id>` | Remove a stored theme; clears `current` if it was selected |
| `PUT /theme` | `pluxy theme set <id>`, `pluxy theme clear` | Select a theme, or clear the selection with an empty `id`. Core takes a stored theme, a built-in theme, and any other ID whose publisher is `pluxy`, since a newer panel or app may hold a built-in this core does not list. Any other ID is 404 `not_found`, and the selection stays |

### Commands not built yet

Core serves the HTTP calls behind these commands today, but the CLI commands themselves are not built yet:

- `pluxy config validate <file>` (`POST /config/validate`) — not built yet.
- `pluxy config apply <file>` (`PUT /config`) — not built yet.
- `pluxy ext config <id> [name=value]...` (`GET`, `PATCH /extensions/{id}/config`) — not built yet.
- `PATCH /settings` has no CLI command yet (`pluxy settings` only reads settings).
- `PATCH /client-keys/{id}` has no CLI command yet, and `pluxy key add` takes no label. The table of `pluxy key list` shows no label; `--json` passes core's reply through, with each key's `label`.

## What a client request gets back

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
- When no account that serves the model can take the request for more than 5 minutes, or none can until a
  person acts (every one turned off, waiting for a new sign-in, closed by its provider or without a running
  provider), the error says `X-Should-Retry: false` and has no `Retry-After`, so a client stops instead of
  retrying; a shorter wait keeps `Retry-After`. Status, body and message do not change.

`routing.explain: false` leaves out the headers and `pluxy`, for a core whose client keys are given to
people who should not see account IDs.

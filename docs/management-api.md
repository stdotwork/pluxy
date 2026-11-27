# Management API

The management API is the HTTP API that controls a running core. The web panel, the desktop app and
the `pluxy` command are clients of it and can do nothing it cannot ([`docs/cli.md`](cli.md) lists the
commands). This page is the reference for every call. The client API that coding tools use
(`/v1/...`) is separate and is not described here.

The Go types of every request and reply, and a Go client, are in
[`next/management/`](../next/management/README.md); core's handlers write the same types.

## Reaching the API

| | |
| --- | --- |
| Address | The `listen` address of the config (`server.host` and `server.port` in a `config.yaml`), with the path prefix `/v0/management`, for example `http://127.0.0.1:8317/v0/management/status`. With `tls.enable` on, use `https`. |
| Key | `Authorization: Bearer <management_key>`, where `management_key` is the config's own key (`management.secret-key` in a `config.yaml`). `pluxy key management --show-key` prints it. |
| Other machines | A call from another machine is refused with 403 `remote_disabled` before its key is read, unless the config sets `management_allow_remote` to `true`. The check covers the management API only: the client API and the panel's page (`/panel/`) are served to other machines whatever this setting says (the client API still needs a client key). The panel's own calls to the management API are management calls, so the check applies to them. |

```bash
curl -H "Authorization: Bearer $MANAGEMENT_KEY" http://127.0.0.1:8317/v0/management/status
```

A core always has a management key. A core started with no config file writes one, and so does
`pluxy init` without starting core: it listens on `127.0.0.1` and has one client key and one
management key. (When pluxy's home has no config and the desktop app keeps a pluxy of its own, a
first start writes nothing and says so.) A config with no management key is refused at start, with a message that says how to
add one; the API is never silently off.

**Where a call comes from.** A call counts as from this machine when it arrives from a loopback
address (`127.0.0.1`, `::1`), or from the very address it arrived at (a program on this machine
calling core at a network address core listens on; a source this machine's own network translation
rewrites to that address, such as a Docker bridge on the host, counts too), and carries none of the headers `X-Forwarded-For`, `X-Real-IP` and
`Forwarded` (unless core took the client from one of them, as below). The client address is the connection's, or,
when the connection is from a proxy listed in `trusted_proxies`, the client that proxy names in
`X-Forwarded-For` or `X-Real-IP`; a client a trusted proxy names counts as this machine only when it
is a loopback address. A call that has any of those headers, where core did not take the client from that header (core
never reads `Forwarded`), does not count as from this machine, whatever address the header names. So a proxy on this machine that forwards calls from elsewhere
does not open the API to them.

**Wrong keys.** Five wrong keys in a row from one other-machine client block that client for 30
minutes: every call, with any key, is 403 `blocked` with a `Retry-After` header, and core logs a line
naming the client. A right key clears the count, and a count is forgotten after 2 hours without a
failure. An IPv6 client is counted by its /64 block. A call with no key at all is 401 `unauthorized`
and does not count. A call from this machine is never counted or blocked, so the person at the
keyboard can always get back in. Behind a proxy on another machine that is not in `trusted_proxies`,
every client has the proxy's address, so one client's wrong keys block all of them.

The management key's rotation calls are under [Management key](#management-key).

## Rules every call follows

- **Bodies.** JSON in and JSON out, UTF-8. A body is read up to 1 MiB (4 MiB for
  `POST /accounts/import`, 256 KiB for a stored theme); a larger one is 413 `invalid_request`.
  A call that takes no body ignores one. Times are RFC 3339, in UTC except `desired.at` and `active.at` in `GET /status`, which carry the server's own offset. Durations are whole milliseconds in
  fields ending `_ms`.
- **Unknown fields.** These calls refuse a field they do not know, and anything after the one JSON
  object, with 400 `invalid_request`: `POST /models/refresh`, `POST /config/validate`, `PUT /config`,
  `PATCH /settings`, `POST /setup/apply`, `POST /setup/import`, `PUT /chains/{name}`, `PUT /theme`,
  `POST /client-keys`, `PATCH /client-keys/{id}`, `POST /management-key/rotate/commit` and
  `PATCH /accounts/{id}`. The other calls that read a JSON body ignore the fields they do not know.
  `PUT /themes/{publisher}/{name}` stores any valid JSON object.
- **Errors.** Every failure is `{"error": {"code": "...", "message": "..."}}` with the HTTP status that
  fits. `code` is stable; `message` is for a person. The error of an account's models, quota or test
  call may add `status`, `provider_code`, `retryable`, `retry_after_ms` and a few more members of the
  extension's error.

  | Status | Codes | Meaning |
  | --- | --- | --- |
  | 400 | `invalid_request` | The request does not parse, lacks a required member, or names a field or value the call does not take |
  | 400 | `file_required`, `invalid_json`, `invalid_service_account`, `project_id_missing` | `POST /vertex/import` did not get a usable service account key |
  | 401 | `unauthorized` | No key, or the wrong key |
  | 403 | `remote_disabled` | The call came from another machine while `management_allow_remote` is off |
  | 403 | `blocked` | Too many wrong keys came from the caller's address; `Retry-After` says for how long |
  | 404 | `not_found` | No such account, package, chain, key, log, theme, login or request |
  | 409 | `conflict` | The thing changed since it was read, or the change would leave core unusable (the last client key) |
  | 413 | `invalid_request` | The body is larger than core reads |
  | 413 | `log_too_large` | A log file is larger than 2 MiB and `format=json` was asked for |
  | 422 | `invalid_config` | An extension's settings do not pass the checks its manifest declares, or `GET /config` found the config file does not parse |
  | 422 | `invalid_request` | An account test names no model, and the account lists none |
  | 422 | `refused` | Core checked the change and will not apply it; `message` says why; nothing was changed |
  | 422 | `unsupported` | The extension that would answer does not take that account call |
  | 500 | `internal` | Core could not finish the call, because of its own error rather than the request's |
  | 502 | `fetch_failed` | A package registry or archive could not be fetched |
  | 502 | `account_closed`, `auth`, `cancelled`, `extension_crashed`, `extension_protocol`, `extension_timeout`, `extension_unavailable`, `internal`, `invalid_request`, `not_found`, `permission`, `quota`, `rate_limit`, `unavailable`, `unsupported`, `upstream` | An account call ended on an extension's or a provider's error, with that error's code; `POST /vertex/import` turns an `auth`, `invalid_request` or `internal` error into its own 400 or 500 instead |
  | 503 | `extension_unavailable` | The extension that would answer an account's models or quota call, or an account test that must list models first, is not running |
  | 504 | `unavailable` | The service account login in `POST /vertex/import` did not finish in time |

- **Secrets.** No reply holds a credential, a client key or the management key, except the reply
  that creates a client key and the replies that create a management key. Account IDs and labels are
  shown.
- **Changes are whole.** A call that changes the config is applied completely or not at all, through
  the same steps as a config reload; requests already running finish on what they started with.
  [Changing the config](#changing-the-config) says how.
- **Compatibility.** `GET /status` has `management_api`, a number that goes up when a call changes in
  a way an older client would misread. Fields are only added between such changes.

Below, `{id}` of a package is `publisher/name`, written as two path segments. A call's reply is the
JSON object named in its row; `204` means an empty reply.

## Core

| Call | What it does |
| --- | --- |
| `GET /status` | Core's live state: `desired` (the newest config version core has seen, with `error` when it was refused) and `active` (the one serving), each with `revision`, `hash` and `at`; `extensions`, one entry per running generation with `id`, `version`, `generation`, `state` (`starting`, `active`, `idle`, `crashed`, `quarantined`, `draining`, `stopped`), `current`, `pid`, `restarts`, `crashes_in_a_row`, `last_error` and `active_requests`; `safe_mode`; `first_run`; `version`, `commit` and `built` (the build, as `pluxy -version` prints it); and `management_api` |
| `POST /reload` | Reloads the config file now. 200 with the status when the new revision is active; 422 with the status when it was refused (`desired.error` says why), and the active revision stays |
| `GET /ui` | The pages of the running extensions and where they are served; see [extension pages](../next/ui/extension-pages/README.md#how-core-serves-pages) |
| `GET /extensions/{id}/config` | An extension's settings: the schema its manifest declares and their values. 404 `not_found` when the extension declares none or is not running |
| `PATCH /extensions/{id}/config` | `{"values": {...}}`: changes those settings and replies as `GET` does. 422 `invalid_config` when a value fails the schema |

## Changing the config

These calls read and change the config file core runs on, whether it is the JSON config or a
`config.yaml` ([`docs/config.md`](config.md)). A change goes through the same steps as a reload: core
checks and prepares the new contents, and only when they are ready to serve does it write the file
and switch to them. The file is replaced in one step (a new file renamed over it, through a symbolic
link if the path is one), so nothing reads half of it. The new file keeps the old one's permissions:
its mode, and on Windows its access list. A refused
change leaves the file and the running config as they were, and `GET /status` keeps showing the file
as it is. When another program writes the file while a change is being prepared, the change is
dropped with 409 `conflict`, so that write is not lost.

A change made with the structured calls (client keys, management key, settings, chains) writes the
file out again. A `config.yaml` keeps its comments and the order of its keys, and is written with
two-space indents and no blank lines; a JSON config keeps the order of its keys and its indent.
`PUT /config` writes the document as sent.

New client keys go to `access.api-keys` in a `config.yaml`, or to the older root `api-keys` list when
the file has that list and no `access.api-keys`. A new management key goes to `management.secret-key`,
or to `remote-management.secret-key` when only that is there.

| Call | What it does |
| --- | --- |
| `GET /config` | `{"format", "hash", "content"}`: `format` is `json` or `yaml`, `hash` is the SHA-256 of the file as `sha256:<hex>`, and `content` is the file with each secret replaced by a placeholder such as `pluxy-redacted-4f1c...`. 422 `invalid_config` when the file does not parse |
| `POST /config/validate` | `{"content": "..."}`: checks a document as a reload would, without starting extensions or writing anything, with placeholders put back as `PUT /config` puts them. 200 with `{"valid": true, "notices": [...]}`, the `notices` being what a load logs (a `config.yaml`'s unsupported settings, a JSON config's unknown keys), or `{"valid": false, "error": "..."}` for a document core would refuse, including one with a placeholder where its secret is not (see [Placeholders](#placeholders)). A document that is refused is not a 4xx here; only a placeholder that matches no secret in the file now is 409 `conflict`. 400 `invalid_request` when `content` is missing |
| `PUT /config` | `{"content": "...", "base_hash": "..."}`: replaces the file with the document and applies it. Replies 200 with the file as `GET /config` shows it. 409 `conflict` when `base_hash` is set and the file has changed since, or a placeholder's secret is no longer in the file; 422 `refused` when core refuses the document, naming the line where it can; a refused document writes nothing |
| `GET /settings` | The basic and routing settings by name (below), and `token_count_estimate` |
| `PATCH /settings` | Changes the settings the body names, for example `{"routing": {"strategy": "fill-first"}}`, and replies with all settings as `GET` does. 400 `invalid_request` for a name or value it does not take |

### Placeholders

`GET /config` replaces each secret with a placeholder. The rules go by key name and are the same in a
JSON config, in a `config.yaml` and in an account's `settings`:

- **Secret names.** A name is compared in lower case, whatever joins its words (`_`, `-`, `.`, a
  space, a capital letter) or nothing: `accessToken`, `access_token` and `accesstoken` are one name.
  Letters fold as core folds them when it matches a JSON key to a setting (`client_keyſ` is
  `client_keys`). A name ending in a number or in `value`, `pem`, `der`, `data`, `raw`, `b64`,
  `base64`, `hex`, `str`, `string`, `text` or `bytes` is read without that ending (`apiKey2`,
  `api_key_value`, `privateKeyPem`). The result is a secret name when it is `apikeys`,
  `managementkey` or `clientkeys`, or ends in `token`, `secret`, `secrets`, `password`, `passwords`,
  `apikey`, `secretkey`, `privatekey`, `secretaccesskey` or `passphrase`.
- **Below a secret name.** The value, or each item of its list, is a secret, and so is everything
  below it, however deep, also when a YAML alias leads there (as a value or as a key's name).
- **Credentials.** Everything in an account's `credential` except its `kind`.
- **Headers.** In any mapping whose name contains `header` (`headers`, `extra_headers`,
  `headers_override`): the values of headers about authentication (`Authorization`, `X-Api-Key`,
  cookies) or named like a secret.
- **URLs.** A URL with a user name, a password or a query, or with a parameter named like a secret in
  its fragment.
- **Copies.** A string secret of 8 bytes or more written again as a whole value elsewhere in the
  file is a secret there too.
- **Provider sections of a `config.yaml`.** The entries of the sections at the top of the file (the
  v8 `api-keys` mapping, such as `codex`, and the older lists such as `codex-api-key`) are read by
  these rules, and any other key of the v8 `api-keys` mapping is a secret name. A secret header value
  there that is written as a number, or with spaces around it, gets the placeholder of the header
  core sends (the text without the spaces).
- **Never secrets.** `true`, `false`, `null` and the empty string. A copy shorter than 8 bytes or not
  a string is shown, and so is a copy of a value that is secret only for being below a secret name
  (the `3` of `client_secret: {rotate_after_days: 3}`). Comments are shown as written.

A secret that is not a string has its type in its placeholder, so the number `1234` and the string
`"1234"` have different placeholders. Messages in replies never show a secret however short, except
a number shorter than 8 bytes that is secret only for being below a secret name.

A document read with `GET /config`, edited and sent back with `PUT /config` keeps its secrets: core
puts each placeholder back from the file as it is now. To change a secret, write the new value in its
place. A placeholder goes back only where its own secret is in the file now, so a secret is never
copied into another account, entry, header or setting. Where it would go elsewhere, core refuses the
document (422 `refused`), naming the line where it can; write the value there itself. The rules:

- A placeholder goes back at the same key. In a list it goes back in the same item: an item is
  matched by its `id`, else its `name`, else by the secrets it holds, so items can be reordered. An
  item that holds a placeholder may not have its `id` or `name` twice, in any letter case. A list
  item that is a plain value may move within its own list only.
- A placeholder that is a copy of a secret goes back only where the file holds that copy now, and
  only while the document still holds the secret it copies. A copy whose key was renamed, that moved
  to another list item, or whose secret changed or was removed, is refused.
- An alias or merge key (`<<`) that reads a placeholder where its secret is not is refused, naming
  its line. So is a mapping key that is a placeholder, or an alias of one.
- A secret written with an explicit YAML tag (`!!int 0x1F`, `!!binary ...`) goes back with its tag.
- A placeholder that carries an anchor (an alias reads it) makes core write the document out again as
  it reads it, so quotes and spacing may change.

One limit: an account or a provider entry that keeps its secrets' placeholders keeps its secrets
when its address or its proxy changes (an account's `base_url` or `proxy_url`, in the file or through
`PATCH /accounts/{id}`, or an entry's `base-url` or `proxy-url` in a `config.yaml`). So a client that
may change an account or an entry may point its kept secrets at another address.

A placeholder is accepted only while the file still holds that secret and core has not restarted
(placeholders are keyed hashes with a key core picks at start); otherwise the call answers 409
`conflict`: read the config again.

### Settings

`GET /settings` and `PATCH /settings` use these names. A field left out of a `PATCH` stays as it is.

| Setting | JSON config key | Values |
| --- | --- | --- |
| `routing.strategy` | `routing.strategy` | `round-robin`, `fill-first`, `weighted-round-robin` (`weighted` in the JSON config), `least-active`, `soonest-reset` |
| `routing.session-affinity` | `routing.session_affinity.enabled` | `true` or `false` |
| `routing.session-affinity-ttl` | `routing.session_affinity.ttl_ms` | A duration above zero such as `1h` or `30m`. In a JSON config a `PATCH` of it is refused while session affinity is off |
| `routing.force-model-prefix` | `routing.force_model_prefix` | `true`: an account with a `prefix` serves its models only under the prefix |
| `routing.retry.max-retry-credentials` | `routing.max_accounts` | Accounts one request may try in a round; 0 is no limit |
| `routing.retry.max-retry-interval` | `routing.max_wait_ms` | Seconds one request may wait for a blocked account |
| `routing.retry.request-retry` | `routing.request_retry` | Rounds a request may run after its first, 0 to 1000. `GET` shows `null` while the config leaves rounds to core's own rules; a `PATCH` cannot set it back to `null` |
| `routing.cooldown.disable-cooling` | `routing.disable_cooling` | `true`: failures leave no cooldown |
| `routing.cooldown.transient-error-cooldown-seconds` | `routing.transient_error_cooldown_ms` | Seconds a failing upstream cools down: 0 for core's own backoff, -1 for none, up to 2592000 |
| `routing.cooldown.quota-response-mode` | `routing.quota_response_mode` | `retryable` (default) or `usage-limit` |
| `routing.timeouts` | `routing.timeouts` | Default upstream timeouts of every account in milliseconds: `header_ms`, `idle_ms`, `total_ms`. `0` turns a limit off, a field left out stays, `null` removes them |
| `routing.ignition` | `routing.ignition` | Window ignition's settings, replaced whole; `null` removes them, which turns ignition off. Absent from `GET` when the config sets none |
| `observability.logs.logging-to-file` | `logs.to_file` | `true` or `false` |
| `observability.logs.logs-max-total-size-mb` | `logs.max_total_size_mb` | Megabytes the log files may take; 0 is no cap |
| `observability.logs.request-log` | `logs.request_log` | `true` or `false` |
| `observability.logs.error-logs-max-files` | `logs.error_logs_max_files` | How many error logs are kept; 0 keeps all |
| `observability.usage.usage-statistics-enabled` | `usage.enabled` | `true` or `false` |
| `token_count_estimate` | `token_count_estimate` | `true` or `false` |
| `proxy-url` | `proxy_url` | The outbound proxy of every account without its own, shown with `redacted` in place of a user name and password; `""` removes it. Sending the value back as shown keeps the password |
| `payload` | `payload` | The payload rules, all five lists at once; absent when there are none. `null` or `{}` removes them |
| `management_allow_remote` | `management_allow_remote` (`management.allow-remote` in a `config.yaml`) | `true` or `false`: whether the management API also answers computers other than the one core runs on. A change applies at once |
| `listen` | `listen` (`server.host` and `server.port` in a `config.yaml`) | The address core serves on, as `host:port`; the host may be empty or `0.0.0.0` for every address. A new address must be an address and a port on a host this computer has (else 400); it is written to the file and used the next time core starts. Until then `GET` shows the running address as `listen` (the port core picked, for a config that listens on port 0) and the new one as `listen_after_restart` |
| `from_this_computer` | | Read only: `true` when the call came from the computer core runs on. The panel uses it to warn before a change that would shut the caller out |

The settings calls have no name for `max_attempts`, `quota_refresh_ms` or `explain`: change those in
the document with `PUT /config`.

In a `config.yaml`, `payload` is at `requests.payload` (or at `payload` when the file has only that)
and `proxy-url` at `requests.proxy-url` (or `proxy-url`). `routing.timeouts` and `routing.ignition`
have no name in a `config.yaml`: a `PATCH` that sets either there is refused with 400 `invalid_request`, and
`null` changes nothing there. A `config.yaml` that sets window ignition does so in
`plugins.configs.lamplighter`, which core reads as `routing.ignition` but does not write.

What each setting does is in [`docs/config.md`](config.md) and [`docs/routing.md`](routing.md).

## Client keys

A client key is what a coding tool sends to the client API. A key has an `id` (a keyed hash, stable
across restarts, kept with the key in `state/key-id.key` beside the config), a `masked` form and an
optional `label`.

| Call | What it does |
| --- | --- |
| `GET /client-keys` | `{"keys": [{"id", "masked", "label"?}]}`. `masked` keeps a key's first and last two characters; a key shorter than 8 characters is hidden whole |
| `POST /client-keys` | Adds a random client key (`sk-` and 26 random characters). An optional body `{"label": "..."}` names it. 201 with `{"key": {...}, "value": "<the key>"}`; `value` is shown this once |
| `PATCH /client-keys/{id}` | `{"label": "..."}` names a key, such as the tool it is for; `""` or `null` removes the name. 200 with the key as stored. 404 for an ID no key has; 422 `refused` when a `config.yaml` sets the label through a YAML alias or merge key (`<<`), which core cannot change in place |
| `DELETE /client-keys/{id}` | Removes the key and its label: 204. 404 for an ID no key has; 409 for the last key, since core refuses a config without client keys |

A label is trimmed, one line, at most 64 characters, and never a client key or the management key
(400 `invalid_request` otherwise). The labels are kept in the config file as `client_key_labels`,
keyed by the key's `id`; usage events record the label a key had at request time. `GET /config` shows
them, since labels are not secrets. A label edited into the file by hand that is not valid (not one
line of at most 64 characters), or that is a client key or the management key, makes core refuse the
file.

## Management key

| Call | What it does |
| --- | --- |
| `POST /management-key/rotate/stage` | Makes a new management key and keeps it in memory only: one at a time (another stage replaces it), for 10 minutes, and not across a restart. The old key keeps working and the config file is not touched. Replies `{"value": "<new key>", "config": "<absolute path of the config file core runs on>"}` |
| `POST /management-key/rotate/commit` | `{"value": "<staged key>"}`: writes the staged key to the config file and puts it into use; the old key stops working. 204. The key already in use also answers 204 and changes nothing, so a commit sent again after a lost reply is safe. Any other value (wrong, replaced, older than 10 minutes, or staged before a restart) is 409 `conflict` and changes nothing |
| `POST /management-key/rotate` | A new management key in one step, written to the config file at once: the old key stops working as the reply is sent. Replies `{"value": "<new key>"}`. A reply lost on its way leaves the caller with neither key, which is why a caller that can should use the two calls above |

## Setup

Setup is what a new install shows first. Core looks at the machine it runs on, says what it found, and
proposes what to install. Nothing is installed until asked.

`first_run` starts true on a core with no `state/setup.json` that has no installed packages, no
accounts and no credential files. Core keeps it in `state/setup.json`, and it stays true until
`POST /setup/done`. Core scans on its own only at a first run's start with no kept scan; otherwise only
on `POST /setup/scan`.

| Call | What it does |
| --- | --- |
| `GET /setup` | The last scan, at once (it is kept on disk): `first_run`; `scanning`; `scanned_at`; `found`; `picks`; and `registries`, how many extension registries core reads (0 when it has neither a bundled registry nor any in `packages.registries`) |
| `POST /setup/scan` | Looks again. Returns at once with `scanning: true`; read `GET /setup` until it is false (each probe has 4 seconds) |
| `POST /setup/apply` | `{"picks": ["pluxy/claude", ...]}`: installs and enables each pick, as `POST /packages` does with `enable` set. At least one pick is required. Replies `{"results": [{"id", "status", "package", "error"}]}`, one result per pick, `status` being `done` or `error`; one failure does not undo the others |
| `POST /setup/done` | Ends first run: `{"first_run": false}`. The panel and the app stop opening on setup |
| `POST /setup/import` | Brings a CLIProxyAPI setup on core's machine across; see [Import](#import) |
| `GET /setup/clients/{tool}/paste` | What to paste into a coding tool's config to point it at this core; see [Client paste text](#client-paste-text) |

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

`clients` lists eight coding tools: `claude-code`, `codex`, `opencode`, `pi`, `omp`, `claude-desktop`,
`grok-build` and `hermes`. A client's `state` is `not-configured`, `configured` (it points at this
core), `needs-update`, `refused` (a file the desktop app's writer for that tool would refuse to
change; the pick that would write that tool's config is not suggested) or `unchecked`. `claude-desktop`, `grok-build` and `hermes` are
always `unchecked`: core does not read their settings, so it cannot say whether they point at this
core, and it has no paste text for them. `sign_ins` only says a sign-in exists; core reads no token
from it.

An import's `last_import` is there once core has brought that setup across (`POST /setup/import`
without `dry_run`): `at`, the time the last such import ended, and `brought`, `changed` and
`not_brought`, the number of rows in each list of its report. A row is a key of the config or a file of
the auth folder, so the numbers do not count accounts. Core keeps the record in `state/setup.json`,
by the config file's path.

`picks`, in the order to show them:

```json
[
  {"id": "pluxy/claude", "kind": "provider", "title": "Claude", "summary": "Claude subscriptions and API keys",
   "installed": false, "suggested": true, "because": "Claude Code is installed on this computer"}
]
```

`suggested` is true only for a pick that something found on the machine points to. A machine where
nothing was found gets the same list with nothing suggested.

### Client paste text

`GET /setup/clients/{tool}/paste` replies:

| Field | Meaning |
| --- | --- |
| `tool` | The tool's canonical ID, whatever name the call used |
| `name` | The tool's name |
| `base_url` | The address a program uses to reach core: core's own, for a caller on core's machine, and the address the caller reached core by (from its `Host` header and whether the connection is TLS) for a caller on another machine |
| `key` | `<client-key>`, or with `key_id` `<client-key:ID (MASKED)>`: never a key's value |
| `files` | Each file to write: `path` (from the home folder) and `content` |
| `note` | What the files need besides themselves; left out when empty |

`{tool}` is the tool's ID (`claude-code`, `codex`, `opencode`, `pi`, `omp`) or another name for it
(`claude`, `oh-my-pi`, `codex-cli`, or a setup package's ID such as `pluxy/pi-setup`, with the `/`
written `%2F`), in any letter case. A caller on another machine whose `Host` header core cannot use gets 400 `invalid_request`. The optional query parameter `key_id` names a client key from `GET /client-keys`. 404
`not_found` for a tool core has no text for, and for a `key_id` core does not hold.

### Import

`POST /setup/import` with `{"config": "<path>", "auth_dir": "<path>", "dry_run": true}` reads a
CLIProxyAPI setup on core's machine and replies with the import report below.
[`docs/moving-from-cpa.md`](moving-from-cpa.md) walks through the move.

- `config` is an absolute path (or starts with `~`). `auth_dir` defaults to the config's
  `oauth.auth-dir`. With `dry_run` nothing is written.
- The config's settings, client keys, API-key accounts, aliases and chains go into core's config
  file in one change, and the auth folder's accounts into its credential store. `listen` stays as it
  is, since it cannot change while core runs, and so does the management key, which is not brought
  (plain or a bcrypt hash): core keeps its own. A `management.allow-remote: true` is brought as `management_allow_remote`. An account or chain core already has, by ID or name,
  stays as it is.
- 400 `invalid_request` for a path that is not absolute or a file that cannot be read; 422 `refused`
  for a file core refuses (the message has the file's secrets taken out) or a credential store inside
  the auth folder; 409 `conflict` for an import that is not a dry run into a core that runs on a
  `config.yaml`.

```json
{
  "dry_run": true,
  "brought": [{"what": "auth file", "name": "codex-work@example.com.json", "as": "pluxy/codex account codex-work-example.com"},
              {"what": "routing.strategy"}],
  "changed": [{"what": "server.host", "from": "(not set: every interface)", "to": "127.0.0.1:8317",
               "why": "core keeps listening on 127.0.0.1:8317: listen cannot change while core runs",
               "keep_with": "\"listen\": \"0.0.0.0:8317\" in pluxy's config file, then restart core"}],
  "not_brought": [{"what": "auth file", "name": "gemini-user.json", "why": "..."}],
  "needs": [{"pick": "pluxy/codex", "why": "3 accounts need it"}]
}
```

Every key of the config and every file of the auth folder is in exactly one of `brought`, `changed`
and `not_brought`; an import whose report could not account for an item fails instead of dropping it.
A key is named by its path in the file (`server.port`); a list is one key; a provider section's entry
is one key (`api-keys.codex[0]`) whose row names the accounts it became. Every `not_brought` row says
why. `needs` lists the provider packages the imported accounts need that core has not installed; core
installs nothing by itself.

## Packages and registries

A package is an extension (a provider or a client add-on) installed from a registry; see
[`docs/extensions.md`](extensions.md). A first start installs nothing: every package, the bundled ones
too, is installed by `POST /packages` or a setup pick.

| Call | What it does |
| --- | --- |
| `GET /packages` | `{"packages": [...]}`: the installed packages with `id`, `version`, `registry`, `url`, `sha256`, `enabled`, `installed_at`, `fingerprint` and `state` (see below), and `error` when `state` is `failed` |
| `GET /registries` | `{"registries": [...]}`: each registry's `name`, `url` and what it offers, `packages`, or `error` when it could not be read. A package has `id`, `title`, `summary`, `kind` (`provider`, `client`) when its registry says them, `tier` (`pick`, `verified`, `unverified`; `unverified` when the registry does not say) and `releases` (`version`, `protocol`, `platforms`). Core enforces nothing by tier |
| `POST /packages` | Installs a package; see below |
| `POST /packages/{id}/enable` | Starts the package and routes to its accounts. 200 with `{"package": {...}}`; 404 when it is not installed; 422 `refused` when it did not start (it stays disabled) |
| `POST /packages/{id}/disable` | Stops routing to it; requests already running on it finish, up to its drain timeout. Its files stay. 200 with the package |
| `DELETE /packages/{id}` | Disables the package and removes its files once its requests have finished. Its accounts stay. 204 |

A package's `state` is `disabled`; the running extension's state (`active`, `idle`, `starting`,
`crashed`, `quarantined`); `failed`, with `error`, when it is enabled but could not start; or
`stopped` when it is enabled in safe mode.

**`POST /packages`** takes `{"id": "pluxy/codex", "version": "...", "registry": "mine", "enable": true}`.
Only `id` is required. Without `version`, core takes the first release listed that runs here; without
`registry`, the first registry that lists the package.

- **It installs the package but does not enable it, unless `enable` is `true`.** A package that is
  installed and not enabled has `state` `disabled` and serves nothing. Installing another version of a
  package that is already enabled keeps it enabled, whatever `enable` says.
- When it enables the package and the package does not start, a package that was not installed before
  stays installed, disabled, and the call replies 422 `refused` with the package in the body.
- Installing another version of an installed package replaces it. When it is enabled, the new version
  starts before the old one drains, and when the new one fails to start the old one stays.
- Installing the same version again changes nothing when its files are intact; with `enable` it
  enables a disabled package. When the registry now pins another archive for an installed version, the
  call is refused (422 `refused`); remove the package first.
- 200 with `{"package": {...}}`. 404 `not_found` when no registry has the package, or the version or
  registry named is unknown; 409 `conflict` when that version is still in use by a removed copy that is
  finishing its requests; 422 `refused` for an invalid `id` or `version`, a package with no release
  for this platform, or an archive or manifest that fails a check (nothing is installed); 502
  `fetch_failed` when the registry or archive could not be fetched.

Package changes go through the same steps as a config change: core reads the config file, prepares
everything with the package change applied, and switches in one step, so a config file that does not
load also refuses a package change. One package change runs at a time.

## Accounts

An account is one way into a provider: a sign-in or an API key. See [`docs/providers.md`](providers.md).

| Call | What it does |
| --- | --- |
| `GET /accounts` | `{"accounts": [...]}`: every account (see [Account fields](#account-fields)) |
| `GET /login-methods` | `{"extensions": [{"extension", "methods"}]}`: how each running extension can add an account. A method has `method` (the name a sign-in call uses), `kind` (`browser`, `device` or `api_key`), `label` and `inputs`; each input has `name`, `label`, `secret` (hide it as it is typed) and `optional` |
| `POST /accounts` | Starts a sign-in that adds an account, and replies 202 with `{"login": {...}}`; see [Sign-ins](#sign-ins) |
| `POST /accounts/{id}/login` | `{"method", "input"}`: signs in again to an account added by a login, followed as a new sign-in is, and checked to be the same upstream account (see [Sign-ins](#sign-ins)). 409 `conflict` for an account set in the config |
| `PATCH /accounts/{id}` | Changes an account; see below |
| `DELETE /accounts/{id}` | Removes an account added by a login: 204. 409 `conflict` for an account set in the config |
| `POST /accounts/{id}/refresh` | Renews the account's credential now. 200 with `{"account": {...}}`. 409 `conflict` when the credential cannot be renewed (an API key) or the account is disabled; 404 for a broken account |
| `POST /accounts/{id}/cooldown/reset` | Forgets the cooldowns routing holds against the account; a ban stays. Replies `{"account": {...}, "cleared": n}` |
| `POST /accounts/{id}/test` | Sends the account one small request, straight to its extension, leaving routing and cooldowns as they are. Optional `{"model": "..."}`, named by its upstream name (what `GET /accounts/{id}/models` shows as the model's own ID), not by a name clients use; without one core takes the first model the account serves (422 `invalid_request` when it lists none). The request is `Hi`, with at most 16 output tokens. Replies `{"result": {"ok", "model", "served_model", "latency_ms", "reply", "finish_reason", "usage", "error"}}`; `ok` is false, with `error`, when the upstream refused |
| `GET /accounts/{id}/quota` | The account's plan and usage windows as its provider reports them: `{"fetched_at", "quota"}`. `quota` has `plan`, `plan_ends_at` (Unix seconds), `limits` and `raw` (the provider's own replies, for what `limits` has no place for). Each limit has `id` and `label`, and of these what the provider gives: `used_percent`, `used`, `limit`, `remaining`, `unit`, `unlimited`, `resets_at` (Unix seconds), `window_seconds`, `models` (when the limit covers only some of the account's models) and `feature` (true for a limit on one feature of the plan, which core never holds the account or a model on). 422 `unsupported` for an account with no plan or balance to read (such as an API key of a provider that has plans, or an upstream with no quota endpoint) or whose extension reads no quota; 502, with the provider's code, when the provider refuses |
| `GET /accounts/{id}/models` | `{"models": [...], "from"}`: the models the account serves, each with `names` (what clients ask for it by after the account's exclusions, aliases and prefix) and `excluded`. `from` is `account` (read from the provider), `extension` (the extension's built-in list), or left out when the config lists them |
| `POST /accounts/import` | Adds the accounts of one CLIProxyAPI credential file: `{"name": "<file>.json", "content": "<its text>"}`. Core reads no file of its own machine for it. `name` is a `.json` file name with no folder, and gives the account its ID. Replies `{"imported": [{"id", "extension", "ignored"}], "skipped": [...]}`: `ignored` lists the file's settings core does not support yet, and `skipped` says why another account of the file was not added (a Codex file can hold two). 400 when nothing could be added: a `name` that is not a `.json` file name, a file that is not a JSON object, of a type the import does not take, with no token, or with routing fields the router refuses. 409 when every account was one core already has: the store or the config has the ID, a running sign-in will take it (letter case aside), or the ID or its public ID is another account's. 413 for a file over 1 MiB. Neither the reply nor core's log holds anything of the file but the account IDs, the extension and the names of the settings it ignored |
| `POST /vertex/import` | Adds a Google service account key as a Vertex account: multipart form data with the key in the field `file` and an optional `location` (a form field or a query parameter; a new account gets `us-central1` without one). Importing a key whose account `vertex-<project>` exists keeps that account's location, and a different `location` is 409 `conflict`, as is an ID that a config account or a non-Vertex account holds. Replies `{"status": "ok", "account", "project_id", "email", "location"}` |

Calls that name an account by `{id}` take its ID. Accounts set in the config are changed only in the
config: the calls above that would change one answer 409 `conflict`, except that `PATCH` with only
`"disabled": false` on a config account lifts its ban. The calls that change nothing about the
account, `cooldown/reset`, `test`, `models` and `quota`, work on a config account as on any other.

An account's credential goes only to the extension the account belongs to, the one that created it.

**`PATCH /accounts/{id}`** takes any of these members; a member left out stays as it is.

| Member | Meaning |
| --- | --- |
| `disabled` | `true` or `false` |
| `label` | The account's name for people: trimmed, one line, at most 64 characters (400 otherwise). `""` or `null` takes it away, which leaves the last label the provider gave the account (an imported credential file's e-mail address, for example), or none when it never gave one. An account stored before core kept the two apart keeps its old label as the provider's |
| `settings` | The extension's settings for the account, an object that replaces all of its settings (core passes them to the extension as they are); `null` removes them. A placeholder as `GET /accounts` shows it is accepted only where that secret is now: a list item may move within its list, a placeholder anywhere else is 400 `invalid_request`, and one that matches no secret the account's settings hold now (or that was made before core restarted) is 409 `conflict` |
| `priority`, `weight`, `disable_cooling`, `max_concurrency`, `request_retry`, `prefix`, `models`, `excluded_models`, `model_alias`, `proxy_url`, `headers`, `timeouts` | The account's routing fields, by their config names; `null` sets one back to its default |

A name not in this table is 400 `invalid_request`. The change is checked as a whole first, so a bad
member changes nothing. Placeholders in `headers` and `settings` go back as they do in the config
([Placeholders](#placeholders)); one that stands for no secret the account holds is 409 or 400.
An account's `proxy_url` sent back as `GET /accounts` shows it keeps its password.

### Account fields

| Field | Meaning |
| --- | --- |
| `id` | The account's ID |
| `public_id` | The short stable ID client replies name the account by, never an email address or a label: `acc_` and 8 hex digits, a keyed digest of the ID. An ID of that shape is its own `public_id`. The key is made at the first start and kept in `state/account-key` beside the config. `X-Pluxy-Account`, the header a request pins an account by, takes either ID |
| `source` | `config` for an account set in the config, `login` for one added by a login |
| `extension`, `kind`, `state`, `label` | The provider's extension, the credential's kind (such as `api_key` or `oauth`), `state` of its credential (`active`, or `reauth_required` when the provider refused the grant itself, so only a new sign-in brings the account back), and its label |
| `disabled`, `reason` | Whether a person turned it off; why it needs a new sign-in |
| `health` | The account's state in one word (below) |
| `priority`, `weight`, `disable_cooling`, `max_concurrency`, `request_retry`, `prefix`, `models`, `excluded_models`, `model_alias`, `endpoint`, `proxy_url`, `headers`, `timeouts` | Its routing fields. `proxy_url` and secret header values are shown with placeholders |
| `settings`, `settings_inexact` | An account added by a login shows its `settings`, each secret replaced by a placeholder, by the rule that everything below a secret key is a secret. `settings_inexact` is true when they hold a number a client that reads numbers as float64 would change when writing it back (such as `9007199254740993`) |
| `cooldowns`, `cooldown_until`, `last_error`, `last_error_at`, `active_requests` | What routing holds against it now, described below. An account that receives no requests (turned off, broken or waiting for a sign-in) has none |
| `check` | The result of core's last account check: `state` (`checking`, `ok`, `failed`, `not_checked`), `detail`, `error` and `at`. Absent while core has none |
| `ignition` | For an account window ignition runs for: `{"windows": [{"id", "state", "next", "why", "failures", "last", "model", "error"}], "error"}`, `state` being `running`, `waiting`, `starting` or `paused` |
| `quota` | `{"fetched_at", "quota"}`, the last reply of `GET /accounts/{id}/quota` as it was, whoever asked for it. Kept in memory until the account is removed, and absent for an account never read. The list reads nothing from providers to fill it |
| `created_at`, `updated_at`, `version`, `expires_at`, `refreshable`, `renewal_error` | The credential's record for an account added by a login |

`health`:

```json
{"state": "exhausted", "until": "2026-11-19T17:00:00Z", "why": "usage window used up",
 "source": "provider", "next_check": "2026-11-19T12:15:00Z"}
```

| `state` | Meaning | Comes back |
| --- | --- | --- |
| `ok` | Serving | |
| `cooling` | A short block: a rate limit or a failing endpoint | When `until` passes, or a request succeeds |
| `exhausted` | A usage window ran out | At its reset (`until`), or sooner when a check finds it has room |
| `needs_login` | The provider refused the sign-in | When the account signs in again |
| `banned` | The provider closed or suspended the account | Only when a person enables it or signs in again |
| `disabled` | Turned off by a person | When a person enables it |
| `broken` | Its credential file cannot be read; `why` names the file | When the file is fixed or replaced and core restarts, or the account is removed |

`source` is `provider` when `until` is the provider's own word, `backoff` when core chose it, and
`unverified` when the provider named a time core does not believe (more than 8 days away, or when the
provider named the usage window's length, more than an hour past it, whichever bound is longer; a window
length over 31 days raises nothing); such a block is held 30
minutes and checked within 15. When several blocks hold the account, an `exhausted` one decides, then
the one that ends last. `next_check` is when core next reads the account's usage from its provider;
absent when no check is due.

Each entry of `cooldowns` is one live block, latest end first: `scope` (`credential` for the whole
account, `model` with its `model`, or `endpoint`, which covers every account that names the same
endpoint), `kind` (`rate_limit`, `quota` or `cooldown`, the last for failures that gave no reset
time), `until`, `backoff_level` (how far up the backoff ladder a block with no reset time from the
upstream is) and `error`, the failure that set it. `cooldown_until` is when the latest block that
covers the whole account (its credential or its endpoint; blocks on one model do not count) ends.
`last_error` and `last_error_at` are the failure of the latest-started attempt that set a block. They
are cleared when an attempt that started after it succeeds. A success from an attempt that started
earlier does not clear them, and a failure from an attempt that started before a success is not
recorded.

A `broken` account is one whose credential file cannot be read: core knows it only by the file's name,
so `extension`, `kind` and `state` are empty and `why` names the file and the error. It never serves a
request, `PATCH` on it is 404, and `DELETE` removes the file.

### Sign-ins

A sign-in is a login in progress, started by `POST /accounts` or `POST /accounts/{id}/login`.

- `POST /accounts` takes `{"id", "extension", "method", "input", "label"}`, `settings` and the
  account's routing fields by their config names. `method` is a method from `GET /login-methods`; `input` holds the values
  the method asks for. Without `id`, core makes one (`acc_` and 8 hex digits). An `id` that, or whose
  public ID, is another account's ID or public ID is 409 `conflict`; so is an account set in the
  config. `extension` can be left out while only one runs. The call waits up to 10 seconds for the
  provider's first prompt, so the reply usually carries it, and the sign-in goes on either way.
- A sign-in's result may carry non-secret account settings (an object of at most 8192 bytes). They are
  defaults for the account that is added: a property of `settings` in the `POST /accounts` body wins
  over the same property there, a `null` too, and a nested object is replaced as a whole. A sign-in to
  an existing account keeps the account's own settings and ignores any a result carries.
- If the config comes to give the account's ID or public ID to an account of its own while the sign-in
  runs, the sign-in ends `failed` with the error code `invalid_request` and a message that says it
  conflicts.
- A sign-in to an existing account (`POST /accounts/{id}/login`) must sign in to the same upstream
  account. Every field of the identity stored with the account must match the new one, letter case
  aside. An account stored without an identity is checked by the last label the provider gave it (never
  a label a person gave), when both have one. Otherwise the sign-in ends `failed` with the code
  `invalid_request`, the account keeps its credential, and the message says to add the other account as
  a new one. A label a person gave stays; the provider's follows the new sign-in.
- A `login` is `{"id", "account", "extension", "method", "state", "prompt", "error"}`. `state` is
  `pending`, `done`, `failed` or `cancelled`. `prompt` is what the sign-in needs now: an address to
  open, a code to type, or fields to fill in.
- `GET /logins/{id}` reads it: 200 with `{"login": {...}}`, 404 for an unknown login. A caller follows a
  sign-in by reading it again until `state` is not `pending`.
- `POST /logins/{id}/input` with `{"input": {"name": "value"}}` sends the values a prompt asks for: 204.
- `DELETE /logins/{id}` cancels the sign-in: 204.

## Models

| Call | What it does |
| --- | --- |
| `GET /models` | `{"models": [...]}`: the models `GET /v1/models` lists, in the same order. Each has `id` (the name clients use), what the account, the catalogue and the extension say about it (`owned_by`, `display_name`, `context_length`, `max_output_tokens`, `capabilities`, `from`), `upstream` (its name upstream), `extension` and `accounts` (the IDs of the accounts a request for the name uses, in the order routing takes them: with a fallback chain, each step's in step order). A name whose accounts all wait for a sign-in or are turned off is left out. Under the `weighted` strategy an account of `weight` 0 is in no entry. An account that serves any model because its list is not known adds no name of its own, but is among the `accounts` of the names its provider serves. A fallback chain that only such accounts serve is listed as described by the first of them, with its step's model as `upstream` |
| `POST /models/refresh` | Asks the providers for their model lists again now. An optional body `{"account": "<id>"}` refreshes one account (404 for an unknown one). Replies `{"models": [...], "accounts": [{"account", "models", "kept", "error"}]}`. A read that fails, times out or comes back empty keeps the last good list, and one that falls back to the extension's built-in list keeps a list read from the provider |

Core also refreshes every known list by itself: about every 30 minutes, plus up to 3 minutes of
random delay so accounts do not all refresh together, and each read has 15 seconds.
In safe mode `POST /models/refresh` reads nothing and `accounts` is empty.

## Routing

| Call | What it does |
| --- | --- |
| `GET /chains` | `{"chains": [{"name", "steps"}]}`, sorted by name: the fallback chains |
| `PUT /chains/{name}` | `{"steps": [{"model": "...", "provider": "pluxy/claude", "accounts": ["..."]}]}`: sets the chain of a model name. Replies with the chain, `{"name", "steps"}`. 422 `refused` for a step no account can serve, a step naming an account core does not know or one that does not serve it, a `provider` that is not an extension's ID (`publisher/name`), more than 8 steps, a step naming another chain, or a step that repeats another. 409 `conflict` for a `config.yaml`, which cannot hold chains |
| `DELETE /chains/{name}` | Removes the chain; the name goes back to the accounts of one provider, the same model. 204. 404 `not_found` when there is no such chain |
| `GET /route?model=<name>` | Says what a request for the model would do now, and sends nothing: `{"model", "order", "left_out"}`. `order` lists each account in the order it would be tried (`id`, `account`, `provider`, `model`); `left_out` lists each account left out, with its `state` and `until`. `id` is the account's ID, which only this API shows, and `account` its public ID, which client replies use. For a model with a chain, `chain` holds its steps, each account has its `step` (from 1), and `left_out` entries name their `provider` and `model`. Under the `soonest-reset` strategy each `order` entry has `rank`: `reset` (its longest usage window's reset, rounded to the hour), `window_seconds` and `full_until` when a reading gives them, `{}` when no reading is known. 400 `invalid_request` without `model` |

Chains are kept in the JSON config. See [`docs/routing.md`](routing.md) for what routing does with
them.

## Usage

Usage statistics are described in [`docs/usage-and-logs.md`](usage-and-logs.md). Both calls take these
filters as query parameters: `since` (an RFC 3339 time, or a duration back from now such as `24h` or
`7d`), `until` (an RFC 3339 time), `model`, `provider`, `account`, `key` (or `key_id`: a client key's
`id`) and `failed` (`true` or `false`). A bad value is 400 `invalid_request`.

| Call | What it does |
| --- | --- |
| `GET /usage` | `{"enabled", ...}`: totals (`total_requests`, `success_count`, `failure_count`, `success_rate`, `average_latency_ms`, `tokens`; `average_latency_ms` is the mean of the requests that reached an account, so a request refused before any account was tried, such as one for a model no account serves, does not count, and it is 0 when none did), breakdowns (`models`, `providers`, `accounts`, `keys`) and a time series (`series`, with `bucket_seconds`), and `oldest`, `evicted`, `dropped` and `write_errors`, which describe the usage record itself. `by=model\|account\|key\|provider` also copies one breakdown into `breakdown` and names it in `by`. `bucket` (a duration from `1s` to `24h`) and `buckets` (1 to 1440) set the series' size. Without them the buckets are 1 hour for a range up to 2 days and 24 hours beyond; with no range at all, 60 steps of 1 minute |
| `GET /usage/events` | `{"enabled", "events", "next", "more", "lost"}`: the recorded requests. `after=<seq>` reads oldest first from after that event, `before=<seq>` newest first from before it (not both), and `limit` is 1 to 1000 (default 100). `lost` is true when events newer than `after` were pushed out before the read (always false with `before`) |

Usage reaches back across all usage files kept on disk (`usage.keep_days`, default 90). Closed UTC
days are read from hourly `usage/summary/YYYY-MM-DD.json` files, which stay when the `.jsonl` files
are pruned, and today from memory and `usage/YYYY-MM-DD.jsonl`. A closed day is counted in whole UTC
hours, so a `since` or `until` inside an hour on a closed day counts that whole hour.

An event holds `seq`, the trace `id`, `timestamp`, `endpoint`, `stream`, the requested `model`, the
account's `upstream_model` when it differs, the `served_model` the upstream reply names, `provider`,
`account`, `key_id` and `label`, `client_ip`, `failed`, `status`, `error_code`, `latency_ms` and
`tokens`. It may also hold `chain_step`, `attempts` (how many accounts the request went to),
`tried` and `left_out`. `tried` lists each attempt in order (`account` public ID, `provider`,
`model`, `result`), the one that ended the request last; `result` is `ok` when it answered, else the
error's code when it is one of core's own, and `error` otherwise. At most 16 entries are kept, the first
15 and the last, so `attempts` can exceed the length of `tried`. It is absent for a request with one
successful attempt. A failed request also has `left_out` (the first 8 accounts routing left out, as the
client error's `pluxy.left_out` names them). They are recorded whatever `routing.explain` says.

## Logs

`GET /logs` and `DELETE /logs` answer 400 `invalid_request` while logging to file is off
(`observability.logs.logging-to-file`); the error and request log calls read the logs folder either
way.

| Call | What it does |
| --- | --- |
| `GET /logs` | `{"lines", "line-count", "latest-timestamp"}`: core's log lines. `after` is a time in Unix seconds (pass back `latest-timestamp` to read only newer lines); `limit` is 1 to 10000 (default 1000); a value that is not valid is 400 `invalid_request`. Without `after` the reply is the newest `limit` lines; with it, the oldest `limit` lines written after that time. Lines written together share one time, so a page that ends inside them holds the rest of them and can have a few more than `limit`; a line with no time of its own counts as written with the line before it. `latest-timestamp` is the time of the newest line returned, in seconds with milliseconds, or `after` itself when no line is returned. Reading on from it misses no line |
| `DELETE /logs` | Empties `main.log` and deletes its rotated files; request and error logs stay. Replies `{"removed": n}`, the rotated files deleted. When a file cannot be deleted or emptied it still tries the rest, then answers 500 `internal` with how many it deleted |
| `GET /request-error-logs` | `{"files": [{"name", "size", "modified"}]}`: the error logs, newest first; `size` in bytes, `modified` in Unix seconds |
| `GET /request-error-logs/{name}` | One error log (`error-<...>.log`) as a plain-text download. With `format=json` the reply is `{"name", "size", "content"}`, and a file larger than 2 MiB is 413 `log_too_large`. 400 `invalid_request` for a name that is not an error log's (`error-<...>.log`); 404 `not_found` when there is no such file or the name is a link, not a regular file |
| `GET /request-log-by-id/{id}` | The request log of the request with that trace ID, in the same two forms. 404 `not_found` when no request log has it, and for an ID that is not a trace ID's shape |

## Requests

| Call | What it does |
| --- | --- |
| `GET /requests` | `{"requests": [...]}`: the client requests core is serving now, oldest first. Each has `id` (the trace ID, the `X-CPA-TRACE-ID` reply header), `started_at`, `endpoint`, `format`, `stream`, `model`, `key_id`, `client_ip` and, once routing has chosen, `account`, `provider`, `upstream_model` and `attempts` for the newest attempt. A Responses WebSocket is listed while one of its turns runs, under the socket's trace ID |
| `POST /requests/{trace}/cancel` | Ends a request as its client going away does: the call to the extension is closed, the client gets the `cancelled` error (a WebSocket is closed), the usage record has the request as `cancelled`, and core's log says an operator cancelled it. Replies `{"request": {...}}`, the request as it was. Best effort: it answers 200 whenever the request is listed, and a reply core had already gathered may still reach the client whole. 404 `not_found` when no request with that trace ID is being served, also one that has just ended |

## Themes

A theme is a JSON object the panel and the app apply. Core stores user-added themes and the selected
theme's ID; the three built-in themes (`pluxy/default`, `pluxy/paper`, `pluxy/terminal`) are held by
the UI, not by core.

| Call | What it does |
| --- | --- |
| `GET /themes` | `{"current": "<id>", "themes": [{"id", "name", "bytes"}]}`: the selected theme and the stored themes |
| `GET /themes/{publisher}/{name}` | One stored theme's JSON, unchanged. 404 `not_found` when none is stored |
| `PUT /themes/{publisher}/{name}` | Stores the theme: the body is one JSON object, up to 256 KiB, and at most 64 themes are stored. Replies `{"id", "name", "bytes"}`. 422 `refused` for a built-in theme's ID, or past 64 themes |
| `DELETE /themes/{publisher}/{name}` | Removes a stored theme and clears `current` if it was selected: 204. 404 `not_found` when none is stored |
| `PUT /theme` | `{"id": "<id>"}` selects a theme; an empty `id` clears the selection. Core takes a stored theme, a built-in theme, and any other ID whose publisher is `pluxy`, since a newer panel or app may hold a built-in this core does not list. Any other ID is 404 `not_found` and the selection stays. Replies `{"id": "<id>"}` |

## What client replies carry

These are not management calls, but the names are the ones `GET /route` and `GET /usage/events` use.

- Every reply to a client request that passed the client key check has the trace ID
  (`X-CPA-TRACE-ID`): the time in UTC (`YYYYMMDDhhmmss`), a hyphen and 16 random hexadecimal digits,
  such as `20261109061500-3f9a0c2e5b7d4e18`. It is there also when no account was tried (a body that
  does not parse, no account for the model, every account blocked). A reply refused for its client key
  (401 or 403) has none. The handshake of a Responses WebSocket has a trace ID, and none of the
  `X-Pluxy-*` headers.
- Once a request has gone to an account, its reply has `X-Pluxy-Account` (its public ID),
  `X-Pluxy-Provider`, `X-Pluxy-Model` (the name sent upstream, left out for an endpoint that takes no
  model) and `X-Pluxy-Attempts`. An error from an account's refusal carries them too, for the last
  account tried.
- A request that ends in an error before a reply starts gets its usual error, with one more member,
  `pluxy`, in the error object of each client format:

```json
{"error": {"code": "model_cooldown", "message": "...", "type": "rate_limit_error",
  "pluxy": {"trace": "20261119120000-9f2c0a5b7d4e1830",
    "tried": [{"account": "acc_4e26e0c2", "provider": "pluxy/codex", "model": "gpt-6.1-sol", "result": "quota"}],
    "left_out": [{"account": "acc_306dcbdb", "state": "exhausted", "until": "2026-11-19T17:00:00Z"},
                 {"account": "acc_0702c008", "state": "needs_login"}]}}}
```

`result` is the code of the error the attempt ended with, one of core's own codes (anything else
reads `error`). `state` is the account's `health` state, `busy` for an account at its
`max_concurrency`, or `skipped` for one the request's limits (`max_attempts`, `max_accounts`) ended
it before reaching. The member holds codes and times, never an upstream's words. An account that is
turned off or waits for a sign-in is named in `left_out` only when it serves the model (its own
`models` name it, or the model list it had read before it went out of service) and belongs to the
provider the model's name goes to. The public IDs come from the key in `state/account-key`: when that
file is deleted, core makes a new key and every account gets a new public ID, except an account whose
ID is already `acc_` and 8 lower-case hexadecimal digits, which is its own public ID. Core writes the
same lists in its own log line for the error (see [Log files](usage-and-logs.md#log-files)).

- When no account that serves the model can take the request for more than 5 minutes, or none can until
  a person acts, the error says `X-Should-Retry: false` and has no `Retry-After`, so a client stops
  instead of retrying; a shorter wait keeps `Retry-After`.
- `routing.explain: false` leaves out the `X-Pluxy-*` headers and `pluxy` (the trace ID header stays), for a core whose client keys are given to
  people who should not see account IDs.

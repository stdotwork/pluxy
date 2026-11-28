# Move from CLIProxyAPI to pluxy

This page takes a working [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) (CPA) setup to
pluxy. One command, `pluxy import cpa`, reads CPA's `config.yaml` and its auth folder (the folder of
signed-in accounts) and brings the settings, client keys, provider keys and accounts across. It
then lists everything it brought, everything it changed on the way and everything it left behind,
with a reason for what it changed or left, so nothing is dropped without a word.

The import never changes CPA's files, so you can go back at any point.

## Before you start

- Build or install pluxy as the [quick start](../README.md#quick-start) does. The commands below
  run in `~/pluxy`, the folder the quick start builds into.
- Find CPA's `config.yaml`. The examples use `~/.cli-proxy-api/config.yaml`; use your own path.
- Decide where the accounts come from. The import reads the auth folder named by `--auth-dir`, else
  by the config's `auth-dir` (`oauth.auth-dir` in the newer layout). With neither, it brings the
  settings and keys and no accounts.
- If CPA keeps serving while you try pluxy, read [Running both side by side](#running-both-side-by-side)
  first: a signed-in account must be used from one of them only.

## Move

### 1. Start pluxy

```bash
cd ~/pluxy
./pluxy serve
```

With no `config.json` in the current folder or in pluxy's home folder ([setup](setup.md)), pluxy
writes its own `config.json` in pluxy's home on this first start (not in `~/pluxy`) and prints its
address: `127.0.0.1` port 8317, or a free
port when 8317 is taken, for example by CPA. The import adds to this config. Leave pluxy running
and open a second terminal in `~/pluxy`.

### 2. See what the import would do

```bash
./pluxy import cpa ~/.cli-proxy-api/config.yaml --dry-run
```

This writes nothing. It prints these lists, which together name every key of the file and every
entry of the auth folder, each once. (A file with `server.tls.enable: true` is refused even here:
see the result notes in step 3.)

- **Brought**: what comes across as it is.
- **Changed**: what comes across differently, with what it was (`FROM`), what it becomes (`TO`), why,
  and, for the address, what to write to keep the old behaviour (`KEEP WITH`).
- **Not brought**: what stays behind, with the reason. A row that ends with "it is off, empty or at
  today's default here, so nothing is lost" costs you nothing; read the others.
- **Needs**: the provider packages the imported accounts need and pluxy has not installed yet.
  It appears only when there are some.

`--json` prints the reply as JSON. A setting the import cannot place fails the whole import, names
it and writes nothing. An auth file it cannot use does not: it goes under **Not brought** with the
reason.

### 3. Import

```bash
./pluxy import cpa ~/.cli-proxy-api/config.yaml
```

Pass `--auth-dir <folder>` when the accounts are in a folder the config does not name.

The same lists print, and this time the settings, client keys and provider keys go into pluxy's
`config.json`, in one change that passes the same checks as any config change. The auth folder's
accounts go into the credential store, the `credentials` folder beside the config. Running the
import again adds nothing twice: an account or fallback chain pluxy already has under the same ID
or name stays as it is, and the lists say so.

The panel can do steps 2 and 3 too. When it finds `~/.cli-proxy-api/config.yaml`, its **Set up pluxy**
page (called **Setup** once first-run setup is done) shows one line above the steps: **Preview import**
lists what would happen and **Import now** does it.

To bring single auth files into a running pluxy instead, use **Import files…** at the end of the
panel's **Add an account** page ("Have auth files from CLIProxyAPI?"), or `pluxy account import <file>`.
Each file is read as the import reads it, and its account serves at once.

Four things to know about the result:

- **pluxy keeps its own management key.** CPA's key, if its file has one, is not brought: neither a
  plain key nor the bcrypt hash CPA writes over it after it first reads it. The key guards every
  client of pluxy, and an import does not change who may manage it. Go on signing in to the panel
  with pluxy's key, in `config.json`; `pluxy key management --show-key` prints it. To change it, run
  `pluxy key rotate-management` or send `POST /v0/management/management-key/rotate`.
- **pluxy keeps its own address.** It goes on listening where it started. The **Changed** list
  shows the address CPA's file asked for and the `"listen"` line to put in `config.json` if you want
  it; restart pluxy after changing it.
- **TLS is refused, in a dry run too.** A file with `server.tls.enable: true` fails, unless pluxy already runs with the same TLS settings, with
  `config: tls cannot change while core runs; restart core to apply it (a renewed certificate in the same files is read without a restart) (refused)`. Set the `tls` section (`enable`, `cert`, `key`) in pluxy's
  `config.json` yourself and restart pluxy, then import a copy of the file without its `tls`
  section.
- **A core that runs on a `config.yaml` takes dry runs only.** The real import writes pluxy's JSON
  config, and a core started with `-config <file>.yaml` reads a CPA-style file itself
  ([config.yaml keys](config-yaml.md)), so it answers
  `core runs on a config.yaml, which it reads with today's core's names already; an import writes core's JSON config (pluxy init writes one)`.
  Run the import on a core that uses a `config.json`.

### 4. Install the providers

A provider is the program that serves one kind of account, installed as a package. Install each one
the **Needs** list named, once:

```bash
./pluxy ext install pluxy/codex
```

`./pluxy account list` shows every account with the package it needs in the `EXTENSION` column. The
panel's **Set up pluxy** page adds providers too: its first step, "Your AI accounts", installs the
kinds of account you tick. Until a provider is installed, its accounts do not serve.

Antigravity is the exception. pluxy does not ship that provider, so `pluxy ext install pluxy/antigravity`
finds nothing until you have packed it and listed the result as a registry of its own. The steps are
in [`next/archive/antigravity/README.md`](../next/archive/antigravity/README.md). If your CPA has
Antigravity accounts, the **Needs** list names `pluxy/antigravity` with that pointer.

### 5. Check

```bash
./pluxy account list
./pluxy model list
./pluxy account test <account-id>
```

`model list` shows the models clients can ask for and the accounts that serve each. `account test`
sends one small request through an account and prints the reply. Your client keys came across, so a
client needs only pluxy's address: the same one as before when you gave pluxy CPA's address.

## Running both side by side

You can keep CPA serving while you try pluxy, with two rules.

**Give pluxy its own port.** A first start while CPA holds port 8317 takes a free port and prints
it (step 1). The import does not change it. Point one client at that address and leave the rest on
CPA.

**Use each signed-in account from one of them only.** A subscription sign-in holds a refresh token,
and the provider replaces it at each renewal. When CPA and pluxy both renew the same sign-in, one of
them is signed out. So do not import the whole auth folder while CPA still uses it. Copy the files of
the accounts you want to try into a folder of their own, import with that folder as the auth folder,
and take those files out of CPA's auth folder:

```bash
mkdir ~/pluxy/cpa-auth
cp ~/.cli-proxy-api/<file>.json ~/pluxy/cpa-auth/
./pluxy import cpa ~/.cli-proxy-api/config.yaml --auth-dir ~/pluxy/cpa-auth
```

Provider API keys in `config.yaml` have no such limit: both can use the same key.

When you are done, stop CPA and run the import once more without `--auth-dir`, to bring the rest of
the accounts from the folder the config names; what pluxy already has stays as it is. Then give pluxy
CPA's address, as the **Changed** list says, so the remaining clients need no change. To go back
instead, stop pluxy and sign in again in CPA to any account whose file you imported and pluxy has
since renewed.

## What carries over

From `config.yaml`:

- the client keys (the example config's public ones, `your-api-key-1` and the rest, are left out);
- trusted proxies and the outbound proxy (`requests.proxy-url`). TLS does not: see step 3;
- `management.allow-remote`, as `management_allow_remote`;
- routing: the strategy, session affinity, `force-model-prefix`, the retry settings
  (`max-retry-credentials`, `request-retry`, `max-retry-interval`) and the cooldown settings
  (`disable-cooling`, `transient-error-cooldown-seconds`, `quota-response-mode`);
- the provider keys (`codex`, `claude`, `gemini`, `vertex`, `interactions`, `xai`,
  `openai-compatibility`): each key becomes an account, with its base URL, proxy, headers (also those
  that copy a header from the client's request, `$` and a header name), priority, weight, prefix,
  excluded models (not for `openai-compatibility`, where CPA ignores them too) and its `models`
  names and aliases. Where models are pooled under one alias in an `openai-compatibility` group, or
  accounts of more than one provider serve one name, the import makes a fallback chain; `pluxy chain
  list` shows them;
- model aliases and excluded models for signed-in accounts (`oauth-model-alias`,
  `oauth-excluded-models`) of the channels `codex`, `claude`, `antigravity`, `vertex`, `kimi`,
  `xai`, `meta` and `devin`; rules for another channel are named and left out;
- the payload rules. A rule path pluxy cannot apply (a `*` wildcard, for example) fails the import,
  naming it, and a rule CPA skipped or dropped is carried the way CPA applied it, with the
  difference named under **Changed**. `protocol` names the extension here, not CPA's upstream API
  format;
- the log, request-log and usage settings (`debug` and the usage queue's retention are not
  brought), and `pprof`, which only a build with `-tags pprof` serves;
- per-provider switches: `disable-codex-cloaking` (as `codex_identity: false` on the Codex accounts
  the import makes from auth files, and on a Codex key that sets it itself), `model-level-cooling`
  for Claude, `inject-x-search` for xAI, `disable-image-generation`, and the sign-in with ChatGPT
  identifier;
- the [Lamplighter plugin](https://github.com/PosvdM/cpa-plugin-lamplighter)'s window ignition, when
  `plugins.enabled` and the plugin's own `enabled` are both true: pluxy has it built in as
  `routing.ignition`. Its hours, waits and retries carry over, held to the plugin's own ranges (a
  retry of at least 60 seconds, a hold of at least 15, hours 0 to 23), and the **Changed** list
  names any value it had to move. The providers it starts windows for carry over with their models: `claude` and `codex`
  become `pluxy/claude` and `pluxy/codex`, and `antigravity`, when its `ignite` is on, becomes
  `pluxy/antigravity`.

From the auth folder, the `.json` files at its top of these types: `codex`, `claude`, `kimi`
(also `kimi-ai`), `vertex`, `xai`, `meta`, `devin` and `antigravity`. Each becomes an account:

- Its ID is the file's name without `.json`. Every character other than letters, digits, `.`, `_`
  and `-` becomes `-`, leading `.`, `_` and `-` are dropped, and the ID is cut to 128 characters.
  When two files make the same ID (ignoring letter case), the later one goes under **Not brought**.
- It keeps the file's label, priority, weight, prefix, excluded models, model aliases, proxy,
  headers, `disable-cooling` and `request-retry` settings, and whether the file was disabled.
- A Codex file with a `siwc` section that holds a token (`access_token`, `access` or
  `refresh_token`) also makes a second account, `<id>-siwc` (`<id>-siwc-2` and so on when another file imports as that ID), which is disabled unless the file says
  `use_siwc: true`.
- A file whose `proxy_url` or `headers` pluxy cannot use goes under **Not brought**, whole.
- A symbolic link, a subfolder and any file that is not a `.json` file are not read.
- An `antigravity` account serves only once the Antigravity extension is installed (step 4).

The reference for every setting is [What each setting does](config-yaml.md#what-each-setting-does), and
for every auth-file field [Auth files](config-yaml.md#auth-files), both in [config.yaml keys](config-yaml.md).

## What does not carry over

| What | Why |
| --- | --- |
| The management key, plain or as a bcrypt hash | pluxy keeps its own; see step 3 |
| The example config's client keys (`your-api-key-1` and the rest) | They are public. The import leaves them out |
| The listen address | pluxy keeps the address it started on; see step 3 |
| pluxy's own settings in the file: `credentials_dir`, `packages`, `extension`, `extension_pages` and `client_key_labels` | pluxy keeps those of the config it runs on. (`extensions`, `cors`, `drop_policy` and `token_count_estimate` are brought.) |
| TLS | Refused while pluxy runs; see step 3 |
| An `openai-compatibility` `excluded-models` | CPA does not use it either |
| The Antigravity provider's own settings under `oauth.providers.antigravity` (`sensitive-words`, `connection-pool`, the signature and credit switches) | The extension does not read them. The import names each one |
| Auth files of any other type, with no `type`, larger than 1 MiB, that are not a JSON object or have no token, or whose ID pluxy already has; entries that are not `.json` files at the top of the folder | No provider in pluxy serves them, or they are unusable. The import names each file |
| The settings that make another client look like Claude Code (cloaking, fingerprint profiles) and the header defaults | pluxy does not make another client look like Claude Code |
| A header that holds `$CPA-SESSION-ID`, or that copies the client's `Authorization`, `Proxy-Authorization`, `X-Api-Key`, `X-Goog-Api-Key` or `Cookie` header | pluxy has no CPA session ID, and never sends a client's key, a proxy password or cookies upstream. The import names each one. A header name pluxy refuses (`Host`, `Content-Length`, `Transfer-Encoding`, `Connection`) in `config.yaml` fails the import, naming it; in an auth file it leaves that file under **Not brought** |
| `plugins`, other than Lamplighter's window ignition | pluxy's providers are separate programs, installed as packages, not libraries loaded into the core |
| Lamplighter's notifications and reminders (Bark, webhooks, the usage thresholds, reset reminders) and its own page | pluxy sends no usage notifications. The panel shows each account's usage, and `pluxy account list` its health |
| Lamplighter's `timezone` | pluxy reads the ignition hours in the machine's local time zone. Move `start_hour` and `end_hour`, or set the machine's time zone |
| The panel download settings (`panel-github-repository`, `disable-auto-update-panel`, `disable-control-panel`, `github-token`) | The panel is built into pluxy; nothing is downloaded, and it is always on with the management API |
| The remote model catalogs (`models.catalog` and the others) | Each provider carries its own model list |
| Response compression, `commercial-mode`, discovery, `max-request-body-bytes` | Left to a reverse proxy, or switches for features pluxy does not have. Request bodies are capped at 24 MiB |
| Settings pluxy does not support (the Codex `enable-apply-patch`, `optimize-multi-agent-v2` and stream bootstrap settings) | Named in the **Not brought** list as "not supported yet". A model's `force-mapping` and `openapi` are named the same way in the **Changed** row of the account |
| A provider key with no `api-key`, a Codex key with no `base-url`, an `openai-compatibility` group with `disabled: true`, and `api-keys.meta` | They make no account. The import names each one |
| `session-affinity-subagents`, `session-affinity-rebind-on`, `management.base-url`, `oauth.providers.devin` (its `sensitive-words`), a key's `websockets` for xAI | No use here: a subagent stays on its session's account, there is no terminal UI, and xAI is served over HTTP. The import names each one |
| Environment variables and `.env` (`MANAGEMENT_PASSWORD`, `PGSTORE_*`, `GITSTORE_*`, `OBJECTSTORE_*`) | pluxy does not read them; the settings are in the config file |
| `save-cooldown-status` | pluxy needs no switch: it saves the blocks it must keep in `state/routing.json` by itself |

You do not have to work out which of these your file uses: the import names each one in your file,
with its reason, in the **Changed** or **Not brought** list. Where it cannot place a setting at all
(TLS that differs from the running pluxy, a refused header name, a rule path pluxy cannot apply, an
invalid `quota-response-mode`, a key `weight` outside 0 to 1000000, an unusable proxy URL), it fails
and writes nothing, naming the setting.

## What is named differently

| In CPA | In pluxy |
| --- | --- |
| Auth file, in the auth folder | Account, in the credential store (the `credentials` folder beside the config) |
| `api-keys` (`access.api-keys` in the newer layout): the keys clients send | Client keys (`client_keys`); `pluxy key list` |
| `secret-key` under `remote-management` or `management` | pluxy's own management key (`management_key`) |
| A provider or channel (`codex`, `claude`, ...) built into the program | A provider extension, installed as a package (`pluxy/codex`, `pluxy/claude`, ...); `pluxy ext list` |
| CPA's login command-line flags | `pluxy account add <provider>`, or the panel's **Add an account** page |
| Several models pooled under one alias in an `openai-compatibility` entry | A fallback chain; `pluxy chain list` |
| `weighted-round-robin` | `weighted` |
| Cooling | An account's health is `cooling` (blocked for now) or `exhausted` (usage used up); `pluxy account list` shows the health, until when and why, and `pluxy account clear <id>` forgets the blocks and the last error |
| `disable-cooling` | `disable_cooling`, in `routing` or on an account |
| `oauth-model-alias`, `oauth-excluded-models` | `model_alias` and `excluded_models` |
| Usage statistics | The usage record; `pluxy usage` |
| The management API at `/v0/management` | The same path, with pluxy's own calls ([management API](management-api.md)) |

The names on the right are the ones in pluxy's `config.json`, its commands and its panel.

## What behaves differently on purpose

- **Nothing is installed at first.** A new pluxy serves no provider until you install one, so a moved
  setup needs step 4 once.
- **pluxy's own config listens on `127.0.0.1`.** A CPA config with no host listens on every
  interface. The import does not carry that over by itself: the **Changed** list says how to keep it.
- **pluxy always has a management key and a client key.** With an empty key, CPA turns the
  management API off, and with no client keys it serves anyone. pluxy refuses to start without
  either.
- **A model name is served by one provider.** When accounts of two providers both serve a name,
  pluxy sends its requests to the accounts of one provider: the provider of the highest-priority
  account that serves the name. To move a request on to another provider or model when the first
  cannot serve, you write that down as a fallback chain (`pluxy chain set`). The import makes chains
  from CPA's pooled aliases for you ([routing](routing.md)).
- **A block ends when the limit does.** pluxy blocks an account for as long as the provider said. A
  rate limit or quota block with more than 15 minutes left is checked every 15 minutes: pluxy reads
  the account's usage from the provider and ends the block once the limit is no longer full. A
  reset time more than 8 days away is not believed, unless the provider also names the usage
  window's length and the reset fits it: the account is then held for 30 minutes and checked after
  15.
- **A failing upstream is retried sooner.** After a server error or a broken connection, with
  `transient-error-cooldown-seconds` at 0, pluxy waits one second at first and doubles the wait up to
  a minute (each wait is cut by a random share of up to half), where CPA waits a fixed minute. A
  `Retry-After` from the upstream wins.
- **Blocks survive a restart.** Blocks on an account or one of its models with more than 30 seconds
  left, and bans, are saved in `state/routing.json` and read back at start, so a restart sends no
  requests to an account whose usage ran out. Blocks on an upstream endpoint are not saved.
- **Window ignition checks that the window started.** `hold_ms` after the request that starts a
  window (60 seconds unless set), pluxy reads the account's usage again; a window that still does
  not run counts as a failed ignition. A failed ignition is retried after `retry_ms`, longer after
  each failure in a row, and `max_failures` in a row pause that window until the next `start_hour`;
  a failure that would repeat (a refused key or account, a missing model) pauses it at once. What
  ignition knows is not saved: after a restart pluxy reads each account at once.

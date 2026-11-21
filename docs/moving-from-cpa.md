# Moving from CLIProxyAPI

This guide takes a working [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) (CPA) setup
to pluxy. One command, `pluxy import cpa`, reads CPA's `config.yaml` and its auth folder and
brings across the settings, client keys, provider keys and signed-in accounts. It then lists
everything it brought, everything it changed on the way and everything it left behind, with the
reason for each, so nothing is dropped without a word.

The import never changes CPA's files, so you can go back at any point.

## Before you start

- Build pluxy as the [quick start](../README.md#quick-start) does (step 1). The commands below
  run in `~/pluxy`.
- Find CPA's `config.yaml`. The examples use `~/.cli-proxy-api/config.yaml`; use your own path.
  The import finds the auth folder from the config's `auth-dir` (`oauth.auth-dir` in the newer
  layout); `--auth-dir <folder>` names another.
- Read [Running both side by side](#running-both-side-by-side) first if CPA will keep serving
  while you try pluxy. A signed-in account must be used from one of them only.

## Move

### 1. Start pluxy

```bash
cd ~/pluxy
./pluxy serve
```

With no config in the folder, pluxy writes its own `config.json` and prints its address:
`127.0.0.1` port 8317, or a free port when CPA still holds 8317. The import adds to this config.
Leave pluxy running and open a second terminal in `~/pluxy`.

### 2. See what the import would do

```bash
./pluxy import cpa ~/.cli-proxy-api/config.yaml --dry-run
```

This writes nothing. It prints three lists that together name every key of the file and every
file of the auth folder, each once:

- **Brought**: what comes across as it is.
- **Changed**: what comes across differently, with what it was, what it becomes, why, and how to
  keep the old behaviour.
- **Not brought**: what stays behind, with the reason. Most rows end with "it is off, empty or
  at today's default here, so nothing is lost"; read the ones that do not.

A last list, **Needs**, appears when the imported accounts need a provider pluxy does not have
installed yet.

### 3. Import

```bash
./pluxy import cpa ~/.cli-proxy-api/config.yaml
```

The same lists, and this time the settings, client keys and provider keys go into pluxy's
`config.json` and the auth folder's accounts into its credential store (the `credentials` folder
beside the config). Running it again adds nothing twice: an account pluxy already has stays as it
is, and the lists say so.

Two things to know about the result:

- **pluxy keeps its own management key.** CPA's key, if its file has one, is not brought: neither
  a plain key nor the bcrypt hash CPA writes over it after it first reads it. The key guards every
  client of pluxy, and the import does not change who may manage it. The **Not brought** list
  names CPA's key and says so. Go on signing in to the panel with pluxy's key, in `config.json`,
  where the `pluxy` commands read it; to change it, send
  `POST /v0/management/management-key/rotate`, which returns the new key once.
- **pluxy keeps its own address.** It goes on listening where it started. The **Changed** list
  shows the address CPA's file asked for and the `"listen"` line to put in `config.json` if you
  want it; restart pluxy after changing it.

### 4. Install the providers

Nothing is installed at first, so the imported accounts do not serve yet. Install each provider
the **Needs** list named, once:

```bash
./pluxy ext install pluxy/codex
```

`./pluxy account list` shows every account with the provider it needs in the `EXTENSION` column.
The panel's **Set up pluxy** page at `/panel/` installs providers too.

### 5. Check

```bash
./pluxy account list
./pluxy model list
./pluxy account test <account-id>
```

`model list` shows the models clients can ask for and the accounts that serve each. `account test`
sends one small request through an account and prints the reply. Your client keys came across, so
a client needs only pluxy's address: the same one as before if you gave pluxy CPA's port.

## What carries over

From `config.yaml`:

- TLS, trusted proxies and the outbound proxy;
- the client keys (never the management key: pluxy keeps its own);
- routing: the strategy, session affinity, retries, and the cooldown settings;
- the provider keys (`codex`, `claude`, `gemini`, `vertex`, `interactions`, `xai`,
  `openai-compatibility`): each key becomes an account, with its base URL, proxy, headers,
  priority, weight, prefix, excluded models and its `models` names and aliases; models pooled
  under one alias become a fallback chain;
- model aliases and excluded models for signed-in accounts (`oauth-model-alias`,
  `oauth-excluded-models`);
- the payload rules;
- the log, request-log and usage settings.

From the auth folder, the files of these types: `codex`, `claude`, `kimi`, `vertex`, `xai`, `meta`
and `devin`. Each becomes an account whose ID is the file's name without `.json`, with the file's
label, priority, prefix, proxy, headers and whether it was disabled.

[`next/README.md`](../next/README.md#configyaml-with-todays-key-names) has the full list: every
CPA setting with what pluxy does with it, and every field read from each type of auth file. It
describes a `config.yaml` written for pluxy's own earlier core, a CPA fork, which it calls
"today's core".

## What does not carry over, and why

| What | Why |
| --- | --- |
| The management key, plain or as a bcrypt hash | pluxy keeps its own management key: it guards every client of core, and an import does not change it. Change it with `POST /v0/management/management-key/rotate` |
| The example config's client keys (`your-api-key-1` and the rest) | They are public. The import leaves them out |
| Antigravity auth files | pluxy does not ship an Antigravity provider. The import names each file it leaves |
| Auth files of any other type, or with no `type` | No provider in pluxy serves them. The import names each one |
| The settings that make another client look like Claude Code (cloaking, fingerprint profiles) and the header defaults | pluxy does not make another client look like Claude Code, so these are left out. One exception: `disable-codex-cloaking` carries over, as `codex_identity: false` on the imported Codex accounts |
| `plugins` | pluxy's providers are separate programs, installed as packages, not libraries loaded into the core |
| The panel download settings (`panel-github-repository`, `disable-auto-update-panel`, `disable-control-panel`) | The panel is built into pluxy; nothing is downloaded, and it is always on with the management API |
| The remote model catalogs (`models.catalog` and the others) | Each provider carries its own model list |
| Response compression, `commercial-mode`, discovery, `max-request-body-bytes` | Left to a reverse proxy, or switches for features pluxy does not have. Request bodies are capped at 24 MiB |
| Environment variables and `.env` (`MANAGEMENT_PASSWORD`, `PGSTORE_*`, `GITSTORE_*`, `OBJECTSTORE_*`) | CPA's environment variables are not read; the settings are in the config file |
| Cooldown state (`save-cooldown-status`) | pluxy needs no switch: it saves the blocks it must keep in `state/routing.json` by itself |
| The listen address | pluxy keeps the address it started on; see step 3 |

You do not have to work out which of these your file uses: the import names each one in your file,
with its reason, in the **Changed** or **Not brought** list.

## What is named differently

| In CPA | In pluxy |
| --- | --- |
| Auth file, in the auth folder (`auth-dir`, `~/.cli-proxy-api`) | Account, in the credential store (the `credentials` folder beside the config) |
| `api-keys` (`access.api-keys` in the newer layout): the keys clients send | Client keys (`client_keys`); `pluxy key list` |
| `secret-key` under `remote-management` or `management` | pluxy's own management key (`management_key`); the import does not bring CPA's. Change it with `POST /v0/management/management-key/rotate` |
| The panel at `/management.html`, downloaded on first use | The panel at `/panel/`, built in |
| A provider or channel (`codex`, `claude`, ...) built into the program | A provider extension, installed as a package (`pluxy/codex`, `pluxy/claude`, ...); `pluxy ext list` |
| Login flags (`--codex-login`, `--claude-login`, ...) | `pluxy account add <provider>`, or the panel's **Add Account** page |
| Several models pooled under one alias in an `openai-compatibility` entry | A fallback chain; `pluxy chain list` |
| `weighted-round-robin` | `weighted` (CPA's name still works in a `config.yaml`) |
| Cooling, `disable-cooling` | Cooldown; `pluxy account list` shows what is blocked, until when and why, and `pluxy account clear <id>` ends it |
| `oauth-model-alias`, `oauth-excluded-models` | `model_alias` and `excluded_models` |
| Usage statistics | The usage record; `pluxy usage` |
| The management API at `/v0/management` | The same path, with pluxy's own calls ([`next/management/README.md`](../next/management/README.md)). A tool written for CPA's management API does not work against pluxy |

The names on the right are the ones in pluxy's `config.json`, its commands and its panel.

## What behaves differently on purpose

- **Nothing is installed at first.** A new pluxy serves no provider until you install one, so a
  moved setup needs step 4 once.
- **pluxy listens on `127.0.0.1` unless told otherwise.** A CPA config with no host listens on
  every interface. The import does not carry that over by itself: the **Changed** list says how
  to keep it.
- **pluxy always has a management key and a client key.** With an empty key, CPA turns the
  management API off, and with no client keys it serves anyone.
- **A model name is served by one provider.** When accounts of two providers both serve a name,
  pluxy sends its requests to one provider's accounts only. To move a request on to another
  provider or model when the first cannot serve, you write that down as a fallback chain
  (`pluxy chain set`). The import turns CPA's pooled aliases into chains for you;
  `pluxy chain list` shows them.
- **Cooldowns end when the limit does.** pluxy blocks an account for as long as the provider
  said. For a block with more than 15 minutes left, it reads the account's usage from the
  provider every 15 minutes and ends the block once the limit is no longer full. A reset more
  than 8 days away is not believed: the account is held for 30 minutes, and checked after 15.
- **A failing upstream is retried sooner.** After a server error from the upstream (a 5xx), with
  `transient-error-cooldown-seconds` at 0, pluxy backs off from one second up to a minute, where
  CPA waits a fixed minute.
- **Quota blocks survive a restart, and are checked at once when pluxy starts**, so a restart
  sends no requests to an account whose usage ran out.

## Running both side by side

You can keep CPA serving while you try pluxy, with two rules.

**Give pluxy its own port.** This happens by itself: started while CPA holds port 8317, pluxy
takes a free port and prints it, and the import does not change it. Point one client at that
address and leave the rest on CPA.

**Use each signed-in account from one of them only.** A subscription sign-in holds a refresh
token, and the provider replaces it at each renewal. When CPA and pluxy both renew the same
sign-in, one of them is signed out. So do not import the whole auth folder while CPA still uses
it. Copy the files of the accounts you want to try into a folder of their own, import with that
folder as the auth folder, and take those files out of CPA's auth folder:

```bash
mkdir ~/pluxy/cpa-auth
cp ~/.cli-proxy-api/<file>.json ~/pluxy/cpa-auth/
./pluxy import cpa ~/.cli-proxy-api/config.yaml --auth-dir ~/pluxy/cpa-auth
```

Provider API keys in `config.yaml` have no such limit: both can use the same key.

When you are done, stop CPA and run the import once more without `--auth-dir` to bring the rest
of the accounts; what pluxy already has stays as it is. Then give pluxy CPA's address, as the
**Changed** list says, so the remaining clients need no change. To go back instead, stop pluxy
and sign in again in CPA to any account whose file you imported and pluxy has since renewed.

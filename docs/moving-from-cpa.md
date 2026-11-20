# Moving from CLIProxyAPI

This guide takes a working [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) (CPA) setup
to pluxy. pluxy reads CPA's `config.yaml` with CPA's key names, and copies the files of CPA's auth
folder into its own store, so you rename nothing and sign in to nothing again.

There is no single import command yet. The move is a handful of commands, and CPA's files are
never changed, so you can go back at any point.

## Before you start

- Build pluxy as the [quick start](../README.md#quick-start) does (step 1). The commands below
  run in `~/pluxy`.
- Find CPA's `config.yaml` and its auth folder. The folder is the config's `auth-dir`
  (`oauth.auth-dir` in the newer layout), by default `~/.cli-proxy-api`.
- Read [Running both side by side](#running-both-side-by-side) first if you want to keep CPA
  serving while you try pluxy. Otherwise stop CPA now: pluxy takes its port from the same config.

## Move

### 1. Copy the config

```bash
cd ~/pluxy
cp ~/.cli-proxy-api/config.yaml config.yaml
```

Use your own path if CPA's `config.yaml` is elsewhere. pluxy reads a file whose name ends in
`.yaml` or `.yml` with CPA's key names, in the newer layout (`server.port`, `access.api-keys`,
`api-keys.codex`) and the older one (`port`, `api-keys` as a list, `codex-api-key`).

Check two things in the copy:

- **The management key must be in plain text.** CPA replaces `secret-key` (under `management`, or
  `remote-management` in the older layout) with a bcrypt hash after it first reads it. pluxy
  cannot use a hash: it says so and does not start. Write the key itself, or a new one, in its
  place. A file with no management key is refused too.
- **The client keys must be your own.** pluxy refuses every client request while the list still
  holds the example config's `your-api-key-1`, `your-api-key-2` or `your-api-key-3`, and it
  refuses a config with no client key at all.

### 2. Import the auth folder

```bash
./pluxy -config config.yaml -import-auth ~/.cli-proxy-api
```

This copies each credential file at the top of the folder into pluxy's credential store (the
`credentials` folder beside the config), prints what it imported and what it skipped, and exits.
It never changes the auth folder. Run it while pluxy is stopped. Running it again skips the files
it has already imported.

Before the list, it prints the settings in your `config.yaml` that pluxy does not support, so you
see them before pluxy ever serves a request.

### 3. Start pluxy and install the providers

```bash
./pluxy serve -config config.yaml
```

Nothing is installed at first, so the accounts do not serve yet. In a second terminal in
`~/pluxy`, list them:

```bash
./pluxy account list --config config.yaml
```

The `EXTENSION` column names the provider each account needs: `pluxy/codex`, `pluxy/claude`,
`pluxy/gemini`, `pluxy/xai`, `pluxy/kimi`, `pluxy/meta`, `pluxy/devin` or, for an
`openai-compatibility` entry, `pluxy/openai-compatible`. Install each provider the list names,
once:

```bash
./pluxy ext install pluxy/codex --config config.yaml
```

The panel's **Set up pluxy** page at `/panel/` installs them too. Until its provider is
installed, an account is listed but cannot serve: a test of it answers
`extension pluxy/codex does not run`.

The `pluxy` commands look for `config.json` unless told otherwise, which is why each one here
names the file with `--config config.yaml`.

### 4. Check

```bash
./pluxy model list --config config.yaml
./pluxy account test <account-id> --config config.yaml
```

`model list` shows the models clients can ask for and the accounts that serve each. `account test`
sends one small request through an account and prints the reply. Your clients keep their settings when pluxy listens on the port
CPA used and the client keys are the same.

## What carries over

From `config.yaml`:

- the listen host and port, TLS, trusted proxies and the outbound proxy;
- the client keys and the management key (in plain text);
- routing: the strategy, session affinity, retries, and the cooldown settings;
- the provider keys (`codex`, `claude`, `gemini`, `vertex`, `interactions`, `xai`,
  `openai-compatibility`): each key becomes an account, with its base URL, proxy, headers,
  priority, weight, prefix, excluded models and its `models` names and aliases;
- model aliases and excluded models for signed-in accounts (`oauth-model-alias`,
  `oauth-excluded-models`);
- the payload rules;
- the log, request-log and usage settings.

From the auth folder, the files of these types: `codex`, `claude`, `kimi`, `vertex`, `xai`, `meta`
and `devin`. Each becomes an account whose ID is the file's name without `.json`, with the file's
label, priority, prefix, proxy, headers and whether it was disabled.

[`next/README.md`](../next/README.md#configyaml-with-todays-key-names) has the full list: every
CPA setting with what pluxy does with it, and every field read from each type of auth file. That
section was written for pluxy's own earlier core, a CPA fork, and calls it "today's core".

## What does not carry over, and why

| What | Why |
| --- | --- |
| A bcrypt-hashed management key | A hash cannot be turned back into the key, and pluxy keeps the key itself. Write a key in plain text |
| Antigravity auth files | pluxy does not ship an Antigravity provider. The import names each file it skips |
| Auth files of any other type, or with no `type` | No provider in pluxy serves them. The import names each one |
| The settings that make another client look like Claude Code or Codex (cloaking, header defaults, fingerprint profiles) | pluxy never changes how a client presents itself |
| `plugins` | pluxy's providers are separate programs, installed as packages, not libraries loaded into the core |
| The panel download settings (`panel-github-repository`, `disable-auto-update-panel`, `disable-control-panel`) | The panel is built into pluxy; nothing is downloaded, and it is always on with the management API |
| The remote model catalogs (`models.catalog` and the others) | Each provider carries its own model list |
| Response compression, `commercial-mode`, discovery, `max-request-body-bytes` | Left to a reverse proxy, or switches for features pluxy does not have. Request bodies are capped at 24 MiB |
| Several upstream models pooled under one alias in an `openai-compatibility` entry | pluxy does not pool models yet: it keeps the first entry and names the rest in its log |
| Environment variables and `.env` (`MANAGEMENT_PASSWORD`, `PGSTORE_*`, `GITSTORE_*`, `OBJECTSTORE_*`) | pluxy reads no environment variables; everything is in the config file |
| Cooldown state | pluxy starts with every account available |

You do not have to work out which of these your file uses. Each time pluxy loads a `config.yaml`,
it logs one line naming the settings in the file that it does not support yet and one line naming
those it ignores for good. `-import-auth` prints the same lines first. A setting that is off,
empty or at CPA's default is not named.

## What is named differently

| In CPA | In pluxy |
| --- | --- |
| Auth file, in the auth folder (`auth-dir`, `~/.cli-proxy-api`) | Account, in the credential store (the `credentials` folder beside the config) |
| `api-keys` (`access.api-keys` in the newer layout): the keys clients send | Client keys (`client_keys`); `pluxy key list` |
| `secret-key` under `remote-management` or `management` | Management key (`management_key`) |
| The panel at `/management.html`, downloaded on first use | The panel at `/panel/`, built in |
| A provider or channel (`codex`, `claude`, ...) built into the program | A provider extension, installed as a package (`pluxy/codex`, `pluxy/claude`, ...); `pluxy ext list` |
| Login flags (`--codex-login`, `--claude-login`, ...) | The panel's **Add Account** page |
| `weighted-round-robin` | `weighted` (CPA's name still works in a `config.yaml`) |
| Cooling, `disable-cooling` | Cooldown; `pluxy account list` shows what is blocked, until when and why, and `pluxy account clear <id>` ends it |
| `oauth-model-alias`, `oauth-excluded-models` | `model_alias` and `excluded_models` |
| Usage statistics | The usage record; `pluxy usage` |
| The management API at `/v0/management` | The same path, with pluxy's own calls ([`next/management/README.md`](../next/management/README.md)). A tool written for CPA's management API does not work against pluxy |

A `config.yaml` keeps CPA's names. The names on the right are the ones in pluxy's own
`config.json`, its commands and its panel.

## What behaves differently on purpose

- **Nothing is installed at first.** A new pluxy serves no provider until you install one, so a
  moved setup needs step 3 once.
- **pluxy always has keys.** It refuses a config with no management key or no client key, where
  CPA turns the management API off or serves anyone.
- **A config with no host listens on every interface, as in CPA.** A config pluxy writes itself
  listens on `127.0.0.1` only. If your `config.yaml` sets no host and you do not want other
  machines to reach pluxy, set `host: "127.0.0.1"`.
- **Cooldowns end when the limit does.** pluxy blocks an account for as long as the provider
  said. For a block with more than 15 minutes left, it reads the account's usage from the
  provider every 15 minutes and ends the block once the limit is no longer full. A reset more
  than 8 days away is not believed: the account is held for 30 minutes and then checked.
- **A failing upstream is retried sooner.** After a server error from the upstream (a 5xx), with
  `transient-error-cooldown-seconds` at 0, pluxy backs off from one second up to a minute, where
  CPA waits a fixed minute.
- **Quota blocks survive a restart, and are checked at once when pluxy starts**, so a restart
  sends no requests to an account whose usage ran out.
- **A bad `proxy-url` stops the load.** CPA logs it and goes on without the proxy.
- **Relative TLS paths are relative to the config file**, not to the folder pluxy was started in.

## Running both side by side

You can keep CPA serving while you try pluxy, with two rules.

**Give pluxy its own port.** In pluxy's copy of the config, change `port` (`server.port` in the
newer layout) to a free port such as 8318. Point one client at that port and leave the rest on
CPA.

**Use each signed-in account from one of them only.** A subscription sign-in holds a refresh
token, and the provider replaces it at each renewal. When CPA and pluxy both renew the same
sign-in, one of them is signed out. So do not import the whole auth folder while CPA still uses
it. Copy the files of the accounts you want to try into a folder of their own, import that
folder, and take those files out of CPA's auth folder:

```bash
mkdir ~/pluxy/cpa-auth
cp ~/.cli-proxy-api/<file>.json ~/pluxy/cpa-auth/
./pluxy -config config.yaml -import-auth ~/pluxy/cpa-auth
```

Provider API keys in `config.yaml` have no such limit: both can use the same key.

When you are done, stop CPA, import the rest of the auth folder, and set pluxy's port to the one
CPA used, so the remaining clients need no change. To go back instead, stop pluxy and sign in
again in CPA to any account whose file you imported and pluxy has since renewed.

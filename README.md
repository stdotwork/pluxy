# pluxy

pluxy is an AI API router that pools the accounts you already have (subscription sign-ins such as
ChatGPT, Claude or Kimi, and API keys) and serves them to coding tools behind OpenAI-, Anthropic-,
Gemini- and Codex-compatible APIs, picking an account for each request. It does the job CLIProxyAPI
and EasyCLIProxyAPI do, built like the Pi coding agent: a small core with extensions you add,
applied to a router like LiteLLM, CLIProxyAPI or 9router. Nothing is installed at first; you add
only the providers you use.

## What it solves

- **An account hits its limit mid-task.** pluxy holds several accounts for a provider; when one is
  refused for a rate or usage limit, the request moves to the next account that serves the model as
  long as no part of the reply has reached the client.
- **An account stays blocked after its limit resets.** pluxy blocks an account for as long as the
  provider said, checks long blocks every 15 minutes, and ends the block once the limit is no longer
  full. `pluxy account list` shows why each account is blocked and until when;
  `pluxy account clear <id>` ends a block now.
- **You cannot see what is left.** `pluxy account quota <id>` and the panel's Home and Accounts
  pages show each account's usage limits as the provider reports them. `pluxy usage` counts requests
  and tokens by model, account, client key or provider from a record kept on disk.
- **Setup means writing a config file by hand.** The first start writes the config. Guided setup
  (**Set up pluxy** in the panel, or `pluxy setup`) checks this computer, suggests matching
  providers, and installs only the ones you pick.
- **Every coding tool wants different settings.** `pluxy client show <tool>` prints what to paste
  into Claude Code, Codex, OpenCode, Pi or Oh My Pi.

## Three parts

- **Core and the `pluxy` command** (Go): Core is the proxy itself and its `pluxy` command, mainly
  for agents and scripts. Each kind of upstream runs as a separate provider extension that core
  starts: Claude, Codex and ChatGPT, Gemini, Grok, Kimi Code, Meta Muse, Devin, and any
  OpenAI-compatible API.
- **The web panel**: Core serves a web panel at `/panel/` for managing a core on a computer without
  a screen or in a container.
- **The desktop app** (Linux only for now): The app runs a core on its own computer or connects to
  one elsewhere, and sets up coding tools.

## Quick start

There is no release to download yet, so the quick start builds pluxy from source. It ends with
one request sent through pluxy and counted in its usage record.

You need [Git](https://git-scm.com/downloads), [Go](https://go.dev/dl/) 1.26.11 or newer and
[Bun](https://bun.sh) 1.4.3. The commands are the same on Linux and macOS. On Windows, run them
in Git Bash, and write `pluxy.exe` wherever a command below says `pluxy`.

### 1. Build

```bash
git clone https://github.com/stdotwork/pluxy.git pluxy-src
cd pluxy-src/next
go generate ./internal/panel
go run ./cmd/pluxy-pack -out ~/pluxy
go build -o ~/pluxy/pluxy ./cmd/pluxy
```

`go generate` builds the web panel, which the program carries inside it. `pluxy-pack` packs the
provider extensions into `~/pluxy/bundled`, where pluxy looks for them. `go build` builds pluxy
itself. The source stays in `pluxy-src`, and the program is in `~/pluxy`. What pluxy writes from
here on (its config, accounts, logs and usage record) goes in its home folder: on Linux
`~/.local/share/pluxy`, on macOS `~/Library/Application Support/pluxy`, on Windows
`%LOCALAPPDATA%\pluxy`.

### 2. Start it

```bash
cd ~/pluxy
./pluxy serve
```

The first time, pluxy writes its config, `config.json`, in its home folder, and prints, among
other lines, where it is and its address:

```text
pluxy: config: /home/you/.local/share/pluxy/config.json (pluxy's home)
pluxy: first start: wrote a new config, /home/you/.local/share/pluxy/config.json, listening on 127.0.0.1:8317; its client key and management key are in that file
pluxy: listening on http://127.0.0.1:8317
```

The port is 8317, or a free one when 8317 is taken. `config.json` holds two keys that pluxy made
for you. The `management_key` signs you in to the panel; `./pluxy key management --show-key`
prints it. The one under `client_keys` is what a client sends with each request. Only your user
can read the file.

Leave pluxy running and open a second terminal in `~/pluxy` for the rest. The `pluxy` commands
find the address and the management key in pluxy's home from any folder; a `config.json` in the
folder they run in comes first ([pluxy's home](docs/config.md#pluxys-home) has the rules).

### 3. Install a provider

Nothing is installed at first. Open `http://127.0.0.1:8317/panel/` in a browser and sign in with
the management key. The panel opens on **Set up pluxy**, which lists what it found on the machine
(coding tools and their sign-ins) and the providers you can install. Tick the providers you have
an account for and press **Start**.

The same from the terminal:

```bash
./pluxy setup
```

It prints what it found and offers to install the providers it suggests. To install any other,
name it: `./pluxy ext available` lists them, and `./pluxy ext install pluxy/claude` installs one.

### 4. Connect an account

On the same page, **Connect an account** now lists the ways to sign in that the installed
providers offer, such as "Claude subscription" (a sign-in in the browser) or "API key". Press **Start** on one, fill in what it asks for and press **Continue**. The **Add an account** page does the same later.
From a terminal, `./pluxy account add <provider>` does the same, and `./pluxy account methods`
lists the providers and their ways to sign in.

Check that the account answers:

```bash
./pluxy account list
./pluxy account test <account-id>
```

`account test` sends one small request through the account and prints the reply.

### 5. Point a client at pluxy

```bash
./pluxy client show claude-code
```

This prints the lines to put in the tool's config file, with `<client-key>` where your client key
goes. The other tools are `codex`, `opencode`, `pi` and `omp`; [docs/clients.md](docs/clients.md)
has each one's settings, and the base URLs for any other client.

### 6. Send a request and see it counted

`./pluxy model list` prints the models your accounts serve. Send one of them a request with the
client key from pluxy's `config.json`:

```bash
curl http://127.0.0.1:8317/v1/chat/completions \
  -H "Authorization: Bearer <client-key>" \
  -H "Content-Type: application/json" \
  -d '{"model": "<model-id>", "messages": [{"role": "user", "content": "Say hello"}]}'
```

Then look at the usage record:

```bash
./pluxy usage
```

It shows one request with its tokens, under the model you asked for. The panel's Activity page shows
the same record. `./pluxy help` lists every command.

## Where to read more

[docs/README.md](docs/README.md) is the index of every page. Start with these:

| You want to | Read |
| --- | --- |
| Build pluxy, start it, or run it in a container | [docs/setup.md](docs/setup.md) |
| Add an account and check each provider's options | [docs/providers.md](docs/providers.md) |
| Connect a coding tool or another client | [docs/clients.md](docs/clients.md) |
| Understand account picking, blocks and fallback chains | [docs/routing.md](docs/routing.md) |
| Look up a config setting or environment variable | [docs/config.md](docs/config.md) |
| Find out why a request failed or fix a problem | [docs/troubleshooting.md](docs/troubleshooting.md) |

## What is in this repository

| Folder | What it holds |
| --- | --- |
| `next/` | Core, the `pluxy` command, the extension protocol and the provider extensions ([README](next/README.md)) |
| `next/ui/` | The UI kit and the web panel core embeds ([README](next/ui/README.md)) |
| `next/app/` | The desktop app ([README](next/app/README.md)) |
| `next/app-tools/` | Optional native tools the desktop app runs on demand ([README](next/app-tools/README.md)) |

## Credits and license

pluxy began as Soul4723's forks of router-for-me's
[CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI),
[EasyCLIProxyAPI](https://github.com/router-for-me/EasyCLIProxyAPI) and
[Cli-Proxy-API-Management-Center](https://github.com/router-for-me/Cli-Proxy-API-Management-Center),
which have since been replaced and removed. Their MIT notices, the Pi adapter's in
`next/app-tools/pi-setup/adapter/`, and the files `next/` keeps or adapts from upstream projects are
in [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md). The root license is not decided yet.

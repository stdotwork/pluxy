# Why pluxy is built the way it is

This page explains what pluxy is for and the design that follows from that: what core does itself,
what is left to extensions, how extensions work, the three ways to use pluxy, where it keeps its
files, and the UI kit. Each part says what is built and what is planned. The confirmed plans not
built yet are listed, in the order they come, in [plans.md](plans.md).

## What pluxy is for

pluxy is one router for coding tools. It pools the accounts a person has, subscription sign-ins
(a ChatGPT, Claude or Kimi subscription, for example) and API keys alike, and serves them behind
OpenAI-, Claude-, Gemini- and Codex-compatible APIs, so any coding tool can point at pluxy and use
any of those accounts. Its goal is the same as CLIProxyAPI's and EasyCLIProxyAPI's (the proxy and
the desktop app pluxy replaces), with smarter routing, a better UI and a different architecture.

The architecture comes from Pi, the coding agent whose features are almost all extensions, applied
to a router of the kind LiteLLM, CLIProxyAPI and 9router are: a small core, and extensions a person
adds when they want them. A first install runs as little as possible and grows only as its user
adds what they need. Being modular must not make it hard to use: someone who does not know what
most of the parts mean should still be able to set it up, mostly by letting pluxy detect what is on
the machine and clicking a few buttons. Speed and a small footprint are measured, not assumed:
[perf.md](perf.md) gives the numbers and the budgets CI holds pluxy to.

## A small core, and extensions for the rest

Core does only what every setup needs, and what must not be left to code someone else wrote. Core
keeps:

- **The client API.** Built: OpenAI Chat Completions (and the older text completions), OpenAI
  Responses over HTTP and WebSocket, Anthropic Messages, Gemini GenerateContent and its token
  counting and embeddings, the paths the Codex CLI calls, model lists, and the endpoints for
  compaction, search, images, audio, embeddings and Gemini Interactions, which core hands to a
  provider that serves them.
- **Format translation and streaming.** Built (see [Inside core](#inside-core)).
- **Routing and retries:** one engine that picks an account for each request, knows each account's
  state, keeps a session on the same account, and decides every retry. Built.
- **Credentials:** storing each account's sign-in or key, and renewing sign-ins. Built.
- **Client keys:** the keys coding tools use to reach pluxy. Built as a list of keys with labels;
  usage is recorded per key. Planned: limits, budgets and spend accounting per key, kept in core so
  that no extension can lose a spend record.
- **The extension manager**, which installs, starts, swaps and stops extensions. Built.
- **The management API** (`/v0/management`). Built: the `pluxy` command, the web panel and the
  desktop app all use it, and nothing else manages core. Planned: an MCP endpoint for agents, as a
  further client of the same API.

Everything else is an extension. Built: every provider is one; eight are bundled (Claude, Codex,
Devin, Gemini, Kimi, Meta, OpenAI-compatible and xAI). Planned: token savers, model combinations,
a vision adapter for models that cannot read images, quota readers and scheduled pings, logging
sinks, guardrails and secret masking, and caching. Two of these exist today in another form: a
provider reads its own accounts' quota (the protocol's `quota` call), and the pings of window
ignition, which start a subscription's usage window on a schedule, are sent by core.

### Nothing installed by default

Built: a first start installs no package. An install carries its provider packages as a bundled
registry on disk that needs no network, and setup, the panel's Extensions page or
`pluxy ext install` installs what a person picks. The registry gives each package a title, a
summary, a kind and a tier: `pick` (offered by setup), `verified` or `unverified`. Core shows the
tier and enforces nothing by it. The Antigravity provider is kept out of the bundle, in
`next/archive/`, and packed only on request.

Planned: a registry in four tiers: hidden dependencies that other packages pull in, the setup picks
(about ten), a verified marketplace, and unverified third-party sources installed only after a
warning. Installing should be as simple as adding a mod to a game: download a package file and drop
it in. Packages are to come from an open-source community marketplace, from an official marketplace
whose packages may be closed-source or paid, or from anywhere else, such as a GitHub repository,
which pluxy does not verify. The official registry is to be signed; today core checks each download
only against the digest its registry lists.

### Why extensions are separate programs

An extension is a program of its own that core starts and talks to over a local socket, not a
library loaded into core. A crash in an extension cannot take core down, and an extension need not
be written in Go. Code that many providers need, such as the client formats and server-sent events,
is in Go libraries (`next/format`, `next/llm`, `next/sdk`) that extensions build in; no extension
depends on another at run time. A separate process keeps a crash from spreading. It is not a
sandbox: an extension can reach whatever its program can reach, and the panel's Extensions page
says so.

Core is written in Go with the standard library and two outside modules (`next/go.mod`). The
desktop app is Tauri 2, so its native side is Rust. Neither is to be rewritten: new features go
into core and the shared UI kit, and into the app's Rust only when they need the app's own machine.

## Inside core

All of this is built.

- **One internal model.** Each client format is translated to and from one model of a request (an
  ordered list of typed items) and its reply (a stream of typed events), in `next/llm`, following
  Vercel's Language Model Specification and Open Responses. A request whose format the upstream
  already speaks passes through unchanged. A translation reports what it carried over only
  approximately or had to drop; the config's `drop_policy` says whether such a request is sent and
  the loss logged (`report`, the default) or refused (`reject`). Fields the model has no place for
  are kept with the item, so they survive a round trip into the same format.
- **One place decides retries.** For each request core picks an account, reads the extension's
  typed outcome, and decides whether to try another account. Once a reply has started reaching the
  client nothing is retried. Extensions do not retry on their own, with two single resends before
  any content reaches the client: the Codex extension drops encrypted reasoning that the upstream
  refuses as foreign and sends once more (unless `drop_policy` is `reject`), and the Gemini
  extension sends once more when the upstream's reply ends with `MALFORMED_FUNCTION_CALL`. Blocks
  after a refusal are kept per credential, model or endpoint, and an earlier request's failure never
  overrides a later success.
- **Accounts have states.** Each account is serving, cooling down, out of its limit until a reset,
  waiting for a new sign-in, closed by the provider, turned off, or unreadable. Blocks end on their
  own, and a long block is checked again against the provider's quota reading, so an account whose
  quota has reset comes back without anyone acting. Blocks longer than 30 seconds, and closed
  accounts, survive a restart.
- **Fallback is built by the user.** A person can make fallback chains: up to 8 steps, each a model
  on a provider. With no chain, a request stays on one provider and keeps its model, so by default
  it falls back only to another account of the same provider and model. A chain may name any model
  and provider; pluxy only never makes a chain for a person.
- **Sessions stay on one account.** Session affinity is on by default: the requests of one session
  (named by the client's session header or its prompt cache key) go to the same account while it
  can serve, so the upstream's prompt cache keeps working. How accounts are picked otherwise is a
  routing strategy; the default takes turns, and one strategy prefers the account whose quota
  resets soonest.
- **Each reply says who served it.** A reply names the account, provider and model that served it,
  and how many attempts it took, in `X-Pluxy-` headers (`routing.explain` turns them off); an error
  lists the accounts tried and the ones left out, with why.
- **Credentials are versioned.** Core stores each credential with a version, and the provider
  extension does the sign-in and the renewal. One renewal runs per credential at a time, and a
  renewal of a version that has since changed is refused. An extension receives only the
  credentials of its own accounts.
- **Config changes are all or nothing.** A reload reads, checks and prepares the new config, then
  switches to it in one step; requests already running keep the config they started with, and a
  reload that fails leaves the running config in place. The one exception: an enabled package
  that was not running before and fails to start is shown as failed while the rest of the reload
  goes ahead. Installing or enabling a package through management fails as a whole when the
  package does not start. Management shows both the newest config core has seen and the one serving.
- **Streams are bounded.** Core reads the extension's next event only once the client has taken the
  last one, so a slow client slows the extension down rather than filling memory. A call ends in
  exactly one `finish` or `error`, and a stream that just stops is an error, never a success.
- **Silent upstreams time out.** A streamed request has no overall time limit, but it fails when
  the upstream sends no reply headers within 60 seconds or nothing at all for 5 minutes; a request
  that is not streamed has a 10-minute limit. All three can be changed. Core also ends an attempt
  whose extension goes silent for longer than its own limits allow.
- **Model lists are pulled, not hard-coded.** Core asks each account's extension for its models
  every 30 minutes (plus up to 3 minutes of random delay, 15 seconds at most per request), on
  sign-in and on demand, and keeps the last good list when a request fails. Context lengths and
  capabilities in `/v1/models` come from an embedded snapshot of models.dev, refreshed once a day.
- **Usage is a bounded record that survives a restart.** Core keeps one event per request in
  memory (10,000 events and 24 hours by default) and writes each to a file of JSON lines for its UTC
  day. At start it reads yesterday's and today's files back. Day files are kept for 90 days by
  default (`usage.keep_days`); a summary of a closed day is written the first time a report covers
  it. Usage is broken down by model, provider, account and key, counts cache writes, and records
  why routing chose each request's account and which accounts were tried on a fallback. Planned:
  the cost of each request.

Details: [routing](routing.md) (accounts, routing, fallback chains, window ignition),
[config](config.md) (every setting and [reloads](config.md#reloading)),
[packages](extensions.md), [usage](usage-and-logs.md) and the
[management API](management-api.md).

## The extension model

Built:

- **Packages.** An extension ships as a package: one manifest with a publisher-qualified ID
  (`publisher/name`), a program for each platform, and optional pages and a settings schema. The
  package, the manifest format, the protocol, the pages and the settings each have their own
  version, so each can change without the others.
- **States.** A package is installed (its files are on disk) and enabled or not (core runs it only
  when enabled). Its accounts are kept apart from it, so removing a package keeps them. A running
  extension is starting, active, idle, draining, crashed, quarantined or stopped.
- **Activation.** Core checks a package's archive against the digest and size its registry lists
  before unpacking it, and checks the files again each time it starts the program. A new version
  starts and answers its handshake before core switches to it; the old version then finishes its
  requests within a deadline (10 minutes by default). If the new one fails, the old one keeps
  serving. A program that keeps crashing is restarted with a growing delay (1 second, doubling to
  60), and quarantined after 5 crashes in a row. `pluxy serve -safe-mode` starts core with no
  extensions.
- **Lazy start.** An extension's program runs only while it is needed: core stops it after it has
  been idle for a set time (`idle_stop_ms`, 10 minutes by default) and starts it again for the next
  request.
- **The protocol.** Each call is one request followed by a stream of events, on a local socket
  connection of its own, with no round trip per token. The calls are those of one kind of
  extension, a provider: `describe`, `generate`, `session`, `login`, `refresh`, `count_tokens`,
  `models`, `check`, `quota` and `endpoint`.
- **Trust.** Core installs only from the registries its config names, plus the bundled one; there
  is no default network registry, and a registry is a local file or an HTTPS address. Core starts
  each program with a cleared environment and gives it only its own accounts' credentials.

Planned, confirmed:

- **More kinds of extension** beside the provider, on the same separate-program model and protocol:
  quota readers (an extension that only reads accounts' quota, including accounts a person uses
  directly in the provider's own apps), request filters (secret masking, token-saving compression,
  scripted hooks, and the work others may write that pluxy will not verify), and event listeners
  (metrics, webhooks). The protocol's README changes with them.
- **Packages that carry themes** as well as extensions and pages. A package is not a way to ship
  agent skills or prompts.

Open, not decided: widening extensions to the breadth of Pi's (hooks on requests, replies and
streams; routing extensions that look to clients like a model of their own; extension CLI commands,
event listeners and pages) and to Pi's way of making one (install from a git address or a folder,
try an extension for one run, and script extensions that need no Go build). This is the direction
the design leans towards, not a decision.

Not planned: pluxy will not ship or verify extensions that make other clients pose as Claude Code,
or that rewrite words in a request to get past a provider's content filter. Others may build such
extensions; the extension model does not stop them.

Contracts: the [extension protocol](../next/protocol/README.md) (manifest, registry, handshake,
calls, versioning) and its [tutorial](../next/protocol/TUTORIAL.md). Installing and managing
packages: [extensions.md](extensions.md).

## Three ways to use pluxy

pluxy has three surfaces over the one management API. The panel and the app are for people and are
much alike; all three are meant to work well for agents too.

- **The `pluxy` command** comes with core and is mainly for agents and for debugging. Built: a
  command for each management call, `--json` to print the API's reply as it is, and exit codes
  that say what happened (unreachable, refused, not found, and so on). See [cli.md](cli.md).
- **The web panel** is for a core that runs on a machine without a screen or in a container. It
  controls core and the machine core runs on. Built: core serves it at `/panel/`, from inside its
  own program. See [panel.md](panel.md).
- **The desktop app** runs anywhere. It either runs a core on its own machine and manages it, as
  EasyCLIProxyAPI did, or connects to a core that is already running, on the same machine or on
  another one over HTTPS, over plain HTTP that Tailscale carries from this computer (a Tailscale
  interface here, and the resolved address in Tailscale's ranges), or over plain HTTP on a network
  the person says, in the native key window, that they trust. It also sets up coding tools on its
  own machine. Built, on Linux only (CI also builds and tests it on Windows and macOS, but the app
  is not supported there yet). See `app.md` and, for how it works inside, `app-design.md`.

Setup should mostly detect, so a person clicks a few buttons instead of filling in forms, in all
three surfaces. Built:

- The app's first launch asks one thing: run pluxy on this computer, or connect to one already
  running. The options for each appear only after the choice.
- Setup in core scans the machine core runs on for eight coding tools (Claude Code, Codex,
  OpenCode, Pi, Oh My Pi, Claude Desktop, Grok Build and Hermes Agent) and reads the settings of
  the first five. Core never writes a coding tool's settings itself: the panel and `pluxy client
  show` give the text to paste.
- The app sets up all eight on its own machine, through native tools it runs on demand. A build
  carries no trust roots yet, so for now only test builds can enable native tools.
- `pluxy import cpa` brings over a CLIProxyAPI config and its sign-in files, and reports every item
  as brought over, changed, not brought over, or needing the person's action.

Planned: built-in control of Tailscale (the private network service), as easy as T3 Code's, so a
core on another machine can be reached without opening it to the internet. Considered: a command
like T3 Code's issue triage, which gathers what an agent needs to look into a problem and hands it
to the person's own coding agent.

## Where pluxy keeps its files

pluxy keeps its state in plain files, not in a database: JSON for settings and records, JSON lines
for usage, text for logs. Agents and people can read them, the standard library is enough to
write them, and a whole pluxy can be backed up or moved by copying one folder.

A pluxy run by a person's user account keeps all its files in one folder, pluxy's home:
`$XDG_DATA_HOME/pluxy` (`~/.local/share/pluxy`) on Linux and other Unix systems,
`~/Library/Application Support/pluxy` on macOS and `%LOCALAPPDATA%\pluxy` on Windows, or the
folder `PLUXY_HOME` names. Every `pluxy` command uses it when it is given no config file and the
folder it runs in has no `config.json`, so the command works from any folder and the app and a
terminal find the same core. Beside its `config.json`, core keeps:

| Folder or file | What it holds |
| --- | --- |
| `credentials/` | one JSON file per account, readable only by its user |
| `packages/` | installed packages, the record of them, and extensions' settings |
| `usage/` | one file of JSON lines per UTC day, and summaries of closed days |
| `logs/` | the main log and request logs |
| `state/` | routing blocks and closed accounts, the model catalogue, setup's last scan, themes, and the key that derives client key IDs |

Credentials are plain files, protected by being readable only by their user (folders `0700` and
files `0600`; an owner-only access list on Windows). When writing a first config, core refuses a
home folder that is a link, owned by another user, or (on Unix) writable by group or others, and on
Windows gives it an owner-only access list. Planned: credentials encrypted at rest.

Why one folder, and why this one:

- **One folder, not a config folder and a data folder.** Core keeps its credentials, packages, logs
  and state beside its config file, and a container keeps them in one volume. Per-user programs
  that keep keys and data together do the same: Syncthing has one home folder (`STHOMEDIR`), T3
  Code `~/.t3` (`T3CODE_HOME`), Ollama `~/.ollama`. The GitHub CLI splits its files four ways, but
  its config folder holds settings a person edits; pluxy's config holds keys, and core rewrites it.
- **The data folder.** The XDG Base Directory Specification puts user data in `XDG_DATA_HOME`, and
  state that is "not important or portable enough", such as logs and history, in
  `XDG_STATE_HOME`. Credentials are the files pluxy can least afford to lose, so the folder goes
  with them. Tailscale's daemon does the same when it runs without root
  (`$XDG_DATA_HOME/tailscale`), and the desktop app keeps its own core in its data folder.
- **macOS and Windows.** On macOS, `~/Library/Application Support` is where Go's
  `os.UserConfigDir`, Syncthing and the desktop app put such files. On Windows the local
  application data folder keeps keys off a roaming profile, which is copied to a server and to the
  person's other computers; Syncthing uses it too, and so does the desktop app for every file that
  holds a key.
- **One variable.** `PLUXY_HOME` names the whole folder, as `STHOMEDIR` and `T3CODE_HOME` do. It
  must be an absolute path: the specification has a program ignore a relative one, and a relative
  one would make each folder pluxy runs in a different home.
- **No second pluxy appears unasked.** A `config.json` in the current folder comes first, and a
  core started with `-config` keeps its files beside that file, so the desktop app's own core keeps
  its files in the app's data folder. While the app keeps a core of its own and pluxy's home has no
  config, a plain `pluxy serve` refuses to write a first config there and names the choice, since
  two pluxys with separate keys and accounts would look like one that lost its accounts.

Sources: the [XDG Base Directory Specification](https://specifications.freedesktop.org/basedir/latest/);
Syncthing's [configuration](https://docs.syncthing.net/users/config.html) and
[locations](https://github.com/syncthing/syncthing/blob/main/lib/locations/locations.go);
Tailscale's [state path for a user](https://github.com/tailscale/tailscale/blob/main/paths/paths_unix.go);
Ollama's [FAQ](https://github.com/ollama/ollama/blob/main/docs/faq.mdx); T3 Code's
[background service](https://github.com/pingdotgg/t3code/blob/main/docs/user/background-service.md);
the GitHub CLI's [environment](https://cli.github.com/manual/gh_help_environment); Go's
[`os` package](https://pkg.go.dev/os). Details: [pluxy's home](config.md#pluxys-home) and
[setup](setup.md#where-pluxy-keeps-its-files).

## The UI kit and themes

All of this is built. The web panel and the desktop app are two builds of one UI kit in `next/ui`,
written with Preact and built with Bun to keep the panel small. Each build implements the kit's
host interface (the browser's or the app's way to call core) and picks its pages; both show the
same five places, Home, Accounts, Activity, Tools and Settings, and the app adds its own pages for
the computer it runs on. Both read the same themes and put shared pages in the same places.

Themes reach far into the UI, within limits that keep it safe:

- **Themes are data.** A theme is a JSON file of tokens (colour, type, spacing, density, shape,
  shadows, motion and limit-gauge cutoffs) and layout options (inset or flat content frame, where
  the navigation sits, lists as tables or cards, where details open, page width, where tabs sit).
  The UI never runs anything from a theme.
- **Custom CSS can be switched off.** A theme, or the person, can add CSS on top. A switch on the
  Themes page turns all custom CSS off, and so does opening the panel with `?safe` in its address,
  for when custom CSS has broken the UI.
- **Themes from T3 Code load as they are.** The kit reads T3 Code's theme files and maps their
  colour roles onto its tokens, so a theme made for one can be used in the other.

pluxy bundles three themes (`pluxy/default`, `pluxy/terminal` and `pluxy/paper`), follows the
system's light or dark setting by default, and keeps added themes in core's `state/` folder, so
the panel and the app connected to one core show the same ones. Contract:
[themes](../next/ui/themes/README.md).

**Extension pages** are shown in a sandboxed frame from a separate site: another host name, not
just another port, because a browser sends a host's cookies to every port. A page talks to the
panel only through a message channel, can only read and write its own extension's settings, and
never receives the management key. Settings are forms the panel draws from the extension's
schema. Contract: [extension pages](../next/ui/extension-pages/README.md).

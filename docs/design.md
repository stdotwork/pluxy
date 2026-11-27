# The design of pluxy's core

pluxy's product is the core in `next/`, its extensions, and the web panel and desktop app built on
one UI kit. It was written from scratch rather than ported from the old core (the Go proxy that was
in `core/` until it was removed from the repository); the old core serves as a reference for
behaviour, checked by the [test corpus](corpus.md). This page records the decisions behind that
design and why they were made. The contracts themselves are in the READMEs it links to.

Parity with the old core and the old desktop app means the same features and a similar UI and
workflow, so that someone who knows the old ones finds things in the same places. It does not mean
copying the old code's bugs. Each intended difference is written down: for the app in
[`next/app/PARITY.md`](../next/app/PARITY.md), and for core's behaviour as `intended` exceptions in
the corpus.

## A small core with extensions

The aim is a small install that runs only what the user asks for, where adding or swapping a piece
is quick and needs no restart. So core does only what every setup needs, and everything else is an
extension. Core keeps:

- the client API in the OpenAI Chat Completions, OpenAI Responses (over HTTP and WebSocket),
  Anthropic Messages and Gemini formats, including the paths the Codex CLI calls;
- routing requests between accounts, and storing accounts and their credentials;
- the config, reloaded without a restart;
- the extension manager, which installs, starts, swaps and stops extensions;
- the management API (`/v0/management`), which the panel and the app use.

Every provider is an extension: a separate program that core starts and talks to over a local
socket. The pages and settings an extension adds to the panel come in its package. An install
ships its provider extensions as bundled packages, a registry on disk that needs no network (the
container image bundles all of them). A first start installs none of them: the registry says what
each package is (`title`, `summary`, `kind`, `tier`), and a person or setup installs what they use.
Core writes its own first config, with a management key, and refuses a config without one.

Extensions are separate programs, not libraries loaded into core, so that a crash cannot take core
down and an extension need not be written in Go. Code that many providers need, such as the
client formats and server-sent events, is in Go libraries (`next/format`) that extensions build in;
one extension never depends on another at run time.

Core is written in Go, with the standard library and two outside modules (`next/go.mod`). The app
is Tauri 2, so its native side is Rust.

## Inside core

- **One internal model.** Each client format is translated to and from one model of a request (an
  ordered list of typed items) and its reply (a stream of typed events), following Vercel's
  Language Model Specification and Open Responses. A request whose format the upstream already
  speaks passes through unchanged. A translation reports what it carried over only approximately
  or had to drop; the config's `drop_policy` says whether a drop is sent and logged or refused.
  Fields the model has no place for are kept with the item, so they survive a round trip into the
  same format.
- **One place decides retries.** For each request core picks an account, reads the extension's
  typed outcome, and decides whether to try another account. Once a reply has started reaching the
  client nothing is retried. Extensions do not retry on their own, with two single-resend
  exceptions before any content reaches the client: the Codex extension drops encrypted reasoning
  items and resends once when the upstream returns HTTP 400 refusing foreign encrypted reasoning
  (`invalid_encrypted_content`, or a message that the encrypted content could not be verified or
  decrypted) and `drop_policy` is not `reject` (a WebSocket turn has no such resend: the refusal
  fails the turn and the socket is given up); the Gemini extension resends a
  `GenerateContent` request once when the upstream reply ends with `MALFORMED_FUNCTION_CALL` before
  any content is sent. Blocks after a refusal are kept per credential, model or endpoint, and an
  earlier request's failure never overrides a later success.
- **Credentials are versioned.** Core stores each credential with a version, and the provider
  extension does the login and renewal. One renewal runs per credential at a time, and a renewal
  of a version that has since changed is refused. An extension receives only the credentials of
  its own accounts.
- **Config changes are all or nothing.** A reload reads, checks and prepares the new config, then
  switches to it in one step; requests already running keep the config they started with, and a
  reload that fails leaves the running config in place. Management shows both the newest config
  core has seen and the one serving.
- **Streams are bounded.** Core reads the extension's next event only once the client has taken
  the last one, so a slow client slows the extension down rather than filling memory. A call ends
  in exactly one `finish` or `error`, and a stream that just stops is an error, never a success. A
  running request has no overall time limit; only connection setup and named liveness checks
  (such as the Codex WebSocket's silence limit) end a connection by time.
- **Usage is a bounded record.** Core keeps one event per request in memory, within a size and age
  limit, and reading it never removes anything. A program that pages through it is told when it
  missed events. Usage is kept in plain files, not in a database: one JSON line per request in a
  file for each UTC day, with a summary file for each closed day. Closed-day files are not
  compressed in this version, which is worth revisiting if the volume grows.

Details: [routing](routing.md), [reloads](config.md#reloading), [packages](extensions.md),
[usage](usage-and-logs.md) and the [management API](management-api.md).

## The extension model

- **Packages.** An extension ships as a package: one manifest with a publisher-qualified ID
  (`publisher/name`), a program per platform, and optional pages and a settings schema. The
  package, the manifest format, the protocol, the pages and the settings each have their own
  version, so each can change without the others.
- **States.** A package is installed (its files are on disk), enabled (core runs it) and
  configured (it has accounts). Removing a package keeps its accounts. A running extension is
  starting, active, idle, draining, crashed or quarantined.
- **Activation.** Core checks a package's archive against the digest its registry lists before
  unpacking it, and checks the files again each time it starts the program. A new version starts
  and answers its handshake before core switches to it; the old version then finishes its
  requests within a deadline. If the new one fails, the old one keeps serving. A program that
  keeps crashing is restarted with a growing delay, then quarantined. `-safe-mode` starts core with
  no extensions.
- **Lazy start.** An extension's program runs only while it is needed: core stops it after a
  configurable idle time and starts it again for the next request.
- **The protocol.** Each call is one request followed by a stream of events over its own local
  socket connection, with no round trip per token.
- **Trust.** A registry earns trust by what it lists: releases pinned by digest. Core installs only
  from the registries its config names, plus the bundled one; there is no default network
  registry. Core starts each program with a cleared environment and gives it only its own accounts'
  credentials. A separate process keeps a crash from spreading; it is not a sandbox, and the
  panel's Extensions page says so: an extension can reach whatever its program can reach.

Contracts: the [extension protocol](../next/protocol/README.md) (manifest, registry, handshake,
calls, versioning) and its [tutorial](../next/protocol/TUTORIAL.md).

## The UI kit and themes

The web panel and the desktop app are two builds of one UI kit in `next/ui`. Each build implements
the kit's host interface (the browser's or the app's way to call core) and picks its pages. The
panel is minimal: it shows what core does, and core serves it at `/panel/`. The app adds its own
pages and tools. Both read the same theme files and put shared pages in the same places. The kit
uses Preact and Bun to keep the panel small; the [UI kit README](../next/ui/README.md) gives the
measurements and the panel's design is in [panel.md](panel.md).

Themes reach far into the UI, within limits that keep it safe:

- **Themes are data.** A theme is a JSON file of tokens (colour, type, spacing, density, shape,
  motion) and layout options (such as where the navigation sits, or lists as tables or cards). The
  UI never runs anything from it.
- **Custom CSS, with a safe mode.** A theme, or the user, can add CSS on top. Safe mode turns all
  custom CSS off, for when it has broken the UI.

Contract: [themes](../next/ui/themes/README.md).

**Extension pages** are shown in a sandboxed frame from a separate site: another host name, not
just another port, because a browser sends a host's cookies to every port. A page talks to the
panel only through a message channel, asks only for what its manifest declares, and never
receives the management key. Settings are forms the panel draws from the extension's schema.
Contract: [extension pages](../next/ui/extension-pages/README.md).

## Where a pluxy keeps its files

A pluxy run by a person's user account keeps all its files in one folder, pluxy's home:
`$XDG_DATA_HOME/pluxy` (`~/.local/share/pluxy`) on Linux and other Unix systems,
`~/Library/Application Support/pluxy` on macOS and `%LOCALAPPDATA%\pluxy` on Windows, or the
folder `PLUXY_HOME` names. Every `pluxy` command uses it when it is given no config file and the
folder it runs in has no `config.json`, so the command works from any folder, and a background
service, the app, the panel and a terminal can all find the same core.

- **One folder, not a config folder and a data folder.** Core keeps its credentials, packages, logs
  and state beside its config file, and a container keeps them in one volume. Per-user programs
  that keep keys and data together do the same: Syncthing has one home folder (`STHOMEDIR`), T3
  Code `~/.t3` (`T3CODE_HOME`), Ollama `~/.ollama`. The GitHub CLI splits its files four ways, but
  its config folder holds settings a person edits; pluxy's config holds keys, and core rewrites it.
- **The data folder.** The XDG Base Directory Specification puts user data in `XDG_DATA_HOME`, and
  state that is "not important or portable enough", such as logs and history, in
  `XDG_STATE_HOME`. Credentials are the files pluxy can least afford to lose, so the folder goes
  with them. Tailscale's daemon does the same when it runs without root
  (`$XDG_DATA_HOME/tailscale`), and the desktop app already keeps its own core in its data folder.
  Syncthing moved its home to `XDG_STATE_HOME` in v1.27, a choice made for a program whose data it
  can sync again.
- **macOS and Windows.** On macOS, `~/Library/Application Support` is where Go's
  `os.UserConfigDir`, Syncthing and the desktop app put such files. On Windows the local
  application data folder keeps keys off a roaming profile, which is copied to a server and to the
  person's other computers; Syncthing uses it too, and so does the desktop app for every file that
  holds a key.
- **One variable.** `PLUXY_HOME` names the whole folder, as `STHOMEDIR` and `T3CODE_HOME` do. It
  must be an absolute path: the specification has a program ignore a relative one, and a relative
  one would make each folder pluxy runs in a different home.
- **Nothing moves, and no second pluxy appears unasked.** A `config.json` in the current folder
  still comes first, and a core started with `-config` keeps its files beside it, so existing
  setups and the desktop app's own core run as before; Syncthing keeps using an existing old
  folder in the same way. pluxy copies nothing into its home. While the desktop app keeps a core
  of its own and pluxy's home has no config, a plain `pluxy serve` refuses to write a first config
  there and names the choice, since two pluxys with separate keys and accounts would look like one
  that lost its accounts. Moving the app's own core into pluxy's home is a separate change.

Sources: the [XDG Base Directory Specification](https://specifications.freedesktop.org/basedir/latest/);
Syncthing's [configuration](https://docs.syncthing.net/users/config.html) and
[locations](https://github.com/syncthing/syncthing/blob/main/lib/locations/locations.go);
Tailscale's [state path for a user](https://github.com/tailscale/tailscale/blob/main/paths/paths_unix.go);
Ollama's [FAQ](https://github.com/ollama/ollama/blob/main/docs/faq.mdx); T3 Code's
[background service](https://github.com/pingdotgg/t3code/blob/main/docs/user/background-service.md);
the GitHub CLI's [environment](https://cli.github.com/manual/gh_help_environment); Go's
[`os` package](https://pkg.go.dev/os). Details: [pluxy's home](config.md#pluxys-home).

## The desktop app

The app reaches core in one of three ways: it runs its own core, connects to a core already
running on the same computer, or connects to a remote core over HTTPS. Closing the window to the
tray destroys the webviews, so the app holds little memory while it sits there. Every app command
is declared and granted only to the app's own webview, and each extension page runs in a webview
of its own with no grants.

Packages installed in the app itself, not in core, come in two kinds. A web package has pages
only, and reads the user's files only through folders or files the user grants it, which can be
revoked. A native tool is a program the app starts when needed and stops when idle, like core's
providers. It has full access to the user's account, and the app says so in a confirmation before
enabling it.

Linux is the only platform the app supports today. Details: the [app README](../next/app/README.md)
and its [package contract](../next/app/app-host/README.md).

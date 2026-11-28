# Command-line reference

`pluxy` is one program. It runs core, writes a first config, and, with every other command, talks to
a core that is already running through its [management API](management-api.md). This page lists every
command and flag, grouped by topic. `pluxy help` lists the commands, `pluxy help <group>` (such as
`pluxy help account`) lists one group, `pluxy <group> -h` does the same, and `pluxy <command> -h`
prints that command's flags and exits 0.

- [Which core a command calls](#which-core-a-command-calls)
- [Output](#output), [Signing in](#signing-in) and [Exit codes](#exit-codes)
- [Running core](#running-core): `serve`, `init`, `-version`, `help`
- [Status and control](#status-and-control): `status`, `reload`, `requests`, `request cancel`, `config show`, `settings`
- [Setup and clients](#setup-and-clients): `setup`, `setup scan`, `setup apply`, `setup done`, `client show`, `import cpa`
- [Accounts](#accounts): `account ...`
- [Routing and models](#routing-and-models): `route`, `chain ...`, `model ...`
- [Extensions](#extensions): `ext ...`
- [Usage](#usage): `usage`, `usage events`, `usage export`
- [Logs](#logs): `logs ...`
- [Keys](#keys): `key ...`
- [Themes](#themes): `theme ...`

## Which core a command calls

Every command except `serve`, `init`, `help` and `key management` is a client of a running core. These
flags work on all of them:

| Flag | What it does |
| --- | --- |
| `--url <address>` | Core's address, such as `http://127.0.0.1:8317`. It must start with `http://` or `https://` and name a host, or the command exits 2. |
| `--key <key>` | The management key. |
| `--config <file>` | The config file to read core's address and management key from. |
| `--json` | Print the API's reply body unchanged on standard output, and nothing else (`{}` when core replied with an empty body). Errors go to standard error as the API's error object, flag and usage errors included. |
| `--verbose` | Say on standard error which core was called and where its address and key came from. |

Flags may come before, between or after a command's arguments, and take one dash or two (`-json` and
`--json` are the same).

**Which core.** The address and the management key each come from the first of:

1. `--url` and `--key`;
2. the environment variables `PLUXY_URL` and `PLUXY_MANAGEMENT_KEY`;
3. the config file: `--config <file>`, else `config.json` in the current folder when there is one,
   else `config.json` in pluxy's home ([config.md](config.md#which-configjson-is-used)). From it come
   its `listen` address (on this machine, when it listens on every interface) and its
   `management_key`. A core that serves TLS with its own certificate is called with that certificate
   trusted.

With no config file anywhere, the error names each place pluxy looked in and what to run (exit 1; exit
5 for `pluxy key management`). When the desktop app keeps a core of its own, the error also names its
config and the two ways on. The client commands read that config only when given it with `--config`.

**A key is not sent where a folder says.** When the key comes from `--key` or `PLUXY_MANAGEMENT_KEY`
and the address only from `config.json` in the current folder, the command compares that file's
`management_key` with the key it was given. When the file loads and has a usable address and the keys
differ, the command sends nothing and exits 2:

```text
pluxy: not sending the management key from <where> to <address>, the address in this folder's config.json, which holds a different key
```

A stray file in a folder cannot steer your key to another machine this way. A file with no
`management_key`, or whose `listen` has port 0, fails first with its own error (exit 1) and sends
nothing either. When the current folder is pluxy's home, its `config.json` counts as the folder's and
is checked. These are not refused: the same key in the file, `--config` (a deliberate choice of file,
so the key goes to its address), `--url` and `PLUXY_URL`, and pluxy's home's `config.json` when the
current folder has none. To call the pluxy the file names, leave out the key the message names, so the
key in that `config.json` is used; to call another, add `--url <its address>`.

**The management key.** `pluxy key management` calls no core: it says where the key the commands use
came from (the `management_key` of the config file read, `PLUXY_MANAGEMENT_KEY` or `--key`), and with
`--show-key` prints the key alone on standard output. With no key to find, it says why: each place it
looked, or the `--config` file that is not there (exit 5), or a config core refuses too, with core's
reason (exit 1). It also exits 1 when the key or its place cannot be written to standard output. Its
`--json` output is `{"from": "config", "config": "<path>"}` (`from` is `config`, `--key` or
`PLUXY_MANAGEMENT_KEY`, and `config` is there for `config`), with `key` added by `--show-key`.
[`pluxy key rotate-management`](#pluxy-key-rotate-management) replaces the key.

## Output

Output is for people by default: aligned columns, one thing per line, and no colour. Help asked for
(`help`, `-h`, `--help`) goes to standard output and exits 0. Errors go to standard error: a sentence,
or with `--json` the API's error object. When standard output is a terminal, characters a terminal
would act on (escape sequences, other control and invisible characters) are shown escaped, such as
`\x1b`, so text that came from a client or an upstream cannot change what is on your screen.

Nothing asks a question unless standard input is a terminal. Commands that ask take `--yes` to answer
yes.

## Signing in

`pluxy account add` and `pluxy account login` start a sign-in and follow it to its end:

- The address to open or the code to type is printed on standard error.
- Core is read again every 2 seconds until the sign-in ends. A read that fails is tried three times in
  a row, and then the command gives up and cancels the sign-in.
- Each value the sign-in asks for is sent to it. A value that `--input name=value` did not give is read
  from standard input, one line for each value, in the order the sign-in asks for them; on a terminal
  each is asked for by its label. A blank line leaves an optional field out. `account login` also
  leaves out a non-secret value the account's settings already hold.
- A field that takes a JSON file (Vertex AI's service account key) is read until the document is whole.
  A value over 1 MiB is refused rather than sent cut short; any other value is one line, whatever it
  starts with.
- A secret's value is never taken from `--input`, where the shell's history and the process list keep
  it, and no value is printed back. A terminal keeps a secret or a JSON file off the screen until the
  whole value is read.
- At the end of standard input, the values read are sent if every required field has one. Piped input
  answers each prompt once, since its next line may be the next prompt's value; the command waits a
  minute for a prompt to move on, then cancels the sign-in.
- With `--json`, nothing is read from a terminal: the values come from `--input` or a pipe, or the
  command exits 2 saying what it needs. A sign-in that needs a value it did not get is cancelled in
  core with exit code 2; so is one that `--no-wait` leaves waiting on a prompt.
- A browser sign-in is the exception to that cancelling, since the browser's return may still reach its
  extension: a piped address that is refused does not end it. When standard input ends at a prompt
  that wants a value, the command says once that nothing more can be pasted, and goes on following the
  sign-in until it finishes, core ends it 15 minutes after it started (exit 1), or Ctrl-C cancels it.
- Ctrl-C, `SIGTERM` and `SIGHUP` cancel the sign-in and put the terminal back as it was.

## Exit codes

| Code | Meaning |
| --- | --- |
| 0 | Done. |
| 1 | The call failed (any error not listed below). |
| 2 | The command line or the request is invalid: an unknown command or flag, a missing argument, a malformed `--url`, or a 400 from core. Also a refusal to send your key to a folder's address. |
| 3 | No core answered at the address. |
| 4 | Refused: no key, a wrong key, a call from another machine, or a lockout (401 or 403). |
| 5 | Not found: a 404, `route` for a model no account serves, or `key management` with no config file to read. |
| 6 | Core refused a change (409 or 422 from a command that changes something). Nothing was changed. |
| 7 | Waiting for a person: a sign-in needs a browser or a code and `--no-wait` was given. |

## Running core

### `pluxy serve`

Runs core. `pluxy` with no command, or with a flag first (`pluxy -config config.json`), does the same.
On a first start with no config, it writes one ([config.md](config.md#a-first-config)); the one
exception is when pluxy's home has no config and the desktop app keeps a core of its own, where it
writes nothing and exits 1 with the two ways on. When no `-config` names the file, core logs which
config file it found. It logs the build line that `pluxy -version` prints when it starts. It prints the address it listens on and the address of the extension pages on
standard error. `SIGHUP` reloads the config; Ctrl-C or `SIGTERM` stops core, after finishing running
requests (at most 10 seconds).

| Flag | Default | What it does |
| --- | --- | --- |
| `-config <file>` | `config.json` in the current folder when there is one, else in pluxy's home | The config file. |
| `-safe-mode` | off | Starts no extension: client requests fail, the management API works. |
| `-watch-interval <duration>` | `1s` | How often core looks for changes to the config file. `0` turns the watch off. |
| `-bundled <index.json>` | `bundled/index.json` beside the program, when it exists | The index of the registry of bundled packages. With neither the flag nor that file, core has no bundled registry: setup has no providers to offer and **Extensions** has nothing to install, unless the config lists a registry under `packages.registries`. |
| `-listen-fd <n>` | none | Serves on the listening TCP socket inherited at this file descriptor (3 or higher; a TCP socket) instead of binding `listen`. For a program that starts core and keeps the socket, such as the desktop app. The config must still set `listen`. The program keeps its own copy of the socket, so no other program can take the port while core stops or after it exits. |
| `-version` | | Prints the build's version, commit and build date, then exits. |

```bash
pluxy serve -config /srv/pluxy/config.json
```

### `pluxy init`

Writes a first config without starting core: it listens on `127.0.0.1`, with one new client key and a
new management key. It never replaces a file, and it refuses a `-config` path that ends in `.yaml`
(the first config is JSON).

| Flag | Default | What it does |
| --- | --- | --- |
| `-config <file>` | `config.json` in pluxy's home; or the one in the current folder when there is one (init then says it exists) | Where to write the config. |
| `-port <n>` | `8317`, or a free port when 8317 is taken | The port core listens on. `0` is a free port. |

### `pluxy -version`

Prints one line, `pluxy <version> (<commit>, <date>)`, and exits. A build that recorded only one of
them prints that one (`pluxy <version> (<commit>)` or `pluxy <version> (built <date>)`); one that
recorded neither (made with `go run`, with `-buildvcs=false` or outside a git checkout) says so:
`pluxy dev (no commit or build date recorded)`. It does not read the config.

### `pluxy help`

`pluxy help` lists the commands and the folder pluxy's home is. `pluxy help <name>` lists the
commands whose names start with that text, so `pluxy help acc` lists the account commands; a name that
starts none exits 2.

## Status and control

### `pluxy status`

Shows core's state: its build, the config revision it serves (and a newer revision it refused, with
the reason), whether first-run setup is still to do or safe mode is on, and each extension's state.

### `pluxy reload`

Reloads the config file now and prints the active revision. When core refuses the file, it exits 6,
says why, and keeps the running config ([config.md](config.md#reloading)).

### `pluxy requests`

Lists the client requests core is serving now, oldest first: age, model, account, client key ID,
endpoint and trace ID.

### `pluxy request cancel <trace>`

Cancels a request core is serving, by its trace ID (the `X-CPA-TRACE-ID` header of its reply, or the
`TRACE` column of `pluxy requests`). The client gets a `cancelled` error. Cancelling is best effort: a
reply core had already gathered may still reach the client whole. Exit 5 when no request has that
trace ID, including one that just ended.

### `pluxy config show`

Prints the config file core runs on, with every secret replaced by a placeholder.

### `pluxy settings`

Prints the basic and routing settings by name. It only reads them; change them in the panel or the
app, or in the config file ([config.md](config.md)).

## Setup and clients

### `pluxy setup`

Shows what core found on this machine (client tools, existing sign-ins, a CLIProxyAPI setup), the
packages it suggests, and, on a terminal, offers to install and enable the suggested ones.

| Flag | What it does |
| --- | --- |
| `--yes` | Applies the suggested picks without asking. |

### `pluxy setup scan`

Looks again for client tools, sign-ins and existing setups. It returns at once; run `pluxy setup` to
see the result.

### `pluxy setup apply`

Installs and enables setup picks, and prints one result per pick. One failure does not undo the
others.

| Flag | What it does |
| --- | --- |
| `--pick <id>` | A package ID to install and enable. May be repeated. |
| `--yes` | Applies the suggested picks without asking, when `--pick` is not given. Without `--pick` or `--yes`, standard input must be a terminal. |

```bash
pluxy setup apply --pick pluxy/claude --pick pluxy/codex
```

### `pluxy setup done`

Marks first-run setup as done.

### `pluxy client show <tool>`

Prints what to paste into a coding tool's config to point it at core: the files, the address, a
client key placeholder and the model the tool starts with ([clients.md](clients.md)). `<tool>` is
`claude-code`, `codex`, `opencode`, `pi` or `omp`; other names for them (`claude`, `codex-cli`,
`oh-my-pi`, and a setup package's ID such as `pluxy/pi-setup`) work too, in any letter case. The
placeholder `<client-key>` stands for a client key of yours: pluxy never prints a stored key.

| Flag | What it does |
| --- | --- |
| `--key-id <id>` | A client key ID from `pluxy key list`: the placeholder then names that key. |
| `--model <name>` | Model the tool starts with, one from `pluxy model list` (at most 240 characters, no control characters). Default: the best match among the models core serves (a Claude model for Claude Code, a `codex` or `gpt-` one for Codex, else the first chat model), or `<model-id>` when core serves none. |

```bash
pluxy client show codex
```

### `pluxy import cpa`

`pluxy import cpa <config.yaml>` brings a CLIProxyAPI setup (its `config.yaml` and its auth folder) into
the running core. It prints three lists: what was brought, what was changed on the way (with why, and
how to keep the old behavior), and what was not brought (with why), then the provider packages the
imported accounts need that are not installed (install them with `pluxy ext install`). Core reads
both files on its own machine. [moving-from-cpa.md](moving-from-cpa.md) walks through a move.

| Flag | What it does |
| --- | --- |
| `--auth-dir <dir>` | The auth folder. Default: the config's `oauth.auth-dir`. |
| `--dry-run` | Prints the same lists and writes nothing. |

```bash
pluxy import cpa ~/.cli-proxy-api/config.yaml --dry-run
```

## Accounts

An account is a signed-in subscription or an API key that serves requests. Accounts are named by ID.
In these commands `<provider>` is an extension's ID (`pluxy/claude`) or its name when that is
unambiguous (`claude`).

### `pluxy account list`

Lists every account: ID, public ID, label, extension, source (`config` for an account set in the
config file, `login` for one added by signing in), health, the time its block ends, and why.

### `pluxy account methods`

Lists how each running extension can add an account: its sign-in methods and what each asks for.

### `pluxy account add <provider>`

Adds an account by signing in. It prints the address to open or the code to type, sends each value
the sign-in asks for, and follows the sign-in to its end. A value that `--input` did not give is read
from standard input, one line for each, in the order the sign-in asks for them; on a terminal each is
asked for by its label. A secret is never taken from `--input`: it is read from a prompt or standard
input, so the shell's history does not keep it. A browser sign-in that nobody finishes ends after 15
minutes. With `--json` on a terminal, nothing is asked: values come from `--input` or a pipe, or the
command exits 2 saying what it needs.

| Flag | What it does |
| --- | --- |
| `--id <id>` | The account's ID. Default: `account-` and 12 hex digits. 1 to 128 letters, digits, dots, underscores or hyphens, starting with a letter or digit and not ending with a dot. |
| `--method <name>` | The sign-in method. Default: the extension's only one. |
| `--input <name=value>` | A value the sign-in asks for, which is not a secret. May be repeated. |
| `--no-wait` | Prints the sign-in's address or code and stops, leaving the sign-in to finish in core. Exits 7, or 2 (and cancels the sign-in) when it waits for a value you did not give. |

```bash
pluxy account add claude --method subscription
```

### `pluxy account login <id>`

Signs in to an existing account again, followed as `account add` is. Same flags as `account add` except
`--id`. A non-secret value the account's settings already hold is left out.

### `pluxy account remove <id>`

Removes an account and its credential.

### `pluxy account refresh <id>`

Renews the account's credential now.

### `pluxy account set <id> <name=value>...`

Changes an account. `name` is one of `disabled`, `priority`, `weight`, `disable_cooling`,
`max_concurrency`, `request_retry`, `prefix`, `models`, `excluded_models`, `model_alias`, `proxy_url`,
`headers`, `timeouts` or `settings` (what each does: [config.md](config.md#accounts)). A value is JSON,
except `prefix` and `proxy_url`, which take their text as it is. `null` puts a field other than
`disabled` back to its default.

```bash
pluxy account set work-codex priority=5 weight=2
pluxy account set work-codex models='["gpt-5","gpt-5-mini"]'
pluxy account set work-codex proxy_url=http://proxy.example:3128
```

### `pluxy account enable <id>` and `pluxy account disable <id>`

Turn an account on or off. A disabled account serves no request and is not called.

### `pluxy account clear <id>`

Forgets the cooldowns routing holds against the account. A ban stays.

### `pluxy account test <id>`

Sends one small request through the account and prints the reply or the failure. It changes nothing.

| Flag | What it does |
| --- | --- |
| `--model <name>` | The model to ask. Default: one the account serves. |

### `pluxy account quota <id>`

Shows the account's usage windows as its provider reports them: the plan, each limit's percent, its
amounts, and when it resets.

### `pluxy account models <id>`

Lists the models the account serves: the account's own `models` list when it has one, else the list
routing keeps, else a list read from its extension. For each: ID, the names clients use, owner,
context length, longest output and whether it is excluded.

### `pluxy account import <file>`

Adds an account from a credential file. A Google service account key (a JSON object with a
`private_key` and a `project_id`) becomes a Vertex AI account; any other file is read as a CLIProxyAPI
credential file, and its name gives the account's ID. `-` reads a service account key from standard
input. A file larger than 1 MiB (exit 1), one that is not a regular file (exit 2), or a credential file
whose name does not end in `.json` (exit 2) is refused.

## Routing and models

### `pluxy route <model>`

Shows what a request for the model would do now, without sending one: the accounts in the order it
would try them, and the accounts it would leave out and why ([routing.md](routing.md)). For a model
with a fallback chain it shows the chain and each account's step. Exit 5 when no account serves the
model.

### `pluxy chain list`

Lists the fallback chains.

### `pluxy chain set <name> <step>...`

Sets the fallback chain for a model name, with at most 8 steps. Core refuses (exit 6) a step that names
an account that does not exist or does not serve the model. A step is `model[@provider][:account,account]`: the model to
ask, optionally the provider (an extension ID) that serves it, and optionally the account IDs that
serve it. A model name that has a colon ends with `@`, as in `llama3:8b@`. What follows the last `@`
counts as a provider only when it is an extension ID, so `claude-opus-4-7@20260416` is a model name.

```bash
pluxy chain set best claude-opus-4-7@pluxy/claude gpt-5@pluxy/codex:work-codex
```

### `pluxy chain remove <name>`

Removes a chain: the name goes back to being served by the accounts of one provider.

### `pluxy model list`

Lists every model clients can ask for: its name, extension, upstream name, context length, longest
output, capabilities, where its details come from, and the accounts that serve it.

### `pluxy model refresh`

Asks providers for their model lists again now, and prints per account how many models it got. A read
that fails keeps the last good list.

| Flag | What it does |
| --- | --- |
| `--account <id>` | Refreshes only this account. Default: every account. |

## Extensions

An extension is a program that connects core to one kind of upstream. A package is an extension
installed with its files ([extensions.md](extensions.md)). A package ID is `publisher/name`, such as
`pluxy/claude`.

### `pluxy ext list`

Lists the installed packages: ID, version, whether it is enabled, its state and its registry.

### `pluxy ext available`

Lists what each registry offers: package, title, kind, tier and versions. With no registry, it says
how to make one.

### `pluxy ext install <id>`

Installs a package and enables it.

| Flag | What it does |
| --- | --- |
| `--version <v>` | The release to install. Default: the newest this core can run. |
| `--registry <name>` | The registry to install from. Default: the first one that has the package. |

```bash
pluxy ext install pluxy/gemini
```

### `pluxy ext enable <id>` and `pluxy ext disable <id>`

Start or stop routing to an installed package.

### `pluxy ext remove <id>`

Removes a package's files once its requests have finished. Its accounts stay.

## Usage

Usage commands read the [usage record](usage-and-logs.md#usage-statistics). Filter flags work the same
on all three.

| Filter flag | What it does |
| --- | --- |
| `--since <time>` | From this time (RFC 3339) or this long ago (such as `24h` or `7d`). |
| `--until <time>` | Up to this time (RFC 3339). |
| `--model <name>` | Only this model. |
| `--account <id>` | Only this account. |
| `--provider <id>` | Only this provider (extension). |
| `--client-key <id>` | Only this client key ID. |
| `--failed <true or false>` | `usage events` and `usage export` only: only failed requests (`true`) or only successful ones (`false`). |

### `pluxy usage`

Prints the number of requests, failures, and input and output tokens, then a breakdown.

| Flag | What it does |
| --- | --- |
| `--by <what>` | The breakdown: `model`, `account`, `key` or `provider`. Default: `model`. |

```bash
pluxy usage --since 24h --by account
```

### `pluxy usage events`

Lists recorded requests: sequence number, time, model, account, client key, status, number of accounts
tried (TRIES), why routing chose the account (WHY, the event's `why`; see
[Usage and logs](usage-and-logs.md#what-is-recorded)), latency and total tokens.

| Flag | What it does |
| --- | --- |
| `--after <seq>` | Oldest first: only events with a sequence number above this. |
| `--before <seq>` | Newest first: only events below this. No value or `0` is the latest page. Not together with `--after`. |
| `--limit <n>` | At most this many events. Default: core's, 100; above 1000 is refused (exit 2). |

### `pluxy usage export`

Writes the recorded requests as CSV on standard output, newest first, as the panel's Export does. It
refuses `--json`: use `pluxy usage events --json` for JSON.

## Logs

Core's log files are described in [usage-and-logs.md](usage-and-logs.md#log-files).

### `pluxy logs`

Prints lines of core's log.

| Flag | What it does |
| --- | --- |
| `--after <time>` | Only lines after this time, in Unix seconds. |
| `--limit <n>` | At most this many lines. Default: core's, 1000; above 10000 is refused (exit 2). |

### `pluxy logs errors [name]`

Lists the error logs (name, size in bytes, modified time). With a name, prints that log's text.

### `pluxy logs request <id>`

Prints the request log of the request with that trace ID. When `logs.request_log` is off, only requests
that failed have one, and the error says so.

### `pluxy logs clear`

Empties `main.log` and deletes its rotated files. Request logs and error logs stay.

## Keys

### `pluxy key list`

Lists the client keys: each one's ID (the same after a restart) and its masked form.

### `pluxy key add`

Adds a new client key and prints it once on standard output. Core cannot show it again.

### `pluxy key remove <id>`

Removes a client key. Core refuses to remove the last one (exit 6).

### `pluxy key management`

Says where the management key the commands use came from: the config file, `PLUXY_MANAGEMENT_KEY` or
`--key`. It calls no core.

| Flag | What it does |
| --- | --- |
| `--show-key` | Prints the key itself on standard output. |

### `pluxy key rotate-management`

Replaces the management key with a new one, which core writes to its config file. The old key works
until the new one is in use. The command asks first on a terminal.

Without `--show-key`, it works only with a pluxy at a loopback address on this machine, and only when
the config file it read is the very file that pluxy runs on (a symbolic link to it counts; a copy does
not). A pluxy at any other address needs `--show-key`. Without `--show-key` it also needs the address
and the key to come from a config file (not `--key`, `PLUXY_MANAGEMENT_KEY` or `--url`), and `--json`
needs `--show-key`; otherwise it exits 2 before any call. When the new key is in use it exits 0 and
ends by naming what needs the new key: the web panel, the desktop app, `PLUXY_MANAGEMENT_KEY` and
`--key`. When you decline the question, it changes nothing and exits 0.

The command changes the key in two steps, so that a lost reply never leaves you without a key that
works:

1. It asks core to stage a new key. Core keeps that key in memory only and names the config file it
   runs on. The old key still works, and nothing is written yet.
2. It makes sure you can get the new key: with `--show-key` it prints it (a failed write ends the
   command here), and without it checks that the config file it read is core's own file. Only then
   does it ask core to commit: core writes the key to its config file, and the old key stops working.

A failure before the commit changes nothing, and the command says the old key still works. When the
commit's reply is lost, the command asks once more: it exits 0 when the new key is in use, and
otherwise fails and says which key to try (the printed one with `--show-key`, else the key in the
config file). Two rotations at the same time are not covered: the later key replaces the earlier one.

| Flag | What it does |
| --- | --- |
| `--show-key` | Prints the new key alone on standard output, before it is put into use. |
| `--yes` | Rotates without asking. Needed when standard input is not a terminal. |

## Themes

A theme is a set of colors for the panel and the app. A theme ID is `publisher/name`.

### `pluxy theme list`

Shows the selected theme and the stored themes: ID, name and size in bytes.

### `pluxy theme show <id>`

Prints a stored theme's JSON.

### `pluxy theme add <file>`

Stores a theme JSON file and prints its ID.

| Flag | What it does |
| --- | --- |
| `--id <publisher/name>` | The theme's ID. Default: for a pluxy theme file, the `id` it holds when that is a valid `publisher/name`; for any other theme file, `t3/` and its `id` or a name made from its `name`; failing both, `t3/` and a name made from the file's name. |

### `pluxy theme remove <id>`

Removes a stored theme.

### `pluxy theme set <id>`

Selects a theme.

### `pluxy theme clear`

Clears the selected theme.

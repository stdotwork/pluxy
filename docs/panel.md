# The panel

The new core in `next/` serves a web panel at `/panel/` while its management API is on. The panel
is the browser build of the UI kit in `next/ui`; the desktop app in `next/app` is its second
build. This page records the decisions behind the panel's pages and why they were made. How to
build the panel, add a page and use the kit is in [`next/ui/README.md`](../next/ui/README.md).

## What parity with the old panel means

The new panel replaced the old one (the React/Vite panel formerly in `extensions/panel`, now
removed). Parity meant that it looked and felt like
the old panel: where a feature was kept, it kept the old page, section, setting and button names,
in a similar place, so someone who knew the old panel found each thing where they expected it. A
label changed only where it named something this core does differently, such as a file, a relay
or a mislabelled setting, or where [docs/terms.md](terms.md), the repo's list of words for the UI,
gives another word: an account's "Renewal time" reads "Subscription ends".

Parity did not mean the same logic. The new core does things its own way, and the panel shows
what it does. Old features were carried over only where they were worth keeping. Each one left out
has a reason, listed page by page in
[What each page leaves out, and why](../next/ui/README.md#what-each-page-leaves-out-and-why).

## A minimal panel

The panel shows what core itself does, in compact pages, and nothing core cannot do. A setting
gets a field only when core runs it; the rest of the config file stays editable as text in the
Config Panel's Source tab, and a setting gets its field when core starts running it.

Richer views and tools the panel does not have, such as the local usage of coding agents and the
tools that set those agents up, belong to the desktop app. The app shows the same core pages in
the same places, and adds its own pages after them. Both show the pages of the extensions that
run.

Nothing in the panel spends from an account behind a click, except an account's "Send a test
request", which says under it that it uses one request.

## Page names

| Page | Why this name |
| --- | --- |
| Home | The landing page, in place of the old panel's Dashboard: it answers whether the person's coding tools can be served, so it is named for where a person starts, not for a dashboard of figures. Its ID stays `status`, from the earlier Status page, so the address `#/status` and theme rules for `data-page="status"` keep working. |
| Accounts | The old Auth Files and AI Providers pages in one, as the tab "All accounts". This core has one kind of account, whether it comes from the config or from a sign-in, so the list groups accounts by what they need from the person (Needs you, Waiting, Serving, Turned off), not by where they come from. |
| An account's page | Replaced the old Auth Files page's details. `#/accounts/<id>` opens it, under All accounts, with the account's name in the header strip; it shows the account's limits, what it did this week and the ways to change it. |
| Add Account | Replaced OAuth Login. It adds API keys as well as sign-ins, from the login methods each running extension offers, so "OAuth" no longer describes it. |
| System | Replaced Management Center Info. "Management Center" was the upstream panel's product name. |
| Routing, Config Panel | The old panel's names: these pages do the same job as the old ones did. |
| Logs | Replaced the old panel's Logs Viewer. It holds two things, pluxy's own log and the full logs of failed requests, and "viewer" named neither. |
| (Quota Management) | Gone: every account's row on All accounts shows its two shortest limits, and its page shows every limit. The old address `#/quota` opens All accounts. |
| Requests, Usage | The two tabs of Activity; see [The Activity page](#the-activity-page). Requests keeps the ID `usage` of the page it replaced, so `#/usage` and `data-page="usage"` still work; Usage is `activity/usage`. |
| Tools | A new page, named for what a person does there: connect a coding tool, copy the address for any other program, and make, name and delete the keys tools send. It sits under the Tools destination; in the desktop app its topics sit beside it. |
| Set up pluxy | A new page, named for what a person does there. The panel opens it by itself on a first start; afterwards it stays among the Settings topics and shows the same things as they are now. |

The navigation has five destinations: Home, Accounts (All accounts, with Routing as the tab
"Order & fallback", and each account's own page under All accounts), Activity (Requests and Usage), Tools (the Tools
page, and in the desktop app its client pages as topics) and Settings, which lists every other page as a topic,
the extensions' pages last. The old panel's groups (Operate, Gateway, Observe, Control) are gone.
Since every text goes through the string layer, renaming a page later is a
change of text; the page ID stays, because it is the page's address and theme hook.

## Settings

Settings lists its pages as topics beside the page, in three groups: This device (General, Themes and,
in the desktop app, its own pages), pluxy (Config, Extensions, Logs, System, Setup) and From
extensions. It opens on General, the topic a person changes most. Every topic has a tip that says
what it holds.

- **General** holds what is set for one window or browser: Appearance, Hide names and, in the panel,
  Sign out. Each applies and saves at once; there is no Save button. Appearance is only here, so a
  setting is not on two pages; when the theme that is on has one appearance, the row says so and
  links to Themes. Hide names is the wording for hiding names while sharing a screen; while it is
  on, the header strip shows a Names hidden button that turns it off. Sign out asks once, because
  without the management key a person cannot sign back in. The desktop app leaves Sign out off
  General, since it is on Where pluxy runs, beside the connection it ends.
- **Themes** says which things pluxy keeps (the theme that is on, shared by every window) and which
  stay on the device (layout, density, custom CSS). Its custom CSS switch is not called safe mode,
  because safe mode is pluxy running with no extensions, shown on System. A notice about the page
  shows one at a time, newest first. A theme file is checked for size before it is read.
- **Logs** has two tabs. **pluxy's log** follows the log as pluxy writes it: every 2 seconds while the
  page is visible and Follow new lines is on, which it is by default, as is Wrap lines. Find and Show
  (All lines, Warnings, Errors) filter the lines already loaded, and Download saves the lines shown, as
  pluxy wrote them: hiding names masks the screen only. The page keeps the latest 10,000 lines, so
  a very large log does not freeze the window, and says so. Clear log asks first and deletes the log
  file; failed requests stay. When pluxy writes no log file, the tab says so and one press on Turn
  on file logging turns the setting on. **Failed requests** lists the full logs pluxy keeps of its
  latest failed requests, and opens one beside the list. `#/logs/<trace ID>` opens that tab on the
  request with that trace ID, which is how Activity and a tool's error lead here; the Trace ID field
  does the same for an ID copied from a tool, after checking its shape. A request that succeeded has a
  log only while `observability.logs.request-log` is on, and one over 2 MiB can be neither shown
  nor downloaded here.
- **System** says which pluxy this is (its version, commit, build time, address, and whether the
  newest settings file is in use), lists the models its tools can ask for by provider with a Copy for
  each name, and gives the base URL each kind of program takes (the OpenAI, Anthropic and Gemini
  APIs, and Codex with an API key or signed in with ChatGPT). A pluxy started in safe mode says so at
  the top.

## Extensions

Extensions (Settings, pluxy) lists the programs that connect pluxy to each provider, as rows with the
provider's name, the package ID, the version and the state, and the extensions on offer to install
under them. A row opens its facts (where it was installed from, when, a checksum, and for each
running copy its process ID, the requests it is serving now and its restarts), the settings form its
schema draws, and two buttons. Turn off stops the program and its accounts stop taking requests until
Turn on; Remove deletes the program but keeps its accounts, which work again when it is installed
again, and it asks first. A pluxy started in safe mode says so at the top, because no extension runs
in it. The list on offer comes from the lists core's config names (`packages.registries`); a pluxy
with none says so and links to the settings file. A list that cannot be read is named in a notice
and does not stop the others. An extension that is still starting is read again until it runs.

## Where an API key added in the panel lives

An API key added on the Add Account page becomes an account in core's credential store: a folder
with one file per account, readable only by core's user and, on Windows, SYSTEM (`credentials_dir`, by default
`credentials` beside the config file). It is never written to `config.yaml`, and no management
reply shows it again.

Accounts set in the config are listed in the panel but changed only in the config. Keys already in
`config.yaml` keep working, and are edited in the Config Panel's Source tab. Keeping panel-added
keys out of the config file means core needs no structured write of config accounts for both
config layouts it reads, and a key is never shown after it is added.

## The Activity page

Activity has two tabs. Both read what core records, and both say so when core is told not to keep
a record (`observability.usage.usage-statistics-enabled` is off): they show one pane that links
to the setting.

**Requests** (`#/activity`, or `#/usage`) lists what core still keeps in memory, newest first,
under the day each request came on, 50 at a time. That is a day by default and at most 10,000
requests (`usage.max_age_ms` and `usage.max_events`), so the list has no time range; its end says
how far back it goes. It is read when the page opens, on Refresh and when a filter changes, not on
a timer. A row opens to why the request went where it did, in a sentence, what each attempt came
to, and its facts. The trace links to the request's log on the Logs page for a failed request,
and for a successful one only while `observability.logs.request-log` is on (the log of a
successful request is kept only then); otherwise it shows as text to copy. The filters are in the
address: `failed=1`, `provider`, `account` (the account's ID, which is what a request
records; a public ID is turned into it), `model` and `key`. `trace=<id>` opens one request, reading down
the record until it finds it. **Save as spreadsheet** writes the newest 10,000 matching requests,
with the columns `Time, Model, Provider, Account, Client key, Failed, Status, Latency, Tokens,
Trace ID`, newest first.

**Usage** (`#/activity/usage`) adds the requests up over 24 hours, 7 days or 30 days: the totals,
a chart with a bar per hour (24 hours) or per local day, and a breakdown by provider, model,
account or key. Core adds closed days from its day files (kept 90 days) and today from memory, so
the 7 and 30 days are real, though Requests holds less. `GET /usage` is asked for hourly steps and
the page adds them into the person's local days. When core could not write some requests to its
usage files (`write_errors` and `dropped` in `GET /usage`), Requests says how many.

The page is in the panel, not only in the desktop app, because core keeps the record and no other
page lists requests. It shows only what core records: tokens, with no prices or cost views, and the
app has none either. Home shows the last 7 days as one bar for each local day, and the ten latest requests; it has no
breakdown by model, account or key, which is what the Usage tab is for.

## Themes reach every page

A theme can restyle and rearrange every page, not only recolour it. So every new page follows
these rules (the [theme contract](../next/ui/themes/README.md) has the details):

- Every list uses the kit's list, so a theme's `list` layout option (`table` or `cards`) applies,
  and every list with details uses the kit's master and detail view.
- Every visible part a page adds carries a `data-part` hook that custom CSS can target.
- Colours come from tokens, never written into the page.
- A page's CSS sits beside the page, inside the `kit` cascade layer.
- A new hook, token or layout value goes into the theme contract and `kit/theme/contract.ts` in the
  same change. Adding one keeps `theme_version`; renaming or removing one raises it, so existing
  themes keep working as pages are added.

## Texts and locales

Every text the panel shows goes through the kit's string layer. Each page declares its English
texts beside it, under its own namespace, so pages built side by side never edit one shared
file. Only English ships today. Other locales come later as data files of texts, which the host
loads; a text a locale lacks shows in English.

# The panel

The new core in `next/` serves a web panel at `/panel/` while its management API is on. The panel
is the browser build of the UI kit in `next/ui`; the desktop app in `next/app` is its second
build. This page records the decisions behind the panel's pages and why they were made. How to
build the panel, add a page and use the kit is in [`next/ui/README.md`](../next/ui/README.md).

## What parity with the old panel means

The new panel replaces the old one (`extensions/panel`). Parity means that it looks and feels
like the old panel: where a feature is kept, it keeps the old page, section, setting and button
names, in a similar place, so someone who knows the old panel finds each thing where they expect
it. A label changes only where it named something this core does differently, such as a file, a
relay or a mislabelled setting.

Parity does not mean the same logic. The new core does things its own way, and the panel shows
what it does. Old features are carried over only where they are worth keeping. Each one left out
has a reason, listed page by page in
[What each page leaves out, and why](../next/ui/README.md#what-each-page-leaves-out-and-why).

## A minimal panel

The panel shows what core itself does, in compact pages, and nothing core cannot do. A setting
gets a field only when core runs it; the rest of the config file stays editable as text in the
Config Panel's Source tab, and a setting gets its field when core starts running it.

Richer views of the same data, such as usage analysis, prices and tools for coding agents, belong
to the desktop app. The app shows the same core pages in the same places, and adds its own pages
after them. Both show the pages of the extensions that run.

Nothing in the panel spends from an account behind a click, except an account's "Test", whose
label says that it spends one request.

## Page names

| Page | Why this name |
| --- | --- |
| Dashboard | The landing page, as in the old panel. Its ID stays `status`, from the earlier Status page, so the address `#/status` and theme rules for `data-page="status"` keep working. |
| Accounts | The old Auth Files and AI Providers pages in one. This core has one kind of account, whether it comes from the config or from a sign-in. The old names stay as the source filter's options, "Auth files" and "AI providers". |
| Add Account | Replaces OAuth Login. It adds API keys as well as sign-ins, from the login methods each running extension offers, so "OAuth" no longer describes it. |
| System | Replaces Management Center Info. "Management Center" was the upstream panel's product name. |
| Routing, Quota Management, Config Panel, Logs Viewer | The old names: these pages do the same job. |
| Usage | A new page; see [The Usage page](#the-usage-page). |

The navigation keeps the old panel's groups: Operate, Gateway, Observe, Control, and the
extensions' pages last. Since every text goes through the string layer, renaming a page later is a
change of text; the page ID stays, because it is the page's address and theme hook.

## Where an API key added in the panel lives

An API key added on the Add Account page becomes an account in core's credential store: a folder
with one file per account, readable only by its owner (`credentials_dir`, by default
`credentials` beside the config file). It is never written to `config.yaml`, and no management
reply shows it again.

Accounts set in the config are listed in the panel but changed only in the config. Keys already in
`config.yaml` keep working, and are edited in the Config Panel's Source tab. Keeping panel-added
keys out of the config file means core needs no structured write of config accounts for both
config layouts it reads, and a key is never shown after it is added.

## The Usage page

Core keeps a record of recent requests, in memory and within the limits `usage.max_events` and
`usage.max_age_ms`. The panel's Usage page lists these requests, newest first, with filters,
paging and a CSV export, and links each request's trace ID to its request log in the Logs Viewer.
The totals and breakdowns by model, provider, account and key are on the Dashboard.

The page is in the panel, not only in the desktop app, because core keeps the record and no other
page lists requests. It shows only what core records; prices and cost views belong to the app.

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

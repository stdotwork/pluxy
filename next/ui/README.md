# pluxy UI kit and panel

One UI kit, two builds. The **panel** is the kit's browser build: core embeds it and always serves it
at `/panel/`; the management API is always on. The desktop app ([`../app`](../app/README.md)) is the
kit's second build, with more pages. Both read the same theme files ([theme contract](themes/README.md)) and put shared
pages in the same places. Core keeps the themes a person adds and which theme is on
([where themes are kept](themes/README.md#where-themes-are-kept)), so both builds show the same ones.

| Folder | What it holds |
| --- | --- |
| `kit/` | What both builds share: the host-services interface (`host.ts`), the management API calls by area (`api/`), the string layer (`strings.ts`), privacy mode's masks (`privacy.ts`), components (`ui.tsx`, `ui.css`), the parts every page shows an account, a request, a setting, a coding tool and a loading, empty or failed pane with (`parts/`), its icons (`icons.tsx`), the shell (`shell.tsx`) and the application frame it shows its pages in (`frame.tsx`, `shell.css`, with the page model in `destinations.ts`), the stylesheet that puts the kit's CSS in order (`kit.css`), the typefaces the default theme names (`fonts/`, declared in `fonts.css`), the theme engine (`theme/`) and the pages core offers (`pages/`, listed in `pages/core.ts`) |
| `panel/` | The browser build: its entry page, `browserHost.ts`, the kit's host in a browser, and `status.ts`, what its status block says |
| `themes/` | The theme contract and the built-in themes, as theme files |
| `extension-pages/` | The extension pages contract, and `pluxy-page.js`, the module an extension's page loads to talk to the panel |
| `tests/` | Unit tests (`bun test`) |
| `e2e/` | The browser test (Playwright), against a real core |

## How a host build uses the kit

A host implements `Host` (`kit/host.ts`): calling core's management API with the key, signing in
and out, keeping small settings, and saying which optional features it has. The kit asks the host
to call core and never builds its URL. After rotation, the kit holds the newly generated management
key in page state until its notice closes or the person leaves its tab after seeing it. A reply
that arrives after leaving the tab waits in page state until the person returns to see it. In the browser,
`panel/browserHost.ts` sends every call to `/v0/management` on the origin that served the panel,
and only those calls carry the key. The desktop app implements the same interface over its own
connection to core, holding the key in its native side.

A page that saves a file (a theme file, a log, the usage table) calls `kit/save.tsx`, which
downloads it as a browser does. A host whose window cannot download has `saveFile`: the desktop
app's saves the file in the Downloads folder, and the page shows the path it answers.

A build then picks its pages and mounts the shell:

```tsx
const host = browserHost();
const themes = new ThemeState(host, document, [defaultTheme, terminal, paper]);
const privacy = prefsPrivacy(host);
render(<Shell host={host} themes={themes} privacy={privacy} pages={corePages()} />, document.body);
```

The **Hide names** setting in General hides account e-mail addresses and
identifiers while the person shares their screen (`kit/privacy.ts`); while it is on, the header
strip's end shows a **Names hidden** button that turns it off. It changes only what pages show: a page passes a
value through `usePrivacy()`'s masks where it shows it (text, `title`, `aria-label`) and keeps the
real value for calls, search, filters, copying and downloads. A key's name (its label) goes through
them; a key's value, even masked, never does: it keeps its own handling in both states. Where the desktop app says it saved a file, privacy
mode names the file and its folder only (`shownPath` in `kit/save.tsx`): the folders above hold
the person's user name. The panel keeps the choice in the host's prefs
(`prefsPrivacy`); the desktop app keeps it natively. Extension pages get it as `privacy` in the
theme snapshot (`extension-pages/README.md`); the kit cannot hide what a page shows itself.

### The frame, and where a page sits in it

The window is a frame that does not scroll (`kit/frame.tsx`). It has four fixed parts and one
that scrolls:

- **The navigation** holds five destinations, Home, Accounts, Activity, Tools and Settings, each
  with a line icon; Settings sits at its foot. A destination no page belongs to is not shown.
- **The status block** at the navigation's foot says whether pluxy runs and where. The host fills
  it (`status` on the shell, a `FrameStatus`): a state (`running`, `down` for not answering,
  `stopped`, `unknown`), a word for it, a line saying where pluxy runs, and optionally a menu of
  items: an address with a Copy button, an action that shows while it runs and why it failed, or
  a link. Escape and a click outside close the menu, and focus returns to the block. With no menu,
  the status can give an `href`, and the block is then a link to it. The panel
  says what it knows, the address it was served from and whether its last call was answered
  (`panel/status.ts`); the desktop app reads its connection: a pluxy it runs says Running, one it only
  connects to says Connected, and a state it cannot show pages in links to Where pluxy runs.
- **The header strip** over the page holds the page's one `h1`, the destination's tabs, and at
  its end the page's main actions, then the Names hidden button while names are hidden. Sign out is
  a row of General where the host can sign out; the desktop app turns that row off
  (`corePages({ general: { signOut: false } })`) because its Sign out is on Where pluxy runs. A
  page gives its title and actions with `PageHead`, as before: inside the
  frame the strip draws them, and a page shown outside a frame draws them in place.
- **Topics**: a destination whose pages are topics (Settings) lists them beside the page, in the
  groups This device (General, Themes and the desktop app's pages), pluxy and From extensions,
  in that order; Settings opens on General. In a window under 820 pixels wide the list is a
  select above the page.
- **The pane** shows the page, and is the only part that scrolls. A change of page opens the new
  page at its top; a change inside a page keeps the position.

The theme's `nav` option draws the navigation as a sidebar, a rail of icons or a row on top; a
narrow window overrides it ([layout options](themes/README.md#layout-options)).

A page is a `PageDef` (`kit/destinations.ts`): a stable ID (its address is `#/<id>`), a title, a
render function, and where it sits:

| Field | What it says |
| --- | --- |
| `dest` | The destination the page belongs to |
| `kind` | `"tab"`: one of the destination's tabs in the header strip. `"topic"`: one of its topics beside the page. Left out: the page is reached from the navigation entry itself or from another page |
| `under` | The ID of the page it sits under. The header strip then shows the way back (`Accounts / Add an account`), and no tabs |
| `addresses` | Other addresses the page answers: an exact old ID (All accounts answers `quota`, the Quota page it replaced), or a prefix ending in `/` for whatever follows it (an account's page answers `accounts/`, so `#/accounts/<id>` opens it). Where several pages match, the longest match wins |
| `label` | Its name in its tab or topic entry, when the title does not fit there (Accounts' tab reads "All accounts") |
| `section` | The group a topic is listed in: `core` (the default), `device` or `extensions` |
| `tip` | The tooltip of its tab or topic entry |

The pages are one flat list in which each page names its destination and, if it has one, its
parent, the way a route names its parent in TanStack Router's code-based routes. A nested tree,
as VS Code declares its settings sections, would have to be merged from three sources that arrive
at different times: core's pages, the host build's and the running extensions'. A destination's
navigation entry opens its first page that sits under no other, and the page of a tabbed
destination is headed by the destination's name, with the tabs saying which page it is.
`corePages()` (`kit/pages/core.ts`) is the pages core offers: the panel shows exactly these, and
the desktop app adds its own. A build only bundles the pages it imports, so pages for the desktop
app add nothing to the panel.

A host with no session to show pages for can show the frame by itself (`Frame`), with the
destinations visible but not choosable and one pane of its own. The desktop app does, around the
pane that says what happened (and Where pluxy runs, opened in it), until a core is connected.

With `extensionPages` set, the shell adds the pages of the extensions that run, after the
build's own: each is a `PageDef` (address `#/ext/<publisher>/<name>/<page>`) whose render shows
the extension's document in a sandboxed frame from a separate site (a host can draw it its own
way with `extensionPage`, as the desktop app does), talking to the panel through
a message channel, never with the management key. The Extensions page draws each extension's
settings form from its schema. The contract for both, and the module pages use, are in
[extension-pages](extension-pages/README.md).

## The panel's pages

The pages core offers, by destination, and the page of the old web panel (the React/Vite
panel that `extensions/panel` held) that each one replaces. A page's ID is its address
(`#/<id>`) and its theme hook `data-page`. Why the pages have these names, and the other
decisions behind them, is in [docs/panel.md](../../docs/panel.md).

The shell matches an address to a page by its ID or one of the other addresses the page answers
(an exact old ID, or a prefix ending in `/`), the longest match, and ignores what follows a `?`
(`#/status?x=1` opens Home; `#/quota`, the retired Quota page's address, opens All accounts).
An address whose first part names a destination that has no page of that ID, such as `#/activity` or `#/activity?trace=<id>`, opens that destination's first page.
Any other unknown address opens the first page.

A change of page opens the new page at its top: the frame scrolls the pane there, so a page
opened from a button far down another one shows from its start. A change inside a page (a tab or
a filter in its address) keeps the place.

Three pages add to their address. `#/accounts/<id>` opens that account's own page (the page
`account`, which answers that prefix), and `#/accounts/<id>/sign-in` opens it with its sign-in in
place. `#/logs/<trace ID>` opens that request's log on the Logs page, in its Failed requests tab.
`#/config/<tab>` opens a tab of the Config Panel: `common` (what `#/config` opens),
`connectivity`, `network`, `logging`, `advanced` or `source`. `#/config/source?account=<id>`
opens the Source tab with that account's ID selected; an account's "Open the settings file" leads
there. An account from a provider key section of `config.yaml` opens the Source tab with nothing
selected, because the file does not hold its ID: core makes the ID when it reads the file.

The Setup page (`#/setup`, `kit/pages/setup.tsx`) is the one page the panel opens by itself.
Once signed in, the shell asks core for `GET /setup`: on a first run (`first_run`), when the
address names no page, Setup opens in place of Home. The shell does not wait for the answer, so
Home may show for an instant. A core without the call (it answers 404) gets no Setup page and
nothing opens by itself. The page stays among the Settings topics after setup, titled "Setup".

The page is three numbered steps, one open at a time: "Your AI accounts" (tick the kinds of
account, which installs their providers with `POST /setup/apply`, then sign in with Add an
account's own form, embedded), "Your coding tool" (the Tools page's list of coding tools,
`ToolListView`, with the same rows and words) and "Check it works" (one
`POST /accounts/{id}/test`, with no model unless the person chooses one).
On a first run the first step not done opens; afterwards every step starts folded. Only the
person moves the open step: data arriving updates text in place. What core's scan found shows in
the step that uses it: a suggested kind is ticked with core's reason, a sign-in is a note above
the form, a tool is its row. Step 1 counts as done once an account can take requests (it serves,
or waits out a limit); its summary says what the others are, such as "1 account, turned off.". A
key made in step 2 is held by that step, so its value is gone once the step folds.
`Finish setup`, `Finish anyway`, `Finish without testing` and `Leave setup for now` all send
`POST /setup/done`, call the host's `onFinish` and go to Home.

When core's scan found a CLIProxyAPI setup, one line above the steps (`kit/pages/setupImport.tsx`)
says so, with "Preview import". The preview sends `POST /setup/import` as a dry run and shows core's
report in three folded lists (what it would import, what pluxy sets differently, what it leaves
out, each with why; privacy mode hides the auth files' names). "Import now" is only possible from
that preview, and sends the same config and auth folder; a new scan, or any other change to what
core found, drops the preview. After the import the page reads Setup, the accounts and the
packages again and ticks the kinds the report says the accounts need that step 1 offers; a kind
this pluxy does not offer is named, with a link to Extensions. A refusal is shown on the line in
plain words; only a core too old for the call (404) and one that keeps its settings in
`config.yaml` (409) offer "More details", with the command. When "Import now" gets no reply,
"Refresh" looks again: a new record of an import (`last_import`) means it ran, none means it did
not. Once core has a record of an import, the line says when, with "Import again". An import
never changes core's management key.

A host adds to the page through `corePages({ setup })` (`SetupOptions` in `kit/pages/setup.tsx`),
and the panel passes nothing: `toolAction`, `extraTools`, `localPages` and `onRefresh` go to
step 2's `ToolList` as they do on Tools; `isRemote` overrides the page's own reading of
`host.address`; `onFinish` runs after setup is finished. The desktop app gives a "Set up" link
to its own page for the tools it can set up on its own computer.

The Tools page (`#/tools`, `kit/pages/tools.tsx`) is where a person connects a coding tool and
manages the keys tools send. It lists the tools core's scan found (Claude Code, Codex, OpenCode, Pi,
Oh My Pi, and the three only the desktop app sets up, shown as `Not checked`), connected ones first,
and `Set up` on a row opens three steps under it: make a key for that tool, put the text in its
settings file, start the tool again. The key made in step 1 is named for the tool and is in that
row's text only; its value is held in the page's memory, never in prefs, storage or the address, and
goes when its notice is closed, the key is deleted or the page is left. Under the tools the page
shows the address programs reach pluxy at and the keys: `New key` asks for a name, `Rename` and
`Delete` (which asks first, and is off for the last key) work on each one. `ToolList`, the list
of tools with its steps and no address or keys, is exported for the guided setup.

A host adds to the page through `corePages({ tools })` (`ToolsOptions` in `kit/pages/tools.tsx`),
and the panel passes nothing: `toolAction(client)` is what goes at the end of a row in place of
`Set up`; `extraTools` are rows core's scan does not give; `keyOwners` reads the tool each key
was made for, by key ID or masked key, for a key with no name; `isRemote` says the tools listed are
on another computer than the one showing the page; `localPages` are links to the host's own pages
for this computer's tools; `onRefresh` runs on `Refresh`. The page reads a key's name from its
`label`, `PATCH /client-keys/{id}` renames it (a pluxy that answers 405 loses `Rename` for the
visit), and `POST /client-keys` sends the name with the new key.

| Page (ID) | Where it sits | Replaces (old route) |
| --- | --- | --- |
| Home (`status`) | Home | Dashboard (`/`) |
| Accounts (`accounts`) | Accounts, the tab "All accounts"; `#/quota` opens it too | Auth Files (`/auth-files`) and AI Providers (`/ai-providers`), with the OAuth excluded models and OAuth model alias pages (`/auth-files/oauth-excluded`, `/auth-files/oauth-model-alias`) |
| An account's page (`account`) | Accounts, under All accounts: `#/accounts/<id>`, and `#/accounts/<id>/sign-in` with its sign-in open | The details of one auth file on Auth Files |
| Add an account (`add-account`) | Accounts, under the Accounts page | OAuth Login (`/oauth`) |
| Order & fallback (`routing`) | Accounts, the tab "Order & fallback" | Routing (`/routing`) |
| Logs (`logs`) | Settings, a topic | Logs Viewer (`/logs`) |
| Usage (`usage`) | Activity | No old panel page: the old desktop app's list of recorded requests |
| Tools (`tools`) | Tools | No old page |
| General (`settings`) | Settings, the first topic | No old page |
| Setup (`setup`) | Settings, a topic | No old page |
| Config Panel (`config`) | Settings, a topic | Config Panel (`/config`; `/settings` and `/api-keys` led there) |
| System (`system`) | Settings, a topic | Management Center Info (`/system`) |
| Extensions (`extensions`) | Settings, a topic | Plugins (`/plugins`) and Plugin Store (`/plugin-store`) |
| Themes (`themes`) | Settings, a topic | No old page |

The pages of running extensions (`#/ext/<publisher>/<name>/<page>`, Settings topics in the group
"From extensions") take
the place of the old plugin pages (`/plugin-pages/...`). The Extensions and Themes pages and the
extension pages were built before the old panel's features were compared page by page, and the
lists below do not cover them.

### What each page leaves out, and why

These are the old panel's features that the new pages leave out on purpose. Where a page keeps a
feature, it keeps the old label, unless the label named something this core does differently or
[docs/terms.md](../../docs/terms.md) gives another word for it.

**Home** (the old Dashboard)

- The "Live" badge: the old page showed it whenever it was connected, but did not refresh on its
  own. Home reads again every 60 seconds while it is visible, and says when a read failed and what
  it still shows.
- The "Reload config" button and the Config summary: Config reloads on save. Home says only when
  core refused the last change to its settings, with a link to Config.
- The "Running extensions" table, the models count, the upstream proxy and the other settings
  facts: the Extensions, System and Config pages show them.
- The 10-minute traffic chart and its table: Home shows the last 7 local days, and the Usage page
  has the numbers behind them.
- "Management keys", a count of client keys: the Config Panel lists the client keys.
- "Debug" and "WebSocket auth": core has neither setting (see the config.yaml table in
  [`next/README.md`](../README.md#what-each-setting-does-here)).
- "Server version" and "Build date": on the System page, as "Version" and "Built".
- "Provider keys", a count of the keys in the config's provider sections: "Credentials" counts
  every account, from the config or from a sign-in.
- The six "Where to go from here" cards, and the "Manage providers" and "Inspect logs" links: the
  navigation, with its tabs and topics, leads to the same pages.

**Accounts**

- "Download", "Download selected" and "Auth file JSON (preview)": core never hands a credential
  back out. Only the extension that created an account receives its credential.
- "Delete All", "Delete filtered results" and "Delete Problem Files": "Select", ticking the accounts
  (the ones that need you are grouped at the top) and "Remove…" do the same.
- Provider tabs, the status, source and sort filters, and paging: "Find an account" matches a
  name, an ID, a provider's name, a condition or the last error, and the groups keep accounts in a
  fixed order (soonest back, then by name).
- "Card layout": every account is one row, the same on every page and every theme.
- "Upload file" on the list: importing CLIProxyAPI auth files is at the end of Add an account,
  "Import files…".
- OpenAI-compatible provider groups (one name and base URL holding several keys): each key is an
  account of its own, with the same base URL.
- The connection test "Hi" with its "Test model" field: the old test built the upstream request
  in the browser, with the account's key in it, and sent it through a general relay call that
  this core does not have. An account's "Send a test request" sends the request from core, and
  says under it that it uses one request.
- "Use SIWC": a Sign in with ChatGPT account is added with the Codex extension's own sign-in
  method for it.
- "Note" and tags: core keeps no note or tags on an account.
- "Rewrite response model to the alias" (`force-mapping`): core does not run it yet. "Account
  error rules" (`request-scoped-errors`): dropped, core already treats a request-scoped error as
  not the account's fault.
- The OAuth excluded models and OAuth model alias pages: these rules are now an extension's
  `excluded_models` and `model_alias` settings, written in the Config Panel's Source tab.
- The OAuth model alias page's "Diagram", which drew each provider's models and their aliases
  joined by lines: the System page's "Models your tools can ask for" lists each extension's models with their
  aliases and the accounts that serve them. A sign-in's own aliases are changed in "Account model
  aliases" on its Edit form.
- "Invert page", which flipped the selection on the current page: with "Select" on, each row has
  its own checkbox.

**Add an account**

- Sign-in cards written for each provider: one flow serves every login method that a running
  extension offers, under the provider's name. "Cancel" is there for every method from the moment
  a sign-in starts. While the sign-in is still starting it leaves for the ways to sign in at once,
  and the sign-in is cancelled when core answers. Once it waits, "Cancel" says "Cancelling…" while
  core is asked, then goes back to the ways to sign in; if core refuses, the refusal stays on
  screen and "Cancel" can be pressed again. A sign-in that had just finished when "Cancel" reached
  core is not lost: the page reads it once more and, when it is done, shows the added account as
  after any sign-in.
- The account ID: a person never types or sees one. Core makes it, and the name is asked after
  the sign-in ("Name", "Save name"), prefilled with what the provider called the account. An
  account the provider gave no name reads "Added your Claude account." and its "Name" is empty,
  with an example. With privacy mode on, "Name" always starts empty and shows the masked name as its
  example, so only what the person types is saved, never the mask.
- "Load from file" fills a secret input from a text file (UTF-8, at most 1 MiB); another kind of
  file is refused.
- For the guided setup, `AddAccountPage` takes `providers` as `{ id, title? }` entries: only those
  extensions, in that order, each under its `title` when given, else its plain name.
- "Bind SIWC": a Sign in with ChatGPT account is added with the Codex extension's own sign-in
  method for it.
- The Kimi China and International sponsor tiles and "Join Open Platform": promotion, not a
  feature.
- The "Requires CLI Proxy API v7.3.1 or later" notes: the panel is built into core, so the two
  always match.

**Routing**

- The review dialog before saving: each change saves at once.
- The route preview for a model: the browser guessed the route without seeing cooldowns or session
  bindings, so it could be wrong.

**Quota Management** (now each account's limits)

- The page itself: every account's row on All accounts shows its two shortest limits, what is
  left and when each resets, and its own page shows every limit, the manual resets left and when
  the subscription ends.
- "Reset quota" with Claude's reset grants, and xAI's paid one-token refresh: both spend from the
  account. Nothing in the panel spends from an account behind a click, except an account's "Send
  a test request".
- The DEV tier preview: a preview for the old panel's developers.
- The quota timeline, which drew each account's quota windows over the next days: a richer view of
  the same data. The desktop app does not have it either.

**Logs**

- The "Method", "Status" and "Path" filters, and "Show Raw Logs": core's log holds no request
  lines. Requests are on the Activity page and in the request logs.
- "Full Screen": a theme's `width` layout option `full` does it.
- New lines every second: "Follow new lines" reads new lines every 2 seconds while the page is
  visible.

**Usage**

- Prices and price sync: core records tokens, not prices. The desktop app dropped cost views too
  ([its keep/drop record](../app/PARITY.md#usage-and-sessions)).
- The storage cap and repair of the old app's database: core keeps its recent record in memory,
  limited by `usage.max_events` and `usage.max_age_ms`, and daily files on disk (`usage.keep_days`).

**Config Panel**

- Settings core does not run (marked Later or Dropped in the config.yaml table in
  [`next/README.md`](../README.md#what-each-setting-does-here)): "Debug Mode", "Commercial Mode",
  "Redis Usage Queue Retention (seconds)", "Auth Auto-refresh Workers", "Pass Through Upstream
  Headers", "WebSocket Authentication", "Subagent session affinity", "Persist cooldown status"
  (core saves quota blocks, rate limits, cooldowns and bans to `state/routing.json` by itself), "GPT Image 2 Base
  Model", "Video result credential cache TTL", the "LAN discovery" section, every Streaming field
  (so there is no Streaming tab), every payload rule (so there is no Payload tab), Claude's and
  Codex's cloaking and header defaults, Codex's response steering and live media relay, the
  Devin fields, the plugin settings, "Disable Control Panel", and the panel update
  settings ("Disable Panel Auto Updates", "Panel Repository", "Global GitHub Token"). Any of them
  can still be written in the Source tab.
- "Disable Image Generation": core has no switch for every account. Each Codex account has its own
  `image_generation_tool` and `images` settings (see [Codex](../README.md#codex)), changed in
  "Settings (settings)" on a sign-in's Edit form, or in the Source tab. The old key still works in
  `config.yaml`, where it sets both on the file's Codex accounts.
- "Host Address", "Port", "OAuth Credential Directory", "Enable TLS" and its paths, "Trusted
  proxies" and "Allow Remote Access": core runs them, but they set where core listens, who may
  reach it, or the folder an import reads (`pluxy import cpa`, or "Bring it across" on the Setup
  page), and core's settings call does not change them.
  They are edited in the Source tab.
- Typing or pasting a client key, "Generate" and the "API key strength" meter: core makes every
  new key itself, at random. A specific key that a client already uses can be written in the
  Source tab.
- Key names ("Name / description (optional)"): the old panel kept them only in that browser's
  storage, so no other browser saw them.
- "Edit", and copying a key made earlier: core shows a key in full only once, when it is made.
- "Set management key": "Rotate" makes a new random key, shows it once and signs the panel in
  with it. In the desktop app, "Rotate" keeps the new key natively and shows it in a
  native window, never in a webview ([the app's README](../app/README.md#how-the-app-reaches-core)).

**System**

- "Management UI Version" and "CLI Proxy API Version": one "Version", since the panel is
  built into core.
- "Check for updates" and "Quick Links": they compared with and linked to the upstream project's
  releases and repositories.
- "Local Login Data" and "Clear login data": "Sign out" forgets the key.
- The hidden "Request Logging" dialog, opened by tapping the version seven times: "Enable request
  logging" is a plain switch on the Config Panel's Logging tab.
- The "CLI Proxy API Management Center" title: the upstream panel's product name.

**Across the panel**

- Locales other than English: the string layer is ready for them (see
  [Texts and locales](#texts-and-locales)), and only English ships today.
- Page transitions, reveal effects and the dot-matrix hero; sponsor forms; community and upstream
  links.
- The panel updater: the panel is built into core, and nothing is downloaded.

## Texts and locales

Every text the kit shows goes through `kit/strings.ts`. Each page or part declares its English
texts beside it under its own namespace, and shows them with the function `strings` returns:

```tsx
const t = strings("accounts", {
  title: "Accounts",
  selected: { one: "{count} account selected", other: "{count} accounts selected" },
});
t("title");                  // "Accounts"
t("selected", { count: 3 }); // "3 accounts selected"
```

`{name}` is a placeholder; a text with a count has a `one` and an `other` form, picked by the
language's plural rules. Where a sentence holds markup, keep the placeholder in the text and fill
it with `rich(t("key"), { name: <span>...</span> })`. There is no central catalogue: a locale is a
data file of texts by `namespace.key` (`{"lang": "de", "texts": {"accounts.title": "Konten"}}`),
which the host loads and passes to `setLocale` before rendering. A key the locale lacks shows the
English text. Only English ships today.

## Adding a page

A page lives in `kit/pages/<page>.tsx` and owns everything about itself, so pages added side by
side touch shared files only by one line each:

- **The page and its strings:** `kit/pages/<page>.tsx` declares its texts with
  `strings("<page>", {...})` and exports its `PageDef` as a function, such as
  `export const routingPage = (): PageDef => ({ id: "routing", dest: "accounts", kind: "tab", label: t("tab"), title: t("title"), tip: t("tip"), render: () => <RoutingPage /> })`.
  A part with texts of its own declares its own namespace. A page as large as two files splits
  along what each part works out, as Accounts does (`accounts.tsx` the list, `account.tsx` one
  account's page, and `accounts.helpers.ts` the groups, counts and search both use).
- **The page list:** one entry in `corePages()` (`kit/pages/core.ts`), in the order the page
  shows among its destination's tabs or topics. The page ID never changes: it is the address and the theme hook
  `data-page`.
- **Management calls:** the area's calls and reply types in `kit/api/<area>.ts`, as
  `export const <area>Api = (host: Host) => ({...})`, and two lines in `kit/api/index.ts` (the
  `export *` and the spread into `api`), so pages call `useKit().api.<call>`. Core's settings
  (`GET` and `PATCH /settings`) are already there, in `kit/api/settings.ts`, for every page that
  shows them.
- **Styles:** `kit/pages/<page>.css` beside the page, its rules inside `@layer kit { ... }`, and
  one `@import` line at the end of `kit/kit.css` (the order of those lines is the cascade order).
  Colours come from tokens.
- **Themes:** lists use the kit's `List`, a list with details `MasterDetail`, and every new
  visible part carries a `data-part` hook. A new hook, token or layout value goes into
  [the theme contract](themes/README.md) and `kit/theme/contract.ts` in the same change.
- **Refreshing:** a page that refreshes on its own does so only while the document is visible.
  Use `useLoad` to read and `usePoll` (`kit/ui.tsx`) to keep a live value fresh; do not write a
  timer in a page.
- **Tests:** a browser test for the page in `e2e/<page>.spec.ts`; unit tests in `tests/` only for
  pure code.

Three checks in `bun run lint` and `bun run test` hold the kit's rules, and each failure says what
to do. Imports (Biome's `noRestrictedImports`, in `biome.json` here and in `../app/biome.json`): the
kit imports nothing from the panel, `e2e/`, `tests/` or `../app`, and the app imports only `kit` and
`themes` from `ui`, so shared code belongs in `kit/`. Colours (`tests/colours.test.ts`): no raw
colour value outside the theme tokens, except the baseline in that file, which may only shrink.
Texts (`tests/terms.test.ts`): no `strings(...)` text uses a term that the "Do not use" column of
[`docs/terms.md`](../../docs/terms.md) bans, except the baseline in that file, which may only shrink.

## Why Preact, built with Bun

The priorities are a small, fast panel, and a kit the desktop app can build in unchanged.

- **Preact** (`preact` 11, with hooks) is about 5 KB gzipped for a one-button page, against
  57 KB for React 19 with react-dom, which the old panel and the old app use (both measured with Bun's
  minifier). It keeps React's component model and JSX, so
  people who know React can work on the kit.
- **Bun's bundler** compiles TypeScript and Preact's JSX and bundles the HTML page, its script
  and its stylesheet, with content-hashed names. The kit needs no Vite, Babel or Sass.
- Solid and Svelte compile their templates with their own compiler, which would be a plugin on top
  of Bun's bundler. Lit (web components) is about 10 KB gzipped for a one-component page, and its
  shadow DOM would keep a theme's custom CSS out of the components.
- The kit uses ordinary DOM and CSS custom properties, so the same build runs in a browser and in
  the desktop app's webview.

Built panel, at the time of writing: about 47 KB of script (17 KB gzipped) and 13 KB of CSS
(3 KB gzipped). `bun run build` prints the current sizes.

## Build and check

Bun `1.4.3` (`packageManager` in `package.json`). From this folder:

```bash
bun install --frozen-lockfile
bun run check          # tsc --noEmit
bun run lint           # Biome: lint and format check
bun run test           # unit tests
bun run build          # writes the panel to ../internal/panel/dist
bun run test:browser   # builds, then runs the browser tests
```

Locally, `bun run test:browser` runs every browser test in Chromium only, so it needs only one
install: `bunx playwright install chromium` (this also installs Chromium's headless shell). That
includes the extension pages test (`e2e/extension-pages.spec.ts`: a hostile page, a benign page and
the settings form).

CI also runs the extension pages test in Firefox and WebKit, because extension pages are a security
boundary. A run in CI or with `PLUXY_ALL_BROWSERS=1` adds those two projects, and a browser that
cannot start fails the run. To run all three engines locally, install them once with
`bunx playwright install firefox webkit` (WebKit also needs its system libraries:
`bunx playwright install-deps webkit`), then run `PLUXY_ALL_BROWSERS=1 bun run test:browser`.

Each test starts its own core through `go run ./e2e/panelcore` (from `next/`), with fake extensions,
on a random port.

### A core with data to look at

`go run ./e2e/panelcore` starts a real core with fake extensions and three accounts, all healthy
and unused, so most pages are empty. Add `-demo` for a core that looks lived in, with no real
provider account:

```bash
go build ./... && go generate ./internal/panel   # once: the panel is not kept in git
go run ./e2e/panelcore -demo -listen 127.0.0.1:8330
```

It prints one line of JSON and then runs until its stdin ends or you press Ctrl-C:
`{"base":"http://127.0.0.1:8330","key":"…","client_key":"…","config":"…"}`. Open `<base>/panel/` and
sign in with `key` (the management key); `client_key` is the one to send requests with. Without
`-listen` the port is random and never 8317. It is ready in a few seconds, and seeded, so every run
has the same accounts, models and requests.

With `-demo` core holds 11 accounts of four providers in these states: healthy, cooling after a
rate limit, exhausted, near its quota limits, with and without quota windows, closed by the
provider (banned), needing a new sign-in, and turned off; one account name and one model name are
very long. It has three client keys, a fallback chain (`demo-best`), and about 600 usage events
over the last week and today (several models, keys and providers, some failed, a few that fell
back to another account), with a request log for each request. While it runs, the demo also keeps
a few streaming requests in flight across different accounts, models and client keys. The requests
are real: the demo
sends them to core, which routes them to the fake extensions, so the usage, the logs and the
accounts' states are core's own. The one exception is the time: nothing in core moves its clock,
so the days before today are written into core's usage folder as day files before core starts
(`e2e/panelcore/demo.go` says how). A broken credential file is the one state it does not make.

`e2e/panelcore_demo_test.go` starts it and checks all of this through the management API.

### Edit loop

To edit the panel and see changes on reload without rebuilding by hand, run `make dev` from `next/`
(or `make dev PORT=8331` to pick another loopback port; the default is `8330`, never `8317`):

```bash
make dev
```

It builds the panel, starts `panelcore -demo` with a temporary `HOME`, and prints the URL to open
and the management key to paste. On any edit under `kit/`, `panel/`, `themes/` or
`extension-pages/`, it rebuilds the panel, restarts the demo core on the same port with the same
management key (the key stays the same until `make dev` is stopped, so a browser reload stays
signed in), and prints one line with the elapsed time. A build error is printed without stopping
the loop, so the next save rebuilds again. Ctrl-C stops the core and its extensions.

### How core gets the panel

The built panel is not kept in git. `go generate ./internal/panel` (from `next/`) runs
`bun install` and `bun run build` here, which write `next/internal/panel/dist`; core embeds that
folder when it is built. A core built without it still builds and runs, logs a warning, and
answers `/panel/` with 503 and how to build the panel. The Next CI workflow builds the panel
before it builds and tests core, so its tests always run against the panel of the same commit.

## What core serves

`/panel/` and its files. The panel is a page for the management API, and the management API is always
on: a config without `management_key` is refused at start and on a reload. Every reply carries a
Content Security Policy that allows scripts, styles and requests only to core itself, images
and fonts also from `data:` URLs, and frames only from the extension pages site. The page itself is revalidated on each load; the other files
have content-hashed names and are cached.

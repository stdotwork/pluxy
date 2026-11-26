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

The **Privacy mode** toggle at the header strip's end hides account e-mail addresses and
identifiers while the person shares their screen (`kit/privacy.ts`). It changes only what pages show: a page passes a
value through `usePrivacy()`'s masks where it shows it (text, `title`, `aria-label`) and keeps the
real value for calls, search, filters, copying and downloads. Keys never go through the masks;
they keep their own handling in both states. Where the desktop app says it saved a file, privacy
mode names the file and its folder only (`shownPath` in `kit/save.tsx`): the folders above hold
the person's user name. The panel keeps the choice in the host's prefs
(`prefsPrivacy`); the desktop app keeps it natively. Extension pages get it as `privacy` in the
theme snapshot (`extension-pages/README.md`); the kit cannot hide what a page shows itself.

### The frame, and where a page sits in it

The window is a frame that does not scroll (`kit/frame.tsx`). It has four fixed parts and one
that scrolls:

- **The navigation** holds five destinations, Home, Accounts, Activity, Tools and Settings, each
  with a line icon; Settings sits at its foot. A destination no page belongs to is not shown: the
  panel has no Tools.
- **The status block** at the navigation's foot says whether pluxy runs and where. The host fills
  it (`status` on the shell, a `FrameStatus`): a state (`running`, `down` for not answering,
  `stopped`, `unknown`), a word for it, a line saying where pluxy runs, and optionally a menu of
  items: an address with a Copy button, an action that shows while it runs and why it failed, or
  a link. Escape and a click outside close the menu, and focus returns to the block. The panel
  says what it knows, the address it was served from and whether its last call was answered
  (`panel/status.ts`); the desktop app reads its connection and links to its Connection page.
- **The header strip** over the page holds the page's one `h1`, the destination's tabs, and at
  its end the page's main actions, then the Privacy mode toggle and, where the host can sign out,
  the sign-out button. A page gives its title and actions with `PageHead`, as before: inside the
  frame the strip draws them, and a page shown outside a frame draws them in place.
- **Topics**: a destination whose pages are topics (Settings) lists them beside the page, in the
  groups pluxy, This device and From extensions. In a window under 820 pixels wide the list is a
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
| `under` | The ID of the page it sits under. The header strip then shows the way back (`Accounts / Add Account`), and no tabs |
| `label` | Its name in its tab or topic entry, when the title does not fit there (Routing's tab reads "Order & fallback") |
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
destinations visible but not choosable and one pane of its own. The desktop app does, around its
Connection page, until a core is connected.

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

A change of page opens the new page at its top: the frame scrolls the pane there, so a page
opened from a button far down another one shows from its start. A change inside a page (a tab or
a filter in its address) keeps the place.

Two pages add to their address. `#/logs/<trace ID>` opens that request's log in the Logs Viewer.
`#/config/<tab>` opens a tab of the Config Panel: `common` (what `#/config` opens),
`connectivity`, `network`, `logging`, `advanced` or `source`. `#/config/source?account=<id>`
opens the Source tab with that account's ID selected; an account's "Edit in Config Panel" leads
there. An account from a provider key section of `config.yaml` opens the Source tab with nothing
selected, because the file does not hold its ID: core makes the ID when it reads the file.

The Setup page (`#/setup`) is the one page the panel opens by itself. Once signed in, the
shell asks core for `GET /setup`: on a first run (`first_run`), when the address names no page,
Setup opens in place of the Dashboard. The shell does not wait for the answer, so the Dashboard
may show for an instant. A core without the call (it answers 404) gets no Setup page and nothing
opens by itself. The page stays among the Settings topics after setup, as a status of the same
things.

When core's scan found a CLIProxyAPI setup, its row on the page has "Bring it across". The button
sends `POST /setup/import` as a dry run and shows core's report in its three lists (what comes
across, what is different in pluxy, what does not come across and why), with the provider packages
the accounts need; nothing is written until the person confirms. After the import the page reads
Setup, the accounts and the packages again and ticks the providers the report says are needed, so
Start installs them. The row also names the command that does the same, `pluxy import cpa`. A
refusal is shown in the row with what to do about it; a core without the call (404) gets the
command only. Once core has a record of the import (the row's `last_import`), and the report is
closed, the row says the setup was brought across, when, and how many of its settings and auth
files each list of the report held. It then has no button: the command is how to bring it across
again, and the row says what that does. An import never changes core's management key: a
management key in the CLIProxyAPI setup stays behind, and the report says why in its "does not
come across" list.

A host can add one button to each client the page lists as found:
`corePages({ setup: { clientAction, clientActionHint } })` (`SetupOptions` in `kit/pages/setup.tsx`). `clientAction` takes the client's ID from core's
scan and returns a label and the ID of the host's page the button opens, or nothing; the hint is
one sentence shown while a client has a button. A client whose settings file was refused gets no
button: the page says to paste the text there. The panel passes none; the desktop app offers
"Set up" for the clients it can set up on its own computer.

| Page (ID) | Where it sits | Replaces (old route) |
| --- | --- | --- |
| Dashboard (`status`) | Home | Dashboard (`/`) |
| Accounts (`accounts`) | Accounts, a tab | Auth Files (`/auth-files`) and AI Providers (`/ai-providers`), with the OAuth excluded models and OAuth model alias pages (`/auth-files/oauth-excluded`, `/auth-files/oauth-model-alias`) |
| Add Account (`add-account`) | Accounts, under the Accounts page | OAuth Login (`/oauth`) |
| Routing (`routing`) | Accounts, the tab "Order & fallback" | Routing (`/routing`) |
| Quota Management (`quota`) | Accounts, the tab "Limits" | Quota Management (`/quota`) |
| Logs Viewer (`logs`) | Settings, a topic | Logs Viewer (`/logs`) |
| Usage (`usage`) | Activity | No old panel page: the old desktop app's list of recorded requests |
| Set up pluxy (`setup`) | Settings, a topic | No old page |
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

**Dashboard**

- The "Live" badge: the old page showed it whenever it was connected, but did not refresh on its
  own. The Dashboard refreshes every 60 seconds while it is visible and shows when it last did.
- "Management keys", a count of client keys: the Config Panel lists the client keys.
- "Debug" and "WebSocket auth": core has neither setting (see the config.yaml table in
  [`next/README.md`](../README.md#what-each-setting-does-here)).
- "Server version" and "Build date": on the System page, as "Core version" and "Build time".
- "Provider keys", a count of the keys in the config's provider sections: "Credentials" counts
  every account, from the config or from a sign-in.
- The six "Where to go from here" cards, and the "Manage providers" and "Inspect logs" links: the
  navigation, with its tabs and topics, leads to the same pages.

**Accounts**

- "Download", "Download selected" and "Auth file JSON (preview)": core never hands a credential
  back out. Only the extension that created an account receives its credential.
- "Delete All", "Delete filtered results" and "Delete Problem Files": "Select filtered" and then
  "Remove" do the same, with the status filter set to "Problem" for problem accounts.
- Provider tabs for providers this core has no extension for (Qwen, iFlow, AIStudio, "Empty"): the
  tabs are the running extensions.
- "Card layout": a theme's `list` layout option (`table` or `cards`) sets it.
- OpenAI-compatible provider groups (one name and base URL holding several keys): each key is an
  account of its own, with the same base URL.
- The connection test "Hi" with its "Test model" field: the old test built the upstream request
  in the browser, with the account's key in it, and sent it through a general relay call that
  this core does not have. The account's "Test" sends the request from core, and its label says
  that it spends a request.
- "Use SIWC": a Sign in with ChatGPT account is added with the Codex extension's own sign-in
  method for it.
- "Note" and tags: core keeps no note or tags on an account.
- "Rewrite response model to the alias" (`force-mapping`): core does not run it yet. "Account
  error rules" (`request-scoped-errors`): dropped, core already treats a request-scoped error as
  not the account's fault.
- The OAuth excluded models and OAuth model alias pages: these rules are now an extension's
  `excluded_models` and `model_alias` settings, written in the Config Panel's Source tab.
- The OAuth model alias page's "Diagram", which drew each provider's models and their aliases
  joined by lines: the System page's "Available models" lists each extension's models with their
  aliases and the accounts that serve them. A sign-in's own aliases are changed in "Account model
  aliases" on its Edit form.
- "Invert page", which flipped the selection on the current page: "Select page", "Select
  filtered" and "Deselect" cover the selections the batch actions need.

**Add Account**

- Sign-in cards written for each provider: one flow serves every login method that a running
  extension offers. "Cancel" works for every method.
- "Bind SIWC": a Sign in with ChatGPT account is added with the Codex extension's own sign-in
  method for it.
- The Kimi China and International sponsor tiles and "Join Open Platform": promotion, not a
  feature.
- The "Requires CLI Proxy API v7.3.1 or later" notes: the panel is built into core, so the two
  always match.

**Routing**

- Drag and drop, and "Drop here to make a new tier": "Move up a tier" and "Move down a tier" move
  an account into the next tier, or into a new tier past the first or the last.
- The review dialog before saving: each change saves at once.
- The route preview for a model: the browser guessed the route without seeing cooldowns or session
  bindings, so it could be wrong.

**Quota Management**

- "Reset quota" with Claude's reset grants, and xAI's paid one-token refresh: both spend from the
  account. Nothing in the panel spends from an account behind a click, except an account's "Test".
- The DEV tier preview: a preview for the old panel's developers.
- The quota timeline, which drew each account's quota windows over the next days: a richer view of
  the same data. The desktop app does not have it either. Each card shows every limit's use and
  when it resets.

**Logs Viewer**

- The "Method", "Status" and "Path" filters, and "Show Raw Logs": core's log holds no request
  lines. Requests are on the Usage page and in the request logs.
- "Full Screen": a theme's `width` layout option `full` does it.
- New lines every second: "Continuous reading" reads new lines every 2 seconds while the page is
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

- "Management UI Version" and "CLI Proxy API Version": one "Core version", since the panel is
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
  along a typed interface, as Accounts does (`accounts.tsx` the list, `accountDetail.tsx` one
  account's details, joined by `AccountDetailProps`).
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

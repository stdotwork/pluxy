# pluxy UI kit and panel

One UI kit, two builds. The **panel** is the kit's browser build: core embeds it and serves it at
`/panel/` while the management API is on. The desktop app ([`../app`](../app/README.md)) is the
kit's second build, with more pages. Both read the same theme files ([theme contract](themes/README.md)) and put shared
pages in the same places.

| Folder | What it holds |
| --- | --- |
| `kit/` | What both builds share: the host-services interface (`host.ts`), the management API calls by area (`api/`), the string layer (`strings.ts`), components (`ui.tsx`, `ui.css`), the shell and its layouts (`shell.tsx`, `shell.css`), the stylesheet that puts the kit's CSS in order (`kit.css`), the theme engine (`theme/`) and the pages core offers (`pages/`, listed in `pages/core.ts`) |
| `panel/` | The browser build: its entry page, and `browserHost.ts`, the kit's host in a browser |
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

A build then picks its pages and mounts the shell:

```tsx
const host = browserHost();
const themes = new ThemeState(host, document, [defaultTheme, terminal, paper]);
render(<Shell host={host} themes={themes} pages={corePages()} />, document.body);
```

A page is a `PageDef`: a stable ID (its address is `#/<id>`), a title, a short label for the rail
navigation, its navigation group, an optional tooltip, and a render function. The navigation shows
the groups Operate, Gateway, Observe, Control and Extension pages, in that order, each holding its
pages in the order the build lists them; a group with no page is not shown. `corePages()`
(`kit/pages/core.ts`) is the pages core offers: the panel shows exactly these, and the desktop app
adds its own after them. A build only bundles the pages it imports, so pages for the desktop app
add nothing to the panel.

With `extensionPages` set, the shell adds the pages of the extensions that run, after the
build's own: each is a `PageDef` (address `#/ext/<publisher>/<name>/<page>`) whose render shows
the extension's document in a sandboxed frame from a separate site (a host can draw it its own
way with `extensionPage`, as the desktop app does), talking to the panel through
a message channel, never with the management key. The Extensions page draws each extension's
settings form from its schema. The contract for both, and the module pages use, are in
[extension-pages](extension-pages/README.md).

## The panel's pages

The pages core offers, in navigation order, and the page of the old panel
([`extensions/panel`](../../extensions/panel)) that each one replaces. A page's ID is its address
(`#/<id>`) and its theme hook `data-page`. Why the pages have these names, and the other
decisions behind them, is in [docs/panel.md](../../docs/panel.md).

| Page (ID) | Group | Replaces (old route) |
| --- | --- | --- |
| Dashboard (`status`) | Operate | Dashboard (`/`) |
| Accounts (`accounts`) | Gateway | Auth Files (`/auth-files`) and AI Providers (`/ai-providers`), with the OAuth excluded models and OAuth model alias pages (`/auth-files/oauth-excluded`, `/auth-files/oauth-model-alias`) |
| Add Account (`add-account`) | Gateway | OAuth Login (`/oauth`) |
| Routing (`routing`) | Gateway | Routing (`/routing`) |
| Quota Management (`quota`) | Observe | Quota Management (`/quota`) |
| Logs Viewer (`logs`) | Observe | Logs Viewer (`/logs`) |
| Usage (`usage`) | Observe | No old panel page: the old desktop app's list of recorded requests |
| Config Panel (`config`) | Control | Config Panel (`/config`; `/settings` and `/api-keys` led there) |
| System (`system`) | Control | Management Center Info (`/system`) |
| Extensions (`extensions`) | Control | Plugins (`/plugins`) and Plugin Store (`/plugin-store`) |
| Themes (`themes`) | Control | No old page |

The pages of running extensions (`#/ext/<publisher>/<name>/<page>`, group Extension pages) take
the place of the old plugin pages (`/plugin-pages/...`). The Extensions and Themes pages and the
extension pages were built before the old panel's features were compared page by page, and the
lists below do not cover them.

### What each page leaves out, and why

These are the old panel's features that the new pages leave out on purpose. Where a page keeps a
feature, it keeps the old label, unless the label named something this core does differently.

**Dashboard**

- The "Live" badge: the old page showed it whenever it was connected, but did not refresh on its
  own. The Dashboard refreshes every 60 seconds while it is visible and shows when it last did.
- "Management keys", a count of client keys: the Config Panel lists the client keys.
- "Debug" and "WebSocket auth": core has neither setting (see the config.yaml table in
  [`next/README.md`](../README.md#what-each-setting-does-here)).
- "Server version" and "Build date": on the System page, as "Core version" and "Build Time".
- "Provider keys", a count of the keys in the config's provider sections: "Credentials" counts
  every account, from the config or from a sign-in.
- The six "Where to go from here" cards, and the "Manage providers" and "Inspect logs" links: the
  navigation leads to the same pages.

**Accounts**

- "Download", "Download selected" and "Auth file JSON (preview)": core never hands a credential
  back out. Only the extension that created an account receives its credential.
- "Delete All", "Delete filtered results" and "Delete Problem Files": "Select filtered" and then
  "Delete" do the same, with the status filter set to "Problem" for problem accounts.
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

**Logs Viewer**

- The "Method", "Status" and "Path" filters, and "Show Raw Logs": core's log holds no request
  lines. Requests are on the Usage page and in the request logs.
- "Full Screen": a theme's `width` layout option `full` does it.
- New lines every second: "Continuous reading" reads new lines every 2 seconds while the page is
  visible.

**Usage**

- Prices and price sync: core records tokens, not prices. Cost views belong to the desktop app.
- The storage cap and repair of the old app's database: core keeps its record in memory, limited
  by `usage.max_events` and `usage.max_age_ms`.

**Config Panel**

- Settings core does not run (marked Later or Dropped in the config.yaml table in
  [`next/README.md`](../README.md#what-each-setting-does-here)): "Debug Mode", "Commercial Mode",
  "Redis Usage Queue Retention (seconds)", "Auth Auto-refresh Workers", "Pass Through Upstream
  Headers", "WebSocket Authentication", "Subagent session affinity", every Streaming field (so
  there is no Streaming tab), every payload rule (so there is no Payload tab), Claude's and Codex's
  cloaking and header defaults, Codex's response steering and live media relay, the Antigravity
  and Devin fields, the plugin settings, "Disable Control Panel", and the panel update settings
  ("Disable Panel Auto Updates", "Panel Repository", "Global GitHub Token"). Any of them can still
  be written in the Source tab.
- "Host Address", "Port", "OAuth Credential Directory", "Enable TLS" and its paths, "Trusted
  proxies" and "Allow Remote Access": core runs them, but they set where core listens, who may
  reach it, or the folder `-import-auth` reads, and core's settings call does not change them.
  They are edited in the Source tab.
- Typing or pasting a client key, "Generate" and the "API key strength" meter: core makes every
  new key itself, at random. A specific key that a client already uses can be written in the
  Source tab.
- Key names ("Name / description (optional)"): the old panel kept them only in that browser's
  storage, so no other browser saw them.
- "Edit", and copying a key made earlier: core shows a key in full only once, when it is made.
- "Set management key": "Rotate" makes a new random key, shows it once and signs the panel in
  with it. The desktop app shows no "Rotate" yet.

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
  `export const routingPage = (): PageDef => ({ id: "routing", group: "gateway", title: t("title"), short: t("short"), tip: t("tip"), render: () => <RoutingPage /> })`.
  A part with texts of its own declares its own namespace. A page as large as two files splits
  along a typed interface, as Accounts does (`accounts.tsx` the list, `accountDetail.tsx` one
  account's details, joined by `AccountDetailProps`).
- **The page list:** one entry in `corePages()` (`kit/pages/core.ts`), in the order the page
  shows within its group. The page ID never changes: it is the address and the theme hook
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
- **Tests:** a browser test for the page in `e2e/<page>.spec.ts`; unit tests in `tests/` only for
  pure code.

## Why Preact, built with Bun

The priorities are a small, fast panel, and a kit the desktop app can build in unchanged.

- **Preact** (`preact` 11, with hooks) is about 5 KB gzipped for a one-button page, against
  57 KB for React 19 with react-dom, which today's panel and app use (both measured with Bun's
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

`bun run test:browser` runs the panel's test in Chromium and the extension pages test
(`e2e/extension-pages.spec.ts`: a hostile page, a benign page and the settings form) in Chromium,
Firefox and WebKit. It needs Playwright's browsers once: `bunx playwright install chromium firefox
webkit` (WebKit also needs its system libraries: `bunx playwright install-deps webkit`). Each test
starts its own core through `go run ./e2e/panelcore` (from `next/`), with fake extensions, on a
random port.

### How core gets the panel

The built panel is not kept in git. `go generate ./internal/panel` (from `next/`) runs
`bun install` and `bun run build` here, which write `next/internal/panel/dist`; core embeds that
folder when it is built. A core built without it still builds and runs, logs a warning, and
answers `/panel/` with 503 and how to build the panel. The Next CI workflow builds the panel
before it builds and tests core, so its tests always run against the panel of the same commit.

## What core serves

`/panel/` and its files, only while the config sets `management_key` (the panel is a page for the
management API; without the key, `/panel/` is not found, as the API is). Every reply carries a
Content Security Policy that allows scripts, styles and requests only to core itself, images
and fonts also from `data:` URLs, and frames only from the extension pages site. The page itself is revalidated on each load; the other files
have content-hashed names and are cached.

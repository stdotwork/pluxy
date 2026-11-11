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

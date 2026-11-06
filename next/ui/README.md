# pluxy UI kit and panel

One UI kit, two builds. The **panel** is the kit's browser build: core embeds it and serves it at
`/panel/` while the management API is on. The desktop app will be the kit's second build, with
more pages. Both read the same theme files ([theme contract](themes/README.md)) and put shared
pages in the same places.

| Folder | What it holds |
| --- | --- |
| `kit/` | What both builds share: the host-services interface (`host.ts`), the management API calls (`api.ts`), components (`ui.tsx`), the shell and its layouts (`shell.tsx`, `kit.css`), the theme engine (`theme/`) and the pages core offers (`pages/`) |
| `panel/` | The browser build: its entry page, and `browserHost.ts`, the kit's host in a browser |
| `themes/` | The theme contract and the built-in themes, as theme files |
| `tests/` | Unit tests (`bun test`) |
| `e2e/` | The browser test (Playwright), against a real core |

## How a host build uses the kit

A host implements `Host` (`kit/host.ts`): calling core's management API with the key, signing in
and out, keeping small settings, and saying which optional features it has. The kit never holds
the management key and never builds a URL to core; it asks the host. In the browser,
`panel/browserHost.ts` sends every call to `/v0/management` on the origin that served the panel,
and only those calls carry the key. The desktop app will implement the same interface over its
own connection to core.

A build then picks its pages and mounts the shell:

```tsx
const host = browserHost();
const themes = new ThemeState(host, document, [defaultTheme, terminal, paper]);
render(<Shell host={host} themes={themes} pages={[statusPage, accountsPage /* ... */]} />, document.body);
```

A page is a `PageDef`: a stable ID (its address is `#/<id>`), a title, a short label for the rail
navigation, and a render function. A build only bundles the pages it imports, so pages for the
desktop app add nothing to the panel.

Extension pages are not built yet. They will be `PageDef`s too: their render function will mount
the extension's page in an isolated document on a separate site, talking to the host through a
message channel, never with the management key. The shell, the navigation and the theme hooks
need no change for them.

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
bun run test:browser   # builds, then runs the browser test in headless Chromium
```

`bun run test:browser` needs Playwright's Chromium once: `bunx playwright install chromium`. It
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
Content Security Policy that allows scripts, styles and requests only to core itself, and images
and fonts also from `data:` URLs. The page itself is revalidated on each load; the other files
have content-hashed names and are cached.

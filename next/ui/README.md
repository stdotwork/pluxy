# pluxy UI kit

The UI kit (`kit/`) is the user interface that core's web panel and the desktop app share. It is
written in Preact and built with Bun's bundler. It has two builds:

- **The panel**, the browser build in `panel/`. Core embeds it and serves it at `/panel/`.
- **The desktop app** ([`../app`](../app/README.md)), which builds the same kit in a Tauri window
  and adds pages of its own.

Both builds read the same theme files ([theme contract](themes/README.md)), and core keeps the
themes a person adds and which one is on ([where themes are kept](themes/README.md#where-themes-are-kept)),
so both show the same ones. What each of the panel's pages does is in
[docs/panel.md](../../docs/panel.md).

| Folder | What it holds |
| --- | --- |
| `kit/` | What both builds share: the host-services interface (`host.ts`), the management API calls by area (`api/`), the string layer (`strings.ts`), privacy mode's masks (`privacy.ts`), components (`ui.tsx`, `ui.css`), the parts every page shows an account, a request, a setting, a coding tool and a loading, empty or failed pane with (`parts/`), its icons (`icons.tsx`), the shell (`shell.tsx`) and the application frame it shows its pages in (`frame.tsx`, `shell.css`, with the page model in `destinations.ts`), the stylesheet that puts the kit's CSS in order (`kit.css`), the typefaces the default theme names (`fonts/`, declared in `fonts.css`), the theme engine (`theme/`) and the pages core offers (`pages/`, listed in `pages/core.ts`) |
| `panel/` | The browser build: its entry page, `browserHost.ts`, the kit's host in a browser, and `status.ts`, what its status block says |
| `themes/` | The theme contract and the built-in themes, as theme files |
| `extension-pages/` | The extension pages contract, and `pluxy-page.js`, the module an extension's page loads to talk to the panel |
| `tests/` | Unit tests (`bun test`) |
| `e2e/` | The browser test (Playwright), against a real core |

## How a host build uses the kit

A host build implements `Host` (`kit/host.ts`): it calls core's management API with the
management key, signs in and out, keeps small settings (`prefs`), and says which optional
features it has (`features`). The kit asks the host to call core and never builds core's URL. In
the browser, `panel/browserHost.ts` sends every call to `/v0/management` on the origin that
served the panel, and only those calls carry the key. The desktop app implements the same
interface over its own connection to core and holds the key in its native side.

A build then picks its pages and mounts the shell. The panel's `panel/main.tsx`, shortened:

```tsx
const host = browserHost();
const themes = new ThemeState(host, document, [defaultTheme, terminal, paper]);
const privacy = prefsPrivacy(host);
render(
  <Shell host={host} themes={themes} privacy={privacy} pages={corePages()} extensionPages safeAddress />,
  document.body,
);
```

| `Shell` prop | What it does |
| --- | --- |
| `pages` | The pages to show. `corePages()` (`kit/pages/core.ts`) is the pages core offers: the panel shows these, and the desktop app adds its own after them. The shell leaves out Setup when core answers `GET /setup` with 404 |
| `privacy` | Privacy mode, kept where the build keeps it: the panel in the host's prefs (`prefsPrivacy`), the desktop app natively |
| `status` | What the status block at the navigation's foot says (a `FrameStatus`, `kit/frame.tsx`) |
| `extensionPages` | Adds the pages of the extensions that run, after the build's own ([extension pages](extension-pages/README.md)) |
| `extensionPage` | Draws an extension page the host's own way, as the desktop app does |
| `safeAddress` | The sign-in page mentions `?safe`, which turns custom CSS off ([turning custom CSS off](themes/README.md#turning-custom-css-off)). Left out by a host whose address cannot take it |

`corePages` takes options a build uses to change core's pages; the panel passes none.
`general: { signOut: false }` removes General's Sign out row (the desktop app's Sign out is on its
Where pluxy runs page). `setup` (`SetupOptions` in `kit/pages/setup.tsx`) and `tools`
(`ToolsOptions` in `kit/pages/tools.tsx`) add the host's own tools, actions and links to the
Setup and Tools pages. A build bundles only the pages it imports, so the desktop app's pages add
nothing to the panel.

A page that saves a file (a theme file, a log, the request list) calls `kit/save.tsx`, which
downloads it as a browser does. A host whose window cannot download implements `saveFile`: the
desktop app saves the file in the Downloads folder, and the page shows the path it answers.

### Privacy mode

The **Hide names** setting in General hides account e-mail addresses and identifiers, for example
while the person shares their screen (`kit/privacy.ts`). While it is on, the header strip shows a
**Names hidden** button that turns it off. It changes only what pages show:

- A page passes a value through `usePrivacy()`'s masks where it shows it (text, `title`,
  `aria-label`), and keeps the real value for calls, search, filters, copying and downloads.
- A key's name (its label) goes through the masks. A key's value never does: it keeps its own
  handling whether privacy mode is on or off.
- Where the desktop app says it saved a file, privacy mode shows the file and its folder only
  (`shownPath` in `kit/save.tsx`), because the folders above it hold the person's user name.
- Extension pages get the setting as `privacy` in the theme snapshot
  ([extension pages](extension-pages/README.md#the-theme-snapshot)); the kit cannot hide what a
  page shows itself.

### The frame, and where a page sits in it

The window is a frame that does not scroll (`kit/frame.tsx`). Only the pane that shows the page
scrolls. Around it:

- **The navigation** holds five destinations: Home, Accounts, Activity, Tools and Settings. A
  destination no page belongs to is not shown. The theme's `nav` option draws it as a sidebar, a
  rail of icons or a row on top, and a narrow window overrides that
  ([layout options](themes/README.md#layout-options)).
- **The status block** at the navigation's foot says whether pluxy runs and where. The host fills
  it through the shell's `status`.
- **The header strip** over the page holds the page's one `h1`, the destination's tabs and the
  page's main actions. A page gives its title and actions with `PageHead` (`kit/ui.tsx`): inside
  the frame the strip draws them, and outside a frame they are drawn in place.
- **Topics**: a destination whose pages are topics (Settings) lists them beside the page, in the
  groups This device, pluxy and From extensions.

A page is a `PageDef` (`kit/destinations.ts`): a stable ID, a title, a render function, and where
it sits. The ID is the page's address (`#/<id>`) and its theme hook (`data-page`), and never
changes.

| Field | What it says |
| --- | --- |
| `dest` | The destination the page belongs to. The destination's navigation entry opens its first page that sits under no other |
| `kind` | `"tab"`: one of the destination's tabs in the header strip. `"topic"`: one of its topics beside the page. Left out: the page is reached from the navigation entry itself or from another page |
| `under` | The ID of the page it sits under. The header strip then shows the way back, and no tabs |
| `addresses` | Other addresses the page answers: an exact address (such as the ID of a page it replaced), or a prefix ending in `/` for whatever follows it (the `account` page answers `accounts/`, so `#/accounts/<id>` opens it) |
| `label` | Its name in its tab or topic entry, when the title does not fit there |
| `section` | The group a topic is listed in: `core` (the default), `device` or `extensions` |
| `tip` | The tooltip of its tab or topic entry |

The shell matches an address to a page by its ID or one of its `addresses`, and the longest match
wins; it ignores what follows a `?`. An address whose first part names a destination opens that
destination's first page when no page matches; any other unknown address opens the first page.

A host with no session to show pages for can show the frame by itself (`Frame`), with the
destinations shown but not choosable and one pane of its own. The desktop app does this until a
core is connected.

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
English text. Only English ships.

## Adding a page

A page lives in `kit/pages/<page>.tsx` and owns everything about itself, so pages added side by
side touch shared files only to list themselves:

- **The page and its strings:** `kit/pages/<page>.tsx` declares its texts with
  `strings("<page>", {...})` and exports its `PageDef` as a function, such as
  `export const routingPage = (): PageDef => ({ id: "routing", dest: "accounts", kind: "tab", label: t("tab"), title: t("title"), tip: t("tip"), render: () => <RoutingPage /> })`.
  A part with texts of its own declares its own namespace. A page as large as two files splits
  along what each part works out, as Accounts does (`accounts.tsx` the list, `account.tsx` one
  account's page, and `accounts.helpers.ts` the groups, counts and search both use).
- **The page list:** one entry in `corePages()` (`kit/pages/core.ts`), in the order the page
  shows among its destination's tabs or topics.
- **Management calls:** the area's calls and reply types in `kit/api/<area>.ts`, as
  `export const <area>Api = (host: Host) => ({...})`, and three lines in `kit/api/index.ts` (the
  `import`, the `export *` and the spread into `api`), so pages call `useKit().api.<call>`. Core's settings
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
- **Tests:** a browser test for the page in `e2e/<page>.spec.ts`; unit tests in `tests/`, where
  `tests/fakeDom.ts` lets a test mount a page without a browser.

Three checks in `bun run lint` and `bun run test` hold the kit's rules, and each failure says what
to do. Imports (Biome's `noRestrictedImports`, in `biome.json` here and in `../app/biome.json`): the
kit imports nothing from the panel, `e2e/`, `tests/` or `../app`, and the app imports only `kit` and
`themes` from `ui`, so shared code belongs in `kit/`. Colours (`tests/colours.test.ts`): no raw
colour value outside the theme tokens, except the baseline in that file, which may only shrink.
Texts (`tests/terms.test.ts`): no `strings(...)` text uses a term that the "Do not use" column of
[`docs/terms.md`](../../docs/terms.md) bans, except the baseline in that file, which may only shrink.

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

`bun run test:browser` runs every browser test in Chromium; install it once with
`bunx playwright install chromium`. In CI, or with `PLUXY_ALL_BROWSERS=1`, the extension pages
test (`e2e/extension-pages.spec.ts`) also runs in Firefox and WebKit, because extension pages are
a security boundary, and a browser that cannot start fails the run. To run those locally, install
them once with `bunx playwright install firefox webkit` and `bunx playwright install-deps webkit`.
Each browser test file starts one core, shared by its tests, through `go run ./e2e/panelcore` (from `next/`), with fake
extensions, on a random port.

### A core with data to look at

`go run ./e2e/panelcore` (from `next/`) starts a real core with fake extensions and three healthy,
unused accounts. Add `-demo` for a core with data on every page and no real provider account:

```bash
go build ./... && go generate ./internal/panel   # once: the panel is not kept in git
go run ./e2e/panelcore -demo -listen 127.0.0.1:8330
```

It prints one line of JSON and runs until its stdin ends or you press Ctrl-C:
`{"base":"http://127.0.0.1:8330","key":"…","client_key":"…","config":"…"}`. Open `<base>/panel/` and
sign in with `key` (the management key); send requests with `client_key`. Without `-listen` the
port is random.

The demo is seeded: 11 accounts of four providers, in every state an account can be in except a
broken credential file, three client keys and a fallback (`demo-best`). When it starts, it sends
core over a hundred requests, which core routes to the fake extensions and logs, and it keeps four
streaming requests in flight, so the usage, request logs and account states are core's own. The
usage of the week before, and of today up to five minutes before core starts, is written into
core's usage folder as day files before core starts (`e2e/panelcore/demo.go`); how many requests
those hold depends on the day of the week and the time of day. `e2e/panelcore_demo_test.go`
checks the accounts, client keys, fallback and usage through the management API.

### Edit loop

`make dev` (from `next/`) builds the panel, starts `panelcore -demo` with a temporary `HOME` on
port `8330` (`make dev PORT=8331` picks another), and prints the URL to open and the management
key. On every edit under `kit/`, `panel/`, `themes/` or `extension-pages/` it rebuilds the panel
and restarts the demo core on the same port with the same key, so a browser reload stays signed in.
A build error is printed and the loop goes on. Ctrl-C stops the core and its extensions.

### How core gets the panel

The built panel is not kept in git. `go generate ./internal/panel` (from `next/`) runs
`bun install` and `bun run build` here, which write `next/internal/panel/dist`; core embeds that
folder when it is built. A core built without it still builds and runs, logs a warning, and
answers `/panel/` with 503 and how to build the panel. The Next CI workflow builds the panel
before it builds and tests core, so its tests always run against the panel of the same commit.

## What core serves

`/panel/` and its files. The panel is a page for the management API, and the management API is always
on: a config without `management_key` is refused at start and on a reload. Every page and file it serves carries a
Content Security Policy that allows scripts, styles and requests only to core itself, images
and fonts also from `data:` URLs, and frames only from the extension pages site. The page itself is revalidated on each load; the other files
have content-hashed names and are cached.

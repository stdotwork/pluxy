# Theme contract, version 0

This is the reference for pluxy theme files. The panel that core serves and the desktop app read
the same files, through the same UI kit (`next/ui/kit`).

A theme is one JSON file. It is data: the UI reads its values, and never runs anything from it. A
theme can change three things:

1. **Tokens:** named values for colour, type, spacing, density, shape and motion.
2. **Layout options:** choices the UI implements, such as where the navigation sits or whether
   lists show as a table or as cards.
3. **Custom CSS:** a stylesheet applied on top, for anything tokens and layout options do not
   cover. Safe mode turns it off.

Tokens and layout options are the stable part of the contract: a theme that only uses them keeps
working as the UI changes. Custom CSS can only rely on the hooks listed under
[Custom CSS](#custom-css); anything else in the UI's markup can change in any release.

## The file

```json
{
  "theme_version": 0,
  "id": "example/paper",
  "name": "Paper",
  "description": "Warm light theme with the navigation on top.",
  "appearance": "both",
  "tokens": {
    "color.bg": "#f6f1e7",
    "color.accent": "#9a3412",
    "color.diff-add": "#1f7a3d",
    "font.body": "Georgia, serif"
  },
  "dark": {
    "color.bg": "#1c1917"
  },
  "layout": {
    "nav": "top",
    "list": "cards",
    "detail": "below",
    "width": "narrow",
    "tabs": "side"
  },
  "css": "[data-part=\"nav\"] { letter-spacing: 0.02em; }"
}
```

| Field | Required | What it is |
| --- | --- | --- |
| `theme_version` | yes (pluxy files) | The contract version the file is written for. This page describes `0`. Omit for a [T3 Code theme](#t3-code-themes). |
| `id` | yes (pluxy files) | `publisher/name`. Each part is 1 to 64 characters from `a-z`, `0-9` and `-`. Optional in a T3 Code theme, where the ID becomes `t3/<id>` or is derived from `name`. |
| `name` | yes | The name people see. 1 to 64 characters. |
| `description` | no | One line, up to 200 characters. |
| `appearance` | no (yes for T3 Code) | `both` (the default for pluxy files), `light` or `dark`: the appearances the theme offers. See [Appearance](#appearance). |
| `tokens` | no | Token values, by token name. Missing tokens keep the UI's default value. |
| `dark` | no | Token values that replace `tokens` while the appearance is dark. |
| `layout` | no | The theme's layout options. Missing options keep the UI's default. |
| `css` | no | Custom CSS, up to 128 KiB. |

A `theme_version: 0` file may also include T3 Code's `canvas`, `accent`, `colors` and `variants`
fields, and a T3 Code file may also include `tokens`, `dark` and `layout`.

A theme file is at most 256 KiB.

### What the UI does with a file it cannot fully use

- **Refused, and the UI keeps the theme it had:** the file is not a JSON object, is over 256 KiB,
  has a `theme_version` newer than the UI supports, has an unsupported T3 Code `version`, has
  neither `theme_version` nor a valid T3 Code shape (`name`, `appearance` of `light` or `dark`, and
  `colors` or both `canvas` and `accent`), or has a missing or invalid `id` or `name`.
- **Used, with a warning shown on the Themes page:** an unknown field, token, T3 Code role or layout
  option is ignored; a token or T3 Code role value that breaks the rules below is ignored and the
  default is kept; `canvas` or `accent` seeds outside hex, `rgb()`/`rgba()` or `oklch()` skip the
  generated palette; a `variants` key other than `light` or `dark`, or repeating the file's own
  appearance, is ignored; a layout value the UI does not offer falls back to that option's default;
  an unknown `appearance` falls back to `both`; custom CSS containing `@import` is dropped as a
  whole.

So a theme written for a later version 0 UI, with tokens this UI does not know yet, still loads.

## Tokens

Each token becomes a CSS custom property: the name with dots replaced by dashes and `--px-` in
front, so `color.bg` is `--px-color-bg`. Custom CSS can read them with `var()`.

A token value is a string of 1 to 200 characters. It may not contain `;`, `{`, `}`, `<`, `>`,
`\`, `@`, `/*` or `url(`. These rules keep a value from ending its declaration and from loading
anything.

### Colour

| Token | Used for | Default (light / dark) |
| --- | --- | --- |
| `color.bg` | Page background | `#f7f7f8` / `#111214` |
| `color.surface` | Navigation, cards, panes, table rows | `#ffffff` / `#1a1b1e` |
| `color.surface-raised` | Hover and selected rows, inputs | `#f0f0f2` / `#24262a` |
| `color.text` | Body text | `#18181b` / `#e7e7ea` |
| `color.text-muted` | Labels, secondary text | `#5f6068` / `#9a9ba3` |
| `color.border` | Borders and dividers | `#e2e2e6` / `#2e3035` |
| `color.accent` | Buttons, selection, links | `#2f5fd0` / `#7aa2ff` |
| `color.accent-text` | Text on `color.accent` | `#ffffff` / `#0b1020` |
| `color.ok` | Healthy states | `#1f7a3d` / `#5cc98a` |
| `color.warn` | Degraded states | `#9a5b00` / `#e6b450` |
| `color.danger` | Errors, destructive actions | `#b42318` / `#ff7b72` |
| `color.focus` | Keyboard focus ring | `#2f5fd0` / `#7aa2ff` |
| `color.meter-track` | Unused part of a quota meter | `var(--px-color-surface-raised)` |
| `color.meter-fill` | Used share of a quota meter | `var(--px-color-accent)` |
| `color.chart-success` | Successful requests in the Dashboard's traffic chart | `var(--px-color-ok)` |
| `color.chart-failure` | Failed requests in the Dashboard's traffic chart | `var(--px-color-danger)` |
| `color.diff-add` | Added lines in a diff | `#1f7a3d` / `#5cc98a` |
| `color.diff-remove` | Removed lines in a diff | `#b42318` / `#ff7b72` |
| `color.nav` | Navigation background | `var(--px-color-surface)` |
| `color.nav-text` | The product name in the navigation, and a hovered entry's text | `var(--px-color-text)` |
| `color.nav-muted` | Navigation entries and group labels | `var(--px-color-text-muted)` |
| `color.nav-hover` | Background of a hovered navigation entry | `var(--px-color-surface-raised)` |
| `color.nav-selected` | Background of the current navigation entry; its text is `color.accent` | `color-mix(in srgb, var(--px-color-accent) 14%, transparent)` |
| `color.nav-border` | The navigation's border, and the lines between its groups | `var(--px-color-border)` |
| `color.overlay` | Dialog background | `var(--px-color-surface)` |
| `color.input-border` | Border of text inputs, selects and text areas | `var(--px-color-border)` |
| `color.placeholder` | Placeholder text in an empty input | `var(--px-color-text-muted)` |
| `color.selected` | Background of a selected table row and of the selected tab | `var(--px-color-surface-raised)` |
| `color.selected-text` | Text of a selected list item | `var(--px-color-text)` |
| `color.danger-surface` | Background of an error notice | `color-mix(in srgb, var(--px-color-danger) 8%, transparent)` |
| `color.warn-surface` | Background of a warning notice | `color-mix(in srgb, var(--px-color-warn) 8%, transparent)` |
| `color.code-bg` | Background of the configuration source editor, log views and diffs | `var(--px-color-surface-raised)` |
| `color.code-text` | Their text | `var(--px-color-text)` |

A default written as `var(--px-…)` follows another token, so a theme that sets only the older
tokens looks as it did before these existed.

### Type

| Token | Used for | Default |
| --- | --- | --- |
| `font.body` | All text | `system-ui, -apple-system, "Segoe UI", sans-serif` |
| `font.mono` | IDs, versions, code | `ui-monospace, "SF Mono", Menlo, Consolas, monospace` |
| `font.size` | Body text size | `14px` |
| `font.size-small` | Labels, table headings | `12px` |
| `font.size-title` | Page titles | `20px` |
| `font.line-height` | Body line height | `1.45` |
| `font.weight-strong` | Headings, emphasis | `600` |

### Spacing and density

| Token | Used for | Default |
| --- | --- | --- |
| `space.unit` | The spacing step; paddings and gaps are multiples of it | `4px` |
| `density` | Multiplies every spacing step: `0.75` is compact, `1.25` roomy. A number from `0.5` to `2`. | `1` |
| `size.nav` | Width of the side navigation | `200px` |
| `size.width-wide` | Content width for the `wide` width option | `1200px` |
| `size.width-narrow` | Content width for the `narrow` width option | `760px` |
| `size.detail` | Width of the detail pane beside a list | `360px` |

### Shape

| Token | Used for | Default |
| --- | --- | --- |
| `shape.radius` | Corners of cards, panes, buttons and inputs | `8px` |
| `shape.border` | Border width | `1px` |
| `shadow.raised` | Shadow of cards and panes | `0 1px 2px rgb(0 0 0 / 0.06)` |

### Motion

| Token | Used for | Default |
| --- | --- | --- |
| `motion.duration` | Length of hover and pane transitions | `120ms` |
| `motion.easing` | Their timing function | `ease-out` |

When the system asks for reduced motion, the UI sets `motion.duration` to `0ms` whatever the theme
says.

## Layout options

Each option has a fixed set of values, which the UI implements. Keyboard order and what a screen
reader announces stay the same in every layout.

| Option | Values (default first) | What it changes |
| --- | --- | --- |
| `nav` | `sidebar`, `rail`, `top` | Navigation as a labelled side column, a narrow column of short labels, or a bar across the top |
| `list` | `table`, `cards` | Lists of accounts and extensions as table rows or as cards |
| `detail` | `side`, `below` | Where the details of a selected item open: in a pane beside the list, or under it |
| `width` | `wide`, `full`, `narrow` | Content width: `size.width-wide`, the whole window, or `size.width-narrow` |
| `tabs` | `top`, `side` | Where a page's tabs appear: across the top or in a side column |

On a window narrower than 720 pixels, `sidebar` shows as `top` and `side` shows as `below`.

### The user's choices come first

On the Themes page a person can set any layout option and `density` themselves. Their choice
overrides the theme's until they reset it, and stays when they switch themes. A theme never turns
features on, changes permissions, or overrides a choice the person made.

## Appearance

The UI is light or dark. What decides it is the theme's `appearance`:

| `appearance` | Light | Dark | The person's choice |
| --- | --- | --- | --- |
| `both` | light defaults, then `tokens` | dark defaults, then `tokens`, then `dark` | light, dark, or "system", which follows the operating system |
| `light` | light defaults, then `tokens` | not offered | has no effect |
| `dark` | not offered | dark defaults, then `tokens`, then `dark` | has no effect |

A `light` theme with a `dark` field gets a warning, and its `dark` is ignored. The root element
carries `data-appearance="light"` or `data-appearance="dark"`, and the UI sets the CSS
`color-scheme` to match, so form controls and scroll bars follow.

## Custom CSS

A theme's `css` and the person's own CSS (set on the Themes page, applied on top of any theme) are
added after the UI's styles. The UI's own styles sit in the cascade layer `kit`, so any custom rule
beats them without needing higher specificity or `!important`.

What custom CSS cannot do:

- **Load anything from elsewhere.** `@import` is refused. The panel's Content Security Policy
  allows images and fonts only from the panel itself and from `data:` URLs, so `url(https://...)`
  does not load. Embed small images and fonts as `data:` URLs.
- **Run code.** CSS cannot run scripts.

### Hooks you can rely on

These stay stable within a theme contract version:

| Hook | Where |
| --- | --- |
| `data-appearance="light"` or `"dark"` | Root element |
| `class="dark"` | Root element, while the appearance is dark |
| `data-nav`, `data-list`, `data-detail`, `data-width`, `data-tabs` | Root element, set to the active layout value |
| `data-page="<page id>"` | Root element: `status`, `accounts`, `add-account`, `routing`, `quota`, `logs`, `usage`, `setup`, `config`, `system`, `extensions`, `themes`, or an extension page's ID |
| `data-part="shell"` | The whole UI below the root |
| `data-part="nav"` | The navigation |
| `data-part="nav-group"` | A group of navigation entries under its label; `data-group` is `operate`, `gateway`, `observe`, `control` or `extensions` (the extensions' pages) |
| `data-part="nav-item"` | One navigation entry; the current one has `aria-current="page"` |
| `data-part="main"` | The page area |
| `data-part="page-title"` | A page's title |
| `data-part="list"` | A list of items, whichever `list` layout it uses |
| `data-part="item"` | One item in a list; a selected one has `aria-current="true"` |
| `data-part="detail"` | The detail pane |
| `data-part="card"` | A card or panel grouping related content |
| `data-part="button"` | A button; `data-variant` is `primary`, `quiet` or `danger` |
| `data-part="badge"` | A state badge; `data-tone` is `ok`, `warn`, `danger` or `muted` |
| `data-part="field"` | A labelled form control |
| `data-part="meter"` | A quota usage meter; `data-tone` is `ok`, `warn` or `danger`. Its child is the fill; warning and danger use the matching state colour. |
| `data-part="tabs"` | A group of tabs |
| `data-part="tab"` | One tab |
| `data-part="tab-panel"` | The panel shown by the selected tab |
| `data-part="code"` | The configuration source editor |
| `data-part="diff"` | A group of changed lines |
| `data-part="diff-line"` | One diff line; context lines have no change attribute; `data-change` is `add` or `remove` |
| `data-part="dialog"` | A dialog window |
| `data-part="log"` | A core log or request log text view |
| `data-part="log-line"` | One core log line; `data-level` is `info`, `warn` or empty for a line without a level |
| `data-part="toolbar"` | A page's search, filters and actions |
| `data-part="pager"` | Page size and previous/next controls |
| `data-part="select"` | An item's batch selection checkbox |
| `data-part="batch"` | Batch selection and account actions |
| `data-part="account-meta"` | Account totals, active accounts and problems |
| `data-part="upload-results"` | The result of each auth file uploaded on Accounts: the account it became, or why it was not imported |
| `data-part="key-notice"` | A notice revealing a newly generated key until closed or its tab is left |
| `data-part="key-value"` | The selectable value of a newly generated key inside its notice |
| `data-part="method"` | A sign-in method tile with its label, kind caption and selected flow. |
| `data-part="flow"` | A reusable account sign-in flow: inputs, prompts, waiting and terminal states. |
| `data-part="device-code"` | The user code to enter on a device sign-in page. |
| `data-part="tier"` | A Routing priority tier, containing its heading and account list. |
| `data-part="tile"` | One fact on the System page's about card: a label, its value and a note. |
| `data-part="model-group"` | The models of one extension on the System page, under the extension's name and their count. |
| `data-part="stat"` | A figure with its label and hint on the Dashboard (requests handled, success rate, credentials); `data-tone` is `ok`, `warn` or `danger` when the figure has a verdict. |
| `data-part="chart"` | The Dashboard's traffic chart (an SVG). Each bar carries `data-outcome`, `success` or `failure`. |
| `data-part="account-tools"` | Account detail health and tools. |
| `data-part="account-check"` | Account check and recent request health. |
| `data-part="account-cooldowns"` | Account routing cooldown list. |
| `data-part="account-cooldown-confirm"` | Explanation of the local cooldown reset. |
| `data-part="account-tool-actions"` | Account tool buttons. |
| `data-part="account-config-link"` | Link to edit a config account. |
| `data-part="account-test-hint"` | Account test cost and behavior. |
| `data-part="account-models"` | Supported models and test model choice. |
| `data-part="account-test-result"` | The last explicit account test result. |
| `data-part="account-test-reply"` | The start of the test reply. |
| `data-part="account-sign-in"` | Sign in to an existing account again. |
| `data-part="account-settings"` | An account's routing fields and settings, read-only. |
| `data-part="account-edit"` | The Edit form of an account added by login. |
| `data-part="account-edit-row"` | One model or model alias row in the Edit form. |
| `data-part="account-edit-discard"` | The Edit form's question before unsaved changes are discarded. |
| `data-part="subscription-end"` | The Quota card's line with the day a subscription ends. |
| `data-part="privacy-toggle"` | The navigation's privacy mode switch; `aria-pressed="true"` while it masks account e-mail addresses and identifiers, and the root then has `data-privacy="on"`. |
| `data-part="setup-found"` | The Setup page's list of what core found on its machine; `aria-busy="true"` while core looks again. |
| `data-part="setup-pick"` | One provider on the Setup page's checklist; `data-state` is `offered`, `installed` or `failed`. |
| `data-part="setup-client"` | One client on the Setup page; `data-state` is `not-configured`, `configured`, `needs-update`, `refused` or `not-found`. |
| `data-part="setup-paste"` | The text to paste into a client's settings files, open under its client. |

Some parts also carry the `data-slot` name that [T3 Code](https://github.com/pingdotgg/t3code)
gives the same part, and the root's `dark` class is the one T3 Code uses. They exist so that the
CSS of a theme written for T3 Code matches the parts both UIs have:

| T3 Code's name | Where |
| --- | --- |
| `data-slot="button"` | A button (`data-part="button"`) |
| `data-slot="input"` | A text, password, number or search input |
| `data-slot="switch"` | The track of a switch |
| `data-slot="select-trigger"` | A select |
| `data-slot="dialog-popup"` | A dialog window (`data-part="dialog"`) |
| `data-slot="sidebar"` | The navigation (`data-part="nav"`), in every `nav` layout |

Every other class name, element and attribute can change in any release.

## Where themes are kept

Core keeps the themes a person adds and which theme is on, so the panel, the desktop app and the
`pluxy` command show the same ones. Core stores a theme file's JSON as it is given and does not
read it; the UI is what checks a file. Core keeps at most 64 themes.

On the Themes page:

- **Add a theme** takes a theme file, or JSON pasted into the box. The UI checks it first: a file
  it refuses is not stored, and the page says why. A theme that is added is turned on at once.
- **Export** saves the file of the theme that is on as `<name>.json`, a built-in theme's too. The
  file holds the JSON that was added; its spacing and the order of its fields can differ. The
  panel downloads it; the desktop app saves it in your Downloads folder and says where. To add a
  built-in theme's file back as a theme of your own, change its `id` first: the page refuses a
  file with a built-in theme's ID.
- **Remove** deletes the stored theme that is on from core, and the default theme comes back.

A window follows a change made in another window or with `pluxy theme` when it next gets focus
or shows again.

Each device keeps its own appearance (system, light or dark), layout and density choices, the
person's own CSS and safe mode. It also keeps a copy of the theme last applied, so a window opens
in that theme before core answers and on the sign-in page.

### The `pluxy theme` commands

```bash
pluxy theme list                     # the theme that is on, and the stored themes
pluxy theme show <id>                # a stored theme's file
pluxy theme add <file> [--id publisher/name]
pluxy theme set <id>                 # turn a theme on; a built-in theme's ID works too
pluxy theme clear                    # back to the default theme
pluxy theme remove <id>
```

`pluxy theme add` stores the file under `--id`. For a pluxy theme file with `theme_version`, it
uses the file's own `id` when it is `publisher/name`, else `t3/<file name>`. For a T3 Code theme
file with no `theme_version`, it uses `t3/<id>` when `id` is a valid T3 Code theme ID; otherwise it
derives the part after `t3/` from `name` as the Themes page does. When neither `id` nor `name` is
usable, it uses `t3/<file name>`. The command does not check the file as the UI does: a stored file
the UI cannot read is listed on the Themes page with the reason and cannot be turned on. The
built-in themes' IDs are `pluxy/default`, `pluxy/terminal` and `pluxy/paper`.

## Safe mode

Safe mode turns off all custom CSS: the theme's and the person's own. Tokens and layout options
keep working. Use it when custom CSS has hidden or broken the UI. It is kept on each device, not by
core.

- On the Themes page: the "Safe mode" switch.
- If custom CSS hides that switch: open the panel with `?safe` in the address, for example
  `http://127.0.0.1:8400/panel/?safe`. Safe mode then stays on, also after reloading, until it is
  turned off on the Themes page. The panel's sign-in page says so too, with `safeAddress` set on
  the shell: a host whose address cannot take `?safe`, such as the desktop app, leaves it out.

## Changes to this contract

- Adding a token, a layout option, a layout value or a hook does not change `theme_version`. A UI
  that does not know them ignores them, as described above.
- Renaming or removing any of them, or changing what one means, raises `theme_version`. A renamed
  token keeps its old name as an alias for the whole next version.
- A UI refuses a theme whose `theme_version` is newer than it supports and keeps the theme it had.

## T3 Code themes

Theme files written for [T3 Code](https://github.com/pingdotgg/t3code) load without editing. Both
of T3 Code's file shapes are accepted when `theme_version` is absent, `name` is present,
`appearance` is `"light"` or `"dark"`, and either `colors` is an object or both `canvas` and
`accent` are given:

- **Exported files** (`version: 1`, `id`, `name`, `appearance`, `colors`, and optional `canvas`,
  `accent`, `variants`, `collection`, `managed`, `css`).
- **Short files** without `version` or `id`, using `canvas` and `accent`, `colors`, or both.

A T3 Code file may also include pluxy's `tokens`, `dark`, and `layout` fields, and a pluxy
`theme_version: 0` file may include `canvas`, `accent`, `colors`, and `variants`.

### What each field does

- `id`: in a T3 Code file, becomes `t3/<id>` when `id` matches `^[a-z0-9][a-z0-9-]{0,47}$`, or
  `t3/<slug>` derived from `name` otherwise.
- `appearance`: becomes `both` when `variants` holds the other appearance (`dark` for a `light`
  file, or `light` for a `dark` file); otherwise stays `light` or `dark`.
- `canvas` and `accent`: when both are given in `#rgb`, `#rgba`, `#rrggbb`, `#rrggbbaa`,
  `rgb()`/`rgba()`, or `oklch()` form, they generate T3 Code's 61-role palette for the file's own
  `appearance`. A seed in another CSS colour form skips the generated palette with one warning, and
  the theme still loads.
- `colors`: sets roles for the file's own `appearance`, replacing the generated palette role by
  role.
- `variants`: `variants.light` or `variants.dark` sets roles for the other appearance.
- `version`, `collection`, and `managed`: accepted without a warning and ignored.

For one appearance, values apply in order: pluxy's defaults, then the generated palette (only for
the file's own `appearance`), then `colors` (for the file's own `appearance`) or
`variants.<appearance>` (for the other appearance) role by role, then `tokens` (and `dark` when the
appearance is dark).

### Role-to-token map

These 26 T3 Code roles set pluxy tokens:

| T3 Code role | Pluxy token |
| --- | --- |
| `canvas` | `color.bg` |
| `surface` | `color.surface` |
| `surfaceRaised` | `color.surface-raised` |
| `surfaceOverlay` | `color.overlay` |
| `text` | `color.text` |
| `textMuted` | `color.text-muted` |
| `border` | `color.border` |
| `input` | `color.input-border` |
| `placeholder` | `color.placeholder` |
| `focus` | `color.focus` |
| `accent` | `color.accent` |
| `accentForeground` | `color.accent-text` |
| `accentSurface` | `color.selected` |
| `accentSurfaceForeground` | `color.selected-text` |
| `errorForeground` | `color.danger` |
| `errorSurface` | `color.danger-surface` |
| `warningForeground` | `color.warn` |
| `warningSurface` | `color.warn-surface` |
| `codeBackground` | `color.code-bg` |
| `codeForeground` | `color.code-text` |
| `sidebar` | `color.nav` |
| `sidebarForeground` | `color.nav-text` |
| `sidebarMutedForeground` | `color.nav-muted` |
| `sidebarRowHover` | `color.nav-hover` |
| `sidebarRowSelected` | `color.nav-selected` |
| `sidebarBorder` | `color.nav-border` |

### Variables and selectors

While a theme carrying T3 Code fields is active, every role the theme supplies or generates sets
T3 Code's `--app-theme-<kebab-role>` CSS custom property on `:root` (`terminalSelection` sets
`--app-theme-terminal-selection-background`) in the same stylesheet as the `--px-*` tokens. A
theme without T3 Code fields sets none of them.

The remaining 35 roles have no pluxy token and are set only as `--app-theme-*` custom properties:
`chrome`, `toolbar`, `toolbarForeground`, `toolbarBorder`, `toolbarControl`,
`toolbarControlForeground`, `toolbarControlHover`, `secondary`, `secondaryForeground`, `muted`,
`mutedForeground`, `secondaryLabel`, `iconMuted`, `error`, `warning`, `update`, `updateForeground`,
`updateSurface`, `messageSurface`, `messageForeground`, `messageAction`, `messageActionForeground`,
`messageActionHover`, `searchMatchBackground`, `searchMatchForeground`,
`searchMatchActiveBackground`, `searchMatchActiveForeground`, `sidebarControlSurface`,
`sidebarRowActive`, `terminalBackground`, `terminalForeground`, `terminalCursor`,
`terminalSelection`, `terminalScrollbar`, and `terminalScrollbarHover`.

For custom CSS written for T3 Code, `class="dark"` on the root element and the six `data-slot`
attributes listed under [Hooks you can rely on](#hooks-you-can-rely-on) (`button`, `input`,
`switch`, `select-trigger`, `dialog-popup`, and `sidebar`) match here. Every other T3 Code class
name or selector matches nothing.

### Departures from T3 Code

- **Missing roles keep pluxy defaults:** a role a file neither gives nor generates keeps pluxy's
  default token value instead of falling back to T3 Code's built-in default palette.
- **Custom CSS uses pluxy's rules:** CSS over 128 KiB or containing `@import` is dropped with a
  warning, and the panel's Content Security Policy blocks remote loads such as `url(https://...)`.

## Built-in themes

The files in this folder are the themes the UI ships with, and examples of the format:
[`default.json`](default.json), [`terminal.json`](terminal.json) and [`paper.json`](paper.json).

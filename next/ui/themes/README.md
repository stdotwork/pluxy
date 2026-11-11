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
| `theme_version` | yes | The contract version the file is written for. This page describes `0`. |
| `id` | yes | `publisher/name`. Each part is 1 to 64 characters from `a-z`, `0-9` and `-`. |
| `name` | yes | The name people see. 1 to 64 characters. |
| `description` | no | One line, up to 200 characters. |
| `appearance` | no | `both` (the default), `light` or `dark`: the appearances the theme offers. See [Appearance](#appearance). |
| `tokens` | no | Token values, by token name. Missing tokens keep the UI's default value. |
| `dark` | no | Token values that replace `tokens` while the appearance is dark. |
| `layout` | no | The theme's layout options. Missing options keep the UI's default. |
| `css` | no | Custom CSS, up to 128 KiB. |

A theme file is at most 256 KiB.

### What the UI does with a file it cannot fully use

- **Refused, and the UI keeps the theme it had:** the file is not a JSON object, is over 256 KiB,
  has a `theme_version` newer than the UI supports, or has a missing or invalid `id` or `name`.
- **Used, with a warning shown on the Themes page:** an unknown field, token or layout option is
  ignored; a token value that breaks the rules below is ignored and the default is kept; a layout
  value the UI does not offer falls back to that option's default; an unknown `appearance` falls
  back to `both`; custom CSS containing `@import` is dropped as a whole.

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
| `color.diff-add` | Added lines in a diff | `#1f7a3d` / `#5cc98a` |
| `color.diff-remove` | Removed lines in a diff | `#b42318` / `#ff7b72` |

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
| `data-nav`, `data-list`, `data-detail`, `data-width`, `data-tabs` | Root element, set to the active layout value |
| `data-page="<page id>"` | Root element: `status`, `accounts`, `quota`, `config`, `extensions`, `themes`, or an extension page's ID |
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
| `data-part="toolbar"` | A page's search, filters and actions |
| `data-part="pager"` | Page size and previous/next controls |
| `data-part="select"` | An item's batch selection checkbox |
| `data-part="batch"` | Batch selection and account actions |
| `data-part="account-meta"` | Account totals, active accounts and problems |

Every other class name, element and attribute can change in any release.

## Safe mode

Safe mode turns off all custom CSS: the theme's and the person's own. Tokens and layout options
keep working. Use it when custom CSS has hidden or broken the UI.

- On the Themes page: the "Safe mode" switch.
- If custom CSS hides that switch: open the panel with `?safe` in the address, for example
  `http://127.0.0.1:8400/panel/?safe`. Safe mode then stays on, also after reloading, until it is
  turned off on the Themes page.

## Changes to this contract

- Adding a token, a layout option, a layout value or a hook does not change `theme_version`. A UI
  that does not know them ignores them, as described above.
- Renaming or removing any of them, or changing what one means, raises `theme_version`. A renamed
  token keeps its old name as an alias for the whole next version.
- A UI refuses a theme whose `theme_version` is newer than it supports and keeps the theme it had.

## Built-in themes

The files in this folder are the themes the UI ships with, and examples of the format:
[`default.json`](default.json), [`terminal.json`](terminal.json) and [`paper.json`](paper.json).

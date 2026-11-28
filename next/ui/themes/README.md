# Theme contract, version 0

This is the reference for pluxy theme files. The panel that core serves and the desktop app read
the same files, through the same UI kit (`next/ui/kit`).

A theme is one JSON file. It is data: the UI reads its values, and never runs anything from it. A
theme can change three things:

1. **Tokens:** named values for colour, type, spacing, density, shape and motion.
2. **Layout options:** choices the UI implements, such as where the navigation sits or whether
   lists show as a table or as cards.
3. **Custom CSS:** a stylesheet applied on top, for anything tokens and layout options do not
   cover. The Custom CSS switch on the Themes page turns it off.

Tokens and layout options are the stable part of the contract: a theme that only uses them keeps
working as the UI changes. Custom CSS can only rely on the hooks listed under
[Custom CSS](#custom-css); anything else in the UI's markup can change whenever the UI does.

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
| `id` | yes (pluxy files) | `publisher/name`. Each part is 1 to 64 characters from `a-z`, `0-9` and `-`; core refuses to store a theme whose publisher is `current`. Optional in a T3 Code theme, where the ID becomes `t3/<id>` or is derived from `name`. |
| `name` | yes | The name people see. 1 to 64 characters. |
| `description` | no | One line, up to 200 characters. |
| `appearance` | no (yes for T3 Code) | `both` (the default for pluxy files), `light` or `dark`: the appearances the theme offers. See [Appearance](#appearance). In a `both` file, T3 Code's `canvas`, `accent` and `colors` apply to one appearance only: the one `variants` does not cover, else dark when the `canvas` seed (or, without one, `colors.canvas`) is a dark colour, else light. |
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
  generated palette; a `colors` object with no roles is ignored; a `variants` key other than `light`
  or `dark`, or repeating the file's own appearance, is ignored; a layout value the UI does not
  offer falls back to that option's default; an unknown `appearance` falls back to `both`; custom
  CSS containing `@import` is dropped as a whole.

## Tokens

Each token becomes a CSS custom property: the name with dots replaced by dashes and `--px-` in
front, so `color.bg` is `--px-color-bg`. Custom CSS can read them with `var()`.

A token value is a string of 1 to 200 characters. It may not contain `;`, `{`, `}`, `<`, `>`,
`\`, `@`, `/*` or `url(`. These rules keep a value from ending its declaration and from loading
anything.

### Colour

The default colours are zinc neutrals with one blue for actions, the palette T3 Code's own look
uses, so a T3 Code theme file lands on the ground it was written for. Tokens added for a T3 Code
colour role take that role's name (`color.chrome`, `color.message-action`); the
[role-to-token map](#role-to-token-map) lists them all.

| Token | Used for | Default (light / dark) |
| --- | --- | --- |
| `color.bg` | The page's background: the content panel with the `inset` frame, the whole window with `flat` | `#fcfcfc` / `#111113` |
| `color.chrome` | The window's ground, around the content panel with the `inset` frame, and the navigation's background unless `color.nav` is set | `color-mix(in srgb, var(--px-color-bg) 96.5%, black)` / `color-mix(in srgb, var(--px-color-bg) 53%, black)` |
| `color.surface` | Cards, panes, table rows | `#ffffff` / `#161618` |
| `color.surface-raised` | Hover and selected rows, inputs | `#f6f6f7` / `#1c1c1f` |
| `color.text` | Body text | `#18181b` / `#f2f2f4` |
| `color.text-muted` | Labels, secondary text | `#6b6b73` / `#9898a1` |
| `color.secondary-label` | The faintest text: column headings, reset times, the Free plan | `var(--px-color-text-muted)` |
| `color.border` | Borders and dividers | `#e6e6e9` / `#242428` |
| `color.accent` | Selection, links, the current tab's underline | `#2f5bea` / `#4f7bff` |
| `color.accent-text` | Text on `color.accent` | `#ffffff` |
| `color.message-action` | A view's one filled button, and a switch that is on | `var(--px-color-accent)` / `color-mix(in srgb, var(--px-color-accent) 85%, black)` |
| `color.message-action-foreground` | Text on `color.message-action` | `var(--px-color-accent-text)` |
| `color.message-action-hover` | The filled button while hovered | `color-mix(in srgb, var(--px-color-message-action) 88%, black)` |
| `color.ok` | Healthy states: text, and a badge's word | `#15803d` / `#4ade80` |
| `color.warn` | Degraded states: text, and a badge's word | `#b45309` / `#fbbf24` |
| `color.danger` | Errors, destructive actions: text, and a badge's word | `#c42b31` / `#ff7a7e` |
| `color.success` | The solid colour of a healthy state: a badge's dot, a full limit's bar | `#14923f` / `#2fbf6a` |
| `color.success-surface` | Background of a notice with good news | `color-mix(in srgb, var(--px-color-success) 8%, var(--px-color-bg))` / `color-mix(in srgb, var(--px-color-success) 16%, var(--px-color-bg))` |
| `color.warning` | The solid colour of a degraded state: dots and bars | `#c26a06` / `#f5b42a` |
| `color.error` | The solid colour of an error: dots and bars | `#e5484d` / `#f2555a` |
| `color.info` | The solid colour of a neutral notice: its bar and a badge's dot | `#3b82f6` / `#60a5fa` |
| `color.info-foreground` | Text of a neutral notice's badge | `#1d4ed8` / `#93c5fd` |
| `color.info-surface` | Background of a neutral notice | `color-mix(in srgb, var(--px-color-info) 8%, var(--px-color-bg))` / `color-mix(in srgb, var(--px-color-info) 16%, var(--px-color-bg))` |
| `color.update` | The solid colour of an update that is ready | `var(--px-color-info)` |
| `color.update-foreground` | Its text | `var(--px-color-info-foreground)` |
| `color.update-surface` | Its notice's background | `var(--px-color-info-surface)` |
| `color.focus` | Keyboard focus ring | `var(--px-color-accent)` |
| `color.meter-track` | The empty part of a limit's gauge | `color-mix(in srgb, var(--px-color-text) 8%, var(--px-color-surface))` |
| `color.meter-fill` | What is left of a limit, on its gauge, with `meter.mid-below` percent left or more | `var(--px-color-success)` |
| `color.meter-mid` | A gauge under `meter.mid-below` percent left | `var(--px-color-warning)` |
| `color.meter-low` | A gauge under `meter.low-below` percent left, or used up | `var(--px-color-error)` |
| `color.meter-unknown` | The hatching of a gauge whose limit was not read | `color-mix(in srgb, var(--px-color-text) 22%, var(--px-color-surface))` |
| `color.chart-success` | Requests that went through, in the charts of requests | `color-mix(in srgb, var(--px-color-text) 78%, var(--px-color-bg))` |
| `color.chart-failure` | Failed requests, stacked on top | `var(--px-color-error)` |
| `color.chart-1` | The first series of a chart split by provider or model (`data-series="1"`) | `#d97757` / `#e08a6d` |
| `color.chart-2` | The second series | `#5b6bf5` / `#7684f7` |
| `color.chart-3` | The third series | `#4f8bff` / `#6c9eff` |
| `color.chart-4` | The fourth series | `var(--px-color-text)` |
| `color.chart-5` | The fifth series | `#14b8a6` / `#2dd4bf` |
| `color.chart-6` | The sixth series | `#a855f7` / `#b875f8` |
| `color.plan-top` | Background of the plan pill of the largest plans (`data-plan-tier="top"`): a warm metal | `linear-gradient(135deg, color-mix(in srgb, var(--px-color-warning) 20%, var(--px-color-surface)), color-mix(in srgb, var(--px-color-warning) 40%, var(--px-color-surface)))` / `linear-gradient(135deg, color-mix(in srgb, var(--px-color-warning) 26%, var(--px-color-surface)), color-mix(in srgb, var(--px-color-warning) 14%, var(--px-color-surface)))` |
| `color.plan-top-text` | Its text | `color-mix(in srgb, var(--px-color-warning) 35%, var(--px-color-text))` / `color-mix(in srgb, var(--px-color-warning) 55%, var(--px-color-text))` |
| `color.plan-top-border` | Its border | `color-mix(in srgb, var(--px-color-warning) 55%, var(--px-color-surface))` / `color-mix(in srgb, var(--px-color-warning) 40%, var(--px-color-surface))` |
| `color.plan-high` | Background of the next plans' pill (`high`): a cool metal | `linear-gradient(180deg, var(--px-color-surface), color-mix(in srgb, var(--px-color-text) 10%, var(--px-color-surface)))` / `linear-gradient(180deg, color-mix(in srgb, var(--px-color-text) 18%, var(--px-color-surface)), color-mix(in srgb, var(--px-color-text) 8%, var(--px-color-surface)))` |
| `color.plan-high-text` | Its text | `var(--px-color-text)` |
| `color.plan-high-border` | Its border | `color-mix(in srgb, var(--px-color-text) 24%, var(--px-color-surface))` |
| `color.plan-mid-text` | Text of the other plans' pill (`mid`), an outline | `var(--px-color-text)` |
| `color.plan-mid-border` | Its outline | `var(--px-color-input-border)` |
| `color.plan-free-text` | The Free plan (`free`), plain text | `var(--px-color-secondary-label)` |
| `color.diff-add` | Added lines in a diff | `var(--px-color-ok)` |
| `color.diff-remove` | Removed lines in a diff | `var(--px-color-danger)` |
| `color.nav` | Navigation background | `var(--px-color-chrome)` |
| `color.nav-text` | The product name and the current entry in the navigation, and a hovered entry's text | `var(--px-color-text)` |
| `color.nav-muted` | Navigation entries, and the status block's second line | `var(--px-color-text-muted)` |
| `color.nav-hover` | Background of a hovered navigation entry | `color-mix(in srgb, var(--px-color-text) 5%, var(--px-color-nav))` |
| `color.nav-active` | Background of a navigation entry while it is pressed | `color-mix(in srgb, var(--px-color-text) 9%, var(--px-color-nav))` |
| `color.nav-selected` | Background of the current navigation entry, which also has a ring in `color.nav-border` | `var(--px-color-surface)` / `var(--px-color-surface-raised)` |
| `color.nav-border` | The navigation's border, the line over its status block, and the current entry's ring | `var(--px-color-border)` |
| `color.overlay` | Background of dialogs and menus | `var(--px-color-surface)` |
| `color.card` | Background of a card and of the detail pane. `transparent` draws a card as a section: its heading, a hairline and its content | `var(--px-color-surface)` |
| `color.card-border` | Border of a card and of the detail pane (the detail pane always has a `color.border` line on the list's side) | `var(--px-color-border)` |
| `color.input-border` | Border of text inputs, selects, text areas and outlined buttons | `#d4d4d8` / `#34343a` |
| `color.placeholder` | Placeholder text in an empty input | `var(--px-color-text-muted)` |
| `color.selected` | Background of a selected table row and of the selected tab | `var(--px-color-surface-raised)` |
| `color.selected-text` | Text of a selected list item | `var(--px-color-text)` |
| `color.danger-surface` | Background of an error notice, which has a 3px bar in `color.error` at its start | `color-mix(in srgb, var(--px-color-error) 8%, var(--px-color-bg))` / `color-mix(in srgb, var(--px-color-error) 16%, var(--px-color-bg))` |
| `color.warn-surface` | Background of a warning notice, which has a 3px bar in `color.warning` at its start | `color-mix(in srgb, var(--px-color-warning) 8%, var(--px-color-bg))` / `color-mix(in srgb, var(--px-color-warning) 16%, var(--px-color-bg))` |
| `color.code-bg` | Background of the configuration source editor, log views and diffs | `var(--px-color-surface-raised)` |
| `color.code-text` | Their text | `var(--px-color-text)` |
| `color.search-match-background` | Background of text that matches a search (a `mark` element) | `#ffde8b` / `#8c6c1f` |
| `color.search-match-foreground` | Its text | `#21180a` / `#f5f5f5` |

A default written as `var(--px-…)` follows another token, so a theme that does not set it gets a
colour that matches the tokens it does set. A default written as `color-mix(…)` is mixed from
other tokens in the same way: the window's ground is a shade darker than `color.bg`, a tinted
notice is its solid colour faded into `color.bg`, and the plan pills are mixed from
`color.warning` (the largest plans' warm metal) and `color.text` (the next plans' cool one) into
`color.surface`. So a theme that sets only the main colours, a T3 Code theme among them, gets
meters, plan pills, tinted notices and a window ground in its own palette.

A state is never shown by colour alone: a badge, a notice and a gauge always have their words
beside them. `color.ok`, `color.warn`, `color.danger` and `color.info-foreground` are for text and
reach 4.5:1 against `color.bg`, `color.surface` and their own surface; `color.success`,
`color.warning`, `color.error` and `color.info` are for dots and bars and reach 3:1 against
`color.meter-track` and `color.surface`.

### Type

| Token | Used for | Default |
| --- | --- | --- |
| `font.body` | All text | `"Inter Variable", -apple-system, BlinkMacSystemFont, "Segoe UI", system-ui, sans-serif` |
| `font.mono` | IDs, versions, code | `"IBM Plex Mono", "SF Mono", ui-monospace, Menlo, Consolas, monospace` |
| `font.size` | Body text size | `14px` |
| `font.size-small` | Secondary lines, table headings | `12px` |
| `font.size-label` | The smallest text: chart labels and capital-letter labels | `11px` |
| `font.size-title` | Page titles, in the header strip | `14px` |
| `font.size-mono` | Monospaced values inside text, such as addresses, IDs and versions | `13px` |
| `font.line-height` | Body line height | `1.4` |
| `font.weight-medium` | Buttons, navigation entries, plan pills | `500` |
| `font.weight-strong` | Headings, emphasis | `600` |

The UI ships Inter (as one variable font, weights 100 to 900) and IBM Plex Sans (weights 400, 500
and 600) and IBM Plex Mono (400 and 500), Latin characters only, as files of its own: the default
`font.body` and `font.mono` look the same on every machine and load nothing from elsewhere, and a
theme can name `"IBM Plex Sans"` too. A character they lack comes from the device's own fonts, as
does a family a theme names that the device has. Digits all have one width, so numbers in a column
line up.

### Spacing and density

| Token | Used for | Default |
| --- | --- | --- |
| `space.unit` | The spacing step; paddings and gaps are multiples of it | `4px` |
| `density` | Multiplies every spacing step: `0.75` is compact, `1.25` roomy. A number from `0.5` to `2`. | `1` |
| `size.nav` | Width of the side navigation | `232px` |
| `size.rail` | Width of the navigation as a rail of icons | `48px` |
| `size.header` | Height of the header strip over the page, and of the product name's row in the navigation. The strip grows when its content needs more | `48px` |
| `size.topics` | Width of the list of topics beside a page | `180px` |
| `size.row` | Height of a one-line row in a list | `32px` |
| `size.row-tall` | Height of a row with a second line under its name, such as a reason | `40px` |
| `size.control` | Height of buttons, inputs, selects and segmented choices | `28px` |
| `size.control-main` | Height of a page's one main action | `32px` |
| `size.width-wide` | Content width for the `wide` width option | `1240px` |
| `size.width-narrow` | Content width for the `narrow` width option | `880px` |
| `size.detail` | Width of the detail pane beside a list | `380px` |
| `size.card-padding` | Room inside a card's border. `0px`, with `color.card` and `color.card-border` set to `transparent`, draws cards as sections instead of boxes | `14px` |
| `size.frame-gap` | Room between the window's edge, the navigation and the content panel with the `inset` frame | `8px` |

`density` does not change the four heights (`size.row`, `size.row-tall`, `size.control` and
`size.control-main`): a theme that wants shorter rows sets them. Buttons, inputs, selects, a
`Choices` group, topics and the header strip's buttons are `size.control` high; the page's main
action in the header strip and the navigation's destinations are `size.control-main`. A table row is
`size.row` high, and its cells' top and bottom padding is half the difference between `size.row-tall`
and `size.row`, so a row whose text takes two lines comes out at about `size.row-tall`. Every
control stays at least 24 pixels to hit, whatever these are set to.

### Shape

| Token | Used for | Default |
| --- | --- | --- |
| `shape.radius` | Corners of buttons and inputs; every other corner is a multiple of it | `8px` |
| `shape.border` | Border width | `1px` |
| `shape.icon-stroke` | Line width of the icons, in pixels of a 16-pixel icon | `1.75` |
| `shadow.raised` | Shadow of cards and panes | `0 1px 2px rgb(0 0 0 / 0.04)` / `none` |
| `shadow.panel` | Shadow of the content panel with the `inset` frame | `0 0 0 1px rgb(0 0 0 / 0.04), 0 1px 2px rgb(0 0 0 / 0.04), 0 8px 24px -12px rgb(0 0 0 / 0.1)` / `0 0 0 1px rgb(255 255 255 / 0.04), 0 12px 32px -16px rgb(0 0 0 / 0.7)` |
| `shadow.overlay` | Shadow of menus | `0 16px 40px -18px rgb(0 0 0 / 0.55)` / `0 16px 40px -18px rgb(0 0 0 / 0.8)` |
| `shadow.dialog` | Shadow of dialogs | `0 24px 64px -24px rgb(0 0 0 / 0.65)` |

Corners follow `shape.radius`: chips and small marks have half of it, small buttons three quarters,
cards one and a half times, the content panel one and three quarters, dialogs twice, and pills
are round. `0px` makes every corner square.

### Motion

| Token | Used for | Default |
| --- | --- | --- |
| `motion.duration` | Length of hover and press transitions | `120ms` |
| `motion.duration-menu` | Length of a menu opening | `160ms` |
| `motion.duration-slow` | Length of a pane or dialog opening | `240ms` |
| `motion.easing` | The timing function of the first two | `cubic-bezier(0.2, 0, 0, 1)` |
| `motion.easing-drawer` | The timing function of anything that slides in | `cubic-bezier(0.32, 0.72, 0, 1)` |
| `motion.press-scale` | How far a button shrinks while pressed | `0.97` |

When the system asks for reduced motion, the UI sets `motion.duration`, `motion.duration-menu` and
`motion.duration-slow` to `0ms` and `motion.press-scale` to `1` whatever the theme says. Nothing
animates when the theme or the appearance changes: while a new theme is applied, the root element
carries `data-theme-switching` and transitions are off.

### Limits

| Token | Used for | Default |
| --- | --- | --- |
| `meter.mid-below` | A limit's gauge turns `color.meter-mid` under this share left, in percent | `60` |
| `meter.low-below` | It turns `color.meter-low` under this share left, in percent | `20` |

Both are numbers from `0` to `100`.

## Layout options

Each option has a fixed set of values, which the UI implements. Keyboard order and what a screen
reader announces stay the same in every layout.

| Option | Values (default first) | What it changes |
| --- | --- | --- |
| `frame` | `inset`, `flat` | The page on its own rounded panel in `color.bg`, inset from the window's ground in `color.chrome` with the navigation on that ground, or the page flush with the window and the navigation beside it |
| `nav` | `sidebar`, `rail`, `top` | Navigation as a labelled side column, a narrow column of icons, or a row across the top |
| `list` | `table`, `cards` | Lists of accounts and extensions as table rows or as cards |
| `detail` | `side`, `below` | Where the details of a selected item open: in a pane beside the list, or under it |
| `width` | `wide`, `full`, `narrow` | Content width: `size.width-wide`, the whole window, or `size.width-narrow` |
| `tabs` | `top`, `side` | Where tabs appear. A page's own tabs: across its top or in a side column. A destination's tabs: in the header strip or in a list beside the page |

The window is a frame that does not scroll: the navigation, the header strip over the page and
the status block at the navigation's foot stay where they are, and only the page area scrolls.
With the `inset` frame the header strip sits at the top of the content panel; under 820 pixels wide
the panel loses its inset and corners and the page fills the window beside the rail.

A narrow window overrides `nav`, and the `data-part="shell"` element's `data-frame` says what is
drawn. Under 820 pixels wide the navigation is the rail whatever the theme says, topics become a select
above the page, and a destination's tabs go back to the header strip even with `tabs: side`. Under 560 pixels the
navigation is a row of icons across the top (`data-frame="bar"`). Under 720 pixels `side` shows as
`below`.

### The user's choices come first

On the Themes page a person can set any layout option and `density` themselves. Their choice
overrides the theme's until they reset it, and stays when they switch themes. A theme never turns
features on, changes permissions, or overrides a choice the person made.

## Appearance

The UI is light or dark. What decides it is the theme's `appearance`:

| `appearance` | Light | Dark | The person's choice |
| --- | --- | --- | --- |
| `both` | light defaults, then `tokens` | dark defaults, then `tokens`, then `dark` | Light, Dark or Follow the system (General's Appearance setting) |
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

When these hooks can change is in [Changes to this contract](#changes-to-this-contract).

| Hook | Where |
| --- | --- |
| `data-appearance="light"` or `"dark"` | Root element |
| `class="dark"` | Root element, while the appearance is dark |
| `data-frame`, `data-nav`, `data-list`, `data-detail`, `data-width`, `data-tabs` | Root element, set to the active layout value (the shell's own `data-frame`, below, is another attribute on another element) |
| `data-theme-switching` | Root element, while a new theme or appearance is being applied; transitions are off |
| `data-series="1"` to `"6"` | A mark in a chart split by provider or model; the kit paints it in `color.chart-1` to `color.chart-6` |
| `data-page="<page id>"` | Root element: `status`, `accounts`, `account` (one account's page), `add-account`, `routing`, `logs`, `usage` (Requests), `activity/usage` (Usage), `tools`, `settings` (General), `setup`, `config`, `system`, `extensions`, `themes`, a desktop app page's ID, or an extension page's ID; `signin` while signed out |
| `data-part="shell"` | The whole UI below the root. Signed in, `data-frame` says how the navigation is drawn: `sidebar`, `rail` or `top` (the `nav` option), or `bar` in a very narrow window; `data-narrow` is set while the window is under 820 pixels wide |
| `data-part="nav"` | The navigation |
| `data-part="nav-idle"` | The navigation drawn where nothing in it can be chosen yet: the desktop app before it is connected. It has the same content and the same `data-slot` |
| `data-part="brand"` | The product name at the navigation's start |
| `data-part="nav-item"` | One of the navigation's destinations; `data-dest` is `home`, `accounts`, `activity`, `tools` or `settings`. The current one has `aria-current="page"`; one that cannot be chosen has `aria-disabled="true"` |
| `data-part="icon"` | An icon (an SVG drawn in the text's colour), such as a destination's |
| `data-part="status"` | The status block at the navigation's foot: a dot, a word for pluxy's state and where it runs; `data-state` is `running`, `down`, `stopped` or `unknown` |
| `data-part="status-menu"` | The menu the status block opens |
| `data-part="header"` | The header strip over the page |
| `data-part="page-title"` | The page's title, in the header strip |
| `data-part="header-tabs"` | A destination's tabs in the header strip |
| `data-part="header-tab"` | One of them; the current one has `aria-current="page"` |
| `data-part="header-actions"` | The page's main actions, at the header strip's end |
| `data-part="privacy-toggle"` | The header strip's Names hidden button, drawn only while names are hidden (the Hide names setting in General turns that on); `aria-pressed="true"`, and the root then has `data-privacy="on"`. |
| `data-part="topics"` | The list of a destination's topics beside the page |
| `data-part="nav-group"` | A group of topics under its label; `data-group` is `core`, `device` or `extensions` (the extensions' pages) |
| `data-part="topic"` | One topic; the current one has `aria-current="page"` |
| `data-part="topics-select"` | The select that replaces a list beside the page in a narrow window |
| `data-part="main"` | The page area, the one part that scrolls |
| `data-part="list"` | A list of items, whichever `list` layout it uses |
| `data-part="list-frame"` | The box a `table` list scrolls in. When the table is wider than the box, `data-more` is `start`, `end` or `both`: the sides with columns out of view, which fade out |
| `data-part="item"` | One item in a list; a selected one has `aria-current="true"` |
| `data-part="detail"` | The detail pane |
| `data-part="card"` | A card or panel grouping related content |
| `data-part="settings-section"` | A section of the Themes page: its heading, a hairline and what it holds. Always a hairline section, never a box |
| `data-part="button"` | A button; `data-variant` is `primary` (a view's main action, filled), `quiet` (the ordinary button, outlined), `ghost` (no border until hovered) or `danger` (outlined, in the danger colour) |
| `data-part="badge"` | A state badge; `data-tone` is `ok`, `warn`, `danger`, `muted`, `info` or `update` |
| `data-part="field"` | A labelled form control |
| `data-part="meter"` | A limit's gauge of what is left, beside "{n}% left": full is plenty. `data-mode` is `left`; `data-tone` is `ok`, `warn` (under `meter.mid-below` percent left) or `danger` (under `meter.low-below` percent left, or used up), and `none` on a gauge whose limit was not read, which is hatched. Its child is the fill, in `color.meter-fill`, `color.meter-mid` or `color.meter-low`. |
| `data-part="tabs"` | A group of tabs |
| `data-part="tab"` | One tab |
| `data-part="tab-panel"` | The panel shown by the selected tab |
| `data-part="code"` | The configuration source editor |
| `data-part="diff"` | A group of changed lines |
| `data-part="diff-line"` | One diff line; context lines have no change attribute; `data-change` is `add` or `remove` |
| `data-part="dialog"` | A dialog window |
| `data-part="log"` | A core log or request log text view |
| `data-part="log-line"` | One core log line; `data-level` is `info`, `warn`, `error` or empty for a line without a level |
| `data-part="select"` | An item's batch selection checkbox |
| `data-part="batch"` | All accounts' bar of actions for the selected accounts, while Select is on |
| `data-part="upload-results"` | The result of each CLIProxyAPI auth file imported on Add an account: the account it became, or why it was not added |
| `data-part="key-notice"` | A notice revealing a newly generated key until closed or its tab is left |
| `data-part="key-value"` | The selectable value of a newly generated key inside its notice |
| `data-part="method"` | One way to sign in on Add an account: its name, how it signs in and Start; its sign-in shows under it once started. |
| `data-part="flow"` | A reusable account sign-in flow: inputs, prompts, waiting and terminal states. |
| `data-part="device-code"` | The user code to enter on a device sign-in page. |
| `data-part="tier"` | One group of a provider's accounts on Order & fallback (First, Backup 1…), with its name, its priority and its accounts; `data-priority` is its priority |
| `data-part="order-row"` | One account in a group on Order & fallback: its handle, mark, name, condition, weight and arrows; `data-condition` is its condition, as on its Accounts row |
| `data-part="fallback"` | One fallback on Order & fallback: the model a tool asks for, its steps in order, Edit and Remove; `data-open` while the editor shows it |
| `data-part="fallback-editor"` | The form that makes or changes a fallback, under the list of fallbacks |
| `data-part="tile"` | One fact on the System page's about card: a label, its value and a note. |
| `data-part="model-group"` | The models of one extension on the System page, under the extension's name and their count. |
| `data-part="days"` | Home's chart of the last 7 days: seven day bars, each with its succeeded part and its failed part on top. |
| `data-part="account-tools"` | An account's models, test request and Renew sign-in now, under its page's "Settings, models and a test request". |
| `data-part="account-test-hint"` | Account test cost and behavior. |
| `data-part="account-models"` | Supported models and test model choice. |
| `data-part="account-test-result"` | The last explicit account test result. |
| `data-part="account-test-reply"` | The start of the test reply. |
| `data-part="account-sign-in"` | Sign in to an existing account again. |
| `data-part="account-settings"` | An account's routing fields and settings, read-only. |
| `data-part="account-edit"` | The Edit form of an account added by login. |
| `data-part="account-edit-row"` | One model or model alias row in the Edit form. |
| `data-part="account-edit-discard"` | The Edit form's question before unsaved changes are discarded. |
| `data-part="focus"` | The focused window with no navigation (shown while checking Setup, and by sign-in, first launch and first-run Setup). |
| `data-part="focus-brand"` | pluxy's mark and name at the start of the focused window's bar. |
| `data-part="focus-card"` | The centred card inside the focused window. |
| `data-part="signin-where"` | The line under the panel's sign-in card saying where the management key is kept. |
| `data-part="first-launch"` | The desktop app's first launch focused window. |
| `data-part="first-choice"` | One of the two stacked answers on the app's first launch card; `data-picked` is set on the picked one. |
| `data-part="setup-focus"` | Setup's focused window on a first run. |
| `data-part="setup-stepper"` | The row of three step buttons above Setup's card on a first run; each item's `data-state` is `open`, `done` or `todo`. |
| `data-part="setup-later"` | Home's "Finish setting up pluxy" card after Finish later. |
| `data-part="setup-step"` | One of the Setup page's three steps; `data-state` is `open`, `done` or `todo`. |
| `data-part="setup-import"` | The line above the Setup steps when a CLIProxyAPI setup was found; `data-state` is `found`, `preview`, `done` or `imported`. |
| `data-part="setup-report"` | What importing that setup would do, or did, in three folded lists; `data-state` is `preview` or `done`. |
| `data-part="setup-pick"` | One kind of account in Setup's step 1; `data-state` is `offered`, `ticked`, `added`, `stopped` or `failed`. |
| `data-part="setup-test"` | The passed test request in Setup's step 3; `data-state` is `passed`. |
| `data-part="account-page"` | One account's own page; `data-group` is its group, as on its row |
| `data-part="account-opening"` | The top of an account's page: its provider and plan, its condition and why, and its one fix |
| `data-part="account-more"` | An account's "Settings, models and a test request" disclosure |
| `data-part="auth-import"` | Add an account's "Have auth files from CLIProxyAPI?" line and its results |
| `data-part="round-notice"` | The notice above a page's lists when a round of reads did not all work: what could not be read, and Try again |
| `data-part="method-group"` | One provider on Add an account, under its name: its ways to sign in, each a row with Start |
| `data-part="more-options"` | A sign-in's optional inputs, folded under More options |
| `data-part="login-details"` | The Details fold of a sign-in that ended with a message from pluxy, such as signing in to a different account |
| `data-part="account-added"` | What Add an account shows once the sign-in is done: Added, the optional Name, Save name, Open account and Add another |
| `data-part="account-row"` | One account on one line, the same on every page: its group's mark, name, provider, plan and limits, and why and its fix when it needs the person or waits. `data-group` is `needs`, `waiting`, `serving` or `off`; `data-condition` is `serving`, `cooling`, `exhausted`, `needs_login`, `banned`, `broken`, `no_provider` or `off`, or `unchecked` when the status that says whether its provider runs could not be read (its mark then has `data-group="unchecked"`); `data-tall` is set on a row that says why under the name; `data-wide` on the Accounts list's wider row |
| `data-part="request-row"` | One request on one line: its time, model, account, key, tokens, time taken and result. `data-failed` is set on a failed one; `data-open` while it shows its details; `data-compact` on Home's shorter row, a link to the request |
| `data-part="setting-row"` | One setting: its name and what it does beside its control. `data-disabled` is set while the control cannot be used |
| `data-part="tool-row"` | One coding tool: its name, version, state and settings file. `data-state` is `configured`, `not-configured`, `needs-update`, `refused`, `not_installed` or `unchecked` |
| `data-part="state-pane"` | What a whole pane shows while it loads, when it is empty, or when it could not be read; `data-kind` is `loading`, `empty` or `failed` |
| `data-part="plan-pill"` | An account's plan as a word; `data-plan-tier` is `top` (the largest plans: Max 20x, Ultra, API), `high` (the next: Max 5x), `mid` (any other plan) or `free`. The default theme draws them as a warm metal pill, a cool metal pill, an outline and plain text, from the `color.plan-*` tokens; never in a state's colour |
| `data-part="limits"` | An account's limits on its row; `data-state` is `read`, `unread`, `reading`, `none` or `failed` |
| `data-part="activity"` | The body of the Requests and Usage tabs |
| `data-part="activity-filters"` | The Requests tab's filter row: All or Failed, then the lists of providers, accounts, models and keys |
| `data-part="usage-chart"` | The Usage tab's chart of requests, a bar per hour or per day; its bars carry `data-outcome` (`success` or `failure`) |
| `data-part="usage-chip"` | The one filter a breakdown click set on the Usage tab, a button that removes it |
| `data-part="usage-numbers"` | The table behind the Usage chart, shown on request |
| `data-part="usage-breakdown"` | The Usage tab's breakdown table by provider, model, account or key |
| `data-part="tool-steps"` | The numbered steps under a coding tool's row on the Tools page: make a key, put the text in the settings file, start the tool again |
| `data-part="tool-key"` | One key on the Tools page: its name, its use this week, Rename and Delete |

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

Every other class name, element and attribute can change whenever the UI does.

## Where themes are kept

Core keeps the themes a person adds and which theme is on, so the panel, the desktop app and the
`pluxy` command show the same ones. Core stores a theme file's JSON as it is given, and reads it
only to check that it is one UTF-8 JSON object of at most 256 KiB and to take its `name` for the
list; the UI is what checks the rest of a file. Core keeps at most 64 themes, and refuses to store
one under a built-in theme's ID.

On the Themes page:

- **Add a theme** takes a theme file, or JSON pasted into the box. The UI checks it first: a file
  it refuses is not stored, and the page says why. A theme that is added is turned on at once.
- **Save theme file** saves the file of the theme that is on, a built-in theme's too, named after the part of its ID
  after `/` (`pluxy/paper` saves as `paper.json`). The
  file holds the JSON that was added; its spacing and the order of its fields can differ. The
  panel downloads it; the desktop app saves it in your Downloads folder and says where. To add a
  built-in theme's file back as a theme of your own, change its `id` first: the page refuses a
  file with a built-in theme's ID.
- **Remove** deletes the stored theme that is on from core, and the default theme comes back.

A window follows a change made in another window or with `pluxy theme` when it next gets focus
or shows again.

Each device keeps its own appearance (system, light or dark), layout and density choices, the
person's own CSS and whether custom CSS is on. It also keeps a copy of the theme last applied, so a window opens
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

`pluxy theme add` does not turn the theme on. It prints the ID alone on standard output, so it can
be piped, and unless that theme is already on, one line on standard error gives the
`pluxy theme set` command to run.

`pluxy theme set` takes a stored theme or a built-in theme. Any other ID is refused and the theme
that is on stays on, except an ID whose publisher is `pluxy`: a newer panel or desktop app may hold
a built-in theme this core does not list, so core accepts it, and a window that does not hold that
theme shows the default.

## Turning custom CSS off

With the Custom CSS switch off, all custom CSS is off: the theme's and the person's own. Tokens and
layout options keep working. Use it when custom CSS has hidden or broken the UI. It is kept on each
device, not by core. (The page does not call it safe mode: that is pluxy's own mode with no
extensions, shown on System.)

- On the Themes page: the "Custom CSS" switch.
- If custom CSS hides that switch: open the panel with `?safe` in the address, for example
  `http://127.0.0.1:8317/panel/?safe`. Custom CSS then stays off, also after reloading, until the
  switch on the Themes page is turned on. The panel's sign-in page says so too, with `safeAddress` set on
  the shell: a host whose address cannot take `?safe`, such as the desktop app, leaves it out.

## Changes to this contract

- Adding a token, a layout option, a layout value or a hook does not change `theme_version`. A UI
  that does not know them ignores them, as described above.
- Renaming or removing any of them, or changing what one means, raises `theme_version`. A renamed
  token keeps its old name as an alias for the whole next version. The one exception: while
  `theme_version` is 0, a page that is rebuilt can rename or drop its own hooks without raising it.
- A UI refuses a theme whose `theme_version` is newer than it supports and keeps the theme it had.

## T3 Code themes

Theme files written for [T3 Code](https://github.com/pingdotgg/t3code) load without editing. Both
of T3 Code's file shapes are accepted when `theme_version` is absent, `name` is present,
`appearance` is `"light"` or `"dark"`, and either `colors` is an object or both `canvas` and
`accent` are given:

- **Exported files** (`version: 1`, `id`, `name`, `appearance`, `colors`, and optional `canvas`,
  `accent`, `variants`, `collection`, `managed`, `css`).
- **Short files** without `version` or `id`, using `canvas` and `accent`, `colors`, or both.

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

These 39 T3 Code roles set pluxy tokens. As in T3 Code, a filled button takes
`messageAction`, not `accent`, and secondary text takes `mutedForeground`, not `textMuted`:

| T3 Code role | Pluxy token |
| --- | --- |
| `canvas` | `color.bg` |
| `chrome` | `color.chrome` |
| `surface` | `color.surface` |
| `surfaceRaised` | `color.surface-raised` |
| `surfaceOverlay` | `color.overlay` |
| `text` | `color.text` |
| `mutedForeground` | `color.text-muted` |
| `secondaryLabel` | `color.secondary-label` |
| `border` | `color.border` |
| `input` | `color.input-border` |
| `placeholder` | `color.placeholder` |
| `focus` | `color.focus` |
| `accent` | `color.accent` |
| `accentForeground` | `color.accent-text` |
| `accentSurface` | `color.selected` |
| `accentSurfaceForeground` | `color.selected-text` |
| `messageAction` | `color.message-action` |
| `messageActionForeground` | `color.message-action-foreground` |
| `messageActionHover` | `color.message-action-hover` |
| `error` | `color.error` |
| `errorForeground` | `color.danger` |
| `errorSurface` | `color.danger-surface` |
| `warning` | `color.warning` |
| `warningForeground` | `color.warn` |
| `warningSurface` | `color.warn-surface` |
| `update` | `color.update` |
| `updateForeground` | `color.update-foreground` |
| `updateSurface` | `color.update-surface` |
| `codeBackground` | `color.code-bg` |
| `codeForeground` | `color.code-text` |
| `searchMatchBackground` | `color.search-match-background` |
| `searchMatchForeground` | `color.search-match-foreground` |
| `sidebar` | `color.nav` |
| `sidebarForeground` | `color.nav-text` |
| `sidebarMutedForeground` | `color.nav-muted` |
| `sidebarRowHover` | `color.nav-hover` |
| `sidebarRowActive` | `color.nav-active` |
| `sidebarRowSelected` | `color.nav-selected` |
| `sidebarBorder` | `color.nav-border` |

### Variables and selectors

While a theme carrying T3 Code fields is active, every role the theme supplies or generates sets
T3 Code's `--app-theme-<kebab-role>` CSS custom property on `:root` (`terminalSelection` sets
`--app-theme-terminal-selection-background`) in the same stylesheet as the `--px-*` tokens. A
theme without T3 Code fields sets none of them.

The remaining 22 roles have no pluxy token and are set only as `--app-theme-*` custom properties:
`toolbar`, `toolbarForeground`, `toolbarBorder`, `toolbarControl`, `toolbarControlForeground`,
`toolbarControlHover`, `textMuted`, `secondary`, `secondaryForeground`, `muted`, `iconMuted`,
`messageSurface`, `messageForeground`, `searchMatchActiveBackground`,
`searchMatchActiveForeground`, `sidebarControlSurface`, `terminalBackground`, `terminalForeground`,
`terminalCursor`, `terminalSelection`, `terminalScrollbar`, and `terminalScrollbarHover`.

A T3 Code theme has no roles for pluxy's own meanings, so they follow the roles it does give:
`color.success` and `color.info` keep pluxy's colours (T3 Code keeps success fixed too), the gauges
take `success`, `warning` and `error`, the largest plans' pill is mixed from `warning` into the
theme's `surface`, and the chart's successful requests from its `text`.

For custom CSS written for T3 Code, `class="dark"` on the root element and the `data-slot`
attributes listed under [Hooks you can rely on](#hooks-you-can-rely-on) match here. Every other T3 Code class
name or selector matches nothing.

### Departures from T3 Code

- **Missing roles keep pluxy defaults:** a role a file neither gives nor generates keeps pluxy's
  default token value instead of falling back to T3 Code's built-in default palette.
- **Custom CSS uses pluxy's rules** ([Custom CSS](#custom-css)), not T3 Code's.

## Built-in themes

The files in this folder are the themes the UI ships with, and examples of the format:
[`default.json`](default.json), [`terminal.json`](terminal.json) and [`paper.json`](paper.json).

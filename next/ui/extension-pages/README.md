# Extension pages, version 0

An extension package can bring its own pages for the panel (and later the desktop app), and a
settings form the panel draws for it. This is the contract for both: what goes in the package,
how core serves the pages, what a page may do, and the message channel a page talks to the
panel through.

The short version: a page is a document from the extension's own files, shown in a sandboxed
frame from a **separate site**. It cannot reach the panel, core's management API, the network
or storage. It can only ask the panel, over a message channel, for the few things its manifest
declares. It never receives the management key.

## What a page can and cannot do

A page **can**:

- run its own scripts, styles, images and fonts from its package's `ui` folder;
- ask the panel for the current theme, and get told when it changes;
- read and change its own extension's settings, when its manifest asks for that permission.

A page **cannot**, whatever its code does:

- read the panel's document, storage, cookies or the management key: it runs on another site
  with an opaque origin, so the browser gives it no access to the panel's window;
- call core's management API: it holds no key, and its Content Security Policy allows it no
  requests at all;
- send data anywhere by `fetch`, XHR, WebSocket, EventSource, `sendBeacon`, images, styles,
  fonts, forms, workers or frames: its policy allows loading only its own package files, and
  no requests from script;
- navigate the panel, open windows or pop-ups, submit forms, or show dialogs (`alert`,
  `confirm`, `prompt`): the frame's sandbox does not allow them;
- navigate its own frame away: the panel's policy lets its frames show only extension pages, so
  the browser refuses to load any other address in the frame; whenever the frame gets another
  document (an error page, or the page reloading itself), the panel closes the page's channel;
- read the clipboard: the frame is given no permissions (`allow=""`);
- keep anything in storage, or read another extension's settings: there is no method for it.

What a page receives through the channel (the theme and its own settings) it can show, change
and lose; anything it receives should be treated as readable by the extension's author. The
panel never passes it anything else. Browsers do not let a page's policy cover everything a
page can do: WebRTC, for one, is outside Content Security Policy. So "a page cannot send data
anywhere" holds for the channels above, not as a promise that no browser feature can ever leak
what the page was given. Nothing secret is given.

## In the package

A package with pages or settings adds `ui` and `config` to its manifest (see the
[manifest](../../protocol/README.md#package-manifest)). Each has its own version, separate from
the manifest's and the protocol's.

```json
{
  "manifest_version": 0,
  "id": "example/usage",
  "version": "0.1.0",
  "protocol": [0],
  "programs": {"linux/amd64": "bin/usage"},
  "ui": {
    "version": 0,
    "dir": "ui",
    "permissions": ["config.read"],
    "pages": [
      {"id": "usage", "title": "Usage", "short": "Usage", "path": "index.html"}
    ]
  },
  "config": {
    "version": 0,
    "schema": {
      "type": "object",
      "properties": {
        "endpoint": {"type": "string", "title": "Endpoint", "maxLength": 200},
        "interval": {"type": "integer", "title": "Interval (seconds)", "minimum": 1, "maximum": 3600, "default": 60},
        "unit": {"type": "string", "title": "Unit", "enum": ["tokens", "requests"], "default": "tokens"},
        "detailed": {"type": "boolean", "title": "Detailed view", "default": false},
        "token": {"type": "string", "title": "Export token", "writeOnly": true}
      },
      "required": ["endpoint"]
    }
  }
}
```

### `ui`

| Field | Meaning |
| --- | --- |
| `version` | Version of this contract: the page rules and the message channel. Version 0 is described here. Core refuses a manifest with a version it does not serve. |
| `dir` | The folder, inside the package, that holds every file the pages load. Core serves nothing outside it. It may hold only regular files and folders, at most 1,000 files and 32 MiB in all. |
| `permissions` | What the pages may ask the panel for, beyond the theme: `config.read` (read this extension's settings) and `config.write` (change them). Anything else is refused at install. A page that calls a method its extension did not declare gets an error. |
| `pages` | 1 to 20 pages, each shown as its own entry in the navigation. |

Each page:

| Field | Meaning |
| --- | --- |
| `id` | 1 to 32 lower-case letters, digits and `-`, unique in the package. The page's address in the panel is `#/ext/<publisher>/<name>/<id>`. |
| `title` | The page's name in the navigation and its heading, 1 to 60 characters. |
| `short` | A label of up to 8 characters, for the rail navigation. |
| `path` | The page's HTML file, relative to `dir`, ending in `.html`. |

The files under `dir` count toward the package's fingerprint, as the manifest and program do:
core refuses to run an installed package whose pages changed since it was installed.

### `config`

`schema` describes the extension's settings as a small part of
[JSON Schema](https://json-schema.org/): one object of named fields, no nesting. The panel draws
a form from it on the extension's page under Extensions, and core checks every change against
it, so the form and a page that writes settings get the same answer.

| Keyword | Where | Meaning |
| --- | --- | --- |
| `type` | top level | `"object"`. |
| `properties` | top level | The fields, in the order the form shows them: 1 to 50, named with 1 to 64 lower-case letters, digits and `_`, starting with a letter. |
| `required` | top level | Fields that must have a value. |
| `type` | field | `"string"`, `"integer"`, `"number"` or `"boolean"`. |
| `title`, `description` | field | The form's label and hint. |
| `default` | field | The value a field has until it is set. It must pass the field's own checks. |
| `enum` | string field | The values allowed; the form shows a choice. |
| `minLength`, `maxLength` | string field | Length in characters. Strings are at most 4,096 characters even without `maxLength`. |
| `minimum`, `maximum` | number and integer fields | Bounds, inclusive. |
| `writeOnly` | field | A secret, such as a token: it can be set and cleared, but core never shows its value again, to the panel or to a page. The form shows whether it is set. |

Any other keyword is refused at install, so a schema never means something core does not
check. Settings are kept by core, per extension, in `{packages.dir}/.settings/<publisher>/<name>.json`
(readable only by core's user), and survive upgrades: a field the new version no longer
declares is dropped the next time the settings change. Core does not send settings to the
extension's program yet; in version 0 they are for the extension's pages.

## How core serves pages

Core serves extension pages from a listener of their own, the **pages site**, never from the
address that serves the panel and the management API. The pages site serves only the files of
the pages of extensions that run now, for `GET` and `HEAD`:

```
<pages origin>/<publisher>/<name>/<fingerprint>/<path under ui.dir>
```

`<fingerprint>` names the version of the files, so a page of a replaced version is no longer
served and the panel loads the new one. A request for anything else is not found.

**Why another site, not just another port.** A browser sends a site's cookies to every port of
that host, and Chromium keeps documents of the same site (any port) in the same process. Pages
on another port of the panel's host would get the panel's cookies sent with their requests and
could share its process. So the panel only shows pages from a different host name than its own:
by default, a panel opened at `127.0.0.1` shows pages from `localhost` on the pages port, and a
panel at `localhost` shows them from `127.0.0.1`. When core is reached by any other name (a
LAN address or a domain behind a proxy), set `extension_pages.url` to an origin on another host
name that reaches the pages listener, such as `https://pages.pluxy.example`. Without one, the
panel shows no extension pages and says why. The panel also refuses an origin on its own host
name, or one of its subdomains or parents, whatever core says.

Config:

```json
{
  "extension_pages": {
    "listen": "127.0.0.1:8401",
    "url": "https://pages.pluxy.example"
  }
}
```

| Field | Meaning |
| --- | --- |
| `listen` | Address of the pages listener. Default: the host of `listen` with a free port core picks at start. Cannot change without a restart. |
| `url` | The pages site's origin as browsers reach it (scheme, host and port only). Default: derived for loopback as above. |

Every response from the pages site carries:

- `Content-Security-Policy: sandbox allow-scripts; default-src 'none'; script-src <files>; style-src <files>; img-src <files> data: blob:; font-src <files> data:; media-src <files> data: blob:; connect-src 'none'; frame-src 'none'; worker-src 'none'; manifest-src 'none'; form-action 'none'; base-uri 'none'`,
  where `<files>` is the page's own folder on the pages site. The `sandbox` directive makes the
  document sandboxed even when it is opened outside the panel. No inline scripts or styles, no
  `eval`.
- `Access-Control-Allow-Origin: *`: a sandboxed document has an opaque origin, and browsers
  load its module scripts and fonts with CORS. The files are public package files; no request
  to the pages site carries or needs a credential.
- `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`, `Cache-Control: no-cache`
  with `Last-Modified`.

Only these file types are served: `.html`, `.js`, `.mjs`, `.css`, `.json`, `.svg`, `.png`,
`.jpg`, `.jpeg`, `.gif`, `.webp`, `.ico`, `.woff`, `.woff2`, `.txt`. The pages site is on while
the management API is.

The management API lists what the panel can show:

| Call | What it does |
| --- | --- |
| `GET /v0/management/ui` | `pages_origin` (or `pages_error` saying why there is none), and `extensions`: each running extension with pages or settings, its `id`, `version`, `permissions`, `pages` (`id`, `title`, `short`, `url`) and `config` (whether it has settings). |
| `GET /v0/management/extensions/{publisher}/{name}/config` | `schema`, `values` (every field with a value or a default, except `writeOnly` fields) and `set` (the `writeOnly` fields that have a value). |
| `PATCH /v0/management/extensions/{publisher}/{name}/config` | `{"values": {...}}`: the fields to change; `null` clears a field back to its default. 200 with what `GET` returns; 422 with `invalid_config` and the first problem when the result breaks the schema (nothing is changed); 404 when the extension does not run or has no settings. |

## The panel's side

The panel shows each page in an `iframe` with `sandbox="allow-scripts"` (no `allow-same-origin`,
`allow-forms`, `allow-popups`, `allow-top-navigation`, `allow-modals` or `allow-downloads`) and
`referrerpolicy="no-referrer"`. The panel's own Content Security Policy allows frames only from
the pages site (`frame-src <pages origin>/`), which is what stops a page from navigating its
frame anywhere else.

The panel binds each frame it creates to the extension that owns the page, as listed by core.
Nothing a page sends can name another extension: there is no such parameter. The panel accepts
one `hello` per frame; when the frame loads a second document (the page navigated or reloaded
itself), the panel closes the channel and shows a button to load the page again.

## The message channel

A page talks to the panel over one `MessagePort`. Every message is a plain object, sent with
structured clone, and must be at most 64 KiB as JSON.

### Connecting

1. The page sends its parent window one message:
   `{"pluxy": "page", "v": 0, "type": "hello", "versions": [0]}`, with target origin `"*"`
   (the page cannot know the panel's origin; the message holds nothing private).
2. The panel checks that it comes from a frame it created and that has not connected before,
   picks a version both speak, and answers on the frame's window with a new port:
   `{"pluxy": "host", "v": 0, "type": "welcome", "extension": "example/usage", "page": "usage", "theme": {...}}`.
   It carries no key, address or anything about other extensions. If no version fits, it answers
   `{"pluxy": "host", "type": "refused", "reason": "..."}` and sends no port.
3. Everything after that goes over the port. The panel ignores window messages from then on.

### Calls

The page sends `{"v": 0, "id": "1", "method": "config.get", "params": null}`: exactly these
four keys, `id` a string of 1 to 64 characters, `params` an object or `null`. The panel answers
`{"v": 0, "id": "1", "result": ...}` or `{"v": 0, "id": "1", "error": {"code": "...", "message": "..."}}`.

| Method | `params` | Permission | Result |
| --- | --- | --- | --- |
| `theme.get` | `null` | none | The theme snapshot (below). |
| `config.get` | `null` | `config.read` | `{"values": {...}, "set": [...]}`, as the management API's `GET` gives them. |
| `config.set` | `{"values": {...}}` | `config.write` | The same, after the change. Fields as for the management API's `PATCH`. |

Error codes: `bad_request` (the message breaks the shape above, or a method's `params` are
wrong), `too_large`, `unknown_method`, `forbidden` (the extension did not declare the
permission), `rate_limited` (more than 30 calls in a second, or more than 8 waiting for an
answer), `invalid_config` (with core's message), `unavailable` (core could not be reached or
refused the call; the message says which, never with the key). The panel answers every call with
a well-formed `id`, once.

### Events

The panel sends `{"v": 0, "event": "theme", "data": {...}}` when the theme or the person's
theme choices change.

### The theme snapshot

```json
{
  "theme_version": 0,
  "appearance": "dark",
  "layout": {"nav": "sidebar", "list": "table", "detail": "side", "width": "wide"},
  "tokens": {"color.bg": "#111214", "color.text": "#e7e7ea", "...": "..."}
}
```

`tokens` holds every token of the [theme contract](../themes/README.md) with its resolved value,
after the theme, the dark values and the person's density. A page sets them as CSS custom
properties on its own root (`color.bg` is `--px-color-bg`, as in the kit), so it follows the
theme as the panel does. The person's and the theme's custom CSS is not sent: it is written for
the panel's markup, not the page's.

## The page library

[`pluxy-page.js`](pluxy-page.js) is a small module that does the page's side: copy it into your
package's `ui` folder and load it with `<script type="module">`.

```js
import { connect } from "./pluxy-page.js";

const pluxy = await connect(); // says hello, waits for the welcome, applies the theme
const { values } = await pluxy.call("config.get");
pluxy.on("theme", (theme) => { /* the library already applied it */ });
```

`connect()` sets the theme's tokens as `--px-*` properties on `document.documentElement`,
sets `data-appearance` and the layout options as `data-*` attributes there, and does it again on
every `theme` event.

## Limits of version 0

- Pages are for the browser panel. The desktop app will show them in a separate webview with no
  native permissions, with the same channel (planned with the app shell).
- A package needs a program even when it only brings pages.
- Settings reach the extension's pages, not its program.
- The page's frame has a fixed size within the panel's content area; a page cannot resize it.

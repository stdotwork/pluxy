# Direction A: Console

The panel for someone who lives in a terminal: everything on one screen, driven from the keyboard.
See `../README.md` for how a direction is loaded and switched.

| File | What it is |
| --- | --- |
| `app.tsx` | The shell: rail, top bar, hint line, sign-in, the command menu's entries, and where each address leads |
| `overview.tsx`, `accounts.tsx`, `requests.tsx` | The three designed screens |
| `fleet.tsx`, `data.ts` | The accounts with how each stands, their usage windows, polling |
| `lib.ts` | The rules and number formats, with nothing drawn (tested in `tests/design-a.test.ts`) |
| `keys.tsx`, `command.tsx` | The key registry and the command menu |
| `parts.tsx`, `icons.tsx`, `brand.tsx` | Buttons, state tag, window bar, chart; the icons; the mark, wordmark and favicon |
| `a.css`, `screens.css`, `legacy.css` | Tokens, shell and parts; the three screens; today's pages inside the shell |

Pages it has not redesigned (Routing, Config, Add account, Quota, Log, Extensions, Themes, System,
Setup) are today's, shown inside its shell. `#/classic/<page>` shows today's version of a page it
has redesigned.

## What it brings with it

- **Icons:** 46 from Lucide 1.78.0 (ISC License, `lucide-LICENSE.txt`), inlined in `icons.tsx`.
- **Font:** IBM Plex Mono, Latin subset, weights 400, 500 and 600 as woff2: 45,216 bytes in all
  (SIL Open Font License 1.1, `plex-mono-LICENSE.txt`). The files are loaded only in this
  direction.
- No new package: nothing was added to `package.json`.

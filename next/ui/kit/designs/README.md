# Design directions

Three whole versions of the panel that run side by side while one is chosen. Nothing here loads
unless a direction is set, so the panel is today's by default.

- `?design=a`, `?design=b` or `?design=c` in the panel's address picks a direction and remembers
  it on this device (`localStorage`, `pluxy.design`). `?design=off` goes back to today's panel.
- While a direction is set, a small switcher sits in the bottom right corner: A, B, C, current.

## What is shared

| File | What it is |
| --- | --- |
| `index.ts` | `currentDesign()`, the types a direction implements, and the lazy loaders |
| `mount.tsx` | Loads the direction, links its stylesheets, renders it with the switcher |
| `switcher.tsx`, `switcher.css` | The switcher |
| `stylesheet.ts` | Links stylesheet files (the panel's CSP allows no inline styles) |
| `session.tsx` | Sign-in state, the kit and privacy contexts, today's page list and the address's path, with nothing drawn |
| `placeholder.tsx` | Today's panel, for a direction that is not built yet |
| `assets.d.ts` | Types for stylesheet and font imports |

## A direction

A direction lives wholly in `designs/<x>/` and is entered through `designs/<x>/app.tsx`:

```tsx
import styles from "./x.css" with { type: "file" };
import type { DesignProps } from "../index";
import { SessionProvider, useSession } from "../session";

export const styles = [styles];

export default function App(props: DesignProps) {
  const session = useSession(props);
  if (!session.signedIn) return <MySignIn session={session} />;
  return (
    <SessionProvider session={session}>
      <MyShell session={session} />
    </SessionProvider>
  );
}
```

- **Styles.** Import each stylesheet `with { type: "file" }` and list it in `styles`. A plain
  `import "./x.css"` would put the rules in the default panel's stylesheet. The files are served
  as written (not bundled), so they cannot `@import` each other and a `url()` in them is not
  rewritten: load a font with the `FontFace` API from a `.woff2` imported the same way. Scope
  every rule under `:root[data-design="<x>"]`, which is set while the direction shows. The kit's
  own CSS is in `@layer kit`, so a direction's unlayered rules win over it.
- **Tokens.** Read the theme's `--px-*` variables; add your own as `--d<x>-*`.
- **Today's pages.** `session.pages` holds them. Render `pageAt(session.pages, session.path)?.render()`
  inside `SessionProvider` for any page the direction has not redesigned.
- **Not shared with today's shell:** a page's unsaved-changes guard (`guardLeave`) is asked only by
  today's Shell; a direction's navigation does not consult it.

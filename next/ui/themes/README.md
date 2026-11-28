# themes

The built-in theme files for pluxy's web panel and desktop app:

- `default.json`: default theme (`pluxy/default`), light and dark.
- `paper.json`: warm light and dark theme with top navigation and card lists (`pluxy/paper`).
- `terminal.json`: compact dark monospaced theme with a narrow navigation rail (`pluxy/terminal`).

The theme contract reference (tokens, layout options, hooks, custom CSS and T3 Code themes) is in [docs/themes.md](../../../docs/themes.md).

Run the theme tests from `next/ui/`:

```bash
bun test tests/theme.test.ts
```

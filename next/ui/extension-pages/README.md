# extension-pages

Holds `pluxy-page.js`, the browser module an extension's page loads to connect to the panel and the desktop app over the extension page message channel.

The extension pages contract (manifest `ui` and `config`, hosting, security rules and the message channel) is in [docs/extension-pages.md](../../../docs/extension-pages.md).

Run the extension page channel tests from `next/ui/`:

```bash
bun test tests/extensions.test.ts
```

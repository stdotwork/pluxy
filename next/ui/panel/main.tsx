// The panel core serves: the kit's shell with the core pages, in a browser.

import { render } from "preact";
import { AccountsPage } from "../kit/pages/accounts";
import { ExtensionsPage } from "../kit/pages/extensions";
import { StatusPage } from "../kit/pages/status";
import { ThemesPage } from "../kit/pages/themes";
import { type PageDef, Shell } from "../kit/shell";
import { ThemeState } from "../kit/theme/state";
import defaultTheme from "../themes/default.json";
import paper from "../themes/paper.json";
import terminal from "../themes/terminal.json";
import { browserHost } from "./browserHost";

const host = browserHost();
const themes = new ThemeState(host, document, [defaultTheme, terminal, paper]);

// ?safe turns safe mode on and keeps it on, for when custom CSS hides the Themes page's switch.
const url = new URL(location.href);
if (url.searchParams.has("safe")) {
  themes.setSafe(true);
  url.searchParams.delete("safe");
  history.replaceState(null, "", url);
}

const pages: PageDef[] = [
  { id: "status", title: "Status", short: "Status", render: () => <StatusPage /> },
  { id: "accounts", title: "Accounts", short: "Accts", render: () => <AccountsPage /> },
  { id: "extensions", title: "Extensions", short: "Ext", render: () => <ExtensionsPage /> },
  { id: "themes", title: "Themes", short: "Theme", render: () => <ThemesPage /> },
];

render(<Shell host={host} themes={themes} pages={pages} />, document.body);

// The panel core serves: the kit's shell with the core pages, in a browser.

import { render } from "preact";
import { corePages } from "../kit/pages/core";
import { Shell } from "../kit/shell";
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

render(<Shell host={host} themes={themes} pages={corePages()} extensionPages />, document.body);

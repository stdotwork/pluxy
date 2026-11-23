// The panel core serves: the kit's shell with the core pages, in a browser.

import { render } from "preact";
import { currentDesign, DESIGN_PARAM } from "../kit/designs";
import { corePages } from "../kit/pages/core";
import { prefsPrivacy } from "../kit/privacy";
import { Shell } from "../kit/shell";
import { ThemeState } from "../kit/theme/state";
import defaultTheme from "../themes/default.json";
import paper from "../themes/paper.json";
import terminal from "../themes/terminal.json";
import { browserHost, watchPref } from "./browserHost";

const host = browserHost();
const themes = new ThemeState(host, document, [defaultTheme, terminal, paper]);
const privacy = prefsPrivacy(host);
// The panel's other tabs share its prefs: they follow a change made in this one, and it theirs.
watchPref("privacy", (value) => privacy.adopt(value === "on"));

// ?safe turns safe mode on and keeps it on, for when custom CSS hides the Themes page's switch.
const url = new URL(location.href);
if (url.searchParams.has("safe")) {
  themes.setSafe(true);
  url.searchParams.delete("safe");
  history.replaceState(null, "", url);
}

const today = () =>
  render(
    <Shell host={host} themes={themes} privacy={privacy} pages={corePages()} extensionPages safeAddress />,
    document.body,
  );

// A design direction (?design=a|b|c, remembered until ?design=off) takes the place of today's
// Shell. Its code is loaded only then; without one, or if it cannot load, the panel is today's.
const design = currentDesign();
if (url.searchParams.has(DESIGN_PARAM)) {
  url.searchParams.delete(DESIGN_PARAM);
  history.replaceState(null, "", url);
}
if (design) {
  import("../kit/designs/mount")
    .then((m) => m.mountDesign(design, { host, themes, privacy, pages: corePages() }))
    .catch((e) => {
      console.error("design direction did not load", e);
      today();
    });
} else {
  today();
}

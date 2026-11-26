// The panel core serves: the kit's shell with the core pages, in a browser.

import { render } from "preact";
import { useEffect, useState } from "preact/hooks";
import { corePages } from "../kit/pages/core";
import { prefsPrivacy } from "../kit/privacy";
import { Shell } from "../kit/shell";
import { ThemeState } from "../kit/theme/state";
import defaultTheme from "../themes/default.json";
import paper from "../themes/paper.json";
import terminal from "../themes/terminal.json";
import { browserHost, watchPref } from "./browserHost";
import { panelStatus, type Reach, ReachWatch } from "./status";

const reached = new ReachWatch();
const host = browserHost({ heard: reached.heard });
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

function Panel() {
  const [reach, setReach] = useState<Reach>("unknown");
  useEffect(() => reached.watch(host, setReach), []);
  return (
    <Shell
      host={host}
      themes={themes}
      privacy={privacy}
      pages={corePages()}
      status={panelStatus(reach, location)}
      extensionPages
      safeAddress
    />
  );
}

render(<Panel />, document.body);

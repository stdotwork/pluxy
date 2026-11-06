// The desktop app's window: the kit's shell with core's pages, the same as the panel's and in the
// same places, then the app's own Connection page. Extension pages open in webviews of their
// own (nativePage.tsx).

import { listen } from "@tauri-apps/api/event";
import { render } from "preact";
import { useEffect, useMemo, useState } from "preact/hooks";
import { AccountsPage } from "../../ui/kit/pages/accounts";
import { ExtensionsPage } from "../../ui/kit/pages/extensions";
import { StatusPage } from "../../ui/kit/pages/status";
import { ThemesPage } from "../../ui/kit/pages/themes";
import { type PageDef, Shell } from "../../ui/kit/shell";
import { ThemeState } from "../../ui/kit/theme/state";
import defaultTheme from "../../ui/themes/default.json";
import paper from "../../ui/themes/paper.json";
import terminal from "../../ui/themes/terminal.json";
import { ConnectionPage } from "./connection";
import { appHost, type Connection, connectionGet } from "./host";
import { NativeExtensionPage } from "./nativePage";

function brand(c: Connection): string {
  return c.mode === "remote" && c.target ? `pluxy · ${new URL(c.target).host}` : "pluxy";
}

function App(props: { initial: Connection }) {
  const [conn, setConn] = useState(props.initial);
  const [generation, setGeneration] = useState(0);
  const changed = (c: Connection) => {
    setConn(c);
    setGeneration((n) => n + 1);
  };
  const host = useMemo(() => appHost(conn, changed), [conn]);
  // The app's own core exited: show it.
  useEffect(() => {
    const off = listen("pluxy-connection", () => connectionGet().then(changed, () => {}));
    return () => {
      off.then(
        (f) => f(),
        () => {},
      );
    };
  }, []);
  const themes = useMemo(() => new ThemeState(host, document, [defaultTheme, terminal, paper]), [host]);
  const pages: PageDef[] = useMemo(
    () => [
      { id: "status", title: "Status", short: "Status", render: () => <StatusPage /> },
      { id: "accounts", title: "Accounts", short: "Accts", render: () => <AccountsPage /> },
      { id: "extensions", title: "Extensions", short: "Ext", render: () => <ExtensionsPage /> },
      { id: "themes", title: "Themes", short: "Theme", render: () => <ThemesPage /> },
      {
        id: "connection",
        title: "Connection",
        short: "Conn",
        render: () => <ConnectionPage connection={conn} onChange={changed} />,
      },
    ],
    [conn],
  );

  // Not connected or not signed in (or the app's own core did not start): only the Connection
  // page, where the person can fix the address or give the key.
  if (!conn.target || !conn.signed_in) {
    return (
      <div class="shell" data-part="shell">
        <main class="main" data-part="main">
          <div class="content">
            <ConnectionPage connection={conn} onChange={changed} />
          </div>
        </main>
      </div>
    );
  }
  return (
    <Shell
      key={generation}
      host={host}
      themes={themes}
      pages={pages}
      brand={brand(conn)}
      extensionPages
      extensionPage={NativeExtensionPage}
    />
  );
}

connectionGet().then(
  (c) => render(<App initial={c} />, document.body),
  (e) => {
    document.body.textContent = `The app could not read its connection: ${e}`;
  },
);

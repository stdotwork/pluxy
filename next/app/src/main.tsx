// The desktop app's window: the kit's shell with core's pages, the same as the panel's and in the
// same places, then the app's own Connection page. Extension pages open in webviews of their
// own (nativePage.tsx).

import { listen } from "@tauri-apps/api/event";
import { render } from "preact";
import { useEffect, useMemo, useState } from "preact/hooks";
import { corePages } from "../../ui/kit/pages/core";
import { type PageDef, Shell } from "../../ui/kit/shell";
import { ThemeState } from "../../ui/kit/theme/state";
import defaultTheme from "../../ui/themes/default.json";
import paper from "../../ui/themes/paper.json";
import terminal from "../../ui/themes/terminal.json";
import { ConnectionPage } from "./connection";
import { appHost, type Connection, connectionGet } from "./host";
import { NativeExtensionPage } from "./nativePage";
import { ClosePrompt } from "./window";

function brand(c: Connection): string {
  return c.mode === "remote" && c.target ? `pluxy · ${new URL(c.target).host}` : "pluxy";
}

function App(props: { initial: Connection }) {
  const [conn, setConn] = useState(props.initial);
  // What the Connection page shows: conn, or newer after a rotation, which refreshes it alone so
  // the shell and its pages (the Config page's notices) stay as they are.
  const [shown, setShown] = useState(props.initial);
  const [generation, setGeneration] = useState(0);
  const changed = (c: Connection) => {
    setConn(c);
    setShown(c);
    setGeneration((n) => n + 1);
  };
  const host = useMemo(() => appHost(conn, changed, setShown), [conn]);
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
      ...corePages(),
      {
        id: "connection",
        title: "Connection",
        short: "Conn",
        group: "control",
        render: () => <ConnectionPage connection={shown} onChange={changed} />,
      },
    ],
    [shown],
  );

  // Not connected or not signed in (or the app's own core did not start): only the Connection
  // page, where the person can fix the address or give the key.
  if (!conn.target || !conn.signed_in) {
    return (
      <div class="shell" data-part="shell">
        <main class="main" data-part="main">
          <div class="content">
            <ConnectionPage connection={shown} onChange={changed} />
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
  (c) =>
    render(
      <>
        <App initial={c} />
        <ClosePrompt />
      </>,
      document.body,
    ),
  (e) => {
    document.body.textContent = `The app could not read its connection: ${e}`;
  },
);

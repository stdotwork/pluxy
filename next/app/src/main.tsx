// The desktop app's window: the kit's shell with core's pages, the same as the panel's and in the
// same places, then the app's own Connection and Device extensions pages. Extension pages, from
// core and from packages installed on this device (devicePage.tsx), open in webviews of their
// own (nativePage.tsx).

import { listen } from "@tauri-apps/api/event";
import { render } from "preact";
import { useCallback, useEffect, useMemo, useState } from "preact/hooks";
import { corePages } from "../../ui/kit/pages/core";
import { type PageDef, Shell } from "../../ui/kit/shell";
import { ThemeState } from "../../ui/kit/theme/state";
import defaultTheme from "../../ui/themes/default.json";
import paper from "../../ui/themes/paper.json";
import terminal from "../../ui/themes/terminal.json";
import { ConnectionPage } from "./connection";
import { type DeviceCatalog, device, newest } from "./device";
import { devicePage } from "./devicePage";
import { appHost, type Connection, connectionGet } from "./host";
import { NativeExtensionPage } from "./nativePage";

function brand(c: Connection): string {
  return c.mode === "remote" && c.target ? `pluxy · ${new URL(c.target).host}` : "pluxy";
}

/** The pages of the enabled packages on this device; none in safe mode. */
function devicePages(catalog?: DeviceCatalog): PageDef[] {
  if (!catalog || catalog.safe_mode) return [];
  return catalog.packages
    .filter((p) => p.enabled)
    .flatMap((p) =>
      p.active.pages.map((page) => ({
        id: `device/${p.id}/${page.id}`,
        title: page.title,
        short: page.short,
        group: "extensions" as const,
        tip: `${page.title} (on this device)`,
        render: () => (
          <NativeExtensionPage
            device
            ext={{
              id: p.id,
              version: p.active.version,
              permissions: [],
              pages: p.active.pages,
              config: false,
            }}
            page={page}
          />
        ),
      })),
    );
}

function App(props: { initial: Connection }) {
  const [conn, setConn] = useState(props.initial);
  const [generation, setGeneration] = useState(0);
  const changed = (c: Connection) => {
    setConn(c);
    setGeneration((n) => n + 1);
  };
  const host = useMemo(() => appHost(conn, changed), [conn]);
  // This device's catalog: reloaded when the native side says it changed, keeping the newest
  // load's answer.
  const [catalog, setCatalog] = useState<DeviceCatalog>();
  const [applyDevice] = useState(() =>
    newest<DeviceCatalog | undefined>((c) => {
      if (c) setCatalog(c);
    }),
  );
  const loadDevice = useCallback(() => applyDevice(device.list().then((r) => r.body)), [applyDevice]);
  useEffect(() => {
    // Listening first: a change made while the first load runs loads again.
    const off = listen("pluxy-device", loadDevice);
    off.then(loadDevice, loadDevice);
    return () => {
      off.then(
        (f) => f(),
        () => {},
      );
    };
  }, [loadDevice]);
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
        render: () => <ConnectionPage connection={conn} onChange={changed} />,
      },
      devicePage(loadDevice),
      ...devicePages(catalog),
    ],
    [conn, catalog, loadDevice],
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

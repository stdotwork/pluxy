// The desktop app's window: the kit's shell with core's pages, the same as the panel's and in the
// same places, then the app's own Connection and Device extensions pages. Extension pages, from
// core and from packages installed on this device (devicePage.tsx), open in webviews of their
// own (nativePage.tsx).

import { listen } from "@tauri-apps/api/event";
import { render } from "preact";
import { useCallback, useEffect, useMemo, useState } from "preact/hooks";
import { api } from "../../ui/kit/api";
import { corePages } from "../../ui/kit/pages/core";
import { PrivacyState } from "../../ui/kit/privacy";
import { type PageDef, Shell } from "../../ui/kit/shell";
import { ThemeState } from "../../ui/kit/theme/state";
import { KitContext } from "../../ui/kit/ui";
import defaultTheme from "../../ui/themes/default.json";
import paper from "../../ui/themes/paper.json";
import terminal from "../../ui/themes/terminal.json";
import { claudeCodePage } from "./claudeCode";
import { ConnectionPage } from "./connection";
import { type DeviceCatalog, device, newest } from "./device";
import { DevicePage, devicePage } from "./devicePage";
import { appHost, type Connection, connectionGet, privacyGet, privacySet } from "./host";
import { launcherPage } from "./launcherPage";
import { NativeExtensionPage } from "./nativePage";
import { opencodePage } from "./opencode";
import { UpdatesPage } from "./updates";
import { ClosePrompt } from "./window";

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

function App(props: { initial: Connection; privacy: PrivacyState }) {
  const { privacy } = props;
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
        render: () => <ConnectionPage connection={shown} onChange={changed} />,
      },
      {
        id: "versions",
        title: "Version Management",
        short: "Vers",
        group: "control",
        tip: "App and core versions, and release notes",
        render: () => <UpdatesPage />,
      },
      devicePage(loadDevice),
      launcherPage(),
      claudeCodePage(),
      opencodePage(),
      ...devicePages(catalog),
    ],
    [shown, catalog, loadDevice],
  );

  // Not connected or not signed in (or the app's own core did not start): the Connection page,
  // where the person can fix the address or give the key, and this device's extensions, which
  // need no core (in safe mode, to disable or remove a package).
  if (!conn.target || !conn.signed_in) {
    const kit = {
      host,
      api: api(host),
      themes,
      privacy,
      unauthorized: () => {},
      extensionsChanged: () => {},
    };
    return (
      <KitContext.Provider value={kit}>
        <div class="shell" data-part="shell">
          <main class="main" data-part="main">
            <div class="content">
              <ConnectionPage connection={shown} onChange={changed} />
              <DevicePage onChanged={loadDevice} />
            </div>
          </main>
        </div>
      </KitContext.Provider>
    );
  }
  return (
    <Shell
      key={generation}
      host={host}
      themes={themes}
      privacy={privacy}
      pages={pages}
      brand={brand(conn)}
      extensionPages
      extensionPage={NativeExtensionPage}
    />
  );
}

// Privacy mode is read before the first render, so a page never shows unmasked first. If it
// cannot be read, it is on.
Promise.all([connectionGet(), privacyGet().catch(() => true)]).then(
  ([c, on]) =>
    render(
      <>
        <App initial={c} privacy={new PrivacyState(on, privacySet)} />
        <ClosePrompt />
      </>,
      document.body,
    ),
  (e) => {
    document.body.textContent = `The app could not read its connection: ${e}`;
  },
);

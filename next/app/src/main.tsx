// The desktop app's window: the kit's shell with core's pages, the same as the panel's and in the
// same places, then the app's own: the eight coding tools' pages (each opens as Tools / {Tool}; the
// Tools page and the guided setup's step 2 link to them), and Where pluxy runs, Updates, Add-ons
// and Launcher among the Settings topics. Until the app is connected and signed in it
// shows the first launch screen or the not-running pane (firstLaunch.tsx). Extension pages, from
// core and from packages installed on this device (devicePage.tsx), open in webviews of their
// own (nativePage.tsx).

import { listen } from "@tauri-apps/api/event";
import { render } from "preact";
import { useCallback, useEffect, useMemo, useState } from "preact/hooks";
import { api, type SetupClient } from "../../ui/kit/api";
import type { Reply } from "../../ui/kit/host";
import { corePages } from "../../ui/kit/pages/core";
import { PrivacyState } from "../../ui/kit/privacy";
import { type PageDef, Shell } from "../../ui/kit/shell";
import { ThemeState } from "../../ui/kit/theme/state";
import { KitContext } from "../../ui/kit/ui";
import defaultTheme from "../../ui/themes/default.json";
import paper from "../../ui/themes/paper.json";
import terminal from "../../ui/themes/terminal.json";
import { agentPages, movedTo, setupOffersNeeded } from "./agentPages";
import { readExtraTools } from "./clientPage";
import { ConnectionPage } from "./connection";
import { WHERE } from "./connectionCopy";
import { type DeviceCatalog, type DeviceRegistry, device, newest } from "./device";
import { DevicePage, devicePage } from "./devicePage";
import { FirstLaunch, NotRunning } from "./firstLaunch";
import { connectionStatus } from "./frameStatus";
import { appHost, type Connection, connectionGet, privacyGet, privacySet } from "./host";
import { launcherPage } from "./launcherPage";
import { NativeExtensionPage } from "./nativePage";
import { toolHooks } from "./toolHooks";
import { updatesPage } from "./updates";
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
        dest: "settings" as const,
        kind: "topic" as const,
        section: "extensions" as const,
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
  // load's answer. unread: the newest load failed, so the rows do not know which add-ons are on.
  const [catalog, setCatalog] = useState<DeviceCatalog>();
  const [unread, setUnread] = useState(false);
  const [applyDevice] = useState(() =>
    newest<Reply<DeviceCatalog>>((r) => {
      if (r.body) setCatalog(r.body);
      setUnread(!r.body);
    }),
  );
  const loadDevice = useCallback(() => applyDevice(device.list()), [applyDevice]);
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
  // What the registries offer this device: a tool's row offers to install its add-on when one of
  // them offers it. Read once the window shows the Tools page for a pluxy on this computer
  // (setupOffersNeeded), not on a start that only shows the Connection page. A read that fails
  // reads as no offers, so the rows stop saying they are checking.
  const [offers, setOffers] = useState<DeviceRegistry[]>();
  const offersNeeded = setupOffersNeeded(conn);
  const loadOffers = useCallback(() => {
    if (!offersNeeded) return;
    device.registries().then((r) => setOffers(r.body ?? []));
  }, [offersNeeded]);
  useEffect(loadOffers, [loadOffers]);
  // Where the window is (its first address part), and the addresses the app used to have.
  const [route, setRoute] = useState(() => location.hash.replace(/^#\/?/, "").split(/[/?]/)[0] ?? "");
  useEffect(() => {
    const here = () => {
      const to = movedTo(location.hash);
      if (to) location.replace(to);
      setRoute(location.hash.replace(/^#\/?/, "").split(/[/?]/)[0] ?? "");
    };
    here();
    window.addEventListener("hashchange", here);
    return () => window.removeEventListener("hashchange", here);
  }, []);
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
  const agents = useMemo(() => agentPages(), []);
  // The tools core does not scan (Claude Desktop, Grok Build, Hermes Agent), read from their
  // add-ons: as the Tools page or the setup opens, when the device's add-ons change, and on
  // Refresh. A pluxy elsewhere has none of them here.
  const [extraTools, setExtraTools] = useState<SetupClient[]>();
  const [applyExtra] = useState(() => newest<SetupClient[]>(setExtraTools));
  const remote = conn.mode === "remote";
  const loadExtra = useCallback(() => {
    if (remote || !conn.signed_in) return;
    const kit = api(host);
    applyExtra(
      kit
        .setup()
        .then(
          (r) => r.body?.found.clients,
          () => undefined,
        )
        .then((found) => readExtraTools(catalog, found))
        .then((rows) => rows.map((r) => r.client)),
    );
  }, [remote, conn.signed_in, host, catalog, applyExtra]);
  const onTools = route === "tools" || route === "setup";
  useEffect(() => {
    if (onTools) loadExtra();
  }, [onTools, loadExtra]);
  const onRefresh = useCallback(() => {
    loadExtra();
    loadOffers();
  }, [loadExtra, loadOffers]);
  const pages: PageDef[] = useMemo(() => {
    const { tools, setup } = toolHooks({
      conn,
      agents,
      view: { catalog, unread, offers },
      extraTools,
      loadDevice,
      onRefresh,
    });
    return [
      ...corePages({ general: { signOut: false }, setup, tools }),
      ...agents,
      {
        id: "connection",
        title: WHERE.title,
        dest: "settings",
        kind: "topic",
        section: "device",
        render: () => <ConnectionPage connection={shown} onChange={changed} />,
      },
      updatesPage(shown),
      devicePage(loadDevice),
      launcherPage(),
      ...devicePages(catalog),
    ];
  }, [conn, shown, catalog, unread, offers, extraTools, loadDevice, onRefresh, agents]);

  // Not connected or not signed in (or the app's own pluxy did not start): the first launch screen
  // while no place is chosen, else the frame with the case in its pane. Where pluxy runs and this
  // device's add-ons need no pluxy, so they open inside that frame (in safe mode, to turn off or
  // remove a package).
  if (!conn.target || !conn.signed_in) {
    const kit = {
      host,
      api: api(host),
      themes,
      privacy,
      unauthorized: () => {},
      extensionsChanged: () => {},
    };
    if (!conn.mode)
      return (
        <KitContext.Provider value={kit}>
          <FirstLaunch connection={shown} onChange={changed} />
        </KitContext.Provider>
      );
    return (
      <KitContext.Provider value={kit}>
        <NotRunning
          connection={shown}
          onChange={changed}
          themes={themes}
          brand={brand(conn)}
          safeMode={catalog?.safe_mode}
          device={<DevicePage onChanged={loadDevice} />}
        />
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
      status={connectionStatus(shown, changed)}
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
    document.body.textContent = `Could not read connection settings: ${e}. Restart the app and try again.`;
  },
);

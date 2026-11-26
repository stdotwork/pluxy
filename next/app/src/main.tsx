// The desktop app's window: the kit's shell with core's pages, the same as the panel's and in the
// same places, then the app's own: the topics of Tools (Beginner Mode, Manage Clients and the
// listed clients' pages), and Where pluxy runs, Updates, Add-ons
// and Launcher among the Settings topics. Until the app is connected and signed in it
// shows the first launch screen or the not-running pane (firstLaunch.tsx). Extension pages, from
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
import { agentPages, setupClientAction, setupOffersNeeded } from "./agentPages";
import { clientListPage } from "./clientList";
import { detected, encode, listed, openClient, PREF, parse } from "./clientListItems";
import { ConnectionPage } from "./connection";
import { WHERE } from "./connectionCopy";
import { type DeviceCatalog, type DeviceRegistry, device, newest } from "./device";
import { DevicePage, devicePage } from "./devicePage";
import { easyModePage } from "./easyMode";
import { FirstLaunch, NotRunning } from "./firstLaunch";
import { connectionStatus } from "./frameStatus";
import { appHost, type Connection, connectionGet, privacyGet, privacySet } from "./host";
import { launcherPage } from "./launcherPage";
import { NativeExtensionPage } from "./nativePage";
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
  // What the registries offer this device: the Setup page offers to set up a client whose tool
  // one of them offers, as Beginner Mode does. Read once the window shows that page for a core on
  // this computer (setupOffersNeeded), not on a start that only shows the Connection page.
  const [offers, setOffers] = useState<DeviceRegistry[]>();
  const offersNeeded = setupOffersNeeded(conn);
  useEffect(() => {
    if (!offersNeeded) return;
    device.registries().then(
      (r) => setOffers(r.body),
      () => {},
    );
  }, [offersNeeded]);
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
  // The agent client pages the navigation lists (clientListItems.ts): the saved choice, or the clients
  // whose tool is installed on this device, and the client page the person is on.
  const agents = useMemo(() => agentPages(), []);
  const clients = useMemo(
    () => agents.map((a) => ({ id: a.id, package: a.package, title: a.title })),
    [agents],
  );
  const [saved, setSaved] = useState(() => parse(host.prefs.get(PREF), clients));
  const saveClients = useCallback(
    (ids: string[] | undefined) => {
      host.prefs.set(PREF, encode(ids));
      setSaved(ids);
    },
    [host],
  );
  const [open, setOpen] = useState(() => openClient(location.hash, clients));
  useEffect(() => {
    const onHash = () => setOpen(openClient(location.hash, clients));
    window.addEventListener("hashchange", onHash);
    return () => window.removeEventListener("hashchange", onHash);
  }, [clients]);
  const pages: PageDef[] = useMemo(() => {
    const installed = detected(clients, catalog);
    const visible = listed(clients, installed, saved, open);
    // Setup lists the clients on core's machine. The app sets a client up on this computer, so
    // it offers to only when core runs here.
    const setup = conn.mode === "remote" ? undefined : setupClientAction(agents, catalog, offers);
    return [
      ...corePages({ general: { signOut: false }, setup }),
      easyModePage(agents, catalog, loadDevice),
      clientListPage(clients, installed, saved, visible.length, saveClients),
      ...visible.flatMap((c) => agents.find((a) => a.id === c.id) ?? []),
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
  }, [conn.mode, shown, catalog, offers, loadDevice, agents, clients, saved, open, saveClients]);

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

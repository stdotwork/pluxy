// The coding tools' setup pages, in the window's page order, and what the Tools page and the guided
// setup's step 2 get from this window: one button for each tool's row (toolAction), the tools core
// does not scan (extraTools, read in main.tsx) and the lines around the list (toolNotes).

import { Fragment, h } from "preact";
import type { SetupClient } from "../../ui/kit/api";
import type { ToolNotes } from "../../ui/kit/pages/tools";
import { strings } from "../../ui/kit/strings";
import { Notice } from "../../ui/kit/ui";
import { type AgentPage, items, LAUNCHER } from "./addonItems";
import { claudeCodePage } from "./claudeCode";
import { claudeDesktopPage } from "./claudeDesktop";
import { codexPage } from "./codex";
import type { DeviceCatalog, DeviceRegistry } from "./device";
import { grokPage } from "./grok";
import { hermesPage } from "./hermes";
import type { Connection } from "./host";
import { ompPage } from "./omp";
import { opencodePage } from "./opencode";
import { piPage } from "./pi";
import { AddonAction, Checking, openedFromSetup, SetupReturn } from "./toolRows";

const t = strings("toolActions", {
  setUp: "Set up",
  open: "Open",
  noAddOn: "The app can set these up for you once their add-ons are installed.",
  noLauncher: "Starting tools from the app needs the Launcher add-on.",
  openAddons: "Open Add-ons",
  safe1: "Safe mode is on, so tool add-ons are paused. ",
  safe2: " to review them, or start the app again without safe mode.",
});

/** The eight tools' pages. Each shows the way back to setup when it was opened from there. */
export const agentPages = (): AgentPage[] =>
  [
    claudeCodePage(),
    codexPage(),
    claudeDesktopPage(),
    opencodePage(),
    ompPage(),
    grokPage(),
    hermesPage(),
    piPage(),
  ].map((page) => ({
    ...page,
    render: () => h(Fragment, null, h(SetupReturn, null), page.render()),
  }));

/**
 * The page of each tool core's setup scan looks for, and of the three the app reads itself: core's
 * ID for the tool, then the ID of its page in this window. The two name the same tool, and need not
 * be the same word.
 */
export const SETUP_CLIENT_PAGES: Readonly<Record<string, string>> = {
  "claude-code": "claude-code",
  codex: "codex",
  opencode: "opencode",
  pi: "pi",
  omp: "omp",
  "claude-desktop": "claude-desktop",
  "grok-build": "grok-build",
  hermes: "hermes",
};

/** What the rows know of this device's add-ons: loading (undefined), read, or not readable. */
export interface AddonView {
  /** The device's add-ons; undefined until read. */
  catalog?: DeviceCatalog;
  /** The last read of them failed: nobody knows which add-ons are on. */
  unread?: boolean;
  /** What the registries offer; undefined until read. */
  offers?: Pick<DeviceRegistry, "name" | "packages">[];
}

/** What a tool's row offers, by its add-on. */
export type RowAction =
  | { kind: "link"; page: string; label: "Open" | "Set up" }
  | { kind: "turn-on"; pkg: string; page: string }
  | { kind: "install"; pkg: string; page: string; registry: string; version?: string }
  | { kind: "checking" }
  | { kind: "none" };

/** The page in this window for a tool core found or the app reads, if it has one. */
export function toolPage(
  client: Pick<SetupClient, "id">,
  agents: Pick<AgentPage, "id" | "package">[],
): AgentPage["id"] | undefined {
  const id = Object.hasOwn(SETUP_CLIENT_PAGES, client.id) ? SETUP_CLIENT_PAGES[client.id] : undefined;
  return agents.find((a) => a.id === id)?.id;
}

/**
 * The one press a row offers. The tool's page, when its add-on is on (Open for a tool that is
 * Connected, else Set up). The add-on's own step when it is installed but off (turn it on) or only
 * offered (install it, then turn it on), which opens the tool's page once the add-on is on. Nothing while the add-ons are still read. Nothing at all when no add-on is
 * to be had: the row keeps the kit's own Set up. In safe mode, or when the device's add-ons cannot
 * be read, the page alone: it says what the add-on needs.
 */
export function rowAction(
  client: Pick<SetupClient, "id" | "state" | "installed">,
  agents: Pick<AgentPage, "id" | "package">[],
  view: AddonView,
): RowAction {
  const page = toolPage(client, agents);
  const agent = agents.find((a) => a.id === page);
  if (!page || !agent) return { kind: "none" };
  const link: RowAction = {
    kind: "link",
    page,
    label: client.state === "configured" && client.installed ? "Open" : "Set up",
  };
  if (view.unread || view.catalog?.safe_mode) return link;
  if (!view.catalog) return { kind: "checking" };
  const [item] = items([agent.package], view.catalog, view.offers);
  if (item?.state === "enabled") return link;
  if (item?.state === "installed") return { kind: "turn-on", pkg: agent.package, page };
  if (item?.state === "offered")
    return {
      kind: "install",
      pkg: agent.package,
      page,
      registry: item.registry ?? "",
      version: item.version,
    };
  return view.offers ? { kind: "none" } : { kind: "checking" };
}

/**
 * What the Tools page and the setup's step 2 get as a row's button: one function for both, so a row
 * reads the same in each. fromSetup is the setup's copy: the page it opens shows the way back.
 */
export function toolAction(
  agents: Pick<AgentPage, "id" | "package">[],
  view: AddonView,
  changed: () => void,
  fromSetup = false,
) {
  return (client: SetupClient) => {
    const action = rowAction(client, agents, view);
    // The tool's page, once its add-on is on: what Set up would open next.
    const opens = (page: string) => () => {
      if (fromSetup) openedFromSetup();
      location.hash = `#/${page}`;
    };
    switch (action.kind) {
      case "link":
        return h(
          "a",
          {
            class: "btn",
            "data-part": "button",
            "data-variant": "quiet",
            "data-row": client.id,
            href: `#/${action.page}`,
            onClick: fromSetup ? openedFromSetup : undefined,
          },
          t(action.label === "Open" ? "open" : "setUp"),
        );
      case "turn-on":
        return h(AddonAction, {
          tool: client.id,
          pkg: action.pkg,
          step: "enable",
          changed,
          opens: opens(action.page),
        });
      case "install":
        return h(AddonAction, {
          tool: client.id,
          pkg: action.pkg,
          step: "install",
          registry: action.registry,
          version: action.version,
          changed,
          opens: opens(action.page),
        });
      case "checking":
        return h(Checking, null);
      default:
        return undefined;
    }
  };
}

/**
 * The lines around the list of tools. In safe mode, one warning above it. Otherwise, once the
 * add-ons are read: under it, that the app can set tools up once their add-ons are installed (only
 * when a listed tool has none to be had) and that starting tools needs the Launcher add-on (only
 * when it is not on).
 */
export function toolNotes(agents: Pick<AgentPage, "id" | "package">[], view: AddonView): ToolNotes {
  return (clients) => {
    const addons = h("a", { href: "#/device" }, t("openAddons"));
    if (view.unread || !view.catalog) return {};
    if (view.catalog.safe_mode)
      return {
        above: h(Notice, { tone: "warn", children: [t("safe1"), addons, t("safe2")] }),
      };
    if (!view.offers) return {};
    const lines = [];
    if (clients.some((c) => rowAction(c, agents, view).kind === "none" && toolPage(c, agents)))
      lines.push(
        h("p", { class: "muted tools-more", "data-part": "addon-line" }, [t("noAddOn"), " ", addons]),
      );
    if (items([LAUNCHER], view.catalog, view.offers)[0]?.state !== "enabled")
      lines.push(
        h("p", { class: "muted tools-more", "data-part": "addon-line" }, [t("noLauncher"), " ", addons]),
      );
    return lines.length > 0 ? { below: h("div", null, lines) } : {};
  };
}

/**
 * Whether the window reads what the registries offer: only when it shows the Tools page or the
 * setup (connected and signed in) and pluxy runs on this computer, since an add-on is installed
 * only then. Reading them fetches every registry's index, so a window that is not signed in, or
 * connected to a pluxy elsewhere, does not.
 */
export function setupOffersNeeded(conn: Pick<Connection, "mode" | "target" | "signed_in">): boolean {
  return Boolean(conn.target) && conn.signed_in && conn.mode !== "remote";
}

/** Where an address the app used to have goes now, if it is one of them. */
export function movedTo(hash: string): string | undefined {
  const path = hash.replace(/^#\/?/, "");
  if (path === "easy-mode") return "#/setup";
  if (path === "clients") return "#/tools";
  return undefined;
}

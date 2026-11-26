// The agent clients' setup pages, in the window's page order. Each names the optional tool it
// drives, and Beginner Mode offers the ones whose tool is installed or offered by a registry.
// The Setup page offers the same ones, for the clients core found (setupClientAction).

import type { SetupOptions } from "../../ui/kit/pages/setup";
import { strings } from "../../ui/kit/strings";
import { claudeCodePage } from "./claudeCode";
import { claudeDesktopPage } from "./claudeDesktop";
import { codexPage } from "./codex";
import type { DeviceCatalog, DeviceRegistry } from "./device";
import type { AgentPage } from "./easyMode";
import { items } from "./easyModeItems";
import { grokPage } from "./grok";
import { hermesPage } from "./hermes";
import type { Connection } from "./host";
import { ompPage } from "./omp";
import { opencodePage } from "./opencode";
import { piPage } from "./pi";

const t = strings("setupClients", {
  setUp: "Set up",
  hint: "Set up opens the client's own page in this app. There the app writes the client's settings itself, after you confirm.",
});

export const agentPages = (): AgentPage[] => [
  claudeCodePage(),
  codexPage(),
  claudeDesktopPage(),
  opencodePage(),
  ompPage(),
  grokPage(),
  hermesPage(),
  piPage(),
];

/**
 * The page of each client core's setup scan looks for: core's ID for the client, then the ID of
 * its page in this window. The two name the same tool, and need not be the same word.
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

/**
 * What the Setup page gets from this window: "Set up" for a client core found, when this window
 * has a page for it and Beginner Mode would list it (its tool is installed on this device or
 * offered by a registry). The button opens the client's own page.
 */
export function setupClientAction(
  agents: Pick<AgentPage, "id" | "package">[],
  catalog?: DeviceCatalog,
  offers?: Pick<DeviceRegistry, "name" | "packages">[],
): SetupOptions {
  return {
    clientAction: (client) => {
      const id = Object.hasOwn(SETUP_CLIENT_PAGES, client) ? SETUP_CLIENT_PAGES[client] : undefined;
      const page = agents.find((a) => a.id === id);
      if (!page || items([page.package], catalog, offers).length === 0) return undefined;
      return { label: t("setUp"), page: page.id };
    },
    clientActionHint: t("hint"),
  };
}

/**
 * Whether the window reads what the registries offer for the Setup page: only when it shows that
 * page (connected and signed in) and core runs on this computer, since "Set up" is offered only
 * then. Reading them fetches every registry's index, so a window that is not signed in does not.
 */
export function setupOffersNeeded(conn: Pick<Connection, "mode" | "target" | "signed_in">): boolean {
  return Boolean(conn.target) && conn.signed_in && conn.mode !== "remote";
}

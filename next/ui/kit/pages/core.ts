// The pages core offers, which the panel shows and the desktop app shows before its own. A
// destination's tabs and topics are listed in this order. A new core page adds one entry.
// A function, so the titles are read in the locale set when the shell renders.

import type { PageDef } from "../shell";
import { accountPage } from "./account";
import { accountsPage } from "./accounts";
import { addAccountPage } from "./addAccount";
import { configPage } from "./config";
import { extensionsPage } from "./extensions";
import { type GeneralOptions, generalPage } from "./general";
import { homePage } from "./home";
import { logsPage } from "./logs";
import { requestsPage } from "./requests";
import { routingPage } from "./routing";
import { type SetupOptions, setupPage } from "./setup";
import { systemPage } from "./system";
import { themesPage } from "./themes";
import { type ToolsOptions, toolsPage } from "./tools";
import { usageTabPage } from "./usage";

/** What a build adds to core's pages; the panel adds nothing. */
export interface CorePageOptions {
  /** What General offers: the desktop app has its Sign out on its own page, so it turns this one off. */
  general?: GeneralOptions;
  setup?: SetupOptions;
  tools?: ToolsOptions;
}

export const corePages = (options?: CorePageOptions): PageDef[] => [
  homePage(),
  accountsPage(),
  accountPage(),
  addAccountPage(),
  routingPage(),
  requestsPage(),
  usageTabPage(),
  toolsPage(options?.tools),
  // Settings' topics, in the order they are listed: this device's, then pluxy's own. General is
  // first, so the navigation's Settings entry opens on it.
  generalPage(options?.general),
  themesPage(),
  configPage(),
  extensionsPage(),
  logsPage(),
  systemPage(),
  setupPage(options?.setup),
];

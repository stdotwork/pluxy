// The pages core offers, which the panel shows and the desktop app shows before its own. A
// destination's tabs and topics are listed in this order. A new core page adds one entry.
// A function, so the titles are read in the locale set when the shell renders.

import type { PageDef } from "../shell";
import { accountsPage } from "./accounts";
import { addAccountPage } from "./addAccount";
import { configPage } from "./config";
import { extensionsPage } from "./extensions";
import { homePage } from "./home";
import { logsPage } from "./logs";
import { quotaPage } from "./quota";
import { routingPage } from "./routing";
import { type SetupOptions, setupPage } from "./setup";
import { systemPage } from "./system";
import { themesPage } from "./themes";
import { usagePage } from "./usage";

/** What a build adds to core's pages; the panel adds nothing. */
export interface CorePageOptions {
  setup?: SetupOptions;
}

export const corePages = (options?: CorePageOptions): PageDef[] => [
  homePage(),
  accountsPage(),
  addAccountPage(),
  routingPage(),
  quotaPage(),
  usagePage(),
  setupPage(options?.setup),
  configPage(),
  systemPage(),
  extensionsPage(),
  themesPage(),
  logsPage(),
];

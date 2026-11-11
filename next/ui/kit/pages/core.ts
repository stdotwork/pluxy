// The pages core offers, which the panel shows and the desktop app shows before its own. Within
// each navigation group they are listed in this order. A new core page adds one entry.
// A function, so the titles are read in the locale set when the shell renders.

import type { PageDef } from "../shell";
import { accountsPage } from "./accounts";
import { addAccountPage } from "./addAccount";
import { configPage } from "./config";
import { extensionsPage } from "./extensions";
import { logsPage } from "./logs";
import { quotaPage } from "./quota";
import { routingPage } from "./routing";
import { statusPage } from "./status";
import { themesPage } from "./themes";
import { usagePage } from "./usage";

export const corePages = (): PageDef[] => [
  statusPage(),
  accountsPage(),
  addAccountPage(),
  routingPage(),
  quotaPage(),
  logsPage(),
  usagePage(),
  configPage(),
  extensionsPage(),
  themesPage(),
];

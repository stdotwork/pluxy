// What the tool pages share about what Start waits for (the Start section, clientPage.tsx).

import { strings } from "../../ui/kit/strings";

const t = strings("clientHelp", {
  needFolder: "Start stays off until you choose a folder.",
});

/** What Start waits for, while the working directory is empty (undefined once it is not). */
export const folderWait = (cwd: string): string | undefined => (cwd.trim() ? undefined : t("needFolder"));

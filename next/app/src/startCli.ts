// What each agent page's Start CLI shares: the terminal choice it keeps when the launcher's
// terminal list reloads, the reload when the device's packages change (pluxy-device), and the
// words for a launcher that is not installed.

import { listen } from "@tauri-apps/api/event";
import { useEffect } from "preact/hooks";
import { strings } from "../../ui/kit/strings";
import { LAUNCHER } from "./easyMode";

const t = strings("startCli", {
  noLauncher:
    "Start CLI needs the launcher ({launcher}), which is not installed on this device. Install it on the Device extensions page.",
});

/** A Start CLI error as the page shows it: when the launcher itself is not installed, what is
 * missing and where to install it, in place of the native side's code; any other error as it came. */
export function launcherProblem(error: string): string {
  return error.includes(`${LAUNCHER} is not installed on this device`)
    ? t("noLauncher", { launcher: LAUNCHER })
    : error;
}

/** The terminal the Start CLI shows and launches with: the person's choice while the launcher still
 * lists it, else Automatic (the list's first entry). Before the list has loaded, the choice stays. */
export function keptTerminal(chosen: string, terminals: readonly { id: string }[] | undefined): string {
  if (!terminals || terminals.some((t) => t.id === chosen)) return chosen;
  return "automatic";
}

/** Calls reload each time the native side says this device's catalog changed (pluxy-device), and
 * stops listening on unmount. */
export function useDeviceChanged(reload: () => void) {
  useEffect(() => {
    const off = listen("pluxy-device", reload);
    return () => {
      off.then(
        (f) => f(),
        () => {},
      );
    };
  }, [reload]);
}

// What each agent page's Start CLI shares: the terminal choice it keeps when the launcher's
// terminal list reloads, and the reload when the device's packages change (pluxy-device).

import { listen } from "@tauri-apps/api/event";
import { useEffect } from "preact/hooks";

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

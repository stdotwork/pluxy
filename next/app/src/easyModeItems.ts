// Beginner Mode's package list (easyMode.tsx), apart from the page so its unit tests need no
// webview: which of the packages a client or the launcher needs this device has, in which state,
// and which a registry offers.

import type { DeviceCatalog, DevicePackage, DeviceRegistry } from "./device";

export type State = "offered" | "installed" | "enabled";

export interface Item {
  id: string;
  state: State;
  /** Where an install comes from, while it is not installed. */
  registry?: string;
  version?: string;
}

/** The packages ids name, as this device has them or a registry offers them; one it neither has
 * nor is offered (or is refused) is left out. */
export function items(
  ids: string[],
  catalog?: DeviceCatalog,
  offers?: Pick<DeviceRegistry, "name" | "packages">[],
): Item[] {
  const out: Item[] = [];
  for (const id of ids) {
    const p: DevicePackage | undefined = catalog?.packages.find((x) => x.id === id);
    if (p) {
      const ready = p.enabled && (p.tier !== "native" || p.authorized);
      out.push({ id, state: ready ? "enabled" : "installed" });
      continue;
    }
    for (const r of offers ?? []) {
      const o = r.packages.find((x) => x.id === id && !x.refused && x.versions.length > 0);
      if (o) {
        out.push({ id, state: "offered", registry: r.name, version: o.versions[0] });
        break;
      }
    }
  }
  return out;
}

// The add-ons the tool rows need, apart from the pages so unit tests need no webview: which of the
// packages a tool or the launcher needs this device has, in which state, and which a registry
// offers.

import type { PageDef } from "../../ui/kit/shell";
import type { DeviceCatalog, DevicePackage, DeviceRegistry } from "./device";

/** A coding tool's setup page in the window, and the add-on it drives. */
export type AgentPage = PageDef & { package: string };

/** The launcher every tool page's Start runs through. */
export const LAUNCHER = "pluxy/execution";

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

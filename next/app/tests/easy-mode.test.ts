// Beginner Mode's package list (src/easyModeItems.ts): which clients and launcher it shows, in which
// state, and where an install comes from.
import { describe, expect, test } from "bun:test";
import type { DeviceCatalog, DevicePackage } from "../src/device";
import { items } from "../src/easyModeItems";

const pkg = (id: string, enabled: boolean, authorized: boolean, tier: "native" | "web" = "native") =>
  ({ id, enabled, authorized, tier }) as unknown as DevicePackage;
const catalog = (...packages: DevicePackage[]): DeviceCatalog => ({ safe_mode: false, packages });

describe("items", () => {
  test("an installed package's state follows enabled, and for a native tool its authorization", () => {
    const c = catalog(
      pkg("a/on", true, true),
      pkg("a/off", false, false),
      pkg("a/unauthorized", true, false),
    );
    expect(items(["a/on", "a/off", "a/unauthorized"], c)).toEqual([
      { id: "a/on", state: "enabled" },
      { id: "a/off", state: "installed" },
      { id: "a/unauthorized", state: "installed" },
    ]);
    expect(items(["w/page"], catalog(pkg("w/page", true, false, "web")))).toEqual([
      { id: "w/page", state: "enabled" },
    ]);
  });

  test("a package not installed is offered by the first registry that offers it, newest version", () => {
    const offers = [
      { name: "other", packages: [{ id: "a/x", versions: ["2.0.0"], refused: "a reserved publisher" }] },
      { name: "curated", packages: [{ id: "a/x", versions: ["1.1.0", "1.0.0"] }] },
      { name: "late", packages: [{ id: "a/x", versions: ["9.0.0"] }] },
    ];
    expect(items(["a/x"], catalog(), offers)).toEqual([
      { id: "a/x", state: "offered", registry: "curated", version: "1.1.0" },
    ]);
  });

  test("an installed package is never offered, and one neither installed nor offered is left out", () => {
    const offers = [
      {
        name: "curated",
        packages: [
          { id: "a/on", versions: ["3.0.0"] },
          { id: "a/none", versions: [] },
        ],
      },
    ];
    expect(items(["a/on", "a/none", "a/missing"], catalog(pkg("a/on", true, true)), offers)).toEqual([
      { id: "a/on", state: "enabled" },
    ]);
    expect(items(["a/on"], undefined, undefined)).toEqual([]);
  });
});

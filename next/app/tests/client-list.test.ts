// Which agent client pages the window lists (src/clientListItems.ts): automatic, a saved choice,
// resetting, an empty choice refused, and the client page the person is on staying listed. The
// expected lists are written out by hand.
import { describe, expect, test } from "bun:test";
import { agentPages } from "../src/agentPages";
import {
  automatic,
  type Client,
  detected,
  encode,
  listed,
  matching,
  openClient,
  parse,
  savable,
  toggled,
} from "../src/clientListItems";
import type { DeviceCatalog, DevicePackage } from "../src/device";

const clients: Client[] = [
  { id: "a", package: "t/a", title: "Alpha" },
  { id: "b", package: "t/b", title: "Beta Code" },
  { id: "c", package: "t/c", title: "Gamma" },
  { id: "d", package: "t/d", title: "Delta Code" },
];
const ids = (list: Client[]) => list.map((c) => c.id);
const pkg = (id: string, enabled: boolean) => ({ id, enabled }) as unknown as DevicePackage;
const catalog = (...packages: DevicePackage[]): DeviceCatalog => ({ safe_mode: false, packages });

describe("detected", () => {
  test("a client is detected when its tool is installed, enabled or not; others and a missing catalog give none", () => {
    const c = catalog(pkg("t/b", false), pkg("t/d", true), pkg("t/other", true));
    expect([...detected(clients, c)].sort()).toEqual(["b", "d"]);
    expect(detected(clients, catalog()).size).toBe(0);
    expect(detected(clients, undefined).size).toBe(0);
  });
});

describe("automatic", () => {
  test("lists the installed clients, installed ones first and each group in the window's order", () => {
    const installed = new Set(["d", "b"]);
    expect(automatic(clients, installed)).toEqual(["b", "d"]);
    expect(ids(listed(clients, installed, undefined))).toEqual(["b", "d"]);
  });

  test("lists none when no tool is installed", () => {
    expect(listed(clients, new Set(), undefined)).toEqual([]);
  });

  test("keeps the client page the person is on, after the installed ones", () => {
    expect(ids(listed(clients, new Set(["d", "b"]), undefined, "c"))).toEqual(["b", "d", "c"]);
    expect(ids(listed(clients, new Set(["b"]), undefined, "b"))).toEqual(["b"]);
  });
});

describe("a saved choice", () => {
  test("lists exactly the saved clients, ignoring which are installed", () => {
    expect(ids(listed(clients, new Set(["b", "d"]), ["c", "a"]))).toEqual(["a", "c"]);
  });

  test("lists installed saved clients before the others", () => {
    expect(ids(listed(clients, new Set(["c"]), ["a", "c"]))).toEqual(["c", "a"]);
  });

  test("keeps the client page the person is on even when the choice hides it", () => {
    expect(ids(listed(clients, new Set(), ["b"], "d"))).toEqual(["b", "d"]);
    expect(ids(listed(clients, new Set(), ["b"]))).toEqual(["b"]);
  });
});

describe("the pref", () => {
  test("round-trips a choice, and automatic is the pref removed", () => {
    expect(encode(["c", "a"])).toBe('["c","a"]');
    expect(parse('["c","a"]', clients)).toEqual(["a", "c"]);
    expect(encode(undefined)).toBeNull();
    expect(parse(encode(undefined), clients)).toBeUndefined();
  });

  test("resetting returns to automatic: the saved list no longer decides what is listed", () => {
    const installed = new Set(["b"]);
    const saved = parse(encode(["a", "c"]), clients);
    expect(ids(listed(clients, installed, saved))).toEqual(["a", "c"]);
    const reset = parse(encode(undefined), clients);
    expect(ids(listed(clients, installed, reset))).toEqual(["b"]);
  });

  test("drops clients this window does not know, and is automatic when none is known or the pref is damaged", () => {
    expect(parse('["gone","b","also-gone"]', clients)).toEqual(["b"]);
    expect(parse('["gone"]', clients)).toBeUndefined();
    expect(parse("[]", clients)).toBeUndefined();
    expect(parse("not json", clients)).toBeUndefined();
    expect(parse('{"b":true}', clients)).toBeUndefined();
    expect(parse("null", clients)).toBeUndefined();
    expect(parse(null, clients)).toBeUndefined();
  });
});

describe("editing the choice", () => {
  test("an empty choice is refused, a choice of one and automatic are not", () => {
    expect(savable([])).toBe(false);
    expect(savable(["a"])).toBe(true);
    expect(savable(undefined)).toBe(true);
  });

  test("unchecking the last client leaves an empty choice, which is refused", () => {
    expect(savable(toggled(["a"], [], "a", false))).toBe(false);
  });

  test("starting from automatic, a checkbox changes the automatic list", () => {
    expect(toggled(undefined, ["b", "d"], "a", true)).toEqual(["b", "d", "a"]);
    expect(toggled(undefined, ["b", "d"], "b", false)).toEqual(["d"]);
  });

  test("checking a client already in the choice leaves it once", () => {
    expect(toggled(["a", "b"], [], "a", true)).toEqual(["a", "b"]);
  });
});

describe("search", () => {
  test("matches the title anywhere, ignoring case and surrounding spaces, in the list's order", () => {
    const installed = new Set(["d"]);
    expect(ids(matching(clients, installed, "  CODE "))).toEqual(["d", "b"]);
    expect(ids(matching(clients, installed, "amm"))).toEqual(["c"]);
  });

  test("an empty search matches every client, and one that matches none gives none", () => {
    expect(ids(matching(clients, new Set(), ""))).toEqual(["a", "b", "c", "d"]);
    expect(matching(clients, new Set(), "zzz")).toEqual([]);
  });
});

describe("the page the address names", () => {
  test("is the client whose ID is the address, or whose ID starts its path", () => {
    expect(openClient("#/a", clients)).toBe("a");
    expect(openClient("#/b/sub", clients)).toBe("b");
    expect(openClient("#d", clients)).toBe("d");
  });

  test("is none for another page, no address, or an ID that only starts with a client's", () => {
    expect(openClient("#/clients", clients)).toBeUndefined();
    expect(openClient("#/ab", clients)).toBeUndefined();
    expect(openClient("", clients)).toBeUndefined();
  });
});

describe("with the window's real client pages", () => {
  const real: Client[] = agentPages().map((a) => ({ id: a.id, package: a.package, title: a.title }));

  test("an installed Codex tool alone makes automatic list Codex", () => {
    const installed = detected(real, catalog(pkg("pluxy/codex-setup", false)));
    expect(ids(listed(real, installed, undefined))).toEqual(["codex"]);
  });

  test("two installed tools list in the window's order", () => {
    const installed = detected(real, catalog(pkg("pluxy/pi-setup", true), pkg("pluxy/claude-code", true)));
    expect(ids(listed(real, installed, undefined))).toEqual(["claude-code", "pi"]);
  });

  test("no client page has the Manage Clients page's ID", () => {
    expect(real.map((c) => c.id)).not.toContain("clients");
  });
});

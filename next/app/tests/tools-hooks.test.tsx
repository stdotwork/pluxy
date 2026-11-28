// What the app gives the Tools page and the guided setup's step 2 (src/toolHooks.ts, toolRows.tsx,
// keyOwners.ts): the add-on step on a row, the way back to setup, the lines around the list, the
// names of keys, and the pages the navigation shows.
import { afterEach, describe, expect, test } from "bun:test";
import { DESTINATIONS, destinationHome, place } from "../../ui/kit/destinations";
import { corePages } from "../../ui/kit/pages/core";
import { agentPages, toolAction } from "../src/agentPages";
import type { DeviceCatalog, DevicePackage } from "../src/device";
import { ownersOf } from "../src/keyOwners";
import { toolHooks } from "../src/toolHooks";
import { AddonAction, addonStatus, cameFromSetup, leftSetup, runAddon, SetupReturn } from "../src/toolRows";
import { restoreDom } from "./fakeDom";
import { mount, never } from "./pageMount";

afterEach(() => {
  restoreDom();
  Reflect.deleteProperty(globalThis, "location");
  leftSetup();
});

const catalog = (...packages: [string, boolean][]): DeviceCatalog => ({
  safe_mode: false,
  packages: packages.map(
    ([id, enabled]) => ({ id, enabled, authorized: true, tier: "native" }) as unknown as DevicePackage,
  ),
});
const agents = agentPages();
const client = (id: string, state = "not-configured") =>
  ({ id, name: id, installed: true, config: "", state }) as never;

// ---- the add-on step on a row ----------------------------------------------------------------

describe("Install add-on and Turn on add-on", () => {
  /** A reply the test answers when it chooses. */
  const later = <T,>() => {
    let done: (v: T) => void = () => {};
    const promise = new Promise<T>((resolve) => {
      done = resolve;
    });
    return { promise, done };
  };
  const install = (pkg: string, changed = () => {}) => (
    <AddonAction tool="codex" pkg={pkg} step="install" registry="curated" version="1.2.0" changed={changed} />
  );
  const turnOn = (pkg: string, changed = () => {}) => (
    <AddonAction tool="codex" pkg={pkg} step="enable" changed={changed} />
  );

  test("Install add-on asks for the offered version, says Installing…, then reads the device again", async () => {
    const reply = later<unknown>();
    let changed = 0;
    const m = await mount(
      install("t/install-1", () => changed++),
      { device_install: () => reply.promise },
    );
    expect(m.labels()).toEqual(["Install add-on"]);
    await m.click("Install add-on");
    expect(m.commands("device_install")).toEqual([
      { registry: "curated", id: "t/install-1", version: "1.2.0" },
    ]);
    expect(m.labels()).toEqual(["Installing…"]);
    expect(m.button("Installing…").disabled).toBe(true);
    reply.done({ safe_mode: false, packages: [] });
    await m.show(install("t/install-1", () => changed++));
    expect(changed).toBe(1);
    expect(m.labels()).toEqual(["Install add-on"]);
  });

  test("Turn on add-on says to answer the confirmation window while it is open, with the button waiting", async () => {
    const m = await mount(turnOn("t/enable-1"), { device_enable: never });
    await m.click("Turn on add-on");
    expect(m.commands("device_enable")).toEqual([{ id: "t/enable-1", enabled: true }]);
    expect(m.text()).toContain("Answer the confirmation window to go on.");
    expect(m.button("Turn on add-on").disabled).toBe(true);
    // A second press while it waits asks nothing more.
    await m.click("Turn on add-on");
    expect(m.commands("device_enable")).toHaveLength(1);
  });

  test("a failed install or turn-on says why in one line and can be pressed again", async () => {
    const m = await mount(install("t/install-2"), {
      device_install: () => Promise.reject("registry unreachable"),
    });
    await m.click("Install add-on");
    expect(m.text()).toContain("Could not install the add-on. Registry unreachable");
    expect(m.button("Install add-on").disabled).toBeFalsy();
    await m.click("Install add-on");
    expect(m.commands("device_install")).toHaveLength(2);

    const n = await mount(turnOn("t/enable-2"), {
      device_enable: () => Promise.reject("the signature does not hold"),
    });
    await n.click("Turn on add-on");
    expect(n.text()).toContain("Could not turn on the add-on. The signature does not hold");
    expect(n.button("Turn on add-on").disabled).toBeFalsy();
  });

  // What the native side says when the confirmation window is answered No, and the short form.
  for (const said of ["native enable cancelled; t/enable-3 stays disabled", "cancelled"]) {
    test(`a cancelled confirmation window says nothing, and the row is ready again (${said})`, async () => {
      const m = await mount(turnOn("t/enable-3"), {
        device_enable: () => Promise.reject(said),
      });
      await m.click("Turn on add-on");
      expect(m.text()).not.toContain("Could not");
      expect(m.text()).not.toContain("Answer the confirmation window");
      expect(m.button("Turn on add-on").disabled).toBeFalsy();
    });
  }

  test("Install add-on goes on to turn the add-on on, in its one confirmation window, then opens the tool's page", async () => {
    let opened = 0;
    const m = await mount(
      <AddonAction
        tool="codex"
        pkg="t/chain-1"
        step="install"
        registry="curated"
        version="1.2.0"
        changed={() => {}}
        opens={() => opened++}
      />,
      {
        device_install: () => catalog(["t/chain-1", false]),
        device_enable: () => catalog(["t/chain-1", true]),
      },
    );
    await m.click("Install add-on");
    expect(m.commands("device_install")).toHaveLength(1);
    expect(m.commands("device_enable")).toEqual([{ id: "t/chain-1", enabled: true }]);
    expect(opened).toBe(1);
  });

  test("a No in the confirmation window, or an install that failed, stops there and opens nothing", async () => {
    let opened = 0;
    const press = (pkg: string, step: "install" | "enable") => (
      <AddonAction
        tool="codex"
        pkg={pkg}
        step={step}
        registry="curated"
        changed={() => {}}
        opens={() => opened++}
      />
    );
    const no = await mount(press("t/chain-2", "install"), {
      device_install: () => catalog(["t/chain-2", false]),
      device_enable: () => Promise.reject("native enable cancelled; t/chain-2 stays disabled"),
    });
    await no.click("Install add-on");
    expect(no.commands("device_enable")).toHaveLength(1);
    const failed = await mount(press("t/chain-3", "install"), {
      device_install: () => Promise.reject("registry unreachable"),
      device_enable: () => catalog(["t/chain-3", true]),
    });
    await failed.click("Install add-on");
    expect(failed.commands("device_enable")).toHaveLength(0);
    const off = await mount(press("t/chain-4", "enable"), {
      device_enable: () => Promise.reject("the signature does not hold"),
    });
    await off.click("Turn on add-on");
    expect(opened).toBe(0);
  });

  test("an install that leaves the add-on on already opens its page without asking", async () => {
    let opened = 0;
    const m = await mount(
      <AddonAction tool="codex" pkg="t/chain-5" step="install" changed={() => {}} opens={() => opened++} />,
      { device_install: () => catalog(["t/chain-5", true]), device_enable: never },
    );
    await m.click("Install add-on");
    expect(m.commands("device_enable")).toHaveLength(0);
    expect(opened).toBe(1);
  });

  test("turned on from setup's step 2, the tool's page opens with the way back to setup", async () => {
    const off = catalog(["pluxy/claude-code", false]);
    const m = await mount(
      toolAction(agents, { catalog: off, offers: [] }, () => {}, true)(client("claude-code")),
      {
        device_enable: () => catalog(["pluxy/claude-code", true]),
      },
    );
    await m.click("Turn on add-on");
    expect(location.hash).toBe("#/claude-code");
    expect(cameFromSetup()).toBe(true);
  });

  test("one step at a time for each add-on, however many rows show it", async () => {
    const reply = later<unknown>();
    let calls = 0;
    const call = () => {
      calls++;
      return reply.promise as never;
    };
    const first = runAddon("t/once", "enable", call, () => {});
    await runAddon("t/once", "enable", call, () => {});
    expect(calls).toBe(1);
    expect(addonStatus("t/once")).toEqual({ working: "enable" });
    reply.done({ status: 200 });
    await first;
    expect(addonStatus("t/once")).toEqual({});
  });
});

// ---- the way back to setup -------------------------------------------------------------------

describe("Back to setup", () => {
  const pressed = async (fromSetup: boolean) => {
    const on = catalog(["pluxy/claude-code", true]);
    const m = await mount(
      toolAction(agents, { catalog: on, offers: [] }, () => {}, fromSetup)(client("claude-code")),
    );
    await m.click("Set up");
    await m.show(<SetupReturn />);
    return m;
  };

  test("a tool's page opened by Set up from step 2 shows the strip, and links back to setup", async () => {
    const m = await pressed(true);
    expect(m.text()).toContain("Setting up pluxy.");
    expect(m.text()).toContain("Back to setup");
  });

  test("a page opened from Tools shows none", async () => {
    const m = await pressed(false);
    expect(m.text()).not.toContain("Back to setup");
    expect(cameFromSetup()).toBe(false);
  });

  test("it goes when the person leaves the tool's page", async () => {
    const m = await pressed(true);
    await m.show(<p>another page</p>);
    expect(cameFromSetup()).toBe(false);
    await m.show(<SetupReturn />);
    expect(m.text()).not.toContain("Back to setup");
  });
});

// ---- what both pages get ---------------------------------------------------------------------

describe("toolHooks", () => {
  const input = (mode: "managed" | "local" | "remote") => ({
    conn: { mode, target: "http://127.0.0.1:8317" },
    agents,
    view: { catalog: catalog(["pluxy/claude-code", true]), offers: [] },
    loadDevice: () => {},
    onRefresh: () => {},
  });

  test("Tools and the setup get the same links, refresh and tools, and isRemote from the mode", () => {
    for (const mode of ["managed", "local"] as const) {
      const { tools, setup } = toolHooks(input(mode));
      expect(tools.isRemote).toBe(false);
      expect(setup.isRemote).toBe(false);
      expect(tools.onRefresh).toBe(setup.onRefresh);
      expect(tools.localPages).toBe(setup.localPages);
      expect(tools.extraTools).toBe(setup.extraTools);
      expect(tools.toolAction).toBeFunction();
      expect(setup.toolAction).toBeFunction();
      // A key's owner is for the Tools page alone.
      expect(tools.keyOwners).toBeFunction();
      expect(setup).not.toHaveProperty("keyOwners");
    }
  });

  test("connected to a pluxy elsewhere: its tools as they are, no add-on buttons or extra rows, eight local links", () => {
    const { tools, setup } = toolHooks(input("remote"));
    for (const hooks of [tools, setup]) {
      expect(hooks.isRemote).toBe(true);
      expect(hooks.toolAction).toBeUndefined();
      expect(hooks.extraTools).toBeUndefined();
      expect(hooks.notes).toBeUndefined();
    }
    expect(tools.localPages).toBe(setup.localPages);
    expect(tools.localPages).toHaveLength(8);
    expect(tools.localPages?.map((p) => p.page).sort()).toEqual(agents.map((a) => a.id).sort());
    expect(tools.localPages?.every((p) => p.name.length > 0)).toBe(true);
  });

  test("a pluxy on this computer gets no local links, so the page keeps its own line", () => {
    expect(toolHooks(input("managed")).tools.localPages).toBeUndefined();
  });
});

describe("the lines around the list", () => {
  const rows = [client("claude-code"), client("codex")];
  const notes = (view: Parameters<typeof toolHooks>[0]["view"]) =>
    toolHooks({
      conn: { mode: "managed", target: "http://127.0.0.1:1" },
      agents,
      view,
      loadDevice: () => {},
      onRefresh: () => {},
    }).tools.notes?.(rows);
  const render = async (node: unknown) => (await mount(node as never)).text();
  const on = catalog(["pluxy/claude-code", true], ["pluxy/execution", true]);

  test("safe mode: one warning above the list, nothing under it", async () => {
    const n = notes({ catalog: { ...on, safe_mode: true }, offers: [] });
    expect(n?.below).toBeUndefined();
    expect(await render(n?.above)).toBe(
      "Safe mode is on, so tool add-ons are paused. Open Add-ons to review them, or start the app again without safe mode.",
    );
  });

  test("a tool with no add-on to be had: one line under the list, with a link to Add-ons", async () => {
    const text = await render(notes({ catalog: on, offers: [] })?.below);
    expect(text).toBe("The app can set these up for you once their add-ons are installed. Open Add-ons");
  });

  test("no such line when every tool has its add-on or is offered one", async () => {
    const offers = [{ name: "curated", packages: [{ id: "pluxy/codex-setup", versions: ["1.0.0"] }] }];
    expect(notes({ catalog: on, offers })?.below).toBeUndefined();
  });

  test("the Launcher add-on missing gets its own line", async () => {
    const text = await render(notes({ catalog: catalog(["pluxy/claude-code", true]), offers: [] })?.below);
    expect(text).toContain("Starting tools from the app needs the Launcher add-on. Open Add-ons");
  });

  test("nothing is said while the add-ons are still read, or when they cannot be", () => {
    expect(notes({})).toEqual({});
    expect(notes({ catalog: on })).toEqual({});
    expect(notes({ catalog: on, unread: true, offers: [] })).toEqual({});
  });
});

// ---- the names of keys -----------------------------------------------------------------------

describe("keyOwners", () => {
  const secret = (id: string, label: string, over: Record<string, unknown> = {}) => ({
    id,
    label,
    client_key: { client: "pluxy/claude-code", core: { origin: "http://127.0.0.1:8317" }, ...over },
  });

  test("names each key by its ID in pluxy, else by its masked form", () => {
    const owners = ownersOf(
      [
        secret("s1", "Claude Code", { key_id: "k1", masked: "sk…11" }),
        secret("s2", "Codex", { masked: "sk…22" }),
        secret("s3", "Nothing to match by"),
      ],
      "http://127.0.0.1:8317/",
    );
    expect([...owners]).toEqual([
      ["k1", "Claude Code"],
      ["sk…22", "Codex"],
    ]);
  });

  test("a key of another pluxy, and a secret the person typed, name nothing here", () => {
    const owners = ownersOf(
      [
        secret("s1", "Claude Code", { key_id: "k1", core: { origin: "http://127.0.0.1:9999" } }),
        { id: "s2", label: "My own key" },
      ],
      "http://127.0.0.1:8317",
    );
    expect(owners.size).toBe(0);
    expect(ownersOf([secret("s1", "Claude Code", { key_id: "k1" })], undefined).size).toBe(0);
  });
});

// ---- the navigation ----------------------------------------------------------------------------

describe("the window's pages", () => {
  // Every page of the window, as main.tsx lists them: core's, with the app's hooks, then the tools'.
  const hooks = toolHooks({
    conn: { mode: "managed", target: "http://127.0.0.1:1" },
    agents,
    view: {},
    // GET /setup failed, or answered 404: the app reads no tool of its own.
    extraTools: undefined,
    loadDevice: () => {},
    onRefresh: () => {},
  });
  const pages = [...corePages({ general: { signOut: false }, ...hooks }), ...agents];

  test("the navigation has five entries, and no tool page is one of them or a topic", () => {
    expect(DESTINATIONS).toEqual(["home", "accounts", "activity", "tools", "settings"]);
    for (const dest of DESTINATIONS) expect(destinationHome(pages, dest)).toBeDefined();
    expect(destinationHome(pages, "tools")?.id).toBe("tools");
    const tools = place(pages, "tools");
    expect(tools?.topics).toEqual([]);
    for (const a of agents)
      expect(tools?.topics.flatMap((g) => g.pages.map((p) => p.id))).not.toContain(a.id);
  });

  test("all eight tool pages open as Tools / {Tool}, whatever GET /setup said", () => {
    expect(agents).toHaveLength(8);
    for (const a of agents) {
      expect(pages.filter((p) => p.id === a.id)).toHaveLength(1);
      const at = place(pages, a.id);
      expect(at?.above.map((p) => p.id)).toEqual(["tools"]);
      expect(at?.dest).toBe("tools");
    }
  });

  test("no old page is left: no Beginner Mode, no Manage Clients", () => {
    expect(pages.some((p) => p.id === "easy-mode" || p.id === "clients")).toBe(false);
    expect(pages.some((p) => /beginner|manage clients/i.test(p.title))).toBe(false);
  });
});

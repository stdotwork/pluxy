// The Extensions page (kit/pages/extensions.tsx) and an extension's settings form: what a person
// sees and what each press sends, against a fake core.
import { afterEach, beforeEach, expect, jest, test } from "bun:test";
import type { ComponentChildren } from "preact";
import { act } from "preact/test-utils";
import {
  api,
  type ExtensionStatus,
  type Package,
  type Registry,
  type Settings,
  type Status,
} from "../kit/api";
import type { Feature, Host, Method, Reply } from "../kit/host";
import { ExtensionsPage } from "../kit/pages/extensions";
import { type Kit, KitContext } from "../kit/ui";
import { all, FakeNode, mountIn, restoreDom, settle } from "./fakeDom";

const unmounts: (() => Promise<void>)[] = [];
// The fake document has no HTMLElement; Detail asks it where focus is.
const hadHTMLElement = Object.getOwnPropertyDescriptor(globalThis, "HTMLElement");
beforeEach(() => {
  unmounts.length = 0;
  Object.defineProperty(globalThis, "HTMLElement", { configurable: true, value: FakeNode });
});
afterEach(() => {
  if (jest.isFakeTimers()) {
    jest.clearAllTimers();
    jest.useRealTimers();
  }
});
afterEach(async () => {
  // Unmount while the fake document is still there: Detail looks at it as it closes.
  for (const unmount of unmounts.splice(0)) await unmount();
  restoreDom();
  if (hadHTMLElement) Object.defineProperty(globalThis, "HTMLElement", hadHTMLElement);
  else Reflect.deleteProperty(globalThis, "HTMLElement");
});

const pkg = (id: string, more: Partial<Package> = {}): Package => ({
  id,
  version: "1.4.0",
  registry: "official",
  url: "https://example.invalid/x.zip",
  sha256: "0123456789abcdef0123456789abcdef",
  enabled: true,
  installed_at: "2026-11-20T10:00:00Z",
  state: "active",
  ...more,
});

const copy = (id: string, more: Partial<ExtensionStatus> = {}): ExtensionStatus => ({
  id,
  version: "1.4.0",
  generation: 1,
  state: "active",
  pid: 4242,
  restarts: 0,
  crashes_in_a_row: 0,
  active_requests: 0,
  current: true,
  ...more,
});

const registry = (name: string, ids: string[], error?: string): Registry => ({
  name,
  error,
  packages: ids.map((id) => ({
    id,
    releases: [{ version: "2.0.0", protocol: [0], platforms: ["linux/amd64"] }],
  })),
});

interface Call {
  method: Method;
  path: string;
  body?: unknown;
}

const SETTINGS: Settings = {
  schema: {
    type: "object",
    properties: {
      greeting: { type: "string", title: "Greeting", description: "Said first." },
      token: { type: "string", title: "Export token", writeOnly: true },
      verbose: { type: "boolean", title: "Detailed view" },
    },
  },
  values: { greeting: "hi", verbose: false },
  set: ["token"],
};

/** A core that keeps its packages, and answers the calls the Extensions page makes. */
function world(
  init: {
    packages?: Package[];
    status?: Partial<Status>;
    copies?: ExtensionStatus[];
    registries?: Registry[];
    fail?: (call: Call) => Reply<unknown> | undefined;
    settings?: Settings | "none";
    hold?: Promise<void>;
  } = {},
) {
  const calls: Call[] = [];
  const packages = init.packages ?? [
    pkg("pluxy/claude"),
    pkg("pluxy/codex", { enabled: false, state: "disabled" }),
  ];
  let changed = 0;
  const answer = (call: Call): Reply<unknown> => {
    calls.push(call);
    const { method, path } = call;
    const refused = init.fail?.(call);
    if (refused) return refused;
    if (method === "GET" && path === "/packages") return { status: 200, body: { packages: [...packages] } };
    if (method === "GET" && path === "/status") {
      const body: Status = {
        safe_mode: false,
        desired: { revision: 2, at: "2026-11-20T10:00:00Z" },
        active: { revision: 2, at: "2026-11-20T10:00:00Z" },
        extensions: init.copies ?? [copy("pluxy/claude", { active_requests: 3 })],
        version: "dev",
        commit: "unknown",
        built: "unknown",
        ...init.status,
      };
      return { status: 200, body };
    }
    if (method === "GET" && path === "/registries") {
      return {
        status: 200,
        body: { registries: init.registries ?? [registry("official", ["pluxy/gemini"])] },
      };
    }
    if (method === "GET" && path.endsWith("/config")) {
      return init.settings === "none"
        ? { status: 404, error: { code: "not_found", message: "no settings" } }
        : { status: 200, body: init.settings ?? SETTINGS };
    }
    if (method === "PATCH" && path.endsWith("/config"))
      return { status: 200, body: init.settings ?? SETTINGS };
    const parts = path.split("/");
    const action = parts.at(-1);
    if (method === "POST" && (action === "enable" || action === "disable")) {
      const p = packages.find((x) => x.id === `${parts[2]}/${parts[3]}`);
      if (p) {
        p.enabled = path.endsWith("/enable");
        p.state = p.enabled ? "active" : "disabled";
      }
      return { status: 200, body: { package: p } };
    }
    if (method === "POST" && path === "/packages") {
      const b = call.body as { id: string };
      packages.push(pkg(b.id, { version: "2.0.0" }));
      return { status: 201, body: { package: packages.at(-1) } };
    }
    if (method === "DELETE" && path.startsWith("/packages/")) {
      const at = packages.findIndex((p) => path === `/packages/${p.id}`);
      if (at >= 0) packages.splice(at, 1);
      return { status: 204 };
    }
    return { status: 404, error: { code: "not_found", message: "no such call" } };
  };
  const host: Host = {
    call: async <T,>(method: Method, path: string, body?: unknown) => {
      if (init.hold && method !== "GET") await init.hold;
      return answer({ method, path, body }) as Reply<T>;
    },
    signedIn: () => true,
    signIn: async () => undefined,
    signOut: () => {},
    prefs: { get: () => null, set: () => {} },
    features: new Set<Feature>(),
  };
  const kit = {
    host,
    api: api(host),
    unauthorized: () => {},
    extensionsChanged: () => {
      changed++;
    },
  } as Kit;
  const mount = async (tree: ComponentChildren) => {
    const m = await mountIn(<KitContext.Provider value={kit}>{tree}</KitContext.Provider>);
    unmounts.push(m.unmount);
    return m;
  };
  return { calls, packages, mount, changed: () => changed };
}

const attribute = (node: FakeNode, name: string) => node.attributes.find((a) => a.name === name)?.value;
const descend = (node: FakeNode): FakeNode[] => [node, ...node.childNodes.flatMap(descend)];
const withClass = (root: FakeNode, cls: string) =>
  descend(root).filter((n) => (attribute(n, "class") ?? "").split(" ").includes(cls));
const buttonIn = (root: FakeNode, label: string) => {
  const found = all(root, "button").find((b) => b.textContent === label);
  if (!found) throw new Error(`no button ${label} in ${root.textContent.slice(0, 300)}`);
  return found;
};
const hasButton = (root: FakeNode, label: string) => all(root, "button").some((b) => b.textContent === label);
const press = async (node: FakeNode) => {
  await act(async () => {
    node.dispatchEvent(new Event("click"));
  });
  await settle();
};
const type = async (input: FakeNode, value: string) => {
  input.value = value;
  await act(async () => {
    input.dispatchEvent(new Event("input"));
  });
};
/** Opens an extension's row and waits for what its detail reads. */
const open = async (root: FakeNode, id: string) => {
  await press(rowOf(root, id));
  await settle();
};
const rowOf = (root: FakeNode, id: string) => {
  const row = withClass(root, "ext-row").find((r) => r.textContent.includes(id) && r.localName === "button");
  if (!row) throw new Error(`no row for ${id}`);
  return row;
};
const calls = (w: { calls: Call[] }, method: Method, prefix: string) =>
  w.calls.filter((c) => c.method === method && c.path.startsWith(prefix));
const labelled = (root: FakeNode, tag: string, label: string) => {
  const found = all(root, tag).find((n) => attribute(n, "aria-label") === label);
  if (!found) throw new Error(`no ${tag} labelled ${label}`);
  return found;
};

// ---- the list ---------------------------------------------------------------------------------

test("the page says what an extension is, and each row shows the provider, its ID, version and state", async () => {
  const w = world();
  const m = await w.mount(<ExtensionsPage />);
  expect(m.text()).toContain(
    "Each extension is a program that connects pluxy to one provider. Accounts need their provider's extension.",
  );
  const claude = rowOf(m.root, "pluxy/claude");
  expect(claude.textContent).toContain("Claude");
  expect(claude.textContent).toContain("1.4.0");
  expect(claude.textContent).toContain("Running");
  // A turned-off extension reads Turned off, as an account does.
  expect(rowOf(m.root, "pluxy/codex").textContent).toContain("Turned off");
  expect(m.text()).toContain("Installed");
  expect(m.text()).toContain("Available to install");
});

test("none of the old words are left on the page", async () => {
  const w = world();
  const m = await w.mount(<ExtensionsPage />);
  await open(m.root, "pluxy/claude");
  const text = m.text();
  for (const old of ["Digest", "Generation", "sandboxed", "Registry", "Disable", "Enable", "Not installed"]) {
    expect(text).not.toContain(old);
  }
});

test("with nothing installed it says so, and the list on offer is still there", async () => {
  const w = world({ packages: [] });
  const m = await w.mount(<ExtensionsPage />);
  expect(m.text()).toContain("No extensions yet");
  expect(m.text()).toContain("pluxy needs one for each provider you use.");
  expect(m.text()).toContain("pluxy/gemini");
});

test("while the lists load, the page says Loading and shows no empty text", async () => {
  let release: () => void = () => {};
  const held = new Promise<void>((resolve) => {
    release = resolve;
  });
  const host: Host = {
    call: async <T,>() => {
      await held;
      return { status: 200, body: { packages: [], registries: [] } } as Reply<T>;
    },
    signedIn: () => true,
    signIn: async () => undefined,
    signOut: () => {},
    prefs: { get: () => null, set: () => {} },
    features: new Set<Feature>(),
  };
  const kit = { host, api: api(host), unauthorized() {}, extensionsChanged() {} } as Kit;
  const m = await mountIn(
    <KitContext.Provider value={kit}>
      <ExtensionsPage />
    </KitContext.Provider>,
  );
  unmounts.push(m.unmount);
  expect(m.text()).toContain("Loading extensions…");
  expect(m.text()).toContain("Loading what is on offer…");
  expect(m.text()).not.toContain("No extensions yet");
  expect(m.text()).not.toContain("no list to install");
  release();
  await settle();
  expect(m.text()).not.toContain("Loading extensions…");
});

test("a list that cannot be read says so and offers Try again, instead of looking empty", async () => {
  const w = world({
    fail: (c) =>
      c.path === "/packages" ? { status: 500, error: { code: "internal", message: "disk gone" } } : undefined,
  });
  const m = await w.mount(<ExtensionsPage />);
  expect(m.text()).toContain("Could not read the installed extensions");
  expect(m.text()).not.toContain("No extensions yet");
  expect(hasButton(m.root, "Try again")).toBe(true);
});

test("safe mode is said once, as a warning", async () => {
  const w = world({ status: { safe_mode: true } });
  const m = await w.mount(<ExtensionsPage />);
  const text =
    "pluxy was started in safe mode, so no extension runs. Start pluxy again normally to leave it.";
  expect(m.text().split(text)).toHaveLength(2);
  const notice = withClass(m.root, "notice").find((n) => n.textContent === text);
  expect(notice && attribute(notice, "data-tone")).toBe("warn");

  const normal = world();
  const n = await normal.mount(<ExtensionsPage />);
  expect(n.text()).not.toContain("safe mode");
});

test("an extension that is starting is read again until it runs, and nothing is polled otherwise", async () => {
  jest.useFakeTimers();
  const advance = async (ms: number) => {
    await act(async () => {
      jest.advanceTimersByTime(ms);
    });
    await settle();
  };
  const reads = (w: { calls: Call[] }) => calls(w, "GET", "/packages").length;
  const w = world({ packages: [pkg("pluxy/claude", { state: "starting" })] });
  const m = await w.mount(<ExtensionsPage />);
  expect(rowOf(m.root, "pluxy/claude").textContent).toContain("Starting");
  const first = reads(w);
  (w.packages[0] as Package).state = "active";
  await advance(2_000);
  expect(reads(w)).toBeGreaterThan(first);
  expect(rowOf(m.root, "pluxy/claude").textContent).toContain("Running");
  // Running: no more reads on a timer.
  const settled = reads(w);
  await advance(30_000);
  expect(reads(w)).toBe(settled);
});

// ---- the detail -------------------------------------------------------------------------------

test("a row opens its facts, with the running copy's requests from active_requests", async () => {
  const w = world();
  const m = await w.mount(<ExtensionsPage />);
  expect(m.text()).not.toContain("Checksum");
  await open(m.root, "pluxy/claude");
  const facts = m.text();
  for (const label of [
    "State",
    "Version",
    "Installed from",
    "Installed",
    "Checksum",
    "Process ID",
    "Requests now",
    "Restarts",
  ]) {
    expect(facts).toContain(label);
  }
  expect(facts).toContain("official");
  expect(facts).toContain("0123456789abcdef");
  expect(facts).not.toContain("0123456789abcdef0");
  const requests = descend(m.root).find((n) => n.localName === "div" && n.textContent === "Requests now3");
  expect(requests).toBeDefined();
  expect(facts).toContain(
    "Extensions run as separate programs with your user's permissions. Install only ones you trust.",
  );
  expect(facts).not.toContain("Why it stopped");
  expect(facts).not.toContain("Finishing requests on the version before");
});

test("a copy still finishing requests is labelled, and an error shows why it stopped", async () => {
  const w = world({
    packages: [pkg("pluxy/claude", { state: "crashed", error: "exit status 2" })],
    copies: [
      copy("pluxy/claude", {
        generation: 1,
        current: false,
        active_requests: 1,
        last_error: "old copy broke",
      }),
      copy("pluxy/claude", { generation: 2, current: true, pid: 5151 }),
    ],
  });
  const m = await w.mount(<ExtensionsPage />);
  await open(m.root, "pluxy/claude");
  expect(m.text()).toContain("Why it stopped");
  expect(m.text()).toContain("exit status 2");
  expect(m.text().split("Finishing requests on the version before")).toHaveLength(2);
  // The running copy comes first, the old one after its label.
  expect(m.text().indexOf("5151")).toBeLessThan(m.text().indexOf("Finishing requests on the version before"));
  expect(m.text()).toContain("old copy broke");
});

test("Turn off says what it does to the accounts, sends one call, and the row then reads Turned off", async () => {
  const w = world();
  const m = await w.mount(<ExtensionsPage />);
  await open(m.root, "pluxy/claude");
  expect(m.text()).toContain("Its accounts stop taking requests until you turn it on again.");
  const turnOff = buttonIn(m.root, "Turn off");
  expect(attribute(turnOff, "data-variant")).toBe("quiet");
  await press(turnOff);
  expect(calls(w, "POST", "/packages/pluxy/claude/disable")).toHaveLength(1);
  expect(w.changed()).toBeGreaterThan(0);
  expect(rowOf(m.root, "pluxy/claude").textContent).toContain("Turned off");
  // Turned off, the button is Turn on, the main action, and the sentence about accounts is gone.
  const turnOn = buttonIn(m.root, "Turn on");
  expect(attribute(turnOn, "data-variant")).toBe("primary");
  expect(m.text()).not.toContain("Its accounts stop taking requests");
  await press(turnOn);
  expect(calls(w, "POST", "/packages/pluxy/claude/enable")).toHaveLength(1);
  expect(rowOf(m.root, "pluxy/claude").textContent).toContain("Running");
});

test("a refused Turn off shows its reason under its button, and can be pressed again", async () => {
  let refuse = true;
  const w = world({
    fail: (c) =>
      refuse && c.path.endsWith("/disable")
        ? { status: 500, error: { code: "internal", message: "could not stop it" } }
        : undefined,
  });
  const m = await w.mount(<ExtensionsPage />);
  await open(m.root, "pluxy/claude");
  await press(buttonIn(m.root, "Turn off"));
  const action = withClass(m.root, "ext-action")[0] as FakeNode;
  expect(action.textContent).toContain("could not stop it");
  const second = withClass(m.root, "ext-action")[1] as FakeNode;
  expect(second.textContent).not.toContain("could not stop it");
  refuse = false;
  await press(buttonIn(m.root, "Turn off"));
  expect(calls(w, "POST", "/packages/pluxy/claude/disable")).toHaveLength(2);
  expect(rowOf(m.root, "pluxy/claude").textContent).toContain("Turned off");
});

test("Remove asks first, says what stays, and removes only on Yes, remove", async () => {
  const w = world();
  const m = await w.mount(<ExtensionsPage />);
  await open(m.root, "pluxy/claude");
  expect(m.text()).toContain(
    "Removes the program. Its accounts stay and work again if you install it again.",
  );
  await press(buttonIn(m.root, "Remove"));
  expect(calls(w, "DELETE", "/packages")).toHaveLength(0);
  expect(hasButton(m.root, "Cancel")).toBe(true);
  await press(buttonIn(m.root, "Cancel"));
  expect(calls(w, "DELETE", "/packages")).toHaveLength(0);
  await press(buttonIn(m.root, "Remove"));
  await press(buttonIn(m.root, "Yes, remove"));
  expect(calls(w, "DELETE", "/packages/pluxy/claude")).toHaveLength(1);
  expect(w.changed()).toBeGreaterThan(0);
  expect(
    withClass(m.root, "ext-row").some(
      (r) => r.textContent.includes("pluxy/claude") && r.localName === "button",
    ),
  ).toBe(false);
  // The detail closed with the extension, and it is on offer again.
  expect(m.text()).not.toContain("Checksum");
});

test("a refused Remove shows its reason under Remove", async () => {
  const w = world({
    fail: (c) =>
      c.method === "DELETE"
        ? { status: 409, error: { code: "conflict", message: "still in use" } }
        : undefined,
  });
  const m = await w.mount(<ExtensionsPage />);
  await open(m.root, "pluxy/claude");
  await press(buttonIn(m.root, "Remove"));
  await press(buttonIn(m.root, "Yes, remove"));
  const action = withClass(m.root, "ext-action")[1] as FakeNode;
  expect(action.textContent).toContain("still in use");
  expect(hasButton(action, "Remove")).toBe(true);
});

// ---- what can be installed --------------------------------------------------------------------

test("Install sends the extension and its list, says Installing… meanwhile, and then lists it as installed", async () => {
  let release: () => void = () => {};
  const hold = new Promise<void>((resolve) => {
    release = resolve;
  });
  const w = world({ hold });
  const m = await w.mount(<ExtensionsPage />);
  expect(m.text()).toContain("Gemini");
  const install = buttonIn(m.root, "Install");
  await press(install);
  expect(hasButton(m.root, "Installing…")).toBe(true);
  release();
  await settle();
  expect(calls(w, "POST", "/packages")).toEqual([
    { method: "POST", path: "/packages", body: { id: "pluxy/gemini", registry: "official", enable: true } },
  ]);
  expect(rowOf(m.root, "pluxy/gemini")).toBeDefined();
  expect(hasButton(m.root, "Install")).toBe(false);
  expect(m.text()).toContain("Every extension on offer is installed.");
});

test("a refused Install says why under its row, and can be pressed again", async () => {
  const w = world({
    fail: (c) =>
      c.method === "POST" && c.path === "/packages"
        ? { status: 400, error: { code: "bad_request", message: "checksum does not match" } }
        : undefined,
  });
  const m = await w.mount(<ExtensionsPage />);
  await press(buttonIn(m.root, "Install"));
  expect(withClass(m.root, "ext-offer")[0]?.textContent).toContain("checksum does not match");
  await press(buttonIn(m.root, "Install"));
  expect(calls(w, "POST", "/packages")).toHaveLength(2);
});

test("with no list to install from, it says why and leads to the settings file", async () => {
  const w = world({ registries: [] });
  const m = await w.mount(<ExtensionsPage />);
  expect(m.text()).toContain("This pluxy has no list to install extensions from.");
  expect(m.text()).toContain("A pluxy built from source has none until one is added to its settings file.");
  const link = all(m.root, "a").find((a) => a.textContent === "Open the settings file");
  expect(link && attribute(link, "href")).toBe("#/config/source");
  expect(m.text()).not.toContain("Every extension on offer");
  expect(m.text()).not.toContain("core's config");
});

test("a list that failed is named, and does not claim everything is installed", async () => {
  const w = world({
    registries: [registry("mirror", [], "timed out")],
    packages: [pkg("pluxy/claude")],
  });
  const m = await w.mount(<ExtensionsPage />);
  expect(m.text()).toContain("Could not read the list mirror: timed out");
  expect(m.text()).not.toContain("Every extension on offer is installed.");
  expect(m.text()).not.toContain("no list to install");
});

test("another list can still offer extensions when one fails", async () => {
  const w = world({
    registries: [registry("mirror", [], "timed out"), registry("official", ["pluxy/gemini"])],
  });
  const m = await w.mount(<ExtensionsPage />);
  expect(m.text()).toContain("Could not read the list mirror: timed out");
  expect(hasButton(m.root, "Install")).toBe(true);
});

// ---- an extension's settings ------------------------------------------------------------------

test("settings are rows with their own Save; a secret never prefills, and Clear sends the clear", async () => {
  const w = world();
  const m = await w.mount(<ExtensionsPage />);
  await open(m.root, "pluxy/claude");
  const form = all(m.root, "form").find((f) => attribute(f, "aria-label") === "Settings of pluxy/claude");
  expect(form).toBeDefined();
  expect(descend(form as FakeNode).filter((n) => attribute(n, "data-part") === "setting-row")).toHaveLength(
    3,
  );
  expect(form?.textContent).toContain("Said first.");
  expect(form?.textContent).toContain("A value is set.");

  const token = labelled(form as FakeNode, "input", "Export token");
  expect(attribute(token, "type")).toBe("password");
  expect(token.value ?? "").toBe("");
  expect(labelled(form as FakeNode, "input", "Greeting").value).toBe("hi");

  // Saving with nothing typed sends no secret at all.
  await act(async () => {
    (form as FakeNode).dispatchEvent(new Event("submit"));
  });
  await settle();
  expect(calls(w, "PATCH", "/extensions/pluxy/claude/config").at(-1)?.body).toEqual({ values: {} });

  // A typed secret is sent; Clear sends null.
  await type(token, "fresh-token");
  await act(async () => {
    (form as FakeNode).dispatchEvent(new Event("submit"));
  });
  await settle();
  expect(calls(w, "PATCH", "/extensions/pluxy/claude/config").at(-1)?.body).toEqual({
    values: { token: "fresh-token" },
  });
  const clear = all(form as FakeNode, "input").find(
    (i) => attribute(i, "type") === "checkbox" && i.parentNode?.textContent === "Clear Export token",
  );
  expect(clear).toBeDefined();
  (clear as FakeNode).checked = true;
  await act(async () => {
    (clear as FakeNode).dispatchEvent(new Event("change"));
  });
  await act(async () => {
    (form as FakeNode).dispatchEvent(new Event("submit"));
  });
  await settle();
  expect(calls(w, "PATCH", "/extensions/pluxy/claude/config").at(-1)?.body).toEqual({
    values: { token: null },
  });
});

test("a turned-off extension shows no settings, and one without settings shows no form", async () => {
  const w = world();
  const m = await w.mount(<ExtensionsPage />);
  await open(m.root, "pluxy/codex");
  expect(all(m.root, "form")).toHaveLength(0);
  expect(calls(w, "GET", "/extensions/pluxy/codex/config")).toHaveLength(0);

  const none = world({ settings: "none" });
  const n = await none.mount(<ExtensionsPage />);
  await open(n.root, "pluxy/claude");
  expect(all(n.root, "form")).toHaveLength(0);
  expect(n.text()).not.toContain("Settings");
});

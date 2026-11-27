// The Config page against a fake core: the Settings tab's rows that save on change, the Settings
// file tab, a settings file that does not parse, and Replace key. The page is driven through
// Preact's DOM renderer and a small fake DOM.
import { afterEach, beforeEach, expect, test } from "bun:test";
import { act } from "preact/test-utils";
import { api } from "../kit/api";
import type { CoreSettings } from "../kit/api/settings";
import type { Feature, Host, Method, Reply } from "../kit/host";
import { ConfigPage } from "../kit/pages/config";
import * as configSettings from "../kit/pages/configSettings";
import { PrivacyContext, privacyMasks } from "../kit/privacy";
import { type Kit, KitContext } from "../kit/ui";
import { all, type FakeNode, mountIn, restoreDom, settle } from "./fakeDom";

const unmounts: (() => Promise<void>)[] = [];
beforeEach(() => {
  Object.defineProperty(globalThis, "location", {
    configurable: true,
    writable: true,
    value: { hash: "#/config", href: "http://panel/#/config" },
  });
  Object.defineProperty(globalThis, "history", {
    configurable: true,
    writable: true,
    value: {
      replaceState: (_: unknown, __: string, url: string) => {
        location.hash = url;
      },
    },
  });
});
afterEach(async () => {
  for (const unmount of unmounts.splice(0)) await unmount();
  restoreDom();
  Reflect.deleteProperty(globalThis, "location");
  Reflect.deleteProperty(globalThis, "history");
});

const SETTINGS: CoreSettings = {
  routing: {
    strategy: "round-robin",
    "session-affinity": false,
    "session-affinity-ttl": "1h",
    "force-model-prefix": false,
    retry: { "request-retry": null, "max-retry-credentials": 0, "max-retry-interval": 0 },
    cooldown: {
      "disable-cooling": false,
      "transient-error-cooldown-seconds": 0,
      "quota-response-mode": "retryable",
    },
  },
  observability: {
    logs: {
      "logging-to-file": false,
      "logs-max-total-size-mb": 0,
      "request-log": false,
      "error-logs-max-files": 10,
    },
    usage: { "usage-statistics-enabled": true },
  },
  token_count_estimate: true,
  "proxy-url": "socks5://redacted:redacted@proxy.example:1080",
};
const FILE = '{\n  "management_key": "<hidden>",\n  "logs": {}\n}\n';
const DOC: Reply<unknown> = { status: 200, body: { format: "json", hash: "h1", content: FILE } };
const STATUS: Reply<unknown> = {
  status: 200,
  body: {
    safe_mode: false,
    desired: { revision: 3, at: "2026-11-27T00:00:00Z" },
    active: { revision: 3, at: "2026-11-27T00:00:00Z" },
    extensions: [],
    version: "dev",
    commit: "unknown",
    built: "unknown",
  },
};

interface Call {
  method: Method;
  path: string;
  body?: unknown;
}

/** A core that answers the calls the Config page makes; answer overrides any of them. */
function world(
  answer: (call: Call) => Reply<unknown> | Promise<Reply<unknown>> | undefined = () => undefined,
  features: Feature[] = ["rotateManagementKey"],
) {
  const calls: Call[] = [];
  const host = {
    call: async (method: Method, path: string, body?: unknown): Promise<Reply<unknown>> => {
      const call = { method, path, body };
      calls.push(call);
      const own = await answer(call);
      if (own) return own;
      if (method === "GET" && path === "/settings") return { status: 200, body: structuredClone(SETTINGS) };
      if (method === "PATCH" && path === "/settings") return { status: 200, body: structuredClone(SETTINGS) };
      if (method === "GET" && path === "/config") return DOC;
      if (method === "GET" && path === "/status") return STATUS;
      return { status: 404, error: { code: "not_found", message: "no such call" } };
    },
    signedIn: () => true,
    prefs: { get: () => null, set: () => {} },
    features: new Set(features),
  } as unknown as Host;
  const kit = {
    host,
    api: api(host),
    themes: { resolved: { layout: { tabs: "top" } }, subscribe: () => () => {} },
    unauthorized: () => {},
    extensionsChanged: () => {},
  } as unknown as Kit;
  const mount = async () => {
    const m = await mountIn(
      <KitContext.Provider value={kit}>
        <PrivacyContext.Provider value={privacyMasks(false)}>
          <ConfigPage />
        </PrivacyContext.Provider>
      </KitContext.Provider>,
    );
    unmounts.push(m.unmount);
    return m;
  };
  return { calls, mount };
}

const attribute = (node: FakeNode, name: string) => node.attributes.find((a) => a.name === name)?.value;
const descend = (node: FakeNode): FakeNode[] => [node, ...node.childNodes.flatMap(descend)];
const withPart = (root: FakeNode, part: string) =>
  descend(root).filter((n) => attribute(n, "data-part") === part);
const withClass = (root: FakeNode, name: string) =>
  descend(root).filter((n) => (attribute(n, "class") ?? "").split(" ").includes(name));
const rows = (root: FakeNode) => withPart(root, "setting-row");
const rowNames = (root: FakeNode) => rows(root).map((r) => withClass(r, "setting-name")[0]?.textContent);
const row = (root: FakeNode, name: string) => {
  const found = rows(root).find((r) => withClass(r, "setting-name")[0]?.textContent === name);
  if (!found) throw new Error(`no row ${name} in ${rowNames(root).join(", ")}`);
  return found;
};
const buttonIn = (root: FakeNode, label: string) => {
  const found = all(root, "button").find((b) => b.textContent === label);
  if (!found) throw new Error(`no button ${label} in ${root.textContent.slice(0, 300)}`);
  return found;
};
const hasButton = (root: FakeNode, label: string) => all(root, "button").some((b) => b.textContent === label);
const tabs = (root: FakeNode) =>
  all(root, "button")
    .filter((b) => attribute(b, "role") === "tab")
    .map((b) => b.textContent);
const press = async (node: FakeNode) => {
  await act(async () => {
    node.dispatchEvent(new Event("click"));
  });
  await settle();
};
const toggle = async (input: FakeNode, on: boolean) => {
  input.checked = on;
  await act(async () => {
    input.dispatchEvent(new Event("change"));
  });
};
const type = async (input: FakeNode, value: string) => {
  input.value = value;
  await act(async () => {
    input.dispatchEvent(new Event("input"));
  });
};
const blur = async (input: FakeNode) => {
  await act(async () => {
    input.dispatchEvent(new Event("blur"));
  });
  await settle();
};
/** A reply the test answers when it wants. */
function held() {
  let answer: (r: Reply<unknown>) => void = () => {};
  const reply = new Promise<Reply<unknown>>((resolve) => {
    answer = resolve;
  });
  return { reply, answer };
}

test("the tabs are exactly Settings and Settings file", async () => {
  const { root } = await world().mount();
  expect(tabs(root)).toEqual(["Settings", "Settings file"]);
});

test("the Settings tab has no routing or token-count row, and shows each setting's place in the file", async () => {
  const { root } = await world().mount();
  expect(rowNames(root)).toEqual([
    "Outbound proxy",
    "Account order, retries and cooldowns",
    "Write pluxy's log to a file",
    "Largest log size (MB)",
    "Failed requests to keep",
    "Keep a record of requests",
    "Log every request",
    "Keys for coding tools",
    "Management key",
  ]);
  const text = root.textContent;
  expect(text).not.toContain("routing.");
  expect(text).not.toContain("token_count_estimate");
  expect(text).not.toContain("Estimate token counts");
  expect(text).not.toContain("Save changes");
  expect(withClass(row(root, "Log every request"), "setting-key")[0]?.textContent).toBe("logs.request_log");
  expect(withClass(row(root, "Management key"), "setting-key")[0]?.textContent).toBe("management_key");
  const links = all(root, "a").map((a) => [a.textContent, attribute(a, "href")]);
  expect(links).toContainEqual(["Open Order & fallback", "#/routing"]);
  expect(links).toContainEqual(["Open Tools", "#/tools"]);
  expect(row(root, "Log every request").textContent).toContain("the Failed requests tab on Logs lists none");
});

test("a row change sends one PATCH with only that field, says Saving… and then shows what core holds", async () => {
  const patch = held();
  const w = world((c) => (c.method === "PATCH" ? patch.reply : undefined));
  const { root } = await w.mount();
  const before = w.calls.length;
  const input = all(row(root, "Log every request"), "input")[0] as FakeNode;
  await toggle(input, true);
  await settle();
  const patches = w.calls.slice(before).filter((c) => c.method === "PATCH");
  expect(patches).toEqual([
    { method: "PATCH", path: "/settings", body: { observability: { logs: { "request-log": true } } } },
  ]);
  expect(row(root, "Log every request").textContent).toContain("Saving…");
  const saved = structuredClone(SETTINGS);
  saved.observability.logs["request-log"] = true;
  patch.answer({ status: 200, body: saved });
  await settle();
  expect(row(root, "Log every request").textContent).not.toContain("Saving…");
  expect((all(row(root, "Log every request"), "input")[0] as FakeNode).checked).toBe(true);
  // A saved row reads the file again, as the file has no edits.
  expect(w.calls.slice(before).filter((c) => c.path === "/config")).toHaveLength(1);
});

test("a refused PATCH puts the row back with core's reason", async () => {
  const w = world((c) =>
    c.method === "PATCH"
      ? { status: 400, error: { code: "invalid_request", message: "no such proxy" } }
      : undefined,
  );
  const { root } = await w.mount();
  const input = all(row(root, "Write pluxy's log to a file"), "input")[0] as FakeNode;
  await toggle(input, true);
  await settle();
  const r = row(root, "Write pluxy's log to a file");
  expect((all(r, "input")[0] as FakeNode).checked).toBe(false);
  expect(withPart(r, "setting-row")[0]?.textContent).toContain("no such proxy");
  expect(all(r, "p").some((p) => attribute(p, "data-tone") === "danger")).toBe(true);
});

test("a number the field refuses goes back without calling core", async () => {
  const w = world();
  const { root } = await w.mount();
  const before = w.calls.length;
  const input = all(row(root, "Failed requests to keep"), "input")[0] as FakeNode;
  await type(input, "2.5");
  await blur(input);
  expect(w.calls.slice(before).filter((c) => c.method === "PATCH")).toHaveLength(0);
  const r = row(root, "Failed requests to keep");
  expect((all(r, "input")[0] as FakeNode).value).toBe("10");
  expect(r.textContent).toContain("Enter a whole number.");
});

test("an untouched proxy address is never sent, and a new one is sent alone on Enter", async () => {
  const w = world();
  const { root } = await w.mount();
  const before = w.calls.length;
  const input = all(row(root, "Outbound proxy"), "input")[0] as FakeNode;
  expect(input.value).toBe(SETTINGS["proxy-url"]);
  await blur(input);
  expect(w.calls.slice(before).filter((c) => c.method === "PATCH")).toHaveLength(0);
  await type(input, "");
  await act(async () => {
    input.dispatchEvent(Object.assign(new Event("keydown"), { key: "Enter" }));
  });
  await settle();
  expect(w.calls.slice(before).filter((c) => c.method === "PATCH")).toEqual([
    { method: "PATCH", path: "/settings", body: { "proxy-url": "" } },
  ]);
});

test("a settings file that does not parse turns every setting off and says so on both tabs", async () => {
  const w = world((c) =>
    c.path === "/config" && c.method === "GET"
      ? {
          status: 422,
          error: {
            code: "invalid_config",
            message:
              "the config file does not parse, so it cannot be shown with its secrets hidden: line 3: unexpected }",
          },
        }
      : undefined,
  );
  const { root } = await w.mount();
  const message =
    "pluxy's settings file has a mistake, so it cannot be shown or changed here: line 3: unexpected }. pluxy keeps working with the settings it read before.";
  expect(root.textContent).toContain(message);
  const settingRows = rows(root).filter((r) => all(r, "fieldset").length > 0 && !all(r, "a").length);
  expect(settingRows.map((r) => withClass(r, "setting-name")[0]?.textContent)).toEqual([
    "Outbound proxy",
    "Write pluxy's log to a file",
    "Largest log size (MB)",
    "Failed requests to keep",
    "Keep a record of requests",
    "Log every request",
    "Management key",
  ]);
  for (const r of settingRows) {
    expect((all(r, "fieldset")[0] as FakeNode).disabled).toBe(true);
    expect(r.textContent).toContain("Fix the settings file first.");
  }
  await press(buttonIn(root, "Settings file"));
  expect(root.textContent).toContain(message);
  expect(all(root, "textarea")).toHaveLength(0);
  expect(hasButton(root, "Try again")).toBe(true);
});

test("a newer settings file pluxy refused is said on the page", async () => {
  const w = world((c) =>
    c.path === "/status"
      ? {
          status: 200,
          body: {
            ...(STATUS.body as object),
            desired: { revision: 4, at: "x", error: "listen: port in use." },
            active: { revision: 3, at: "x" },
          },
        }
      : undefined,
  );
  const { root } = await w.mount();
  expect(root.textContent).toContain(
    "pluxy is not using the latest version of its settings file: listen: port in use. It keeps working with the version before.",
  );
});

test("a check that finds a mistake with no words still says there is one", async () => {
  const w = world((c) =>
    c.path === "/config/validate" ? { status: 200, body: { valid: false, error: "" } } : undefined,
  );
  const { root } = await w.mount();
  await press(buttonIn(root, "Settings file"));
  await press(buttonIn(root, "Check for mistakes"));
  expect(root.textContent).toContain("There is a mistake in the file.");
});

test("a save refused because the file changed keeps the edits in the editor", async () => {
  const w = world((c) =>
    c.method === "PUT"
      ? { status: 409, error: { code: "conflict", message: "the config file changed" } }
      : undefined,
  );
  const { root } = await w.mount();
  await press(buttonIn(root, "Settings file"));
  expect(hasButton(root, "Review and save")).toBe(false);
  const editor = all(root, "textarea")[0] as FakeNode;
  await type(editor, `${FILE}// edited\n`);
  await press(buttonIn(root, "Review and save"));
  expect(root.textContent).toContain("Review changes");
  expect(root.textContent).toContain("Removed");
  expect(root.textContent).toContain("Added");
  await press(buttonIn(root, "Save"));
  expect(root.textContent).toContain(
    "The file changed while you were editing. Copy your edits, press Refresh, and make them again.",
  );
  expect((all(root, "textarea")[0] as FakeNode).value).toBe(`${FILE}// edited\n`);
});

test("switching tabs with edits in the file asks first, and Stay keeps them", async () => {
  const w = world();
  const { root } = await w.mount();
  await press(buttonIn(root, "Settings file"));
  await type(all(root, "textarea")[0] as FakeNode, "edited");
  await press(buttonIn(root, "Settings"));
  expect(root.textContent).toContain("You have edits that are not saved. Leave and lose them?");
  await press(buttonIn(root, "Stay"));
  expect((all(root, "textarea")[0] as FakeNode).value).toBe("edited");
  await press(buttonIn(root, "Settings"));
  await press(buttonIn(root, "Leave"));
  expect(rowNames(root)).toContain("Log every request");
});

test("Replace key calls nothing until Yes, replace key, then shows the new key once", async () => {
  const w = world((c) =>
    c.path === "/management-key/rotate" ? { status: 200, body: { value: "new-key-123" } } : undefined,
  );
  const { root } = await w.mount();
  await press(buttonIn(root, "Replace key"));
  expect(w.calls.filter((c) => c.path === "/management-key/rotate")).toHaveLength(0);
  expect(hasButton(root, "Cancel")).toBe(true);
  await press(buttonIn(root, "Cancel"));
  await press(buttonIn(root, "Replace key"));
  await press(buttonIn(root, "Yes, replace key"));
  expect(w.calls.filter((c) => c.path === "/management-key/rotate")).toHaveLength(1);
  const notice = withPart(root, "key-notice")[0] as FakeNode;
  expect(notice.textContent).toContain("New management key");
  expect(notice.textContent).toContain("This browser already uses it. Copy it now: it is not shown again.");
  expect(withPart(notice, "key-value")[0]?.textContent).toBe("new-key-123");
  // Leaving the tab takes the key away; it is not shown again.
  await press(buttonIn(root, "Settings file"));
  await press(buttonIn(root, "Settings"));
  expect(root.textContent).not.toContain("new-key-123");
});

test("in the app, Replace key says the app showed the key in its own window, and never shows it", async () => {
  const w = world((c) =>
    c.path === "/management-key/rotate" ? { status: 200, body: { kept: "device" } } : undefined,
  );
  const { root } = await w.mount();
  await press(buttonIn(root, "Replace key"));
  await press(buttonIn(root, "Yes, replace key"));
  expect(root.textContent).toContain(
    "Replaced. The app uses the new key and showed it once in its own window.",
  );
  expect(withPart(root, "key-value")).toHaveLength(0);
});

test("no answer to Replace key says what to do, and the button stays off", async () => {
  const w = world((c) => (c.path === "/management-key/rotate" ? { status: 0 } : undefined));
  const { root } = await w.mount();
  await press(buttonIn(root, "Replace key"));
  await press(buttonIn(root, "Yes, replace key"));
  expect(root.textContent).toContain(
    "pluxy did not answer, so the key may have been replaced. Sign in again with the old key. If it is refused, run pluxy key management --show-key on the computer pluxy runs on to see the new one.",
  );
  expect((buttonIn(root, "Replace key") as FakeNode).disabled).toBe(true);
});

test("a host that cannot replace the key says how to do it instead", async () => {
  const { root } = await world(undefined, []).mount();
  const r = row(root, "Management key");
  expect((all(r, "fieldset")[0] as FakeNode).disabled).toBe(true);
  expect(r.textContent).toContain(
    "Replace it with pluxy key rotate-management on the computer pluxy runs on.",
  );
});

test("pluxy not answering puts a pane with Try again on the Settings tab, and the file tab stays reachable", async () => {
  const w = world((c) => (c.path === "/settings" && c.method === "GET" ? { status: 0 } : undefined));
  const { root } = await w.mount();
  expect(root.textContent).toContain("Could not read pluxy's settings");
  expect(hasButton(root, "Try again")).toBe(true);
  expect(tabs(root)).toEqual(["Settings", "Settings file"]);
});

test("changeSetting is no longer exported from configSettings", () => {
  expect("changeSetting" in configSettings).toBe(false);
  expect("settingsChange" in configSettings).toBe(false);
});

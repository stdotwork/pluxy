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
  "proxy-url": "socks5://redacted@proxy.example:1080",
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
  privacy = false,
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
        <PrivacyContext.Provider value={privacyMasks(privacy)}>
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
const enter = async (input: FakeNode) => {
  await act(async () => {
    input.dispatchEvent(Object.assign(new Event("keydown"), { key: "Enter" }));
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

const patchesIn = (calls: Call[]) => calls.filter((c) => c.method === "PATCH").map((c) => c.body);

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
  expect(input.value).toBe("socks5://proxy.example:1080");
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

test("a saved proxy shows its address and a saved password apart; a new address without its own is not sent", async () => {
  const w = world();
  const { root } = await w.mount();
  const input = all(row(root, "Outbound proxy"), "input")[0] as FakeNode;
  expect(input.value).toBe("socks5://proxy.example:1080");
  expect(row(root, "Outbound proxy").textContent).toContain("Saved with a password.");
  expect(row(root, "Outbound proxy").textContent).not.toContain(
    "A saved password stays unless you type a new one.",
  );
  // Only the host changed: sent as it is, core would keep no password at all.
  await type(input, "socks5://new.example:1080");
  await enter(input);
  expect(patchesIn(w.calls)).toEqual([]);
  expect(row(root, "Outbound proxy").textContent).toContain(
    "The saved password belongs to the old address. Type the new address with its user name and password",
  );
  expect(input.value).toBe("socks5://new.example:1080");
  // The visible "redacted" kept with another host is no password either.
  await type(input, "socks5://redacted@new.example:1080");
  await enter(input);
  expect(patchesIn(w.calls)).toEqual([]);
  // With its own user name and password, the new address is sent.
  await type(input, "socks5://bob:pw@new.example:1080");
  await enter(input);
  expect(patchesIn(w.calls)).toEqual([{ "proxy-url": "socks5://bob:pw@new.example:1080" }]);
});

test("text typed while a row's save is on its way stays when the reply comes", async () => {
  const patch = held();
  const w = world((c) => (c.method === "PATCH" ? patch.reply : undefined));
  const { root } = await w.mount();
  const input = all(row(root, "Outbound proxy"), "input")[0] as FakeNode;
  await type(input, "http://a:1@first.example:8080");
  await enter(input);
  expect(patchesIn(w.calls)).toEqual([{ "proxy-url": "http://a:1@first.example:8080" }]);
  await type(input, "http://b:2@second.example:8080");
  patch.answer({
    status: 200,
    body: { ...structuredClone(SETTINGS), "proxy-url": "http://redacted@first.example:8080" },
  });
  await settle();
  expect(input.value).toBe("http://b:2@second.example:8080");
});

test("a row's save with no answer keeps the text and says it may or may not be saved", async () => {
  const w = world((c) => (c.method === "PATCH" ? { status: 0 } : undefined));
  const { root } = await w.mount();
  const input = all(row(root, "Outbound proxy"), "input")[0] as FakeNode;
  await type(input, "socks5://carol:pw@other.example:1080");
  await enter(input);
  expect(input.value).toBe("socks5://carol:pw@other.example:1080");
  expect(row(root, "Outbound proxy").textContent).toContain(
    "pluxy did not answer, so this may or may not be saved.",
  );
  // Sent again, it goes again.
  await enter(input);
  expect(patchesIn(w.calls)).toHaveLength(2);
});

test("replies from two rows that come back in reverse order leave both rows as saved", async () => {
  const first = held();
  const second = held();
  const logs = (c: Call) =>
    (c.body as { observability?: { logs?: Record<string, unknown> } })?.observability?.logs;
  const w = world((c) =>
    c.method !== "PATCH" ? undefined : logs(c)?.["request-log"] !== undefined ? first.reply : second.reply,
  );
  const { root } = await w.mount();
  await toggle(all(row(root, "Log every request"), "input")[0] as FakeNode, true);
  await settle();
  await toggle(all(row(root, "Write pluxy's log to a file"), "input")[0] as FakeNode, true);
  await settle();
  expect(patchesIn(w.calls)).toHaveLength(2);
  // Core took the first change, then the second; the second's reply comes first.
  const both = structuredClone(SETTINGS);
  both.observability.logs["request-log"] = true;
  both.observability.logs["logging-to-file"] = true;
  const older = structuredClone(SETTINGS);
  older.observability.logs["request-log"] = true;
  second.answer({ status: 200, body: both });
  await settle();
  first.answer({ status: 200, body: older });
  await settle();
  expect((all(row(root, "Log every request"), "input")[0] as FakeNode).checked).toBe(true);
  expect((all(row(root, "Write pluxy's log to a file"), "input")[0] as FakeNode).checked).toBe(true);
});

test("a save refused with 422 after the page loaded reads the file again, and a file that no longer parses turns the settings off", async () => {
  let reads = 0;
  const w = world((c) => {
    if (c.method === "PATCH")
      return { status: 422, error: { code: "refused", message: "yaml: line 2: did not find expected key" } };
    if (c.method === "GET" && c.path === "/config" && ++reads > 1)
      return {
        status: 422,
        error: {
          code: "invalid_config",
          message:
            "the config file does not parse, so it cannot be shown with its secrets hidden: line 2: did not find expected key",
        },
      };
    return undefined;
  });
  const { root } = await w.mount();
  expect(root.textContent).not.toContain("pluxy's settings file has a mistake");
  await toggle(all(row(root, "Log every request"), "input")[0] as FakeNode, true);
  await settle();
  expect(w.calls.filter((c) => c.method === "GET" && c.path === "/config")).toHaveLength(2);
  expect(w.calls.filter((c) => c.path === "/status").length).toBeGreaterThan(1);
  expect(root.textContent).toContain(
    "pluxy's settings file has a mistake, so it cannot be shown or changed here: line 2: did not find expected key.",
  );
  expect((all(row(root, "Write pluxy's log to a file"), "fieldset")[0] as FakeNode).disabled).toBe(true);
  expect(row(root, "Write pluxy's log to a file").textContent).toContain("Fix the settings file first.");
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
  // The two rows that open another page stay usable on purpose: those pages say for themselves
  // what they can do while the file does not parse.
  const linkRows = ["Account order, retries and cooldowns", "Keys for coding tools"];
  for (const name of linkRows) {
    const r = row(root, name);
    expect((all(r, "fieldset")[0] as FakeNode).disabled).toBe(false);
    expect(r.textContent).not.toContain("Fix the settings file first.");
    expect(all(r, "a")).toHaveLength(1);
  }
  const settingRows = rows(root).filter(
    (r) => !linkRows.includes(withClass(r, "setting-name")[0]?.textContent ?? ""),
  );
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

test("the review hides a newly typed key or password, also in privacy mode, and still shows the line changed", async () => {
  for (const privacy of [false, true]) {
    const w = world(undefined, ["rotateManagementKey"], privacy);
    const { root } = await w.mount();
    await press(buttonIn(root, "Settings file"));
    const typed =
      '{\n  "management_key": "brand-new-key-0123456789",\n  "proxy_url": "http://bob:hunter2pass@proxy.example:3128",\n' +
      '  "client_keys": ["sk-client-abcdef123456"],\n  "logs": {}\n}\n';
    await type(all(root, "textarea")[0] as FakeNode, typed);
    await press(buttonIn(root, "Review and save"));
    const diff = withPart(root, "diff")[0] as FakeNode;
    for (const secret of ["brand-new-key-0123456789", "hunter2pass", "bob:", "sk-client-abcdef123456"])
      expect(diff.textContent).not.toContain(secret);
    const added = withPart(diff, "diff-line").filter((l) => attribute(l, "data-change") === "add");
    expect(added.map((l) => l.textContent)).toEqual([
      '+   "management_key": "••••••••",\n',
      '+   "proxy_url": "http://••••••••@proxy.example:3128",\n',
      '+   "client_keys": ["••••••••"],\n',
    ]);
    // The removed line holds core's placeholder, not a secret, and shows as it was.
    expect(diff.textContent).toContain('"management_key": "<hidden>"');
    for (const unmount of unmounts.splice(0)) await unmount();
  }
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

test("in the app, Replace key asks nothing on the page, says the app showed the key in its own window, and never shows it", async () => {
  const w = world(
    (c) => (c.path === "/management-key/rotate" ? { status: 200, body: { kept: "device" } } : undefined),
    ["rotateManagementKey", "confirmsKeyReplacement"],
  );
  const { root } = await w.mount();
  await press(buttonIn(root, "Replace key"));
  // The app asks in its own native confirmation: the page asks nothing more.
  expect(hasButton(root, "Yes, replace key")).toBe(false);
  expect(w.calls.filter((c) => c.path === "/management-key/rotate")).toHaveLength(1);
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

test("a Replace key reply that comes after leaving Config is shown on coming back", async () => {
  const rotate = held();
  const w = world((c) => (c.path === "/management-key/rotate" ? rotate.reply : undefined));
  const first = await w.mount();
  await press(buttonIn(first.root, "Replace key"));
  await press(buttonIn(first.root, "Yes, replace key"));
  // Another page opens: Config goes away before core answers.
  unmounts.splice(unmounts.indexOf(first.unmount), 1);
  await first.unmount();
  rotate.answer({ status: 200, body: { value: "late-key-456" } });
  await settle();
  const again = await w.mount();
  const notice = withPart(again.root, "key-notice")[0] as FakeNode;
  expect(withPart(notice, "key-value")[0]?.textContent).toBe("late-key-456");
  // Seen once, it goes when Config is left again.
  unmounts.splice(unmounts.indexOf(again.unmount), 1);
  await again.unmount();
  const third = await w.mount();
  expect(third.root.textContent).not.toContain("late-key-456");
});

test("a refusal from the app before anything was replaced shows its reason and Replace key stays usable", async () => {
  const w = world(
    (c) =>
      c.path === "/management-key/rotate"
        ? { status: 409, error: { code: "refused", message: "finish native key entry first" } }
        : undefined,
    ["rotateManagementKey", "confirmsKeyReplacement"],
  );
  const { root } = await w.mount();
  await press(buttonIn(root, "Replace key"));
  expect(row(root, "Management key").parentNode?.textContent).toContain("finish native key entry first");
  expect(root.textContent).not.toContain("pluxy did not answer");
  expect((buttonIn(root, "Replace key") as FakeNode).disabled).toBe(false);
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

// The Logs page: pluxy's log read as it is written, and the full logs of failed requests, against a
// fake core. The page is driven through Preact's DOM renderer and a small fake DOM.
import { afterEach, beforeEach, expect, jest, test } from "bun:test";
import { act } from "preact/test-utils";
import { api } from "../kit/api";
import type { Host, Method, Reply } from "../kit/host";
import { LogsPage } from "../kit/pages/logs";
import { failedRequest, fileSize } from "../kit/pages/logs.helpers";
import { PrivacyContext, privacyMasks } from "../kit/privacy";
import { type Kit, KitContext } from "../kit/ui";
import { all, type FakeNode, mountIn, restoreDom, settle } from "./fakeDom";

const unmounts: (() => Promise<void>)[] = [];
beforeEach(() => {
  Object.defineProperty(globalThis, "location", { configurable: true, writable: true, value: { hash: "" } });
});
afterEach(async () => {
  for (const unmount of unmounts.splice(0)) await unmount();
  restoreDom();
  Reflect.deleteProperty(globalThis, "location");
  if (jest.isFakeTimers()) {
    jest.clearAllTimers();
    jest.useRealTimers();
  }
});

const TRACE = "20261125220609-f1787f10d870f9e7";
const FAILED = `error-v1-chat-completions-2026-11-25T220609-${TRACE}.log`;
const MODIFIED = Date.UTC(2026, 10, 25, 22, 6, 9) / 1000;
const OFF: Reply<unknown> = {
  status: 400,
  error: { code: "bad_request", message: "logging to file is off" },
};

const pad = (n: number) => String(n).padStart(2, "0");
/** A line of pluxy's log, written today so its time shows alone. */
function line(n: number, level: "info" | "warn" | "error" = "info", text = "something happened") {
  const d = new Date();
  const day = `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
  return `[${day} 12:${pad(Math.floor(n / 60) % 60)}:${pad(n % 60)}.000] [${level.padEnd(5)}] ${text} ${n}`;
}
const logs = (lines: string[], latest = lines.length): Reply<unknown> => ({
  status: 200,
  body: { lines, "line-count": lines.length, "latest-timestamp": latest },
});

interface Call {
  method: Method;
  path: string;
  body?: unknown;
}

/** A core that answers the calls the Logs page makes; answer overrides any of them. */
function world(answer: (call: Call) => Reply<unknown> | undefined = () => undefined, privacy = false) {
  const calls: Call[] = [];
  const saved: { name: string; text: string }[] = [];
  const host = {
    call: async (method: Method, path: string, body?: unknown): Promise<Reply<unknown>> => {
      const call = { method, path, body };
      calls.push(call);
      const own = answer(call);
      if (own) return own;
      if (method === "GET" && path.startsWith("/logs")) return logs([]);
      if (method === "GET" && path === "/request-error-logs") return { status: 200, body: { files: [] } };
      if (method === "GET" && path === "/accounts") return { status: 200, body: { accounts: [] } };
      return { status: 404, error: { code: "not_found", message: "no such call" } };
    },
    signedIn: () => true,
    prefs: { get: () => null, set: () => {} },
    features: new Set(),
    saveFile: async (name: string, text: string) => {
      saved.push({ name, text });
      return `/downloads/${name}`;
    },
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
          <LogsPage />
        </PrivacyContext.Provider>
      </KitContext.Provider>,
    );
    unmounts.push(m.unmount);
    return m;
  };
  return { calls, saved, mount };
}

const attribute = (node: FakeNode, name: string) => node.attributes.find((a) => a.name === name)?.value;
const descend = (node: FakeNode): FakeNode[] => [node, ...node.childNodes.flatMap(descend)];
const withPart = (root: FakeNode, part: string) =>
  descend(root).filter((n) => attribute(n, "data-part") === part);
const box = (root: FakeNode) => {
  const found = withPart(root, "log")[0];
  if (!found) throw new Error("no log box");
  return found;
};
const buttonIn = (root: FakeNode, label: string) => {
  const found = all(root, "button").find((b) => b.textContent === label);
  if (!found) throw new Error(`no button ${label} in ${root.textContent.slice(0, 300)}`);
  return found;
};
const hasButton = (root: FakeNode, label: string) => all(root, "button").some((b) => b.textContent === label);
const control = (root: FakeNode, label: string, tag: "input" | "select") => {
  const field = all(root, "label").find((l) => l.textContent.startsWith(label) && all(l, tag).length > 0);
  const found = field && all(field, tag)[0];
  if (!found) throw new Error(`no ${tag} labelled ${label}`);
  return found;
};
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
const pick = async (select: FakeNode, value: string) => {
  select.value = value;
  await act(async () => {
    select.dispatchEvent(new Event("change"));
  });
};
const submit = async (form: FakeNode) => {
  await act(async () => {
    form.dispatchEvent(new Event("submit"));
  });
  await settle();
};
const notices = (root: FakeNode) => withPartless(root, "notice");
const withPartless = (root: FakeNode, cls: string) =>
  descend(root)
    .filter((n) => attribute(n, "class")?.split(" ").includes(cls))
    .map((n) => n.textContent);
const links = (root: FakeNode, text: string) => all(root, "a").filter((a) => a.textContent === text);
const calledWith = (calls: Call[], method: string, start: string) =>
  calls.filter((c) => c.method === method && c.path.startsWith(start));
const advance = async (ms: number) => {
  await act(async () => {
    jest.advanceTimersByTime(ms);
  });
  await settle();
};
const openFailed = async (m: { root: FakeNode }) => press(buttonIn(m.root, "Failed requests"));
const tabs = (root: FakeNode) =>
  all(root, "button")
    .filter((b) => attribute(b, "role") === "tab")
    .map((b) => b.textContent);

// ---- pluxy's log -------------------------------------------------------------------------------

test("the log has two tabs, and following and wrapping are on", async () => {
  const w = world((c) => (c.path.startsWith("/logs") ? logs([line(1)]) : undefined));
  const m = await w.mount();
  expect(tabs(m.root)).toEqual(["pluxy's log", "Failed requests"]);
  const follow = control(m.root, "Follow new lines", "input");
  const wrap = control(m.root, "Wrap lines", "input");
  expect(follow.checked).toBe(true);
  expect(wrap.checked).toBe(true);
  const log = box(m.root);
  expect(attribute(log, "data-wrap")).toBe("");
  expect(attribute(log, "aria-label")).toBe("pluxy's log");
  expect(attribute(log, "tabIndex")).toBe("0");
  // The lines of today show their time alone, as HH:MM:SS.
  const time = descend(log).find((n) => n.localName === "time");
  expect(time?.textContent).toMatch(/^\d\d:\d\d:\d\d\.\d+$/);
  expect(attribute(time as FakeNode, "title")).toMatch(/^\d{4}-\d\d-\d\d \d\d:\d\d:\d\d\.\d+$/);
});

test("the Show list offers all lines, warnings and errors, and picks one level", async () => {
  const w = world((c) =>
    c.path.startsWith("/logs")
      ? logs([line(1, "info", "fine"), line(2, "warn", "careful"), line(3, "error", "broken")])
      : undefined,
  );
  const m = await w.mount();
  const show = control(m.root, "Show", "select");
  expect(all(show, "option").map((o) => o.textContent)).toEqual(["All lines", "Warnings", "Errors"]);
  await pick(show, "error");
  expect(withPart(m.root, "log-line").map((n) => n.textContent)).toHaveLength(1);
  expect(box(m.root).textContent).toContain("broken");
  expect(box(m.root).textContent).not.toContain("careful");
  await pick(show, "warn");
  expect(box(m.root).textContent).toContain("careful");
  expect(box(m.root).textContent).not.toContain("broken");
  await type(control(m.root, "Find in log", "input"), "nothing like this");
  expect(box(m.root).textContent).toContain("No lines match.");
  await press(buttonIn(m.root, "Clear filters"));
  expect(withPart(m.root, "log-line")).toHaveLength(3);
});

test("a run of failed reads leaves one notice, and a read that works removes it", async () => {
  jest.useFakeTimers();
  let reads = 0;
  const w = world((c) => {
    if (!c.path.startsWith("/logs")) return undefined;
    reads++;
    if (reads === 1) return logs([line(1)], 100);
    if (reads <= 4) return { status: 500, error: { code: "internal", message: `boom ${reads}` } };
    return logs([line(2)], 200);
  });
  const m = await w.mount();
  expect(notices(m.root)).toEqual([]);
  for (const n of [2, 3, 4]) {
    await advance(2000);
    // One notice, with the latest failure in it: they do not pile up.
    expect(notices(m.root)).toEqual([`Could not read new lines: boom ${n}. Trying again every 2 seconds.`]);
  }
  await advance(2000);
  expect(notices(m.root)).toEqual([]);
  expect(withPart(m.root, "log-line")).toHaveLength(2);
  // New lines are asked for after the newest one read.
  expect(w.calls.filter((c) => c.path.startsWith("/logs")).map((c) => c.path)).toEqual([
    "/logs",
    "/logs?after=100",
    "/logs?after=100",
    "/logs?after=100",
    "/logs?after=100",
  ]);
});

test("turning following off stops the reading", async () => {
  jest.useFakeTimers();
  const w = world((c) => (c.path.startsWith("/logs") ? logs([line(1)]) : undefined));
  const m = await w.mount();
  const follow = control(m.root, "Follow new lines", "input");
  follow.checked = false;
  await act(async () => {
    follow.dispatchEvent(new Event("change"));
  });
  const before = calledWith(w.calls, "GET", "/logs").length;
  await advance(6000);
  expect(calledWith(w.calls, "GET", "/logs")).toHaveLength(before);
});

test("a log that is not written to a file says so, and one press turns that on", async () => {
  let off = true;
  const w = world((c) => {
    if (c.method === "PATCH" && c.path === "/settings") {
      off = false;
      return { status: 200, body: {} };
    }
    if (c.path.startsWith("/logs")) return off ? OFF : logs([]);
    return undefined;
  });
  const m = await w.mount();
  expect(m.text()).toContain("pluxy is not writing its log to a file");
  expect(m.text()).toContain("Turn it on to read the log here. It starts with the next thing pluxy logs.");
  expect(hasButton(m.root, "Download")).toBe(false);
  await press(buttonIn(m.root, "Turn on file logging"));
  const patches = calledWith(w.calls, "PATCH", "/settings");
  expect(patches).toHaveLength(1);
  expect(patches[0]?.body).toEqual({ observability: { logs: { "logging-to-file": true } } });
  // The log is read again: now it is empty, and says so.
  expect(m.text()).toContain("Nothing logged yet.");
  expect(m.text()).not.toContain("not writing its log");
});

test("turning file logging on shows why it failed under the button, and can be pressed again", async () => {
  let patches = 0;
  const w = world((c) => {
    if (c.method === "PATCH") {
      patches++;
      return { status: 400, error: { code: "bad_request", message: "the settings file is locked" } };
    }
    if (c.path.startsWith("/logs")) return OFF;
    return undefined;
  });
  const m = await w.mount();
  await press(buttonIn(m.root, "Turn on file logging"));
  expect(notices(m.root)).toEqual(["Could not turn on file logging: the settings file is locked"]);
  await press(buttonIn(m.root, "Turn on file logging"));
  expect(patches).toBe(2);
});

test("with Log every request on, Failed requests says why its list is empty and how to open a log", async () => {
  const words =
    "Log every request is on, so failed requests are not listed here. Open a request's log by its trace ID.";
  const settings = (requestLog: boolean): Reply<unknown> => ({
    status: 200,
    body: { observability: { logs: { "request-log": requestLog } } },
  });
  const on = world((c) => (c.path === "/settings" ? settings(true) : undefined));
  const a = await on.mount();
  await openFailed(a);
  expect(a.text()).toContain(words);
  await a.unmount();
  unmounts.pop();
  restoreDom();
  const off = world((c) => (c.path === "/settings" ? settings(false) : undefined));
  const b = await off.mount();
  await openFailed(b);
  expect(b.text()).toContain("pluxy keeps the full log of its latest failed requests.");
  expect(b.text()).not.toContain(words);
});

test("every link to the logging settings is #/config/logging", async () => {
  // The off pane.
  const off = world((c) => (c.path.startsWith("/logs") ? OFF : undefined));
  const a = await off.mount();
  expect(links(a.root, "Logging settings").map((l) => attribute(l, "href"))).toEqual(["#/config/logging"]);
  await a.unmount();
  unmounts.pop();
  restoreDom();
  // A trace that has no log.
  (globalThis as { location: { hash: string } }).location.hash = `#/logs/${TRACE}`;
  const missing = world((c) =>
    c.path.startsWith("/request-log-by-id")
      ? { status: 404, error: { code: "not_found", message: "no request log has this trace ID" } }
      : undefined,
  );
  const b = await missing.mount();
  expect(links(b.root, "Logging settings").map((l) => attribute(l, "href"))).toEqual(["#/config/logging"]);
});

test("more than 10,000 lines show the cap notice, and 10,000 do not", async () => {
  const many = (n: number) => Array.from({ length: n }, (_, i) => line(i));
  const at = world((c) => (c.path.startsWith("/logs") ? logs(many(10000)) : undefined));
  const a = await at.mount();
  expect(a.text()).not.toContain("Showing the latest 10,000 lines.");
  await a.unmount();
  unmounts.pop();
  restoreDom();
  const over = world((c) => (c.path.startsWith("/logs") ? logs(many(10001)) : undefined));
  const b = await over.mount();
  expect(notices(b.root)).toEqual([
    "Showing the latest 10,000 lines. Older lines are left out of Find and Download.",
  ]);
  expect(withPart(b.root, "log-line")).toHaveLength(10000);
});

test("with names hidden the screen is masked and a download keeps the real text", async () => {
  const real = [line(1, "info", "signed in alice@example.com"), line(2, "info", "second line")];
  const w = world((c) => (c.path.startsWith("/logs") ? logs(real) : undefined), true);
  const m = await w.mount();
  expect(m.text()).not.toContain("alice@example.com");
  expect(m.text()).toContain("Names are hidden on screen only. A download has the log as pluxy wrote it.");
  await press(buttonIn(m.root, "Download"));
  expect(w.saved).toEqual([{ name: "pluxy-log.txt", text: real.join("\n") }]);
  await m.unmount();
  unmounts.pop();
  restoreDom();
  // Names shown: no notice about hiding them, and the screen has the text.
  const open = world((c) => (c.path.startsWith("/logs") ? logs(real) : undefined));
  const n = await open.mount();
  expect(n.text()).toContain("alice@example.com");
  expect(n.text()).not.toContain("Names are hidden on screen only.");
});

test("a download is what is shown: the lines that match Find", async () => {
  const w = world((c) =>
    c.path.startsWith("/logs") ? logs([line(1, "info", "alpha"), line(2, "info", "beta")]) : undefined,
  );
  const m = await w.mount();
  await type(control(m.root, "Find in log", "input"), "BETA");
  await press(buttonIn(m.root, "Download"));
  expect(w.saved).toHaveLength(1);
  expect(w.saved[0]?.text).toBe(line(2, "info", "beta"));
});

test("Clear log asks once, and afterwards the box is empty", async () => {
  const w = world((c) => {
    if (c.method === "DELETE") return { status: 200, body: { removed: 1 } };
    if (c.path.startsWith("/logs")) return logs([line(1)]);
    return undefined;
  });
  const m = await w.mount();
  await press(buttonIn(m.root, "Clear log"));
  expect(m.text()).toContain("Deletes pluxy's log. Failed requests stay.");
  expect(calledWith(w.calls, "DELETE", "/logs")).toHaveLength(0);
  await press(buttonIn(m.root, "Cancel"));
  expect(calledWith(w.calls, "DELETE", "/logs")).toHaveLength(0);
  await press(buttonIn(m.root, "Clear log"));
  await press(buttonIn(m.root, "Yes, clear"));
  expect(calledWith(w.calls, "DELETE", "/logs")).toHaveLength(1);
  expect(box(m.root).textContent).toContain("Nothing logged yet.");
});

test("a log that cannot be cleared says why", async () => {
  const w = world((c) => {
    if (c.method === "DELETE")
      return { status: 500, error: { code: "internal", message: "disk is read-only" } };
    if (c.path.startsWith("/logs")) return logs([line(1)]);
    return undefined;
  });
  const m = await w.mount();
  await press(buttonIn(m.root, "Clear log"));
  await press(buttonIn(m.root, "Yes, clear"));
  expect(notices(m.root)).toEqual(["Could not clear the log: disk is read-only"]);
  expect(withPart(m.root, "log-line")).toHaveLength(1);
});

test("a log that cannot be read at first says why, and Try again reads it", async () => {
  let reads = 0;
  const w = world((c) => {
    if (!c.path.startsWith("/logs")) return undefined;
    return ++reads === 1
      ? { status: 500, error: { code: "internal", message: "no log today" } }
      : logs([line(1)]);
  });
  const m = await w.mount();
  expect(notices(m.root)).toEqual(["Could not read pluxy's log: no log today"]);
  await press(buttonIn(m.root, "Try again"));
  expect(notices(m.root)).toEqual([]);
  expect(withPart(m.root, "log-line")).toHaveLength(1);
});

// ---- failed requests ---------------------------------------------------------------------------

const failedList = {
  status: 200,
  body: { files: [{ name: FAILED, size: 2048, modified: MODIFIED }] },
} as const;

test("a failed request shows the request its file name gives, its time and its trace", async () => {
  const w = world((c) => (c.path === "/request-error-logs" ? failedList : undefined));
  const m = await w.mount();
  await openFailed(m);
  expect(m.text()).toContain("pluxy keeps the full log of its latest failed requests.");
  const row = withPart(m.root, "item")[0] as FakeNode;
  const cells = all(row, "th")
    .concat(all(row, "td"))
    .map((c) => c.textContent);
  expect(cells).toEqual(["v1-chat-completions", expect.any(String), TRACE, "2 KB"]);
  const time = descend(row).find((n) => n.localName === "time");
  expect(attribute(time as FakeNode, "dateTime")).toBe(new Date(MODIFIED * 1000).toISOString());
  // The path cannot be rebuilt from the name, so the name's own words are shown, not the file's.
  expect(row.textContent).not.toContain("error-");
  expect(row.textContent).not.toContain(".log");
  expect(
    attribute(descend(row).find((n) => attribute(n, "class") === "logs-trace") as FakeNode, "title"),
  ).toBe(TRACE);
});

test("the file name's parts, and a name that does not fit", () => {
  expect(failedRequest(FAILED)).toEqual({ request: "v1-chat-completions", trace: TRACE });
  expect(failedRequest("main.log")).toEqual({ request: "main.log" });
  expect(fileSize(812)).toBe("812 B");
  expect(fileSize(3500)).toBe("3.4 KB");
  expect(fileSize(5 * 1024 * 1024)).toBe("5 MB");
});

test("a failed request opens its log, which can be downloaded and its trace copied", async () => {
  const w = world((c) => {
    if (c.path === "/request-error-logs") return failedList;
    if (c.path === `/request-error-logs/${FAILED}?format=json`)
      return { status: 200, body: { name: FAILED, size: 30, content: "=== REQUEST INFO ===\nTrace ID: x" } };
    return undefined;
  });
  const m = await w.mount();
  await openFailed(m);
  await press(buttonIn(m.root, "v1-chat-completions"));
  const detail = withPart(m.root, "detail")[0] as FakeNode;
  expect(attribute(detail, "aria-label")).toBe(`Details: ${TRACE}`);
  expect(attribute(detail, "tabIndex")).toBe("-1");
  const text = withPart(detail, "log")[0] as FakeNode;
  expect(text.textContent).toBe("=== REQUEST INFO ===\nTrace ID: x");
  expect(attribute(text, "tabIndex")).toBe("0");
  expect(attribute(text, "aria-label")).toBe(`Log of ${TRACE}`);
  expect(hasButton(detail, "Copy trace ID")).toBe(true);
  await press(buttonIn(detail, "Download"));
  expect(w.saved).toEqual([{ name: FAILED, text: "=== REQUEST INFO ===\nTrace ID: x" }]);
});

test("the list says when no request failed, and when it could not be read", async () => {
  const none = world();
  const a = await none.mount();
  await openFailed(a);
  expect(a.text()).toContain("No failed requests.");
  await a.unmount();
  unmounts.pop();
  restoreDom();
  let reads = 0;
  const failing = world((c) => {
    if (c.path !== "/request-error-logs") return undefined;
    return ++reads === 1 ? { status: 500, error: { code: "internal", message: "folder gone" } } : failedList;
  });
  const b = await failing.mount();
  await openFailed(b);
  expect(b.text()).toContain("Could not read the failed requests");
  expect(b.text()).not.toContain("No failed requests.");
  await press(buttonIn(b.root, "Try again"));
  expect(b.text()).toContain("v1-chat-completions");
});

test("a malformed trace is refused before any call", async () => {
  const w = world();
  const m = await w.mount();
  await openFailed(m);
  const field = control(m.root, "Trace ID", "input");
  await type(field, "not-a-trace");
  const form = descend(m.root).find((n) => n.localName === "form") as FakeNode;
  await submit(form);
  expect(notices(m.root)).toEqual(["That is not a trace ID. It looks like 20261125220609-f1787f10d870f9e7."]);
  expect(calledWith(w.calls, "GET", "/request-log-by-id")).toHaveLength(0);
  // An address with a malformed trace is refused the same way.
  await m.unmount();
  unmounts.pop();
  restoreDom();
  (globalThis as { location: { hash: string } }).location.hash = "#/logs/%E0%A4%A";
  const n = await w.mount();
  expect(notices(n.root)).toEqual(["That is not a trace ID. It looks like 20261125220609-f1787f10d870f9e7."]);
  expect(calledWith(w.calls, "GET", "/request-log-by-id")).toHaveLength(0);
});

test("a trace opens its log, from the address or from the field", async () => {
  const w = world((c) =>
    c.path.startsWith("/request-log-by-id/")
      ? { status: 200, body: { name: FAILED, size: 9, content: "the log" } }
      : undefined,
  );
  (globalThis as { location: { hash: string } }).location.hash = `#/logs/${TRACE}`;
  const m = await w.mount();
  // The address opens the second tab with that request open.
  expect(
    attribute(
      all(m.root, "button").find((b) => b.textContent === "Failed requests") as FakeNode,
      "aria-selected",
    ),
  ).toBe("true");
  expect(calledWith(w.calls, "GET", "/request-log-by-id")).toHaveLength(1);
  expect(calledWith(w.calls, "GET", "/request-log-by-id")[0]?.path).toBe(
    `/request-log-by-id/${TRACE}?format=json`,
  );
  expect(control(m.root, "Trace ID", "input").value).toBe(TRACE);
  expect(box(m.root).textContent).toBe("the log");
  // The field opens one too.
  const other = "20261125220610-0123456789abcdef";
  const field = control(m.root, "Trace ID", "input");
  await type(field, other);
  await submit(descend(m.root).find((n) => n.localName === "form") as FakeNode);
  expect(calledWith(w.calls, "GET", "/request-log-by-id").map((c) => c.path)).toEqual([
    `/request-log-by-id/${TRACE}?format=json`,
    `/request-log-by-id/${other}?format=json`,
  ]);
});

test("a trace that has no log, and one that is too big, say so", async () => {
  (globalThis as { location: { hash: string } }).location.hash = `#/logs/${TRACE}`;
  const missing = world((c) =>
    c.path.startsWith("/request-log-by-id/")
      ? { status: 404, error: { code: "not_found", message: "no request log has this trace ID" } }
      : undefined,
  );
  const a = await missing.mount();
  expect(notices(a.root)).toEqual([
    "No log is kept for this request. pluxy keeps logs of its latest failed requests, and of every request only while Log every request is on. Logging settings",
  ]);
  await a.unmount();
  unmounts.pop();
  restoreDom();
  const big = world((c) =>
    c.path.startsWith("/request-log-by-id/")
      ? { status: 413, error: { code: "too_large", message: "log is too large" } }
      : undefined,
  );
  const b = await big.mount();
  expect(notices(b.root)).toEqual([
    "This log is over 2 MiB, too big to show or download here. It is in pluxy's logs folder on the computer pluxy runs on.",
  ]);
  expect(hasButton(b.root, "Download")).toBe(false);
});

test("a log that cannot be opened says why", async () => {
  (globalThis as { location: { hash: string } }).location.hash = `#/logs/${TRACE}`;
  const w = world((c) =>
    c.path.startsWith("/request-log-by-id/")
      ? { status: 500, error: { code: "internal", message: "disk error" } }
      : undefined,
  );
  const m = await w.mount();
  expect(notices(m.root)).toEqual(["Could not open this log: disk error"]);
});

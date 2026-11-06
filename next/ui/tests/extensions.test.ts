import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import {
  type Channel,
  MAX_CALLS_PER_SECOND,
  MAX_WAITING,
  type Outcome,
  type Port,
  pickVersion,
  type Services,
  serve,
} from "../kit/extensions/channel";
import { changeOf } from "../kit/extensions/settings";
import { pageProblem, related } from "../kit/extensions/site";

/** A port whose sent messages are kept; deliver() plays a message from the page. */
function fakePort() {
  const sent: Record<string, unknown>[] = [];
  let closed = false;
  const port: Port = {
    postMessage: (m) => sent.push(m as Record<string, unknown>),
    onmessage: null,
    close: () => {
      closed = true;
    },
  };
  const deliver = (data: unknown) => port.onmessage?.({ data } as MessageEvent);
  return { port, sent, deliver, isClosed: () => closed };
}

const settled = () => new Promise((r) => setTimeout(r, 0));

function services(over: Partial<Services> = {}): Services & { calls: string[] } {
  const calls: string[] = [];
  return {
    calls,
    theme: () => ({ theme_version: 0 }),
    configGet: async () => {
      calls.push("get");
      return { result: { values: { a: 1 }, set: [] } };
    },
    configSet: async (values) => {
      calls.push(`set ${JSON.stringify(values)}`);
      return { result: { values, set: [] } };
    },
    ...over,
  };
}

function open(permissions: string[], s = services(), now = () => 0) {
  const p = fakePort();
  const ch: Channel = serve(p.port, { extension: "pub/ext", permissions }, s, now);
  return { ...p, ch, s };
}

describe("the channel", () => {
  test("answers allowed calls once, with their id", async () => {
    const { sent, deliver } = open(["config.read"]);
    deliver({ v: 0, id: "1", method: "theme.get", params: null });
    deliver({ v: 0, id: "2", method: "config.get", params: null });
    await settled();
    expect(sent).toEqual([
      { v: 0, id: "1", result: { theme_version: 0 } },
      { v: 0, id: "2", result: { values: { a: 1 }, set: [] } },
    ]);
  });

  test("refuses a method the extension did not declare, without running it", async () => {
    const { sent, deliver, s } = open(["config.read"]);
    deliver({ v: 0, id: "1", method: "config.set", params: { values: { a: 2 } } });
    await settled();
    expect(sent[0]?.error).toMatchObject({ code: "forbidden" });
    expect(s.calls).toEqual([]);
  });

  test("refuses unknown methods, including inherited object properties", async () => {
    const { sent, deliver } = open(["config.read", "config.write"]);
    for (const method of ["key.get", "constructor", "__proto__", "toString", "hasOwnProperty"]) {
      deliver({ v: 0, id: method, method, params: null });
    }
    await settled();
    expect(sent.map((m) => (m.error as { code: string }).code)).toEqual(Array(5).fill("unknown_method"));
  });

  test("refuses every malformed call, answering only those with a usable id", async () => {
    const { sent, deliver, s } = open(["config.read", "config.write"]);
    const bad: unknown[] = [
      { v: 0, id: "a", method: "config.get", params: null, extension: "other/ext" },
      { v: 1, id: "b", method: "theme.get", params: null },
      { v: 0, id: "c", method: "config.get", params: { extension: "other/ext" } },
      { v: 0, id: "d", method: "config.set", params: { values: { a: 1 }, extension: "other/ext" } },
      { v: 0, id: "e", method: "config.set", params: { values: [1] } },
      { v: 0, id: "f", method: 5, params: null },
      { v: 0, id: "g", method: "theme.get", params: [] },
      { v: 0, id: "x".repeat(65), method: "theme.get", params: null },
      { v: 0, id: 7, method: "theme.get", params: null },
      "theme.get",
      null,
      [1, 2],
    ];
    for (const m of bad) deliver(m);
    await settled();
    expect(sent.map((m) => [m.id, (m.error as { code: string }).code])).toEqual([
      ["a", "bad_request"],
      ["b", "bad_request"],
      ["c", "bad_request"],
      ["d", "bad_request"],
      ["e", "bad_request"],
      ["f", "bad_request"],
      ["g", "bad_request"],
    ]);
    expect(s.calls).toEqual([]);
  });

  test("refuses a message over 64 KiB, and one that is not JSON", async () => {
    const { sent, deliver } = open([]);
    deliver({ v: 0, id: "big", method: "theme.get", params: { x: "é".repeat(33 * 1024) } });
    const cyclic: Record<string, unknown> = { v: 0, id: "loop", method: "theme.get" };
    cyclic.params = cyclic;
    deliver(cyclic);
    await settled();
    expect(sent.map((m) => [m.id, (m.error as { code: string }).code])).toEqual([
      ["big", "too_large"],
      ["loop", "bad_request"],
    ]);
  });

  test("limits calls per second and calls waiting for an answer", async () => {
    let t = 0;
    const { sent, deliver } = open([], services(), () => t);
    for (let i = 0; i < MAX_CALLS_PER_SECOND + 5; i++)
      deliver({ v: 0, id: `r${i}`, method: "theme.get", params: null });
    await settled();
    const limited = sent.filter((m) => (m.error as { code?: string } | undefined)?.code === "rate_limited");
    expect(limited.length).toBe(5);
    t = 1001;
    deliver({ v: 0, id: "later", method: "theme.get", params: null });
    await settled();
    expect(sent.at(-1)).toMatchObject({ id: "later", result: { theme_version: 0 } });

    const never = new Promise<Outcome>(() => {});
    const slow = open(["config.read"], services({ configGet: () => never }), () => 0);
    for (let i = 0; i <= MAX_WAITING; i++)
      slow.deliver({ v: 0, id: `w${i}`, method: "config.get", params: null });
    await settled();
    expect(slow.sent).toEqual([
      { v: 0, id: `w${MAX_WAITING}`, error: { code: "rate_limited", message: "too many calls" } },
    ]);
  });

  test("after close, it answers nothing, even calls that were waiting", async () => {
    let finish: (o: Outcome) => void = () => {};
    const { sent, deliver, ch, isClosed } = open(
      ["config.read"],
      services({ configGet: () => new Promise((r) => (finish = r)) }),
    );
    deliver({ v: 0, id: "1", method: "config.get", params: null });
    ch.close();
    finish({ result: "late" });
    deliver({ v: 0, id: "2", method: "theme.get", params: null });
    ch.event("theme", {});
    await settled();
    expect(sent).toEqual([]);
    expect(isClosed()).toBe(true);
  });

  test("sends events", () => {
    const { sent, ch } = open([]);
    ch.event("theme", { appearance: "dark" });
    expect(sent).toEqual([{ v: 0, event: "theme", data: { appearance: "dark" } }]);
  });

  test("picks version 0 only when the page speaks it", () => {
    expect(pickVersion({ versions: [2, 0] })).toBe(0);
    expect(pickVersion({ versions: [1] })).toBeUndefined();
    expect(pickVersion({ versions: "0" })).toBeUndefined();
    expect(pickVersion({})).toBeUndefined();
  });
});

describe("where pages may come from", () => {
  test("only the pages site, and never the panel's own site", () => {
    const page = "http://localhost:8401/pub/ext/0123/index.html";
    expect(pageProblem(page, "http://localhost:8401", "127.0.0.1")).toBeUndefined();
    expect(pageProblem(page, "http://localhost:8401", "localhost")).toContain("the panel's own site");
    expect(pageProblem("http://evil.example/x.html", "http://localhost:8401", "127.0.0.1")).toContain(
      "not on the extension pages site",
    );
    expect(pageProblem(page, undefined, "127.0.0.1")).toContain("no address");
    expect(pageProblem("javascript:alert(1)", "javascript:", "127.0.0.1")).toBeDefined();
    expect(related("pages.pluxy.example", "pluxy.example")).toBe(true);
    expect(related("pluxy.example", "PAGES.pluxy.example.")).toBe(true);
    expect(related("pluxy-pages.example", "pluxy.example")).toBe(false);
  });
});

describe("the settings form", () => {
  const settings = {
    schema: {
      type: "object" as const,
      properties: {
        greeting: { type: "string" as const },
        interval: { type: "integer" as const },
        on: { type: "boolean" as const },
        token: { type: "string" as const, writeOnly: true },
      },
    },
    values: { greeting: "hi", interval: 5, on: false },
    set: ["token"],
  };

  test("sends only what changed; an emptied field goes back to its default", () => {
    expect(changeOf(settings, { greeting: "hi", interval: "5", on: false, token: "" }, new Set())).toEqual(
      {},
    );
    expect(changeOf(settings, { greeting: "", interval: "7", on: true, token: "" }, new Set())).toEqual({
      greeting: null,
      interval: 7,
      on: true,
    });
    expect(changeOf(settings, { greeting: "hi", interval: "x", on: false, token: "" }, new Set())).toEqual({
      interval: "x",
    });
  });

  test("a secret is sent only when typed, and cleared only when asked", () => {
    const same = { greeting: "hi", interval: "5", on: false };
    expect(changeOf(settings, { ...same, token: "new" }, new Set())).toEqual({ token: "new" });
    expect(changeOf(settings, { ...same, token: "" }, new Set(["token"]))).toEqual({ token: null });
  });
});

describe("the contract README", () => {
  const readme = readFileSync(join(import.meta.dir, "..", "extension-pages", "README.md"), "utf8");
  test("lists the limits the channel enforces", () => {
    expect(readme).toContain("at most 64 KiB");
    expect(readme).toContain(`more than ${MAX_CALLS_PER_SECOND} calls in a second`);
    expect(readme).toContain(`more than ${MAX_WAITING} waiting`);
    for (const code of [
      "bad_request",
      "too_large",
      "unknown_method",
      "forbidden",
      "rate_limited",
      "invalid_config",
      "unavailable",
    ]) {
      expect(readme).toContain(`\`${code}\``);
    }
  });
});

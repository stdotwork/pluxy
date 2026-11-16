import { expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { runInNewContext } from "node:vm";

function wrapper() {
  const requests: { body: { type: string; data: { id?: string } }; resolve: (value: unknown) => void }[] = [];
  const messages: unknown[] = [];
  const frame = {
    contentWindow: { postMessage: (value: unknown) => messages.push(value) },
    addEventListener() {},
  };
  const context = {
    document: { createElement: () => frame },
    window: { addEventListener() {} },
    fetch: (_url: string, options: { body: string }) =>
      new Promise((resolve) => requests.push({ body: JSON.parse(options.body), resolve })),
  };
  const code = readFileSync(new URL("../page/frame.js", import.meta.url), "utf8").split(
    'let r = await fetch("/frame");',
  )[0];
  Object.assign(context, { replies: messages });
  runInNewContext(`${code ?? ""}\nport = { postMessage: value => replies.push(value) };`, context);
  return {
    requests,
    messages,
    send: (type: string, id?: string) =>
      runInNewContext(`send(${JSON.stringify(type)},${JSON.stringify(id ? { id } : null)})`, context),
  };
}
const tick = () => new Promise((resolve) => setTimeout(resolve, 0));
const ok = { ok: true };

test("the wrapper serializes a bounded FIFO and never drops lifecycle messages", async () => {
  const w = wrapper();
  w.send("call", "write");
  w.send("call", "read");
  for (let i = 0; i < 12; i++) w.send("call", String(i));
  w.send("hello");
  w.send("closed");
  expect(w.requests.length).toBe(1);
  for (let i = 0; i < 10; i++) {
    expect(w.requests.length).toBe(i + 1);
    w.requests[i]?.resolve(ok);
    await tick();
  }
  expect(w.requests.length).toBe(10);
  expect(w.requests.slice(0, 2).map((r) => r.body.data.id)).toEqual(["write", "read"]);
  expect(w.requests.slice(-2).map((r) => r.body.type)).toEqual(["hello", "closed"]);
});

test("the wrapper passes a native hello refusal to the frame", async () => {
  const w = wrapper();
  const refusal = { pluxy: "host", type: "refused", reason: "no channel version in common" };
  w.send("hello");
  w.requests[0]?.resolve({ ok: false, json: async () => refusal });
  await tick();
  expect(w.messages).toEqual([refusal]);
});

test("an oversized transport refusal uses the original call id", async () => {
  const w = wrapper();
  w.send("call", "oversized");
  w.requests[0]?.resolve({
    ok: false,
    status: 413,
    json: async () => ({ v: 0, id: null, error: { code: "too_large" } }),
  });
  await tick();
  expect(w.messages).toEqual([
    {
      v: 0,
      id: "oversized",
      error: { code: "too_large", message: "native page transport refused the call" },
    },
  ]);
});

/** Runs the whole wrapper with /frame answered by replies, in order; the last one repeats. */
async function shown(replies: { status: number; info?: unknown }[]) {
  const frame: Record<string, unknown> & { addEventListener(): void } = { addEventListener() {} };
  let asked = 0;
  let appended = false;
  const context = {
    document: { createElement: () => frame, body: { append: () => (appended = true) } },
    window: { addEventListener() {} },
    setTimeout: (f: () => void) => setTimeout(f, 0),
    fetch: async (url: string) => {
      expect(url).toBe("/frame");
      const reply = replies[Math.min(asked++, replies.length - 1)] as { status: number; info?: unknown };
      return { status: reply.status, ok: reply.status === 200, json: async () => reply.info };
    },
  };
  const code = readFileSync(new URL("../page/frame.js", import.meta.url), "utf8");
  const error = await (runInNewContext(`(async () => {\n${code}\n})()`, context) as Promise<void>).then(
    () => undefined,
    (e: unknown) => String(e),
  );
  return { asked, appended, src: frame.src, error };
}

test("the wrapper waits while the app is not yet watching the frame's navigations", async () => {
  const info = {
    url: "http://localhost:8401/p/",
    title: "Page",
    generation: "1",
  };
  const r = await shown([{ status: 503 }, { status: 503 }, { status: 200, info }]);
  expect(r).toEqual({ asked: 3, appended: true, src: "http://localhost:8401/p/", error: undefined });
});

test("the wrapper shows no frame when the app never gives its address", async () => {
  const r = await shown([{ status: 503 }]);
  expect(r.asked).toBe(51);
  expect(r.appended).toBe(false);
  expect(r.src).toBeUndefined();
  expect(r.error).toContain("unavailable (503)");
  expect((await shown([{ status: 403 }])).asked).toBe(1);
});

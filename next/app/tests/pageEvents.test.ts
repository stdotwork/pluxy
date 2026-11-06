import { expect, test } from "bun:test";
import { MAX_LABELS, MAX_WAITING, type PageMessage, PageRouter } from "../src/pageEvents";

const hello = (label: string): PageMessage => ({ label, type: "hello", data: { pluxy: "page" } });

test("a hello that comes before the window has the label still reaches its page", () => {
  const r = new PageRouter();
  r.deliver(hello("page-1"));
  const got: PageMessage[] = [];
  r.claim("page-1", (m) => got.push(m));
  expect(got).toEqual([hello("page-1")]);
  r.deliver({ label: "page-1", type: "call", data: 1 });
  expect(got.length).toBe(2);
});

test("messages go to their own page only, and stop once it is dropped", () => {
  const r = new PageRouter();
  const one: PageMessage[] = [];
  const two: PageMessage[] = [];
  const stop = r.claim("page-1", (m) => one.push(m));
  r.claim("page-2", (m) => two.push(m));
  r.deliver(hello("page-2"));
  expect([one.length, two.length]).toEqual([0, 1]);
  stop();
  r.deliver(hello("page-1"));
  expect(one.length).toBe(0);
});

test("waiting is bounded", () => {
  const r = new PageRouter();
  for (let i = 0; i < MAX_WAITING + 5; i++) r.deliver(hello("page-1"));
  for (let i = 0; i < MAX_LABELS + 5; i++) r.deliver(hello(`page-x${i}`));
  const got: PageMessage[] = [];
  r.claim("page-1", (m) => got.push(m));
  expect(got.length).toBe(MAX_WAITING);
  const late: PageMessage[] = [];
  r.claim(`page-x${MAX_LABELS + 2}`, (m) => late.push(m));
  expect(late.length).toBe(0);
});

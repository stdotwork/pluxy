// Home's card after Finish later (kit/pages/setupLater.tsx): shown only while this pluxy's note is
// in the host's prefs, with the steps done and left, the way back to Setup, and Hide.
import { afterEach, expect, test } from "bun:test";
import { HomePage } from "../kit/pages/home";
import { SetupLaterCard } from "../kit/pages/setupLater";
import { all, restoreDom } from "./fakeDom";
import { account, attribute, buttonIn, client, linkIn, press, withPart, world } from "./setupWorld";

afterEach(restoreDom);

const HERE = "http://127.0.0.1:8317";

test("no note, or a note about another pluxy: no card, and nothing is read", async () => {
  const notes: Record<string, string>[] = [{}, { "setup-later": "http://10.0.0.5:8317" }];
  for (const prefs of notes) {
    const w = world({ firstRun: false, prefs });
    const m = await w.mount(<SetupLaterCard />);
    expect(withPart(m.root, "setup-later")).toHaveLength(0);
    expect(w.calls).toEqual([]);
    restoreDom();
  }
});

test("after Finish later: the three steps, which are done, how many are left, and Continue setup", async () => {
  const w = world({ firstRun: false, prefs: { "setup-later": HERE }, accounts: [account("acc_00000001")] });
  const m = await w.mount(<SetupLaterCard />);
  const card = withPart(m.root, "setup-later")[0];
  expect(card?.textContent).toContain("Finish setting up pluxy");
  expect(card?.textContent).toContain("2 steps left.");
  expect(
    all(card as never, "li").map((li) => [li.childNodes[1]?.textContent, attribute(li, "data-state")]),
  ).toEqual([
    ["Your AI accounts", "done"],
    ["Your coding tool", "todo"],
    ["Check it works", "todo"],
  ]);
  expect(attribute(linkIn(card as never, "Continue setup"), "href")).toBe("#/setup");
});

test("a tool that uses pluxy counts; without a passed test recorded, Check it works is still left", async () => {
  const w = world({
    firstRun: false,
    prefs: { "setup-later": HERE },
    accounts: [account("acc_00000001")],
    clients: [client("claude-code", "Claude Code", { state: "configured" })],
  });
  const m = await w.mount(<SetupLaterCard />);
  expect(m.text()).toContain("1 step left.");
});

test("a passed test recorded on the note marks Check it works done, and hides the card once all three are done", async () => {
  const partial = world({
    firstRun: false,
    prefs: { "setup-later": `${HERE} tested` },
    accounts: [account("acc_00000001")],
  });
  const m = await partial.mount(<SetupLaterCard />);
  const card = withPart(m.root, "setup-later")[0];
  expect(card?.textContent).toContain("1 step left.");
  expect(
    all(card as never, "li").map((li) => [li.childNodes[1]?.textContent, attribute(li, "data-state")]),
  ).toEqual([
    ["Your AI accounts", "done"],
    ["Your coding tool", "todo"],
    ["Check it works", "done"],
  ]);
  restoreDom();

  const complete = world({
    firstRun: false,
    prefs: { "setup-later": `${HERE} tested` },
    accounts: [account("acc_00000001")],
    clients: [client("claude-code", "Claude Code", { state: "configured" })],
  });
  const m2 = await complete.mount(<SetupLaterCard />);
  expect(withPart(m2.root, "setup-later")).toHaveLength(0);
});

test("Hide takes the card and the note away", async () => {
  const w = world({ firstRun: false, prefs: { "setup-later": HERE } });
  const m = await w.mount(<SetupLaterCard />);
  await press(buttonIn(m.root, "Hide"));
  expect(withPart(m.root, "setup-later")).toHaveLength(0);
  expect(w.prefs.get("setup-later")).toBeUndefined();
});

test("a pluxy that cannot say what setup found gets no card", async () => {
  const w = world({
    firstRun: false,
    prefs: { "setup-later": HERE },
    reply: (c) =>
      c.path === "/setup" ? { status: 404, error: { code: "not_found", message: "no" } } : undefined,
  });
  const m = await w.mount(<SetupLaterCard />);
  expect(withPart(m.root, "setup-later")).toHaveLength(0);
});

test("Home shows the card after Finish later even with no accounts yet", async () => {
  const w = world({ firstRun: false, prefs: { "setup-later": HERE }, accounts: [] });
  const m = await w.mount(<HomePage />);
  const card = withPart(m.root, "setup-later")[0];
  expect(card?.textContent).toContain("Finish setting up pluxy");
  expect(card?.textContent).toContain("3 steps left.");
  expect(attribute(linkIn(card as never, "Continue setup"), "href")).toBe("#/setup");
  await press(buttonIn(card as never, "Hide"));
  expect(withPart(m.root, "setup-later")).toHaveLength(0);
});

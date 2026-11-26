import { afterEach, expect, test } from "bun:test";
import { lifetimeFor } from "../src/launcherForm";
import { LauncherPage } from "../src/launcherPage";
import { all, restoreDom } from "./fakeDom";
import { mount, never } from "./pageMount";

afterEach(() => {
  restoreDom();
  Reflect.deleteProperty(globalThis, "location");
});

test("a terminal launch is always detached, whatever was chosen before the kind changed", () => {
  expect(lifetimeFor("terminal", "managed")).toBe("detached");
  expect(lifetimeFor("terminal", "detached")).toBe("detached");
});

test("a direct launch keeps the lifetime chosen, managed included", () => {
  expect(lifetimeFor("direct", "managed")).toBe("managed");
  expect(lifetimeFor("direct", "detached")).toBe("detached");
});

// ---- the page ----------------------------------------------------------------------------------------------

const entry = {
  id: "e1",
  client: "Claude Code",
  executable: "claude",
  argv: ["--resume"],
  cwd: "/home/me/project",
  used: 1_780_000_000_000,
};
const state = (over: Record<string, unknown> = {}) => ({
  available: true,
  notice: true,
  entries: [entry],
  history: {},
  ...over,
});
const runtime = {
  runtime: "r1",
  state: "running",
  pid: 4242,
  executable: "claude",
  argv: ["--model", "opus"],
};
const page = (handlers: Record<string, unknown> = {}) =>
  mount(<LauncherPage />, {
    launcher_state: state(),
    launcher_terminals: { terminals: [{ id: "automatic", label: "Automatic" }] },
    program_status: { runtimes: [] },
    ...handlers,
  });

test("until the first reply, the page says it is reading", async () => {
  const m = await page({ launcher_state: never });
  expect(m.text()).toContain("Reading launches…");
  expect(m.text()).not.toContain("Remembered launches");
  expect(m.text()).not.toContain("No remembered launches yet.");
});

test("without the Launcher add-on, a notice links to Add-ons and shows the reason under Details", async () => {
  const m = await page({
    launcher_state: state({ available: false, reason: "pluxy/execution is not installed." }),
  });
  expect(m.text()).toContain("Starting tools from here needs the Launcher add-on.");
  const link = m.button("Open Add-ons");
  expect(m.attr(link, "href")).toBe("#/device");
  expect(m.text()).toContain("Details");
  expect(m.text()).toContain("pluxy/execution is not installed.");
  // Remembered launches still show, with Start off; New launch and Running now are not drawn.
  expect(m.text()).toContain("Claude Code");
  expect(m.button("Start").disabled).toBe(true);
  expect(m.text()).not.toContain("New launch");
  expect(m.text()).not.toContain("Running now");
});

test("with the add-on, there is no notice and no Details", async () => {
  const m = await page();
  expect(m.text()).not.toContain("needs the Launcher add-on");
  expect(m.has("Open Add-ons")).toBe(false);
  expect(m.text()).toContain("New launch");
  expect(m.text()).toContain("Running now");
  expect(m.text()).toContain("Nothing started from here is running.");
});

test("an empty list of remembered launches says how to get one", async () => {
  const m = await page({ launcher_state: state({ entries: [] }) });
  expect(m.text()).toContain(
    "No remembered launches yet. Start one below and tick Remember in the confirmation window.",
  );
});

test("Start… waits for a program and a folder, and sends the launch for confirmation", async () => {
  const m = await page({ launcher_launch: null });
  expect(m.button("Start…").disabled).toBe(true);
  await m.input("Program", "claude");
  expect(m.button("Start…").disabled).toBe(true);
  await m.input("Folder", "/home/me/project");
  expect(m.button("Start…").disabled).toBe(false);
  await m.click("Start…");
  expect(m.commands("launcher_launch")).toEqual([
    {
      launch: {
        client: "claude",
        kind: "terminal",
        program: "claude",
        args: [],
        cwd: "/home/me/project",
        terminal: "automatic",
        lifetime: "detached",
      },
    },
  ]);
  expect(m.text()).toContain("A confirmation window shows exactly what will run.");
});

test("a terminal launch has no choice of what happens when the app quits; a direct one has", async () => {
  const m = await page();
  expect(m.text()).toContain("When the app quits");
  expect(m.text()).toContain("A terminal keeps running after the app quits.");
  expect(m.text()).not.toContain("Keep it running");
  const direct = m.radios().find((i) => i.value === "direct");
  await m.fire(direct as never, "change");
  expect(m.text()).not.toContain("A terminal keeps running after the app quits.");
  expect(m.text()).toContain("Keep it running");
  expect(m.text()).toContain("Stop it");
});

test("Stop and Restart ask first, and send their command only after Yes", async () => {
  const m = await page({ program_status: { runtimes: [runtime] }, program_call: null });
  expect(m.text()).toContain("4242");
  await m.click("Stop");
  expect(m.commands("program_call").length).toBe(0);
  expect(m.has("Yes, stop")).toBe(true);
  await m.click("Yes, stop");
  expect(m.commands("program_call")).toEqual([
    { package: "pluxy/execution", op: "control", input: { op: "stop", runtime: "r1" } },
  ]);
  await m.click("Restart");
  expect(m.commands("program_call").length).toBe(1);
  await m.click("Yes, restart");
  expect(m.commands("program_call")[1]).toEqual({
    package: "pluxy/execution",
    op: "control",
    input: { op: "restart", runtime: "r1" },
  });
});

test("Forget asks first, and Start sends the remembered launch", async () => {
  const m = await page({ launcher_forget: state({ entries: [] }), launcher_start: null });
  await m.click("Forget");
  expect(m.commands("launcher_forget").length).toBe(0);
  await m.click("Yes, forget");
  expect(m.commands("launcher_forget")).toEqual([{ id: "e1" }]);
  restoreDom();
  const s = await page({ launcher_start: null });
  await s.click("Start");
  expect(s.commands("launcher_start")).toEqual([{ id: "e1" }]);
});

test("the notification switch saves the moment it changes", async () => {
  const m = await page({ launcher_settings: state({ notice: false }) });
  const toggle = all(m.root, "input").find((i) => m.attr(i, "role") === "switch");
  expect(toggle).toBeDefined();
  (toggle as { checked: boolean }).checked = false;
  await m.fire(toggle as never, "change");
  expect(m.commands("launcher_settings")).toEqual([{ notice: false }]);
});

// The Setup page: its three steps, what each shows and sends, the import line above them, and
// the one finish. Against a fake core (setupWorld.tsx), through Preact's DOM renderer.
import { afterEach, beforeEach, expect, test } from "bun:test";
import type { ImportReport } from "../kit/api";
import { SetupPage } from "../kit/pages/setup";
import { defaultTestAccount, joinNames, kindGroups, orderedKinds } from "../kit/pages/setup.helpers";
import { PrivacyContext, privacyMasks } from "../kit/privacy";
import { all, fakeWindow, restoreDom } from "./fakeDom";
import {
  account,
  attribute,
  box,
  buttonIn,
  CLAUDE_PICK,
  CODEX_PICK,
  choose,
  client,
  descend,
  flush,
  hasButton,
  heading,
  IMPORT,
  kindName,
  kindRow,
  kindRows,
  linkIn,
  must,
  openStep,
  pick,
  press,
  step,
  steps,
  tick,
  withPart,
  world,
} from "./setupWorld";

/** Lets the page's one-second scan poll run once, and what it reads settle. */
async function tickTimers() {
  await new Promise((r) => setTimeout(r, 1100));
  await flush();
}

const place = { hash: "#/setup" };
const original = Object.getOwnPropertyDescriptor(globalThis, "location");
beforeEach(() => {
  place.hash = "#/setup";
  Object.defineProperty(globalThis, "location", { configurable: true, value: place });
});
afterEach(() => {
  restoreDom();
  if (original) Object.defineProperty(globalThis, "location", original);
  else Reflect.deleteProperty(globalThis, "location");
});

const ACC = account("acc_00000001", { label: "Work" });
const CONNECTED = client("claude-code", "Claude Code", { state: "configured" });

// ---- the steps ----------------------------------------------------------------------------------

test("three steps in order, and nothing a scan found is a step of its own", async () => {
  const w = world({
    signIns: [
      { provider: "pluxy/claude", from: "~/.claude", label: "Claude Code is signed in on this computer" },
    ],
  });
  const m = await w.mount(<SetupPage />);
  expect(steps(m.root).map((s) => all(s, "h2")[0]?.textContent)).toEqual([
    "Your AI accounts",
    "Your coding tool",
    "Check it works",
  ]);
  expect(withPart(m.root, "setup-import")).toHaveLength(0);
  expect(m.text()).not.toContain("Found on");
  expect(m.text()).toContain(
    "pluxy lets your coding tools use your AI accounts. Three short steps set it up, and you can skip any of them.",
  );
});

test("the import line is above the steps, and only when core found a CLIProxyAPI setup", async () => {
  const w = world({ imports: [IMPORT] });
  const m = await w.mount(<SetupPage />);
  const line = withPart(m.root, "setup-import")[0];
  expect(line?.textContent).toContain(
    "Found a CLIProxyAPI setup with 3 account files. pluxy can bring its accounts and settings across; nothing changes until you confirm.",
  );
  expect(line?.textContent).toContain("~/.cli-proxy-api/config.yaml");
  const order = descend(m.root);
  expect(order.indexOf(line as never)).toBeLessThan(order.indexOf(step(m.root, 1)));
});

test("a first run opens the first step not done", async () => {
  const none = await world().mount(<SetupPage />);
  expect(openStep(none.root)).toBe(1);
  restoreDom();
  const signed = await world({ accounts: [ACC] }).mount(<SetupPage />);
  expect(openStep(signed.root)).toBe(2);
  restoreDom();
  const both = await world({ accounts: [ACC], clients: [CONNECTED] }).mount(<SetupPage />);
  expect(openStep(both.root)).toBe(3);
});

test("after the first run every step is folded, with what each came to, and Back to Home", async () => {
  const w = world({
    firstRun: false,
    accounts: [ACC],
    clients: [CONNECTED, client("codex", "Codex CLI", { state: "configured" })],
  });
  const m = await w.mount(<SetupPage />);
  expect(openStep(m.root)).toBeUndefined();
  expect(m.text()).toContain("Review or change your accounts and coding tools.");
  expect(step(m.root, 1).textContent).toContain("1 account connected.");
  expect(step(m.root, 2).textContent).toContain("Claude Code and Codex use pluxy.");
  expect(step(m.root, 3).textContent).toContain("Not tested yet.");
  expect(steps(m.root).map((s) => attribute(s, "data-state"))).toEqual(["done", "done", "todo"]);
  expect(attribute(linkIn(m.root, "Back to Home"), "href")).toBe("#/");
  expect(hasButton(m.root, "Leave setup for now")).toBe(false);
  expect(m.text()).not.toContain("Set up pluxy");
});

test("folded summaries when nothing is done yet", async () => {
  const m = await world({ firstRun: false }).mount(<SetupPage />);
  expect(step(m.root, 1).textContent).toContain("No account yet.");
  expect(step(m.root, 2).textContent).toContain("No tool connected yet.");
});

test("step 1 counts only accounts that can take requests, and says what the others are", async () => {
  const off = account("acc_00000001", { label: "Work", disabled: true });
  const one = await world({ firstRun: false, accounts: [off] }).mount(<SetupPage />);
  expect(step(one.root, 1).textContent).toContain("1 account, turned off.");
  expect(step(one.root, 1).textContent).not.toContain("connected");
  expect(attribute(step(one.root, 1), "data-state")).toBe("todo");
  restoreDom();
  Object.defineProperty(globalThis, "location", { configurable: true, value: place });
  // A first run with only that account opens step 1, not step 2.
  const first = await world({ accounts: [off] }).mount(<SetupPage />);
  expect(openStep(first.root)).toBe(1);
  restoreDom();
  Object.defineProperty(globalThis, "location", { configurable: true, value: place });
  const stopped = account("acc_00000002", { label: "Home", extension: "pluxy/gone" });
  const mixed = await world({
    firstRun: false,
    accounts: [ACC, off, stopped],
    running: ["pluxy/claude"],
  }).mount(<SetupPage />);
  expect(step(mixed.root, 1).textContent).toContain("1 account connected; 2 more cannot take requests.");
  expect(attribute(step(mixed.root, 1), "data-state")).toBe("done");
  restoreDom();
  Object.defineProperty(globalThis, "location", { configurable: true, value: place });
  const none = await world({ firstRun: false, accounts: [off, stopped], running: ["pluxy/claude"] }).mount(
    <SetupPage />,
  );
  expect(step(none.root, 1).textContent).toContain("2 accounts, none can take requests.");
});

test("a scan ending or an account appearing does not move the open step", async () => {
  const w = world({ unscanned: true, scanning: true, clients: [] });
  const m = await w.mount(<SetupPage />);
  expect(openStep(m.root)).toBe(1);
  // The scan ends with a connected tool, and an account is added in another window.
  w.state.setup.scanning = false;
  w.state.setup.scanned_at = "2026-11-20T11:00:00Z";
  w.state.setup.found.clients = [CONNECTED];
  w.state.accounts.push(ACC);
  fakeWindow().dispatch("focus");
  await tickTimers();
  expect(step(m.root, 1).textContent).toContain("1 account connected.");
  expect(openStep(m.root)).toBe(1);
  expect(step(m.root, 2).textContent).toContain("Claude Code uses pluxy.");
});

test("Skip and Continue open the next step and move focus to its heading; Open opens that step", async () => {
  const w = world();
  const m = await w.mount(<SetupPage />);
  await press(buttonIn(step(m.root, 1), "Skip"));
  expect(openStep(m.root)).toBe(2);
  expect(m.document.activeElement).toBe(heading(m.root, 2));
  await press(buttonIn(step(m.root, 2), "Skip"));
  expect(openStep(m.root)).toBe(3);
  expect(m.document.activeElement).toBe(heading(m.root, 3));
  await press(buttonIn(step(m.root, 1), "Open"));
  expect(openStep(m.root)).toBe(1);
  expect(m.document.activeElement).toBe(heading(m.root, 1));
});

test("every step can be skipped: Skip on steps 1 and 2, Finish without testing on step 3", async () => {
  const m = await world().mount(<SetupPage />);
  expect(hasButton(step(m.root, 1), "Skip")).toBe(true);
  await press(buttonIn(step(m.root, 1), "Skip"));
  expect(hasButton(step(m.root, 2), "Skip")).toBe(true);
  await press(buttonIn(step(m.root, 2), "Skip"));
  expect(hasButton(step(m.root, 3), "Finish without testing")).toBe(true);
  expect(hasButton(m.root, "Leave setup for now")).toBe(true);
  expect(m.text()).toContain("It won't open by itself again; it stays under Settings.");
});

test("Continue, not Skip, once a step is done", async () => {
  const m = await world({ accounts: [ACC], clients: [CONNECTED] }).mount(<SetupPage />);
  await press(buttonIn(step(m.root, 1), "Open"));
  expect(attribute(buttonIn(step(m.root, 1), "Continue"), "data-variant")).toBe("primary");
  await press(buttonIn(step(m.root, 1), "Continue"));
  expect(attribute(buttonIn(step(m.root, 2), "Continue"), "data-variant")).toBe("primary");
});

// ---- step 1: which kinds -----------------------------------------------------------------------

const EIGHT = [
  CLAUDE_PICK,
  pick("pluxy/codex", "Codex", { suggested: true, because: "Codex CLI is installed on this computer" }),
  pick("pluxy/gemini", "Gemini CLI"),
  pick("pluxy/openai-compatible", "OpenAI Compatible"),
  pick("pluxy/kimi", "Kimi", { suggested: true, because: "Kimi is signed in on this computer" }),
  pick("pluxy/qwen", "Qwen"),
  pick("pluxy/iflow", "iFlow"),
  pick("pluxy/vertex", "Vertex AI"),
];
const FIVE_TOOLS = ["claude-code", "codex-setup", "opencode", "pi-setup", "omp-setup"].map((id) =>
  pick(`pluxy/${id}`, `Tool package ${id}`, { kind: "client", suggested: id === "claude-code" }),
);

test("the kinds: found ones first and ticked with why, then the four by plain name, the rest under More", async () => {
  const w = world({ picks: [...EIGHT, ...FIVE_TOOLS] });
  const m = await w.mount(<SetupPage />);
  expect(m.text()).toContain("Tick the kinds of account you have.");
  expect(m.text()).not.toContain("Tool package");
  expect(kindRows(m.root).map(kindName)).toEqual([
    "ChatGPT (Codex)",
    "Kimi",
    "Claude",
    "Gemini",
    "Another service with an API key",
  ]);
  expect(box(kindRow(m.root, "ChatGPT (Codex)"))?.checked).toBe(true);
  expect(kindRow(m.root, "ChatGPT (Codex)").textContent).toContain("Codex CLI is installed on this computer");
  expect(box(kindRow(m.root, "Kimi"))?.checked).toBe(true);
  expect(box(kindRow(m.root, "Claude"))?.checked).toBe(false);
  expect(kindRow(m.root, "Claude").textContent).toContain(
    "Claude models with a Claude subscription or an Anthropic API key.",
  );
  const more = buttonIn(m.root, "More (3)");
  expect(attribute(more, "aria-expanded")).toBe("false");
  await press(more);
  expect(kindRows(m.root).map(kindName).slice(5)).toEqual(["Qwen", "iFlow", "Vertex AI"]);
  expect(attribute(buttonIn(m.root, "Hide more"), "aria-expanded")).toBe("true");
});

test("Add sends the ticked IDs; a failed one stays ticked with why, and Add tries it again", async () => {
  const w = world({
    picks: EIGHT,
    reply: (c) => {
      if (c.method !== "POST" || c.path !== "/setup/apply") return undefined;
      const ids = (c.body as { picks: string[] }).picks;
      for (const id of ids)
        if (id !== "pluxy/gemini") must(w.state.setup.picks.find((p) => p.id === id)).installed = true;
      return {
        status: 200,
        body: {
          results: ids.map((id) =>
            id === "pluxy/gemini"
              ? { id, status: "error", error: "the registry did not answer." }
              : { id, status: "done" },
          ),
        },
      };
    },
  });
  const m = await w.mount(<SetupPage />);
  await tick(must(box(kindRow(m.root, "Gemini"))), true);
  await press(buttonIn(m.root, "Add ChatGPT (Codex), Gemini and Kimi"));
  expect(w.sent("POST", "/setup/apply").map((c) => c.body)).toEqual([
    { picks: ["pluxy/codex", "pluxy/gemini", "pluxy/kimi"] },
  ]);
  expect(w.changed.times).toBe(1);
  expect(kindRow(m.root, "Gemini").textContent).toContain(
    "Could not add Gemini: the registry did not answer.",
  );
  expect(box(kindRow(m.root, "Gemini"))?.checked).toBe(true);
  expect(kindRow(m.root, "ChatGPT (Codex)").textContent).toContain("Added");
  expect(box(kindRow(m.root, "ChatGPT (Codex)"))).toBeUndefined();
  await press(buttonIn(m.root, "Add Gemini"));
  expect(w.sent("POST", "/setup/apply").map((c) => c.body)).toEqual([
    { picks: ["pluxy/codex", "pluxy/gemini", "pluxy/kimi"] },
    { picks: ["pluxy/gemini"] },
  ]);
});

test("nothing ticked and nothing added: Add waits, with Tick at least one", async () => {
  const m = await world().mount(<SetupPage />);
  const add = buttonIn(m.root, "Add");
  expect(add.disabled).toBe(true);
  expect(m.text()).toContain("Tick at least one.");
});

test("an added kind that does not run says so and links to Extensions", async () => {
  const w = world({
    picks: [{ ...CLAUDE_PICK, installed: true }],
    packages: [
      {
        id: "pluxy/claude",
        version: "1",
        registry: "r",
        url: "",
        sha256: "",
        enabled: false,
        installed_at: "",
        state: "disabled",
      },
    ],
  });
  const m = await w.mount(<SetupPage />);
  const row = kindRow(m.root, "Claude");
  expect(row.textContent).toContain("Added, not running");
  expect(attribute(linkIn(row, "Open Extensions"), "href")).toBe("#/extensions");
});

test("no kinds to add says so and offers Extensions, never a build command", async () => {
  const m = await world({ picks: [], registries: 0 }).mount(<SetupPage />);
  expect(step(m.root, 1).textContent).toContain("This copy of pluxy has no kinds of account to add.");
  expect(attribute(linkIn(step(m.root, 1), "Open Extensions"), "href")).toBe("#/extensions");
  expect(m.text()).not.toContain("go run");
});

// ---- step 1: signing in ------------------------------------------------------------------------

test("once a kind runs the list folds, and Add an account offers only the added kinds, in the step's order", async () => {
  const w = world({
    picks: [
      { ...CLAUDE_PICK, installed: true },
      { ...CODEX_PICK, installed: true },
      pick("pluxy/gemini", "Gemini CLI"),
    ],
    signIns: [
      { provider: "pluxy/claude", from: "~/.claude", label: "Claude Code is signed in on this computer" },
    ],
  });
  const m = await w.mount(<SetupPage />);
  const one = step(m.root, 1);
  expect(one.textContent).toContain("pluxy can now connect to Claude and ChatGPT (Codex).");
  expect(one.textContent).not.toContain("Added:");
  expect(one.textContent).toContain(
    "Claude Code is signed in on this computer. Sign in below with the same account.",
  );
  expect(withPart(one, "method-group").map((g) => all(g, "h2")[0]?.textContent)).toEqual([
    "Claude",
    "ChatGPT (Codex)",
  ]);
  expect(one.textContent).not.toContain("ID");
  await press(buttonIn(one, "Add another kind"));
  expect(kindRows(m.root).map(kindName)).toEqual(["Claude", "ChatGPT (Codex)", "Gemini"]);
});

test("after a sign-in here the count shows above the form, which keeps its done state", async () => {
  const w = world({ picks: [{ ...CLAUDE_PICK, installed: true }] });
  const m = await w.mount(<SetupPage />);
  await press(
    must(all(step(m.root, 1), "button").find((b) => attribute(b, "aria-label") === "Start: Claude sign-in")),
  );
  const one = step(m.root, 1);
  expect(one.textContent).toContain("1 account connected.");
  expect(attribute(linkIn(one, "See them on Accounts"), "href")).toBe("#/accounts");
  expect(withPart(one, "account-added")).toHaveLength(1);
  expect(attribute(buttonIn(one, "Continue"), "data-variant")).toBe("primary");
});

test("accounts already there: the count and Add another account, which opens the form", async () => {
  const w = world({ picks: [{ ...CLAUDE_PICK, installed: true }], accounts: [ACC], firstRun: false });
  const m = await w.mount(<SetupPage />);
  await press(buttonIn(step(m.root, 1), "Open"));
  const one = step(m.root, 1);
  expect(one.textContent).toContain("1 account connected.");
  expect(withPart(one, "method-group")).toHaveLength(0);
  await press(buttonIn(one, "Add another account"));
  expect(withPart(step(m.root, 1), "method-group")).toHaveLength(1);
});

// ---- step 2 ------------------------------------------------------------------------------------

test("step 2 is the Tools list: Set up shows the steps, and Make key puts a key named for the tool in the text", async () => {
  const w = world({ accounts: [ACC] });
  const m = await w.mount(<SetupPage />);
  expect(openStep(m.root)).toBe(2);
  const two = step(m.root, 2);
  expect(two.textContent).toContain("Detected on this computer.");
  const row = must(withPart(two, "tool-row")[0]);
  await press(buttonIn(row, "Set up"));
  await press(buttonIn(row, "Make key"));
  expect(w.sent("POST", "/client-keys").map((c) => c.body)).toEqual([{ label: "Claude Code" }]);
  expect(all(row, "pre")[0]?.textContent).toContain('"key": "sk-secret-1"');
});

test("a key made in step 2 is gone once the step folds: reopened, the text has no key and Make key is back", async () => {
  const w = world({ accounts: [ACC] });
  const m = await w.mount(<SetupPage />);
  await press(buttonIn(must(withPart(step(m.root, 2), "tool-row")[0]), "Set up"));
  await press(buttonIn(must(withPart(step(m.root, 2), "tool-row")[0]), "Make key"));
  expect(m.text()).toContain("sk-secret-1");
  // Fold step 2 by opening step 1, then open step 2 again.
  await press(buttonIn(step(m.root, 1), "Open"));
  expect(m.text()).not.toContain("sk-secret-1");
  await press(buttonIn(step(m.root, 2), "Open"));
  const row = must(withPart(step(m.root, 2), "tool-row")[0]);
  if (hasButton(row, "Set up")) await press(buttonIn(row, "Set up"));
  expect(m.text()).not.toContain("sk-secret-1");
  expect(all(row, "pre")[0]?.textContent).toContain('"key": "<client-key>"');
  expect(hasButton(row, "Make key")).toBe(true);
});

test("with no account, step 2's notice opens step 1", async () => {
  const m = await world().mount(<SetupPage />);
  await press(buttonIn(step(m.root, 1), "Skip"));
  const two = step(m.root, 2);
  expect(two.textContent).toContain(
    "pluxy has no accounts yet, so a coding tool you set up here will get errors.",
  );
  await press(buttonIn(two, "Add an account"));
  expect(openStep(m.root)).toBe(1);
});

test("a pluxy on another computer says so in step 2", async () => {
  for (const [init, options] of [
    [{ address: "http://10.0.0.5:8317" }, undefined],
    [{}, { isRemote: true }],
  ] as const) {
    const m = await world({ accounts: [ACC], ...init }).mount(<SetupPage options={options} />);
    expect(step(m.root, 2).textContent).toContain("Detected on the computer pluxy runs on.");
    expect(step(m.root, 2).textContent).not.toContain("Detected on this computer.");
    restoreDom();
    Object.defineProperty(globalThis, "location", { configurable: true, value: place });
  }
  const local = await world({ accounts: [ACC], address: "http://10.0.0.5:8317" }).mount(
    <SetupPage options={{ isRemote: false }} />,
  );
  expect(step(local.root, 2).textContent).toContain("Detected on this computer.");
});

test("network reach is said only when core says other computers can reach it", async () => {
  const far = await world({ accounts: [ACC], reachable: true }).mount(<SetupPage />);
  expect(step(far.root, 2).textContent).toContain("Other computers on your network can reach pluxy.");
  restoreDom();
  Object.defineProperty(globalThis, "location", { configurable: true, value: place });
  const near = await world({ accounts: [ACC] }).mount(<SetupPage />);
  expect(step(near.root, 2).textContent).not.toContain("Other computers on your network");
});

test("the host's tool button and its own tools are used in step 2", async () => {
  const extra = client("hermes", "Hermes Agent", { state: "configured" });
  const m = await world({ accounts: [ACC] }).mount(
    <SetupPage
      options={{ toolAction: (c) => <a href={`#/${c.id}`}>Open {c.name}</a>, extraTools: [extra] }}
    />,
  );
  await press(buttonIn(step(m.root, 1), "Open"));
  await press(buttonIn(step(m.root, 2), "Open"));
  const two = step(m.root, 2);
  expect(attribute(linkIn(two, "Open Claude Code"), "href")).toBe("#/claude-code");
  expect(two.textContent).toContain("Hermes Agent");
  expect(hasButton(two, "Set up")).toBe(false);
});

test("the host's notes are shown in step 2", async () => {
  const m = await world({ accounts: [ACC] }).mount(
    <SetupPage
      options={{ notes: () => ({ above: <p>Above the list.</p>, below: <p>Under the list.</p> }) }}
    />,
  );
  await press(buttonIn(step(m.root, 1), "Open"));
  await press(buttonIn(step(m.root, 2), "Open"));
  const two = step(m.root, 2);
  expect(two.textContent).toContain("Above the list.");
  expect(two.textContent).toContain("Under the list.");
});

test("Refresh in step 2 asks pluxy to look again and tells the host", async () => {
  let refreshed = 0;
  const w = world({ accounts: [ACC] });
  const m = await w.mount(<SetupPage options={{ onRefresh: () => refreshed++ }} />);
  await press(buttonIn(step(m.root, 2), "Refresh"));
  expect(w.sent("POST", "/setup/scan")).toHaveLength(2); // the one as the page opened, and this
  expect(refreshed).toBe(1);
});

// ---- words --------------------------------------------------------------------------------------

test("no developer command and no 'this machine' anywhere on the page", async () => {
  const w = world({
    imports: [IMPORT],
    reply: (c) => (c.path === "/setup/import" ? { status: 200, body: REPORT } : undefined),
    picks: [{ ...CLAUDE_PICK, installed: true }, ...EIGHT.slice(1)],
    signIns: [
      { provider: "pluxy/claude", from: "~/.claude", label: "Claude Code is signed in on this computer" },
    ],
    accounts: [ACC],
  });
  const m = await w.mount(<SetupPage />);
  const seen: string[] = [m.text()];
  for (const n of [1, 2, 3]) {
    if (hasButton(step(m.root, n), "Open")) await press(buttonIn(step(m.root, n), "Open"));
    seen.push(m.text());
  }
  await press(buttonIn(step(m.root, 2), "Open"));
  await press(buttonIn(must(withPart(m.root, "tool-row")[0]), "Set up"));
  seen.push(m.text());
  // The import preview, with its lists: text in a closed <details> is in textContent too.
  await press(buttonIn(line(m.root), "Preview import"));
  expect(withPart(m.root, "setup-report")).toHaveLength(1);
  seen.push(m.text());
  for (const text of seen) {
    for (const banned of [
      "go run",
      "pluxy model list",
      "pluxy key add",
      "pluxy import",
      "this machine",
      "pluxy-pack",
    ])
      expect(text).not.toContain(banned);
  }
});

// ---- step 3 ------------------------------------------------------------------------------------

test("the test sends no model by default, says what passed, and offers Finish setup", async () => {
  const w = world({ accounts: [ACC], clients: [CONNECTED] });
  const m = await w.mount(<SetupPage />);
  const three = step(m.root, 3);
  expect(three.textContent).toContain(
    "This sends one short test message through Work. The provider counts it against that account's limits. pluxy does not list it in Activity or Usage.",
  );
  expect(all(three, "select")).toHaveLength(0);
  await press(buttonIn(three, "Send a test request"));
  expect(
    w.sent("POST", "/accounts/acc_00000001/test").map((c) => (c.body as { model?: string }).model),
  ).toEqual([undefined]);
  const passed = withPart(step(m.root, 3), "setup-test")[0];
  expect(passed?.textContent).toContain("It works: Work replied in 420 ms using model-a.");
  expect(descend(must(passed)).some((n) => attribute(n, "aria-label") === "Succeeded")).toBe(true);
  expect(attribute(buttonIn(step(m.root, 3), "Finish setup"), "data-variant")).toBe("primary");
  // Folded, the step says what passed.
  await press(buttonIn(step(m.root, 1), "Open"));
  expect(step(m.root, 3).textContent).toContain("Passed: model-a in 420 ms.");
});

test("a chosen model is sent; excluded models are not offered", async () => {
  const w = world({ accounts: [ACC], clients: [CONNECTED] });
  const m = await w.mount(<SetupPage />);
  await press(buttonIn(step(m.root, 3), "Choose a model"));
  const select = must(all(step(m.root, 3), "select")[0]);
  expect(all(select, "option").map((o) => o.textContent)).toEqual([
    "First model the account offers",
    "model-a",
    "model-b",
  ]);
  await choose(select, "model-b");
  await press(buttonIn(step(m.root, 3), "Send a test request"));
  expect(
    w.sent("POST", "/accounts/acc_00000001/test").map((c) => (c.body as { model?: string }).model),
  ).toEqual(["model-b"]);
});

test("a refused sign-in says so, with Sign in again, Test again and Finish anyway", async () => {
  const w = world({
    accounts: [ACC],
    clients: [CONNECTED],
    reply: (c) =>
      c.path.endsWith("/test")
        ? {
            status: 200,
            body: {
              result: {
                ok: false,
                model: "model-a",
                latency_ms: 80,
                error: { code: "auth", message: "the key was refused." },
              },
            },
          }
        : undefined,
  });
  const m = await w.mount(<SetupPage />);
  await press(buttonIn(step(m.root, 3), "Send a test request"));
  const three = step(m.root, 3);
  expect(three.textContent).toContain("Sign-in refused: the key was refused");
  expect(attribute(linkIn(three, "Sign in again"), "href")).toBe("#/accounts/acc_00000001/sign-in");
  // Signing in again is what fixes it, so it is the filled button.
  expect(attribute(linkIn(three, "Sign in again"), "data-variant")).toBe("primary");
  expect(attribute(buttonIn(three, "Test again"), "data-variant")).toBe("quiet");
  expect(hasButton(three, "Test again")).toBe(true);
  expect(hasButton(three, "Finish anyway")).toBe(true);
  expect(hasButton(three, "Finish setup")).toBe(false);
});

test("with privacy mode on, a failed test's message hides the account's name", async () => {
  const w = world({
    accounts: [account("acc_00000001", { label: "Juniper Lab" })],
    clients: [CONNECTED],
    reply: (c) =>
      c.path.endsWith("/test")
        ? {
            status: 200,
            body: {
              result: {
                ok: false,
                model: "model-a",
                latency_ms: 80,
                error: { code: "auth", message: "the key for Juniper Lab was refused." },
              },
            },
          }
        : undefined,
  });
  const m = await w.mount(
    <PrivacyContext.Provider value={privacyMasks(true)}>
      <SetupPage />
    </PrivacyContext.Provider>,
  );
  await press(buttonIn(step(m.root, 3), "Send a test request"));
  const three = step(m.root, 3);
  expect(three.textContent).toContain("Sign-in refused: the key for");
  expect(three.textContent).not.toContain("Juniper");
});

test("a config-file account's refused key leads to See what to do", async () => {
  const w = world({
    accounts: [account("acc_00000001", { source: "config", label: "Work" })],
    clients: [CONNECTED],
    reply: (c) =>
      c.path.endsWith("/test")
        ? {
            status: 200,
            body: {
              result: { ok: false, model: "m", latency_ms: 1, error: { code: "permission", message: "no" } },
            },
          }
        : undefined,
  });
  const m = await w.mount(<SetupPage />);
  await press(buttonIn(step(m.root, 3), "Send a test request"));
  expect(attribute(linkIn(step(m.root, 3), "See what to do"), "href")).toBe("#/accounts/acc_00000001");
});

test("a provider that fails first (502) reads with K3's failure word", async () => {
  const w = world({
    accounts: [ACC],
    clients: [CONNECTED],
    reply: (c) =>
      c.path.endsWith("/test")
        ? { status: 502, error: { code: "unavailable", message: "connection refused" } }
        : undefined,
  });
  const m = await w.mount(<SetupPage />);
  await press(buttonIn(step(m.root, 3), "Send a test request"));
  expect(step(m.root, 3).textContent).toContain("Provider unreachable: connection refused");
  expect(hasButton(step(m.root, 3), "Sign in again")).toBe(false);
});

test("an account with no models says there is nothing to test it with", async () => {
  const w = world({
    accounts: [ACC],
    clients: [CONNECTED],
    reply: (c) =>
      c.path.endsWith("/test")
        ? { status: 422, error: { code: "invalid_request", message: "no models" } }
        : undefined,
  });
  const m = await w.mount(<SetupPage />);
  await press(buttonIn(step(m.root, 3), "Send a test request"));
  expect(step(m.root, 3).textContent).toContain(
    "This account lists no models, so there is nothing to test it with.",
  );
  expect(hasButton(step(m.root, 3), "Finish anyway")).toBe(true);
  expect(hasButton(step(m.root, 3), "Test again")).toBe(false);
});

test("an account gone since the page read them: the accounts are read again and the default picked again", async () => {
  const w = world({
    accounts: [ACC, account("acc_00000002", { label: "Home" })],
    clients: [CONNECTED],
    reply: (c) => {
      if (c.path !== "/accounts/acc_00000001/test") return undefined;
      w.state.accounts.splice(0, 1);
      return { status: 404, error: { code: "not_found", message: "no such account" } };
    },
  });
  const m = await w.mount(<SetupPage />);
  const reads = w.sent("GET", "/accounts").length;
  await press(buttonIn(step(m.root, 3), "Send a test request"));
  expect(w.sent("GET", "/accounts").length).toBeGreaterThan(reads);
  expect(step(m.root, 3).textContent).toContain("test message through Home.");
});

test("with two accounts an Account choice shows, naming each one's kind, and the chosen one is tested", async () => {
  const w = world({
    accounts: [ACC, account("acc_00000002", { label: "Home", extension: "pluxy/codex" })],
    clients: [CONNECTED],
  });
  const m = await w.mount(<SetupPage />);
  const select = must(all(step(m.root, 3), "select")[0]);
  expect(all(select, "option").map((o) => o.textContent)).toEqual(["Work (Claude)", "Home (Codex)"]);
  await choose(select, "acc_00000002");
  await press(buttonIn(step(m.root, 3), "Send a test request"));
  expect(w.sent("POST", "/accounts/acc_00000002/test")).toHaveLength(1);
});

test("no account that can take a test: Add an account opens step 1, or finish without testing", async () => {
  const w = world({
    accounts: [
      account("acc_00000001", { disabled: true }),
      account("acc_00000002", { extension: "pluxy/gone" }),
    ],
    clients: [CONNECTED],
    running: ["pluxy/claude"],
  });
  const m = await w.mount(<SetupPage />);
  // Neither account can take requests, so step 1 is not done and opens first.
  expect(openStep(m.root)).toBe(1);
  await press(buttonIn(step(m.root, 3), "Open"));
  const three = step(m.root, 3);
  expect(three.textContent).toContain("There is no account to test yet.");
  expect(hasButton(three, "Finish without testing")).toBe(true);
  await press(buttonIn(three, "Add an account"));
  expect(openStep(m.root)).toBe(1);
});

test("the default test account: the one added here, else the first Serving, else the first that can take one", () => {
  const off = account("a", { disabled: true });
  const cooling = account("b", {
    cooldowns: [{ kind: "rate_limit", scope: "account", until: "2999-01-01T00:00:00Z" }] as never,
  });
  const serving = account("c");
  const later = account("d");
  expect(defaultTestAccount([off, cooling, serving, later], undefined, "d")?.id).toBe("d");
  expect(defaultTestAccount([off, cooling, serving, later])?.id).toBe("c");
  expect(defaultTestAccount([off, cooling])?.id).toBe("b");
  expect(defaultTestAccount([off])).toBeUndefined();
});

// ---- finishing ----------------------------------------------------------------------------------

test("every finish button sends POST /setup/done, tells the host, and goes Home", async () => {
  const cases: [
    string,
    Parameters<typeof world>[0],
    (m: Awaited<ReturnType<ReturnType<typeof world>["mount"]>>) => Promise<void>,
  ][] = [
    ["Leave setup for now", {}, async () => {}],
    ["Finish without testing", { accounts: [ACC], clients: [CONNECTED] }, async () => {}],
    [
      "Finish setup",
      { accounts: [ACC], clients: [CONNECTED] },
      async (m) => press(buttonIn(step(m.root, 3), "Send a test request")),
    ],
    [
      "Finish anyway",
      {
        accounts: [ACC],
        clients: [CONNECTED],
        reply: (c) =>
          c.path.endsWith("/test")
            ? { status: 502, error: { code: "upstream", message: "down" } }
            : undefined,
      },
      async (m) => press(buttonIn(step(m.root, 3), "Send a test request")),
    ],
  ];
  for (const [label, init, before] of cases) {
    place.hash = "#/setup";
    let finished = 0;
    const w = world(init);
    const m = await w.mount(<SetupPage options={{ onFinish: () => finished++ }} />);
    await before(m);
    await press(buttonIn(m.root, label));
    expect([label, w.sent("POST", "/setup/done").length, finished, place.hash]).toEqual([label, 1, 1, "#/"]);
    restoreDom();
    Object.defineProperty(globalThis, "location", { configurable: true, value: place });
  }
});

test("a finish that fails stays, with why", async () => {
  let finished = 0;
  const w = world({
    reply: (c) =>
      c.path === "/setup/done"
        ? { status: 500, error: { code: "internal", message: "disk full." } }
        : undefined,
  });
  const m = await w.mount(<SetupPage options={{ onFinish: () => finished++ }} />);
  await press(buttonIn(m.root, "Leave setup for now"));
  expect(m.text()).toContain("Could not finish setup: disk full.");
  expect(finished).toBe(0);
  expect(place.hash).toBe("#/setup");
});

test("a 401 from any call signs out", async () => {
  const w = world({
    reply: (c) =>
      c.path === "/setup/done" ? { status: 401, error: { code: "auth", message: "no" } } : undefined,
  });
  const m = await w.mount(<SetupPage />);
  await press(buttonIn(m.root, "Leave setup for now"));
  expect(w.signedOut.times).toBe(1);
});

test("loading and failed panes", async () => {
  const never = await world({
    reply: (c) => (c.path === "/setup" ? new Promise(() => {}) : undefined),
  }).mount(<SetupPage />);
  expect(never.text()).toContain("Checking what pluxy already has…");
  restoreDom();
  Object.defineProperty(globalThis, "location", { configurable: true, value: place });
  const w = world({
    reply: (c) =>
      c.path === "/setup" ? { status: 500, error: { code: "internal", message: "boom" } } : undefined,
  });
  const m = await w.mount(<SetupPage />);
  expect(m.text()).toContain("Could not read setup.");
  expect(hasButton(m.root, "Try again")).toBe(true);
});

// ---- the import --------------------------------------------------------------------------------

const REPORT: ImportReport = {
  dry_run: true,
  brought: [
    { what: "auth file", name: "claude-work.json", as: "pluxy/claude account claude-work" },
    { what: "api-keys" },
  ],
  changed: [
    // Core's own words for a listen it cannot change (internal/legacy/config.go).
    {
      what: "port",
      why: "listen cannot change while pluxy runs",
      keep_with: '"listen": ":8317" in pluxy\'s config file, then restart core',
    },
  ],
  not_brought: [{ what: "auth file", name: "qwen-a.json", why: "no extension takes it" }],
  needs: [{ pick: "pluxy/claude", why: "2 accounts need it" }],
};

const importing = (
  answer?: (dry: boolean) => { status: number; body?: unknown; error?: { code: string; message: string } },
) =>
  world({
    imports: [IMPORT],
    reply: (c) => {
      if (c.path !== "/setup/import") return undefined;
      const dry = (c.body as { dry_run: boolean }).dry_run;
      return answer?.(dry) ?? { status: 200, body: { ...REPORT, dry_run: dry } };
    },
  });
const line = (root: Parameters<typeof withPart>[0]) => must(withPart(root, "setup-import")[0]);
const imports = (w: ReturnType<typeof world>) =>
  w.sent("POST", "/setup/import").map((c) => (c.body as { dry_run: boolean }).dry_run);

test("the preview comes first: nothing is imported until Import now, from that preview", async () => {
  const w = importing();
  const m = await w.mount(<SetupPage />);
  expect(hasButton(line(m.root), "Import now")).toBe(false);
  await press(buttonIn(line(m.root), "Preview import"));
  expect(imports(w)).toEqual([true]);
  expect(w.sent("POST", "/setup/import")[0]?.body).toEqual({
    config: "~/.cli-proxy-api/config.yaml",
    auth_dir: "~/.cli-proxy-api/auths",
    dry_run: true,
  });
  const report = must(withPart(m.root, "setup-report")[0]);
  expect(report.textContent).toContain(
    "Importing would bring across 2, set 1 differently in pluxy and leave out 1.",
  );
  expect(all(report, "summary").map((s) => s.textContent)).toEqual([
    "Will import (2)",
    "Set differently in pluxy (1)",
    "Left out (1)",
  ]);
  expect(report.textContent).toContain("listen cannot change while pluxy runs");
  expect(report.textContent).toContain(
    'To keep it: "listen": ":8317" in pluxy\'s config file, then restart core',
  );
  await press(buttonIn(line(m.root), "Cancel"));
  expect(withPart(m.root, "setup-report")).toHaveLength(0);
  expect(imports(w)).toEqual([true]);
  await press(buttonIn(line(m.root), "Preview import"));
  await press(buttonIn(line(m.root), "Import now"));
  expect(imports(w)).toEqual([true, true, false]);
  // Import now sends exactly what was previewed.
  const [, preview, done] = w.sent("POST", "/setup/import").map((c) => c.body);
  expect(done).toEqual({ ...(preview as object), dry_run: false });
  expect(line(m.root).textContent).toContain("Imported.");
  expect(line(m.root).textContent).not.toContain("nothing changes until you confirm");
  expect(line(m.root).textContent).toContain("The imported accounts need Claude. They are ticked in step 1.");
  expect(box(kindRow(m.root, "Claude"))?.checked).toBe(true);
  expect(kindRow(m.root, "Claude").textContent).toContain("2 accounts need it");
  await press(buttonIn(line(m.root), "Close"));
  expect(withPart(m.root, "setup-report")).toHaveLength(0);
});

const refusals: [number, string, string | undefined][] = [
  [
    400,
    "Could not read the CLIProxyAPI setup: there is no file at x. Fix the file, then try again. Nothing changed.",
    undefined,
  ],
  [422, "This setup cannot be imported: port is not a number. Nothing changed.", undefined],
  [
    409,
    "This pluxy keeps its settings in config.yaml, which import cannot change. Nothing changed.",
    "pluxy init",
  ],
  [
    404,
    "This pluxy is too old to import from here.",
    "pluxy import cpa ~/.cli-proxy-api/config.yaml --auth-dir ~/.cli-proxy-api/auths",
  ],
  [0, "pluxy did not answer, so nothing was read or changed.", undefined],
];

test("each refusal of the preview says its words, drops the preview, and only 404 and 409 offer More details", async () => {
  for (const [status, words, command] of refusals) {
    const message =
      status === 400 ? "there is no file at x." : status === 422 ? "port is not a number" : "refused";
    const w = importing(() => ({ status, error: status ? { code: "x", message } : undefined }));
    const m = await w.mount(<SetupPage />);
    await press(buttonIn(line(m.root), "Preview import"));
    const l = line(m.root);
    expect([status, l.textContent.includes(words)]).toEqual([status, true]);
    expect(withPart(m.root, "setup-report")).toHaveLength(0);
    const details = all(l, "summary").find((s) => s.textContent === "More details");
    expect([status, details !== undefined]).toEqual([status, command !== undefined]);
    if (command) expect(all(l, "pre")[0]?.textContent).toBe(command);
    restoreDom();
    Object.defineProperty(globalThis, "location", { configurable: true, value: place });
  }
});

test("a lost reply to Import now says it is not known whether it ran, and Refresh looks again", async () => {
  const w = importing((dry) => (dry ? { status: 200, body: REPORT } : { status: 0 }));
  const m = await w.mount(<SetupPage />);
  await press(buttonIn(line(m.root), "Preview import"));
  await press(buttonIn(line(m.root), "Import now"));
  expect(line(m.root).textContent).toContain(
    "pluxy did not answer, so it is not known whether the import ran. Press Refresh to see.",
  );
  expect(withPart(m.root, "setup-report")).toHaveLength(0);
  expect(hasButton(line(m.root), "Import now")).toBe(false);
  await press(buttonIn(line(m.root), "Refresh"));
  expect(w.sent("POST", "/setup/scan")).toHaveLength(2); // the one as the page opened, and this
  // Core has no record of the import: it did not run, and a new preview can start.
  expect(line(m.root).textContent).not.toContain("not known whether the import ran");
  expect(line(m.root).textContent).toContain("The import did not run: pluxy has no record of it.");
  expect(hasButton(line(m.root), "Preview import")).toBe(true);
});

test("after a lost reply to Import now, Refresh shows core's record when the import ran", async () => {
  const w = importing((dry) => {
    if (dry) return { status: 200, body: REPORT };
    // Core ran it and kept the record; the reply was lost on the way.
    w.state.setup.found.imports = [
      { ...IMPORT, last_import: { at: "2026-11-20T10:30:00Z", brought: 2, changed: 1, not_brought: 1 } },
    ];
    return { status: 0 };
  });
  const m = await w.mount(<SetupPage />);
  await press(buttonIn(line(m.root), "Preview import"));
  await press(buttonIn(line(m.root), "Import now"));
  await press(buttonIn(line(m.root), "Refresh"));
  expect(line(m.root).textContent).not.toContain("not known whether the import ran");
  expect(line(m.root).textContent).toContain("Imported a CLIProxyAPI setup");
  expect(attribute(line(m.root), "data-state")).toBe("imported");
});

test("a preview is dropped when the auth folder changes under it: Import now cannot send the old one", async () => {
  const w = importing();
  const m = await w.mount(<SetupPage />);
  await press(buttonIn(line(m.root), "Preview import"));
  expect(hasButton(line(m.root), "Import now")).toBe(true);
  // A new scan finds the same config naming another auth folder.
  w.state.setup.found.imports = [{ ...IMPORT, auth_dir: "~/elsewhere/auths" }];
  await press(buttonIn(step(m.root, 2), "Open"));
  await press(buttonIn(step(m.root, 2), "Refresh"));
  expect(hasButton(line(m.root), "Import now")).toBe(false);
  expect(withPart(m.root, "setup-report")).toHaveLength(0);
  expect(imports(w)).toEqual([true]);
  // The next preview, and the import from it, name the new folder.
  await press(buttonIn(line(m.root), "Preview import"));
  await press(buttonIn(line(m.root), "Import now"));
  expect(w.sent("POST", "/setup/import").map((c) => (c.body as { auth_dir?: string }).auth_dir)).toEqual([
    "~/.cli-proxy-api/auths",
    "~/elsewhere/auths",
    "~/elsewhere/auths",
  ]);
});

test("a background scan finishing during or after a refused preview does not drop the refusal alert", async () => {
  let finishImport:
    | ((reply: { status: number; error: { code: string; message: string } }) => void)
    | undefined;
  let scanCount = 0;
  const w = world({
    imports: [IMPORT],
    reply: (c) => {
      if (c.path === "/setup/scan") {
        scanCount++;
        w.state.setup.scanned_at = `2026-11-20T10:0${scanCount}:00Z`;
        return { status: 200, body: structuredClone(w.state.setup) };
      }
      if (c.path === "/setup/import") {
        return new Promise((resolve) => {
          finishImport = resolve;
        });
      }
      return undefined;
    },
  });
  const m = await w.mount(<SetupPage />);
  // Start Preview import; while it is in flight, a background scan reads the broken config
  // (falling back to 0 accounts and a newer scanned_at).
  await press(buttonIn(line(m.root), "Preview import"));
  w.state.setup.found.imports = [{ ...IMPORT, auth_dir: "~/.cli-proxy-api", accounts: 0 }];
  fakeWindow().dispatch("focus");
  await flush();
  finishImport?.({ status: 422, error: { code: "refused", message: "bad yaml" } });
  await flush();
  expect(line(m.root).textContent).toContain("This setup cannot be imported: bad yaml. Nothing changed.");
  // Another background scan afterwards still keeps the refusal visible.
  fakeWindow().dispatch("focus");
  await flush();
  expect(line(m.root).textContent).toContain("This setup cannot be imported: bad yaml. Nothing changed.");
});

test("a background scan that finds the same setup does not drop an open preview or completed import", async () => {
  let scanCount = 0;
  const w = world({
    imports: [IMPORT],
    reply: (c) => {
      if (c.path === "/setup/scan") {
        scanCount++;
        w.state.setup.scanned_at = `2026-11-20T10:0${scanCount}:00Z`;
        return { status: 200, body: structuredClone(w.state.setup) };
      }
      if (c.path === "/setup/import") {
        const dry = (c.body as { dry_run: boolean }).dry_run;
        if (!dry) {
          w.state.setup.found.imports = [
            {
              ...IMPORT,
              last_import: { at: "2026-11-20T10:30:00Z", brought: 2, changed: 1, not_brought: 1 },
            },
          ];
        }
        return { status: 200, body: { ...REPORT, dry_run: dry } };
      }
      return undefined;
    },
  });
  const m = await w.mount(<SetupPage />);
  await press(buttonIn(line(m.root), "Preview import"));
  expect(hasButton(line(m.root), "Import now")).toBe(true);
  // A focus scan returns the same config, auth folder and account count with a newer scanned_at.
  fakeWindow().dispatch("focus");
  await flush();
  expect(hasButton(line(m.root), "Import now")).toBe(true);
  expect(withPart(m.root, "setup-report")).toHaveLength(1);
  // Once imported, a later scan still keeps the completion report until Close.
  await press(buttonIn(line(m.root), "Import now"));
  expect(line(m.root).textContent).toContain("Imported.");
  fakeWindow().dispatch("focus");
  await flush();
  expect(line(m.root).textContent).toContain("Imported.");
  expect(hasButton(line(m.root), "Close")).toBe(true);
});

test("after a lost Import now reply, an unchanged background scan keeps the warning and a new record clears it", async () => {
  let scanCount = 0;
  const w = world({
    imports: [IMPORT],
    reply: (c) => {
      if (c.path === "/setup/scan") {
        scanCount++;
        w.state.setup.scanned_at = `2026-11-20T10:0${scanCount}:00Z`;
        return { status: 200, body: structuredClone(w.state.setup) };
      }
      if (c.path === "/setup/import") {
        const dry = (c.body as { dry_run: boolean }).dry_run;
        return dry ? { status: 200, body: REPORT } : { status: 0 };
      }
      return undefined;
    },
  });
  const m = await w.mount(<SetupPage />);
  await press(buttonIn(line(m.root), "Preview import"));
  await press(buttonIn(line(m.root), "Import now"));
  expect(line(m.root).textContent).toContain("not known whether the import ran");
  // Background scan with no new record: keep the warning.
  fakeWindow().dispatch("focus");
  await flush();
  expect(line(m.root).textContent).toContain("not known whether the import ran");
  // Background scan finds a new import record: clear the warning and show Imported.
  w.state.setup.found.imports = [
    { ...IMPORT, last_import: { at: "2026-11-20T10:30:00Z", brought: 2, changed: 1, not_brought: 1 } },
  ];
  fakeWindow().dispatch("focus");
  await flush();
  expect(line(m.root).textContent).not.toContain("not known whether the import ran");
  expect(line(m.root).textContent).toContain("Imported a CLIProxyAPI setup");
});

test("an import that needs a kind this pluxy does not offer says so by name, with Open Extensions", async () => {
  const report = {
    ...REPORT,
    needs: [
      { pick: "pluxy/claude", why: "2 accounts need it" },
      { pick: "pluxy/antigravity", why: "1 account needs it, and pluxy does not ship it" },
    ],
  };
  const w = importing((dry) => ({ status: 200, body: { ...report, dry_run: dry } }));
  const m = await w.mount(<SetupPage />);
  await press(buttonIn(line(m.root), "Preview import"));
  await press(buttonIn(line(m.root), "Import now"));
  const l = line(m.root);
  expect(l.textContent).toContain("The imported accounts need Claude. They are ticked in step 1.");
  expect(l.textContent).toContain(
    "This pluxy does not offer Antigravity, so the accounts that need it cannot be used yet.",
  );
  expect(attribute(linkIn(l, "Open Extensions"), "href")).toBe("#/extensions");
  expect(m.text()).not.toContain("pluxy/antigravity");
  restoreDom();
  Object.defineProperty(globalThis, "location", { configurable: true, value: place });
  // Only kinds it does not offer: nothing is said to be ticked.
  const only = importing((dry) => ({
    status: 200,
    body: { ...report, needs: [report.needs[1]], dry_run: dry },
  }));
  const o = await only.mount(<SetupPage />);
  await press(buttonIn(line(o.root), "Preview import"));
  await press(buttonIn(line(o.root), "Import now"));
  expect(line(o.root).textContent).not.toContain("ticked in step 1");
  expect(line(o.root).textContent).toContain("This pluxy does not offer Antigravity");
});

test("with privacy mode on, the import report hides account file names", async () => {
  const report: ImportReport = {
    dry_run: true,
    brought: [{ what: "auth file", name: "claude-alice@example.com.json" }],
    changed: [],
    not_brought: [{ what: "auth file", name: "juniper_lab.json", why: "juniper_lab has no type" }],
    needs: [],
  };
  const w = importing(() => ({ status: 200, body: report }));
  const m = await w.mount(
    <PrivacyContext.Provider value={privacyMasks(true)}>
      <SetupPage />
    </PrivacyContext.Provider>,
  );
  await press(buttonIn(line(m.root), "Preview import"));
  const text = must(withPart(m.root, "setup-report")[0]).textContent;
  expect(text).toContain("auth file");
  expect(text).not.toContain("alice");
  expect(text).not.toContain("juniper");
});

test("Try again after a 400 looks again, then starts a new preview of what the new scan found", async () => {
  let first = true;
  const w = importing(() => {
    if (!first) return { status: 200, body: REPORT };
    first = false;
    // The next scan finds the config naming another auth folder.
    w.state.setup.found.imports = [{ ...IMPORT, auth_dir: "~/fresh/auths" }];
    return { status: 400, error: { code: "invalid_request", message: "no file" } };
  });
  const m = await w.mount(<SetupPage />);
  await press(buttonIn(line(m.root), "Preview import"));
  await press(buttonIn(line(m.root), "Try again"));
  expect(w.sent("POST", "/setup/scan")).toHaveLength(2); // the one as the page opened, and this
  expect(imports(w)).toEqual([true, true]);
  expect(w.sent("POST", "/setup/import").map((c) => (c.body as { auth_dir?: string }).auth_dir)).toEqual([
    "~/.cli-proxy-api/auths",
    "~/fresh/auths",
  ]);
  expect(withPart(m.root, "setup-report")).toHaveLength(1);
});

test("a 401 from the import signs out", async () => {
  const w = importing(() => ({ status: 401, error: { code: "auth", message: "no" } }));
  const m = await w.mount(<SetupPage />);
  await press(buttonIn(line(m.root), "Preview import"));
  expect(w.signedOut.times).toBe(1);
});

test("an earlier import reads as done, and Import again starts a preview", async () => {
  const w = world({
    imports: [
      { ...IMPORT, last_import: { at: "2026-11-20T09:00:00Z", brought: 4, changed: 1, not_brought: 1 } },
    ],
  });
  const m = await w.mount(<SetupPage />);
  expect(line(m.root).textContent).toContain("Imported a CLIProxyAPI setup");
  await press(buttonIn(line(m.root), "Import again"));
  expect(imports(w)).toEqual([true]);
});

// ---- helpers -------------------------------------------------------------------------------------

test("kinds are grouped found, main, more; an import's needs count as found", () => {
  const setup = { picks: [...EIGHT, ...FIVE_TOOLS] } as never;
  const groups = kindGroups(setup, new Set(["pluxy/qwen"]));
  expect(groups.found.map((p) => p.id)).toEqual(["pluxy/codex", "pluxy/kimi", "pluxy/qwen"]);
  expect(groups.main.map((p) => p.id)).toEqual(["pluxy/claude", "pluxy/gemini", "pluxy/openai-compatible"]);
  expect(groups.more.map((p) => p.id)).toEqual(["pluxy/iflow", "pluxy/vertex"]);
  expect(orderedKinds(setup)).toHaveLength(8);
  expect(joinNames(["A"])).toBe("A");
  expect(joinNames(["A", "B", "C"])).toBe("A, B and C");
});

// ---- the words: which computer, and what was added ------------------------------------------------

test("what pluxy found says which computer when pluxy runs on another: step 1's tool and sign-in lines", async () => {
  const picks = [
    pick("pluxy/claude", "Claude", { suggested: true, because: "Claude Code is installed on this computer" }),
  ];
  const signIns = [
    { provider: "pluxy/claude", from: "~/.claude", label: "Claude Code is signed in on this computer" },
  ];
  const remote = await world({ picks, signIns, address: "http://10.0.0.5:8317" }).mount(<SetupPage />);
  expect(kindRow(remote.root, "Claude").textContent).toContain(
    "Claude Code is installed on the computer pluxy runs on",
  );
  expect(remote.text()).not.toContain("this computer");
  restoreDom();
  Object.defineProperty(globalThis, "location", { configurable: true, value: place });
  // The page on pluxy's own computer keeps "this computer".
  const here = await world({ picks, signIns }).mount(<SetupPage />);
  expect(kindRow(here.root, "Claude").textContent).toContain("Claude Code is installed on this computer");
});

test("a kind that only runs is not an account: the line says pluxy can connect to it, and the long kind name reads as a phrase", async () => {
  const api = pick("pluxy/openai-compatible", "OpenAI Compatible", { installed: true });
  const m = await world({ picks: [CLAUDE_PICK, api], running: ["pluxy/openai-compatible"] }).mount(
    <SetupPage />,
  );
  const one = step(m.root, 1);
  expect(one.textContent).toContain("pluxy can now connect to another service. Add your account below.");
  expect(one.textContent).not.toContain("Added");
  expect(one.textContent).not.toContain("connect to Another service with an API key");
});

test("the kinds button names the long kind as a phrase: Add Claude and another service", async () => {
  const w = world({
    picks: [CLAUDE_PICK, pick("pluxy/openai-compatible", "OpenAI Compatible")],
  });
  const m = await w.mount(<SetupPage />);
  await tick(must(box(kindRow(m.root, "Claude"))), true);
  await tick(must(box(kindRow(m.root, "Another service with an API key"))), true);
  expect(hasButton(m.root, "Add Claude and another service")).toBe(true);
  expect(m.text()).not.toContain("Add Claude and Another service");
});

// ---- step 2 follows the tool's settings --------------------------------------------------------

test("opening the page asks pluxy to look at the tools again, so a tool connected on another page shows at once", async () => {
  // Claude Code was connected on its own page after pluxy's last look: the kept scan says it is not.
  const w = world({
    accounts: [ACC],
    clients: [client("claude-code", "Claude Code")],
    reply: (c) => {
      if (c.path === "/setup/scan") {
        w.state.setup.found.clients = [client("claude-code", "Claude Code", { state: "configured" })];
      }
      return undefined;
    },
  });
  const m = await w.mount(<SetupPage />);
  expect(w.sent("POST", "/setup/scan")).toHaveLength(1);
  expect(step(m.root, 2).textContent).toContain("1 connected");
  expect(step(m.root, 2).textContent).not.toContain("0 connected");
});

test("step 2 reads the tools again when the person comes back to the page, so pasted settings show", async () => {
  const w = world({ accounts: [ACC], clients: [client("claude-code", "Claude Code")] });
  const m = await w.mount(<SetupPage />);
  expect(step(m.root, 2).textContent).toContain("0 connected");
  const before = w.sent("POST", "/setup/scan").length;
  // The settings are pasted into the tool's file; the person comes back to this window.
  w.state.setup.found.clients = [client("claude-code", "Claude Code", { state: "configured" })];
  fakeWindow().dispatch("focus");
  await tickTimers();
  expect(w.sent("POST", "/setup/scan").length).toBe(before + 1);
  expect(step(m.root, 2).textContent).toContain("1 connected");
  expect(step(m.root, 2).textContent).toContain("Connected");
  expect(step(m.root, 2).textContent).not.toContain("0 connected");
});

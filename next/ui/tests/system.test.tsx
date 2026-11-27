// The System page: which pluxy this is, the models its tools can ask for, and the addresses for
// other programs, against a fake core.
import { afterEach, expect, test } from "bun:test";
import { act } from "preact/test-utils";
import { api, type ServedModel, type Status } from "../kit/api";
import type { Host, Method, Reply } from "../kit/host";
import { groupModels, programAddresses, SystemPage } from "../kit/pages/system";
import { PrivacyContext, privacyMasks } from "../kit/privacy";
import { type Kit, KitContext } from "../kit/ui";
import { all, type FakeNode, mountIn, restoreDom, settle } from "./fakeDom";

const unmounts: (() => Promise<void>)[] = [];
afterEach(async () => {
  for (const unmount of unmounts.splice(0)) await unmount();
  restoreDom();
});

const COMMIT = "0123456789abcdef0123456789abcdef01234567";
const status = (more: Partial<Status> = {}): Status => ({
  safe_mode: false,
  desired: { revision: 3, at: "2026-11-25T22:00:00Z" },
  active: { revision: 3, at: "2026-11-25T22:00:00Z" },
  extensions: [],
  version: "0.1.0",
  commit: COMMIT,
  built: "2026-11-20T10:00:00Z",
  ...more,
});
const model = (id: string, extension: string, more: Partial<ServedModel> = {}): ServedModel => ({
  id,
  upstream: id,
  extension,
  accounts: ["acc_1"],
  ...more,
});
const MODELS = [
  model("claude-sonnet", "pluxy/claude"),
  model("sonnet", "pluxy/claude", { upstream: "claude-sonnet" }),
  model("gpt-5", "pluxy/codex", { accounts: ["alice@example.com"] }),
];

interface Init {
  status?: Reply<unknown>;
  models?: Reply<unknown>;
  address?: string | null;
  privacy?: boolean;
}

function world(init: Init = {}) {
  const calls: string[] = [];
  const copied: string[] = [];
  Object.defineProperty(globalThis, "navigator", {
    configurable: true,
    value: {
      clipboard: {
        writeText: async (text: string) => {
          copied.push(text);
        },
      },
    },
  });
  const host = {
    call: async (method: Method, path: string): Promise<Reply<unknown>> => {
      calls.push(`${method} ${path}`);
      if (path === "/status") return init.status ?? { status: 200, body: status() };
      if (path === "/models") return init.models ?? { status: 200, body: { models: MODELS } };
      if (path === "/accounts") return { status: 200, body: { accounts: [{ id: "acc_1" }] } };
      return { status: 404 };
    },
    signedIn: () => true,
    prefs: { get: () => null, set: () => {} },
    features: new Set(),
    address: init.address === null ? undefined : (init.address ?? "http://127.0.0.1:8317"),
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
        <PrivacyContext.Provider value={privacyMasks(init.privacy ?? false)}>
          <SystemPage />
        </PrivacyContext.Provider>
      </KitContext.Provider>,
    );
    unmounts.push(m.unmount);
    return m;
  };
  return { calls, copied, mount };
}

const attribute = (node: FakeNode, name: string) => node.attributes.find((a) => a.name === name)?.value;
const descend = (node: FakeNode): FakeNode[] => [node, ...node.childNodes.flatMap(descend)];
const card = (root: FakeNode, title: string) => {
  const found = all(root, "section").find((s) => attribute(s, "aria-label") === title);
  if (!found) throw new Error(`no section ${title}`);
  return found;
};
const buttonIn = (root: FakeNode, label: string, name?: string) => {
  const found = all(root, "button").find(
    (b) => b.textContent === label && (name === undefined || attribute(b, "aria-label") === name),
  );
  if (!found) throw new Error(`no button ${label} ${name ?? ""} in ${root.textContent.slice(0, 300)}`);
  return found;
};
const press = async (node: FakeNode) => {
  await act(async () => {
    node.dispatchEvent(new Event("click"));
  });
  await settle();
};
const notices = (root: FakeNode) =>
  descend(root)
    .filter((n) => attribute(n, "class")?.split(" ").includes("notice"))
    .map((n) => n.textContent);
const rows = (root: FakeNode) =>
  all(root, "tr")
    .filter((r) => attribute(r, "data-part") === "item")
    .map((r) =>
      all(r, "th")
        .concat(all(r, "td"))
        .map((c) => c.textContent),
    );

test("which pluxy this is: its version, its commit's first 12 characters, when it was built", async () => {
  const w = world();
  const m = await w.mount();
  const about = card(m.root, "About");
  const facts = all(about, "div").filter((d) => all(d, "dt").length === 1);
  expect(facts.map((f) => all(f, "dt")[0]?.textContent)).toEqual([
    "Version",
    "Built",
    "Address",
    "Settings file",
  ]);
  expect(about.textContent).toContain(`0.1.0 ${COMMIT.slice(0, 12)}`);
  expect(about.textContent).not.toContain(COMMIT.slice(0, 13));
  expect(all(about, "time")).toHaveLength(1);
  expect(about.textContent).toContain("http://127.0.0.1:8317");
  expect(about.textContent).toContain("pluxy is using it as it is saved.");
  expect(about.textContent).not.toContain("In use");
  // Connection status, config revision and safe mode are no longer rows.
  for (const gone of ["Connection status", "Config revision", "Safe mode", "Core version"])
    expect(m.text()).not.toContain(gone);
});

test("a settings file that is not in use says why", async () => {
  const w = world({
    status: {
      status: 200,
      body: status({
        desired: { revision: 4, at: "2026-11-25T22:05:00Z", error: "line 3: unknown key" },
      }),
    },
  });
  const m = await w.mount();
  expect(card(m.root, "About").textContent).toContain(
    "pluxy is not using the latest version of its settings file: line 3: unknown key. It keeps working with the version before.",
  );
  expect(card(m.root, "About").textContent).not.toContain("using it as it is saved");
});

test("safe mode has its own notice", async () => {
  const w = world({ status: { status: 200, body: status({ safe_mode: true }) } });
  const m = await w.mount();
  expect(notices(m.root)).toEqual([
    "pluxy was started in safe mode, so no extension runs and every request fails. Start pluxy again normally to leave it.",
  ]);
  const calm = await world().mount();
  expect(notices(calm.root)).toEqual([]);
});

test("the five addresses for other programs, for http://127.0.0.1:8317", async () => {
  expect(programAddresses("http://127.0.0.1:8317").map((a) => [a.name, a.url])).toEqual([
    ["OpenAI API", "http://127.0.0.1:8317/v1"],
    ["Anthropic (Claude) API", "http://127.0.0.1:8317"],
    ["Gemini API", "http://127.0.0.1:8317"],
    ["Codex with an API key", "http://127.0.0.1:8317/v1"],
    ["Codex signed in with ChatGPT", "http://127.0.0.1:8317/backend-api/codex"],
  ]);
  const w = world();
  const m = await w.mount();
  const addresses = card(m.root, "Addresses for other programs");
  expect(rows(addresses)).toEqual([
    ["OpenAI API", "http://127.0.0.1:8317/v1", "Copy"],
    ["Anthropic (Claude) API", "http://127.0.0.1:8317", "Copy"],
    ["Gemini API", "http://127.0.0.1:8317", "Copy"],
    ["Codex with an API key", "http://127.0.0.1:8317/v1", "Copy"],
    ["Codex signed in with ChatGPT", "http://127.0.0.1:8317/backend-api/codex", "Copy"],
  ]);
  expect(addresses.textContent).toContain(
    "Coding tools are set up in Tools. Other programs take the base URL for the API they speak and a key from Tools.",
  );
  const tools = all(addresses, "a").find((a) => a.textContent === "Tools");
  expect(attribute(tools as FakeNode, "href")).toBe("#/tools");
  // A base URL is cut with an ellipsis when narrow, and its title holds the whole.
  const url = descend(addresses).find((n) => attribute(n, "class")?.includes("system-url"));
  expect(attribute(url as FakeNode, "title")).toBe("http://127.0.0.1:8317/v1");
  expect(m.text()).not.toContain("chatgpt_base_url");
});

test("without an address the page says it cannot tell, and shows no rows", async () => {
  const w = world({ address: null });
  const m = await w.mount();
  const addresses = card(m.root, "Addresses for other programs");
  expect(rows(addresses)).toEqual([]);
  expect(notices(addresses)).toEqual([
    "This page cannot tell pluxy's address. Tools shows the address pluxy listens on.",
  ]);
  expect(card(m.root, "About").textContent).not.toContain("Address");
});

test("Copy puts an address, or a model's name, on the clipboard and says Copied", async () => {
  const w = world();
  const m = await w.mount();
  const addresses = card(m.root, "Addresses for other programs");
  const copy = buttonIn(addresses, "Copy", "Copy http://127.0.0.1:8317/backend-api/codex");
  await press(copy);
  expect(w.copied).toEqual(["http://127.0.0.1:8317/backend-api/codex"]);
  expect(copy.textContent).toBe("Copied");
  await press(buttonIn(m.root, "Copy", "Copy gpt-5"));
  expect(w.copied).toEqual(["http://127.0.0.1:8317/backend-api/codex", "gpt-5"]);
});

test("models are grouped by provider, each with Copy, its other names and its accounts", async () => {
  const w = world();
  const m = await w.mount();
  const models = card(m.root, "Models your tools can ask for");
  expect(models.textContent).toContain("3 models");
  const groups = all(models, "section").filter((s) => attribute(s, "data-part") === "model-group");
  expect(groups.map((g) => attribute(g, "aria-label"))).toEqual(["Claude", "Codex"]);
  const claude = groups[0] as FakeNode;
  expect(claude.textContent).toContain("pluxy/claude");
  expect(claude.textContent).toContain("1 model");
  expect(rows(claude)).toEqual([["claude-sonnetCopy", "sonnet", "acc_1"]]);
  expect(rows(groups[1] as FakeNode)).toEqual([["gpt-5Copy", "", "alice@example.com"]]);
});

test("with names hidden the accounts of a model are masked", async () => {
  const w = world({ privacy: true });
  const m = await w.mount();
  expect(m.text()).not.toContain("alice@example.com");
});

test("groupModels folds the names served for one upstream model into one row", () => {
  expect(groupModels(MODELS)).toEqual([
    { extension: "pluxy/claude", rows: [{ id: "claude-sonnet", aliases: ["sonnet"], accounts: ["acc_1"] }] },
    { extension: "pluxy/codex", rows: [{ id: "gpt-5", aliases: [], accounts: ["alice@example.com"] }] },
  ]);
});

test("with no model it says so, and leads to Accounts and Extensions", async () => {
  const w = world({ models: { status: 200, body: { models: [] } } });
  const m = await w.mount();
  const models = card(m.root, "Models your tools can ask for");
  expect(models.textContent).toContain("No models yet");
  expect(models.textContent).toContain(
    "Models appear once an account is added and its provider's extension runs.",
  );
  const links = all(models, "a").map((a) => [a.textContent, attribute(a, "href")]);
  expect(links).toEqual([
    ["Open Accounts", "#/accounts"],
    ["Open Extensions", "#/extensions"],
  ]);
});

test("models that cannot be read say so, never that there are none, and Try again reads them", async () => {
  const failing = world({ models: { status: 500, error: { code: "internal", message: "no list today" } } });
  const m = await failing.mount();
  const models = card(m.root, "Models your tools can ask for");
  expect(models.textContent).toContain("Could not read the models");
  expect(models.textContent).not.toContain("No models yet");
  expect(models.textContent).not.toContain("0 models");
  const before = failing.calls.filter((c) => c === "GET /models").length;
  await press(buttonIn(models, "Try again"));
  expect(failing.calls.filter((c) => c === "GET /models").length).toBe(before + 1);
});

test("Refresh reads the status and the models again", async () => {
  const w = world();
  const m = await w.mount();
  expect(w.calls.filter((c) => c === "GET /status")).toHaveLength(1);
  await press(buttonIn(m.root, "Refresh"));
  expect(w.calls.filter((c) => c === "GET /status")).toHaveLength(2);
  expect(w.calls.filter((c) => c === "GET /models")).toHaveLength(2);
});

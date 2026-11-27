// Add an account and its sign-in (LoginFlow) through Preact's DOM renderer against a fake core:
// no ID asked for or sent, Cancel back to the methods, optional inputs under More options, the
// sentence for a different account, naming the account after it is added, and the embedded,
// onSuccess and providers props the guided setup builds on.
import { afterEach, expect, jest, test } from "bun:test";
import type { ComponentChildren } from "preact";
import { act } from "preact/test-utils";
import { type Account, api, type LoginMethod, type LoginMethodsExtension } from "../kit/api";
import type { Host, Method, Reply } from "../kit/host";
import { LoginFlow } from "../kit/loginFlow";
import { AddAccountPage, groupMethods } from "../kit/pages/addAccount";
import { PrivacyContext, privacyMasks } from "../kit/privacy";
import { type Kit, KitContext } from "../kit/ui";
import { all, type FakeNode, mountIn, restoreDom, settle } from "./fakeDom";

afterEach(() => {
  restoreDom();
  if (jest.isFakeTimers()) {
    jest.clearAllTimers();
    jest.useRealTimers();
  }
});

const apiKey: LoginMethod = {
  method: "api_key",
  kind: "api_key",
  label: "API key",
  inputs: [
    { name: "api_key", label: "API key", secret: true },
    { name: "base_url", label: "Base URL", optional: true },
  ],
};
const browser: LoginMethod = { method: "browser", kind: "browser", label: "Claude subscription" };
const device: LoginMethod = { method: "device", kind: "device", label: "ChatGPT sign-in" };
const EXTENSIONS: LoginMethodsExtension[] = [
  { extension: "pluxy/claude", methods: [browser, apiKey] },
  { extension: "pluxy/codex", methods: [device] },
  { extension: "pluxy/gemini", methods: [apiKey] },
  { extension: "pluxy/devin", methods: [] },
];

const ok = (body: unknown = {}): Reply<unknown> => ({ status: 200, body });
const failure = (status: number, code: string, message: string): Reply<unknown> => ({
  status,
  error: { code, message },
});
const login = (state: string, more: Record<string, unknown> = {}) => ({
  id: "login-1",
  account: "acc_1a2b3c4d",
  extension: "pluxy/claude",
  method: "browser",
  state,
  ...more,
});
const BROWSER_PROMPT = {
  kind: "browser",
  url: "https://claude.example/sign-in?x=1",
  expires_in: 600,
  inputs: [{ name: "redirect_url", label: "Address the browser ended on" }],
};
const account = (more: Partial<Account> = {}): Account => ({
  id: "acc_1a2b3c4d",
  source: "login",
  state: "active",
  refreshable: false,
  extension: "pluxy/claude",
  ...more,
});

interface Call {
  method: Method;
  path: string;
  body?: unknown;
}

/**
 * A fake core: the sign-in methods, the accounts, and what the sign-in calls answer. reply gives
 * an answer to any call first (undefined leaves it to the defaults).
 */
function world(
  init: {
    methods?: Reply<unknown>[];
    accounts?: Account[];
    reply?: (call: Call) => Reply<unknown> | Promise<Reply<unknown>> | undefined;
    privacy?: boolean;
  } = {},
) {
  const calls: Call[] = [];
  const methods = init.methods ?? [ok({ extensions: EXTENSIONS })];
  const host = {
    call: async (method: Method, path: string, body?: unknown): Promise<Reply<unknown>> => {
      const call = { method, path, body };
      calls.push(call);
      const given = init.reply?.(call);
      if (given) return given;
      if (method === "GET" && path === "/login-methods")
        return (methods.length > 1 ? methods.shift() : methods[0]) as Reply<unknown>;
      if (method === "GET" && path === "/accounts") return ok({ accounts: init.accounts ?? [] });
      if (method === "PATCH" && path.startsWith("/accounts/")) return ok({ account: account() });
      return { status: 204 };
    },
    signedIn: () => true,
    prefs: { get: () => null, set: () => {} },
    features: new Set(),
  } as unknown as Host;
  const kit = {
    host,
    api: api(host),
    themes: { resolved: { layout: { tabs: "top" } }, subscribe: () => () => {} },
    unauthorized: () => {},
    extensionsChanged: () => {},
  } as unknown as Kit;
  const wrap = (tree: ComponentChildren) => (
    <KitContext.Provider value={kit}>
      <PrivacyContext.Provider value={privacyMasks(init.privacy ?? false)}>{tree}</PrivacyContext.Provider>
    </KitContext.Provider>
  );
  return {
    calls,
    wrap,
    mount: (tree: ComponentChildren) => mountIn(wrap(tree)),
    posts: () => calls.filter((c) => c.method === "POST" && c.path === "/accounts"),
    changes: () => calls.filter((c) => c.method !== "GET").map((c) => `${c.method} ${c.path}`),
  };
}

async function click(node: FakeNode) {
  await act(async () => {
    node.dispatchEvent(new Event("click"));
  });
  await settle();
}

async function type(node: FakeNode, value: string) {
  node.value = value;
  await act(async () => {
    node.dispatchEvent(new Event("input"));
  });
}

async function submit(form: FakeNode) {
  await act(async () => {
    form.dispatchEvent(new Event("submit", { cancelable: true }));
  });
  await settle();
}

/** A promise settled by hand, for a call a test wants to hold in flight. */
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((r) => {
    resolve = r;
  });
  return { promise, resolve };
}

const attr = (node: FakeNode, name: string) => node.attributes.find((a) => a.name === name)?.value;
const buttons = (root: FakeNode) => all(root, "button");
const labelled = (root: FakeNode, text: string) => buttons(root).find((b) => b.textContent === text);
const must = <T,>(found: T | undefined, what: string): T => {
  if (found === undefined) throw new Error(`no ${what}`);
  return found;
};
const start = (root: FakeNode, method: string) =>
  must(
    buttons(root).find((b) => attr(b, "aria-label") === `Start: ${method}`),
    `Start button for ${method}`,
  );
/** The input a label names. */
const field = (root: FakeNode, label: string) =>
  must(
    all(root, "label")
      .find((l) => l.textContent.startsWith(label))
      ?.childNodes.flatMap((c) => all(c, "input"))[0],
    `input ${label}`,
  );
const advance = async (ms: number) => {
  await act(async () => {
    jest.advanceTimersByTime(ms);
  });
  await settle();
};

// ---- the page -----------------------------------------------------------------------------------

test("a provider is named as AccountRow names it, never by its package ID, and given providers show alone, in order", () => {
  const all3 = groupMethods(EXTENSIONS);
  expect(all3.map((g) => [g.extension, g.title, g.rows.map((r) => r.id)])).toEqual([
    ["pluxy/claude", "Claude", ["pluxy/claude/browser", "pluxy/claude/api_key"]],
    ["pluxy/codex", "Codex", ["pluxy/codex/device"]],
    ["pluxy/gemini", "Gemini", ["pluxy/gemini/api_key"]],
  ]);
  const two = groupMethods(EXTENSIONS, [
    { id: "pluxy/gemini" },
    { id: "pluxy/claude" },
    { id: "pluxy/missing" },
  ]);
  expect(two.map((g) => [g.extension, g.title])).toEqual([
    ["pluxy/gemini", "Gemini"],
    ["pluxy/claude", "Claude"],
  ]);
});

test("providers carry the guided setup's own names: a title replaces the plain name, in the given order", async () => {
  const ext = (extension: string, methods: LoginMethod[]) => ({ extension, methods });
  const extensions = [
    ext("pluxy/claude", [browser]),
    ext("pluxy/codex", [device]),
    ext("pluxy/openai-compatible", [apiKey]),
    ext("acme/custom", [apiKey]),
  ];
  const providers = [
    { id: "pluxy/openai-compatible", title: "Another service with an API key" },
    { id: "pluxy/codex", title: "ChatGPT (Codex)" },
    { id: "acme/custom", title: "Acme AI" },
    { id: "pluxy/claude" },
  ];
  expect(groupMethods(extensions, providers).map((g) => [g.extension, g.title])).toEqual([
    ["pluxy/openai-compatible", "Another service with an API key"],
    ["pluxy/codex", "ChatGPT (Codex)"],
    ["acme/custom", "Acme AI"],
    ["pluxy/claude", "Claude"],
  ]);
  const w = world({ methods: [ok({ extensions })] });
  const page = await w.mount(<AddAccountPage embedded providers={providers} />);
  const headings = all(page.root, "h2").map((h) => h.textContent);
  expect(headings).toEqual(["Another service with an API key", "ChatGPT (Codex)", "Acme AI", "Claude"]);
  expect(page.text()).not.toContain("OpenAI Compatible");
});

test("Add an account asks for no ID: each provider's ways to sign in are rows with Start", async () => {
  const w = world();
  const page = await w.mount(<AddAccountPage />);
  const text = page.text();
  expect(text).toContain("Add an account");
  expect(text).toContain("Choose how to add it.");
  for (const word of [
    "Claude",
    "Codex",
    "Gemini",
    "Opens your browser",
    "Shows a code to enter",
    "Uses an API key",
  ])
    expect(text).toContain(word);
  expect(text).not.toContain("pluxy/");
  expect(text).not.toMatch(/Account ID|acc_/);
  // Only the Import files… line has a file input; there is no text input.
  expect(all(page.root, "input").filter((i) => attr(i, "type") !== "file")).toEqual([]);
  expect(buttons(page.root).filter((b) => b.textContent === "Start")).toHaveLength(4);
});

test("sign-in methods: loading, failed with Try again, and no provider with Open Extensions", async () => {
  const slow = world({ methods: [ok()] });
  slow.calls.length = 0;
  // Never answers: the loading pane, not an empty one.
  const host = world({
    reply: (c) => (c.path === "/login-methods" ? (new Promise(() => {}) as never) : undefined),
  });
  const loading = await host.mount(<AddAccountPage />);
  expect(loading.text()).toContain("Loading sign-in methods…");
  expect(loading.text()).not.toContain("There is no provider");
  await loading.unmount();

  const broken = world({
    methods: [failure(502, "bad_gateway", "core hiccup"), ok({ extensions: EXTENSIONS })],
  });
  const failed = await broken.mount(<AddAccountPage />);
  expect(failed.text()).toContain("Could not read the sign-in methods.");
  await click(must(labelled(failed.root, "Try again"), "Try again"));
  expect(failed.text()).toContain("Choose how to add it.");

  const none = world({ methods: [ok({ extensions: [{ extension: "pluxy/devin", methods: [] }] })] });
  const empty = await none.mount(<AddAccountPage />);
  expect(empty.text()).toContain("There is no provider to sign in with yet.");
  expect(empty.text()).toContain("Add one in Extensions.");
  const link = all(empty.root, "a").find((a) => a.textContent === "Open Extensions");
  expect(attr(must(link, "Open Extensions"), "href")).toBe("#/extensions");
});

test("an API-key account is added with no id sent, and its sign-in form keeps the key secret", async () => {
  const w = world({
    reply: (c) =>
      c.method === "POST" && c.path === "/accounts"
        ? ok({ login: login("done", { method: "api_key" }) })
        : undefined,
  });
  const page = await w.mount(<AddAccountPage />);
  await click(start(page.root, "API key"));
  const key = field(page.root, "API key");
  expect(attr(key, "type")).toBe("password");
  // The fake DOM has no autoComplete property, so Preact writes the attribute under the name it was given.
  expect(attr(key, "autoComplete") ?? attr(key, "autocomplete")).toBe("new-password");
  expect(labelled(page.root, "Load from file")).toBeDefined();
  await type(key, "sk-test-1");
  await submit(must(all(page.root, "form")[0], "form"));
  const [post] = w.posts();
  expect(post?.body).toEqual({
    extension: "pluxy/claude",
    method: "api_key",
    input: { api_key: "sk-test-1" },
  });
  expect(post?.body).not.toHaveProperty("id");
});

test("optional inputs sit under More options; required ones do not", async () => {
  const w = world();
  const page = await w.mount(<AddAccountPage />);
  await click(start(page.root, "API key"));
  const more = must(all(page.root, "details")[0], "More options");
  expect(must(all(more, "summary")[0], "summary").textContent).toBe("More options");
  const inside = all(more, "input").map((i) => attr(i, "type"));
  expect(inside).toEqual(["text"]);
  expect(more.textContent).toContain("Base URL");
  expect(more.textContent).not.toContain("API key");
  // The secret key is asked first, outside the disclosure.
  expect(all(more, "input").includes(field(page.root, "API key"))).toBe(false);
  expect(attr(field(page.root, "Base URL"), "required")).toBeUndefined();
});

test("Cancel stops a browser sign-in and goes back to the methods at once", async () => {
  const w = world({
    reply: (c) =>
      c.method === "POST" && c.path === "/accounts"
        ? ok({ login: login("pending", { prompt: BROWSER_PROMPT }) })
        : undefined,
  });
  const page = await w.mount(<AddAccountPage />);
  await click(start(page.root, "Claude subscription"));
  expect(page.text()).toContain("Waiting for sign-in…");
  expect(labelled(page.root, "Open sign-in page")).toBeDefined();
  expect(labelled(page.root, "Copy link")).toBeDefined();
  expect(page.text()).toContain("Address the browser ended on");
  expect(labelled(page.root, "Continue")).toBeDefined();
  await click(must(labelled(page.root, "Cancel"), "Cancel"));
  expect(w.changes()).toContain("DELETE /logins/login-1");
  // The methods are back, with no dead end between.
  expect(page.text()).toContain("Choose how to add it.");
  expect(page.text()).not.toContain("Sign-in cancelled");
  expect(buttons(page.root).filter((b) => b.textContent === "Start")).toHaveLength(4);
  expect(w.posts()).toHaveLength(1);
});

test("a refused Cancel says why and stays", async () => {
  const w = world({
    reply: (c) =>
      c.method === "POST" && c.path === "/accounts"
        ? ok({ login: login("pending", { prompt: BROWSER_PROMPT }) })
        : c.method === "DELETE"
          ? failure(400, "invalid_request", "Action refused")
          : undefined,
  });
  const page = await w.mount(<AddAccountPage />);
  await click(start(page.root, "Claude subscription"));
  await click(must(labelled(page.root, "Cancel"), "Cancel"));
  expect(page.text()).toContain("Action refused");
  expect(page.text()).not.toContain("Choose how to add it.");
});

test("a device sign-in shows its code large, with Copy code and Open sign-in page, and the time left", async () => {
  const w = world({
    reply: (c) =>
      c.method === "POST" && c.path === "/accounts"
        ? ok({
            login: login("pending", {
              method: "device",
              prompt: {
                kind: "device",
                user_code: "TEST-1234",
                verification_uri: "https://x.example/device",
                expires_in: 90,
              },
            }),
          })
        : undefined,
  });
  const page = await w.mount(<AddAccountPage />);
  await click(start(page.root, "ChatGPT sign-in"));
  const code = must(
    all(page.root, "strong").find((n) => attr(n, "data-part") === "device-code"),
    "code",
  );
  expect(code.textContent).toBe("TEST-1234");
  expect(labelled(page.root, "Copy code")).toBeDefined();
  expect(labelled(page.root, "Open sign-in page")).toBeDefined();
  expect(labelled(page.root, "Copy link")).toBeUndefined();
  expect(page.text()).toMatch(/Time left: 1:30/);
});

test("when the sign-in is done: Added, the name pluxy has, Save name, Open account and Add another", async () => {
  const w = world({
    accounts: [account({ label: "alice@example.com" })],
    reply: (c) => (c.method === "POST" && c.path === "/accounts" ? ok({ login: login("done") }) : undefined),
  });
  let added = "";
  const page = await w.mount(<AddAccountPage onSuccess={(id) => (added = id)} />);
  await click(start(page.root, "Claude subscription"));
  expect(added).toBe("acc_1a2b3c4d");
  expect(page.text()).toContain("Added alice@example.com.");
  const name = field(page.root, "Name");
  expect(name.value).toBe("alice@example.com");
  expect(labelled(page.root, "Save name")?.disabled).toBe(true);
  const open = all(page.root, "a").find((a) => a.textContent === "Open account");
  expect(attr(must(open, "Open account"), "href")).toBe("#/accounts/acc_1a2b3c4d");

  await type(name, "  Work Claude ");
  expect(labelled(page.root, "Save name")?.disabled).toBe(false);
  await submit(must(all(page.root, "form")[0], "form"));
  const patch = w.calls.find((c) => c.method === "PATCH");
  expect(patch).toEqual({ method: "PATCH", path: "/accounts/acc_1a2b3c4d", body: { label: "Work Claude" } });
  expect(page.text()).toContain("Saved.");
  expect(labelled(page.root, "Save name")?.disabled).toBe(true);

  await click(must(labelled(page.root, "Add another"), "Add another"));
  expect(page.text()).toContain("Choose how to add it.");
  expect(w.posts()).toHaveLength(1);
});

test("an API-key account has no name yet: the field is empty with an example, and a name is optional", async () => {
  const w = world({
    accounts: [account()],
    reply: (c) => (c.method === "POST" && c.path === "/accounts" ? ok({ login: login("done") }) : undefined),
  });
  const page = await w.mount(<AddAccountPage />);
  await click(start(page.root, "Claude subscription"));
  const name = field(page.root, "Name");
  expect(name.value).toBe("");
  expect(attr(name, "placeholder")).toBe("For example: Work OpenRouter");
  expect(page.text()).toContain("Added your Claude account.");
  expect(page.text()).not.toContain("acc_");
  // Nothing was typed, so nothing is saved, and the person may go on.
  expect(labelled(page.root, "Save name")?.disabled).toBe(true);
  expect(w.changes()).toEqual(["POST /accounts"]);
});

test("a name over 64 characters is refused before anything is sent; a name may be taken away", async () => {
  const w = world({
    accounts: [account({ label: "alice@example.com" })],
    reply: (c) => (c.method === "POST" && c.path === "/accounts" ? ok({ login: login("done") }) : undefined),
  });
  const page = await w.mount(<AddAccountPage />);
  await click(start(page.root, "Claude subscription"));
  await type(field(page.root, "Name"), "x".repeat(65));
  await submit(must(all(page.root, "form")[0], "form"));
  expect(page.text()).toContain("Use at most 64 characters.");
  expect(w.changes()).toEqual(["POST /accounts"]);
  await type(field(page.root, "Name"), "");
  await submit(must(all(page.root, "form")[0], "form"));
  expect(w.calls.find((c) => c.method === "PATCH")?.body).toEqual({ label: "" });
});

test("a name core refuses is said, masked in privacy mode", async () => {
  const w = world({
    privacy: true,
    accounts: [account({ label: "alice@example.com" })],
    reply: (c) =>
      c.method === "POST" && c.path === "/accounts"
        ? ok({ login: login("done") })
        : c.method === "PATCH"
          ? failure(409, "conflict", "alice@example.com is taken")
          : undefined,
  });
  const page = await w.mount(<AddAccountPage />);
  await click(start(page.root, "Claude subscription"));
  expect(page.text()).not.toContain("alice");
  await type(field(page.root, "Name"), "Work");
  await submit(must(all(page.root, "form")[0], "form"));
  expect(page.text()).not.toContain("alice");
  expect(page.text()).toContain("is taken");
});

test("embedded: no page title, no Open account and no CLIProxyAPI import; providers narrows the ways", async () => {
  const w = world({
    accounts: [account({ label: "alice@example.com" })],
    reply: (c) => (c.method === "POST" && c.path === "/accounts" ? ok({ login: login("done") }) : undefined),
  });
  const page = await w.mount(<AddAccountPage embedded providers={[{ id: "pluxy/gemini" }]} />);
  expect(page.text()).not.toContain("Add an account");
  expect(page.text()).not.toContain("Have auth files from CLIProxyAPI?");
  expect(page.text()).toContain("Gemini");
  expect(page.text()).not.toContain("Claude");
  expect(page.text()).not.toContain("Codex");
  await click(start(page.root, "API key"));
  await type(field(page.root, "API key"), "k");
  await submit(must(all(page.root, "form")[0], "form"));
  expect(page.text()).toContain("Added");
  expect(page.text()).not.toContain("Open account");
  expect(labelled(page.root, "Add another")).toBeDefined();
});

test("the page without embedded ends with the CLIProxyAPI import, quiet", async () => {
  const w = world();
  const page = await w.mount(<AddAccountPage />);
  expect(page.text()).toContain("Have auth files from CLIProxyAPI?");
  expect(attr(must(labelled(page.root, "Import files…"), "Import files…"), "data-variant")).toBe("quiet");
});

test("with privacy on the Name field starts empty, shows the name only masked as its example, and never saves the mask", async () => {
  const w = world({
    privacy: true,
    accounts: [account({ label: "alice@example.com" })],
    reply: (c) => (c.method === "POST" && c.path === "/accounts" ? ok({ login: login("done") }) : undefined),
  });
  const page = await w.mount(<AddAccountPage />);
  await click(start(page.root, "Claude subscription"));
  const name = field(page.root, "Name");
  expect(page.text()).not.toContain("alice");
  expect(name.value).toBe("");
  expect(attr(name, "placeholder")).toBe("al***@e***.com");
  // Nothing typed: nothing to save, also after typing and clearing again.
  expect(labelled(page.root, "Save name")?.disabled).toBe(true);
  await type(name, "x");
  expect(labelled(page.root, "Save name")?.disabled).toBe(false);
  await type(name, "");
  expect(labelled(page.root, "Save name")?.disabled).toBe(true);
  await type(name, "Work Claude");
  expect(labelled(page.root, "Save name")?.disabled).toBe(false);
  await submit(must(all(page.root, "form")[0], "form"));
  const patch = w.calls.find((c) => c.method === "PATCH");
  expect(patch?.body).toEqual({ label: "Work Claude" });
  expect(JSON.stringify(patch?.body)).not.toContain("***");
});

test("the guided setup's provider title names an unnamed account: Added your {title} account", async () => {
  const w = world({
    accounts: [account({ extension: "pluxy/codex" })],
    reply: (c) =>
      c.method === "POST" && c.path === "/accounts"
        ? ok({ login: login("done", { extension: "pluxy/codex", method: "device" }) })
        : undefined,
  });
  const page = await w.mount(
    <AddAccountPage embedded providers={[{ id: "pluxy/codex", title: "ChatGPT (Codex)" }]} />,
  );
  await click(start(page.root, "ChatGPT sign-in"));
  expect(page.text()).toContain("Added your ChatGPT (Codex) account.");
});

test("Cancel is there while the sign-in starts, leaves at once, and cancels the login when it arrives", async () => {
  const start1 = deferred<Reply<unknown>>();
  const w = world({
    reply: (c) => (c.method === "POST" && c.path === "/accounts" ? start1.promise : undefined),
  });
  const page = await w.mount(<AddAccountPage />);
  await click(start(page.root, "Claude subscription"));
  expect(page.text()).toContain("Generating sign-in link…");
  // The first request is still out, and Cancel is already there.
  await click(must(labelled(page.root, "Cancel"), "Cancel while starting"));
  expect(page.text()).toContain("Choose how to add it.");
  expect(w.changes()).toEqual(["POST /accounts"]);
  // The login it began is cancelled when core answers.
  start1.resolve(ok({ login: login("pending", { prompt: BROWSER_PROMPT }) }));
  await settle();
  expect(w.changes()).toEqual(["POST /accounts", "DELETE /logins/login-1"]);
  expect(page.text()).toContain("Choose how to add it.");
});

test("Cancel says Cancelling… and cannot be pressed again while the request to cancel is out", async () => {
  const gone = deferred<Reply<unknown>>();
  const w = world({
    reply: (c) =>
      c.method === "POST" && c.path === "/accounts"
        ? ok({ login: login("pending", { prompt: BROWSER_PROMPT }) })
        : c.method === "DELETE"
          ? gone.promise
          : c.method === "GET" && c.path === "/logins/login-1"
            ? ok({ login: login("cancelled") })
            : undefined,
  });
  const page = await w.mount(<AddAccountPage />);
  await click(start(page.root, "Claude subscription"));
  await click(must(labelled(page.root, "Cancel"), "Cancel"));
  const waiting = must(labelled(page.root, "Cancelling…"), "Cancelling…");
  expect(waiting.disabled).toBe(true);
  expect(labelled(page.root, "Cancel")).toBeUndefined();
  expect(page.text()).not.toContain("Choose how to add it.");
  gone.resolve({ status: 204 });
  await settle();
  expect(page.text()).toContain("Choose how to add it.");
  expect(w.changes().filter((c) => c.startsWith("DELETE"))).toHaveLength(1);
});

test("a sign-in that finished before Cancel reached core goes to the done screen, not the methods", async () => {
  const w = world({
    accounts: [account({ label: "alice@example.com" })],
    reply: (c) =>
      c.method === "POST" && c.path === "/accounts"
        ? ok({ login: login("pending", { prompt: BROWSER_PROMPT }) })
        : c.method === "GET" && c.path === "/logins/login-1"
          ? ok({ login: login("done") })
          : undefined,
  });
  let added = "";
  const page = await w.mount(<AddAccountPage onSuccess={(id) => (added = id)} />);
  await click(start(page.root, "Claude subscription"));
  await click(must(labelled(page.root, "Cancel"), "Cancel"));
  expect(w.changes()).toContain("DELETE /logins/login-1");
  expect(added).toBe("acc_1a2b3c4d");
  expect(page.text()).toContain("Added alice@example.com.");
  expect(page.text()).not.toContain("Choose how to add it.");
  expect(labelled(page.root, "Add another")).toBeDefined();
});

test("Load from file puts text in the field and refuses a file that is not text, before it reaches the field", async () => {
  const w = world();
  const page = await w.mount(<AddAccountPage />);
  await click(start(page.root, "API key"));
  const picker = all(page.root, "input").find(
    (i) => attr(i, "type") === "file" && attr(i, "aria-label") === "Load from file",
  );
  const choose = async (bytes: number[]) => {
    const input = must(picker, "file input") as FakeNode & { files?: unknown[] };
    const buffer = new Uint8Array(bytes).buffer;
    input.files = [
      {
        size: bytes.length,
        arrayBuffer: async () => buffer,
        text: async () => new TextDecoder().decode(buffer),
      },
    ];
    await act(async () => {
      input.dispatchEvent(new Event("change"));
    });
    await settle();
  };
  await choose([...new TextEncoder().encode("sk-from-file\n")]);
  expect(field(page.root, "API key").value).toBe("sk-from-file\n");
  // A PNG header: bytes that are not UTF-8.
  await choose([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0xff, 0xfe]);
  expect(page.text()).toContain("That file is not text.");
  expect(field(page.root, "API key").value).toBe("sk-from-file\n");
  // Bytes that decode but hold a NUL are binary too.
  await choose([0x50, 0x4b, 0x00, 0x00, 0x01]);
  expect(page.text()).toContain("That file is not text.");
  expect(field(page.root, "API key").value).toBe("sk-from-file\n");
});

// ---- LoginFlow ----------------------------------------------------------------------------------

function flow(w: ReturnType<typeof world>, props: Partial<Parameters<typeof LoginFlow>[0]> = {}) {
  const seen = { reset: 0, success: [] as string[] };
  return {
    seen,
    mount: () =>
      w.mount(
        <LoginFlow
          extension="pluxy/claude"
          method={browser}
          onSuccess={(id) => seen.success.push(id)}
          onReset={() => seen.reset++}
          {...props}
        />,
      ),
  };
}

test("signing in to an account again sends to that account, and its different-account failure has its own sentence", async () => {
  const core =
    'the login signed in to a different account (its email differs from account "acc_1a2b3c4d"\'s); to use the other account, add it as a new one';
  const w = world({
    reply: (c) =>
      c.path === "/accounts/acc_1a2b3c4d/login"
        ? ok({ login: login("pending", { prompt: BROWSER_PROMPT }) })
        : c.method === "GET" && c.path === "/logins/login-1"
          ? ok({ login: login("failed", { error: { code: "invalid_request", message: core } }) })
          : undefined,
  });
  jest.useFakeTimers();
  const f = flow(w, { existingAccountId: "acc_1a2b3c4d" });
  const page = await f.mount();
  expect(w.calls.map((c) => `${c.method} ${c.path}`)).toContain("POST /accounts/acc_1a2b3c4d/login");
  expect(w.posts()).toEqual([]);
  await advance(2000);
  expect(page.text()).toContain(
    "That was a different Claude account, so this one keeps its old sign-in. To use the other one, add it as a new account.",
  );
  expect(page.text()).not.toContain("Sign-in did not finish");
  // Core's own words are under Details.
  const details = must(all(page.root, "details")[0], "Details");
  expect(must(all(details, "summary")[0], "summary").textContent).toBe("Details");
  expect(details.textContent).toContain("the login signed in to a different account");
  await click(must(labelled(page.root, "Try again"), "Try again"));
  expect(f.seen.reset).toBe(1);
});

test("another failure reads Sign-in did not finish, with core's reason", async () => {
  const w = world({
    reply: (c) =>
      c.method === "POST" && c.path === "/accounts"
        ? ok({
            login: login("failed", {
              error: { code: "invalid_request", message: "the provider refused this sign-in" },
            }),
          })
        : undefined,
  });
  const page = await flow(w, { method: apiKey, extension: "pluxy/claude" }).mount();
  await type(field(page.root, "API key"), "bad");
  await submit(must(all(page.root, "form")[0], "form"));
  expect(page.text()).toContain("Sign-in did not finish: the provider refused this sign-in");
});

test("a sign-in already running for the account is said in plain words", async () => {
  const w = world({
    reply: (c) =>
      c.path === "/accounts/acc_1a2b3c4d/login"
        ? failure(
            409,
            "conflict",
            'conflict: a login for account "acc_1a2b3c4d" is already running (login-3)',
          )
        : undefined,
  });
  const page = await flow(w, { existingAccountId: "acc_1a2b3c4d" }).mount();
  expect(page.text()).toContain("A sign-in for this account is already running.");
  expect(page.text()).not.toContain("conflict");
});

test("three failed checks in a row stop the sign-in, in the words of the spec", async () => {
  const w = world({
    reply: (c) =>
      c.method === "POST" && c.path === "/accounts"
        ? ok({ login: login("pending", { prompt: BROWSER_PROMPT }) })
        : c.method === "GET" && c.path === "/logins/login-1"
          ? failure(503, "unavailable", "Login unavailable")
          : undefined,
  });
  jest.useFakeTimers();
  const page = await flow(w).mount();
  await advance(2000);
  await advance(4000);
  await advance(8000);
  expect(page.text()).toContain("Could not check the sign-in three times in a row. Login unavailable");
  expect(labelled(page.root, "Try again")).toBeDefined();
});

test("with privacy on, the different-account Details hide the account's ID and e-mail address", async () => {
  const core =
    'the login signed in to a different account (its email differs from account "acc_1a2b3c4d"\'s); to use the other account, add it as a new one';
  const w = world({
    privacy: true,
    reply: (c) =>
      c.path === "/accounts/acc_1a2b3c4d/login"
        ? ok({ login: login("pending", { prompt: BROWSER_PROMPT }) })
        : c.method === "GET" && c.path === "/logins/login-1"
          ? ok({ login: login("failed", { error: { code: "invalid_request", message: core } }) })
          : undefined,
  });
  jest.useFakeTimers();
  const f = flow(w, { existingAccountId: "acc_1a2b3c4d", identities: ["alice@example.com"] });
  const page = await f.mount();
  await advance(2000);
  const details = must(all(page.root, "details")[0], "Details");
  expect(details.textContent).toContain("the login signed in to a different account");
  expect(page.text()).not.toContain("acc_1a2b3c4d");
  expect(page.text()).toContain("ac***4d");
});

test("with privacy on, another failure's reason hides the ID of the account that was just added", async () => {
  const w = world({
    privacy: true,
    reply: (c) =>
      c.method === "POST" && c.path === "/accounts"
        ? ok({
            login: login("failed", {
              error: { code: "invalid_request", message: 'account "acc_1a2b3c4d" refused it' },
            }),
          })
        : undefined,
  });
  const page = await flow(w, { method: apiKey }).mount();
  await type(field(page.root, "API key"), "bad");
  await submit(must(all(page.root, "form")[0], "form"));
  expect(page.text()).toContain("Sign-in did not finish");
  expect(page.text()).not.toContain("acc_1a2b3c4d");
});

// ---- what a new account came to: the check, the name, the sentence ---------------------------------

const loginDone = (c: Call) =>
  c.method === "POST" && c.path === "/accounts" ? ok({ login: login("done") }) : undefined;

test("a new account says pluxy checked it with the service, once the check has passed", async () => {
  const w = world({
    accounts: [account({ check: { state: "ok", detail: "the upstream lists 2 models" } })],
    reply: loginDone,
  });
  const page = await w.mount(<AddAccountPage />);
  await click(start(page.root, "Claude subscription"));
  expect(page.text()).toContain("pluxy checked it with the service, and it works.");
  expect(page.text()).not.toContain("upstream");
});

test("a new account whose check failed says why, and one still being checked says so and asks again", async () => {
  const failed = world({
    accounts: [
      account({
        check: { state: "failed", error: { code: "unauthorized", message: "the service refused the key" } },
      }),
    ],
    reply: loginDone,
  });
  const page = await failed.mount(<AddAccountPage />);
  await click(start(page.root, "Claude subscription"));
  expect(page.text()).toContain("pluxy could not check it with the service: the service refused the key");
  expect(page.text()).not.toContain("and it works");
  restoreDom();

  jest.useFakeTimers();
  let asked = 0;
  const w = world({
    accounts: [account()],
    reply: (c) => {
      if (c.method === "GET" && c.path === "/accounts") {
        asked++;
        return ok({
          accounts: [account({ check: { state: asked < 3 ? "checking" : "ok" } })],
        });
      }
      return loginDone(c);
    },
  });
  const later = await w.mount(<AddAccountPage />);
  await click(start(later.root, "Claude subscription"));
  expect(later.text()).toContain("Checking it with the service…");
  expect(later.text()).not.toContain("and it works");
  // Each second the page asks again, until the check answers.
  await advance(1000);
  await advance(1000);
  await advance(1000);
  expect(later.text()).toContain("pluxy checked it with the service, and it works.");
  expect(later.text()).not.toContain("Checking it with the service…");
});

test("an account with an address and no name is offered the address as its name, which Save name saves", async () => {
  const w = world({
    accounts: [
      account({ extension: "pluxy/openai-compatible", settings: { base_url: "http://127.0.0.1:18500/v1" } }),
    ],
    reply: loginDone,
  });
  const page = await w.mount(<AddAccountPage />);
  await click(start(page.root, "Claude subscription"));
  const name = field(page.root, "Name");
  expect(name.value).toBe("127.0.0.1:18500");
  expect(labelled(page.root, "Save name")?.disabled).toBe(false);
  // Offered, not saved: nothing was sent until Save name.
  expect(w.changes()).toEqual(["POST /accounts"]);
  await submit(must(all(page.root, "form")[0], "form"));
  expect(w.calls.find((c) => c.method === "PATCH")?.body).toEqual({ label: "127.0.0.1:18500" });
});

test("the sentence for an unnamed account in a long-named kind reads as English: Added your account for another service", async () => {
  const w = world({
    accounts: [account({ extension: "pluxy/gemini" })],
    reply: (c) =>
      c.method === "POST" && c.path === "/accounts"
        ? ok({ login: login("done", { extension: "pluxy/gemini", method: "api_key" }) })
        : undefined,
  });
  const page = await w.mount(
    <AddAccountPage
      embedded
      providers={[
        {
          id: "pluxy/gemini",
          title: "Another service with an API key",
          account: "account for another service",
        },
      ]}
    />,
  );
  await click(start(page.root, "API key"));
  await type(field(page.root, "API key"), "k");
  await submit(must(all(page.root, "form")[0], "form"));
  expect(page.text()).toContain("Added your account for another service.");
  expect(page.text()).not.toContain("Another service with an API key account");
});

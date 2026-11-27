// A fake core for the Setup page's tests: what GET /setup found and offers, the packages, the
// accounts and their sign-in, the import, the test request and finishing, each answer changeable
// by the test. The page is driven through Preact's DOM renderer and a small fake DOM (fakeDom.ts).
import type { ComponentChildren } from "preact";
import { act } from "preact/test-utils";
import {
  type Account,
  api,
  type Package,
  type Setup,
  type SetupClient,
  type SetupImport,
  type SetupPick,
  type SetupSignIn,
} from "../kit/api";
import type { Feature, Host, Method, Reply } from "../kit/host";
import { type Kit, KitContext } from "../kit/ui";
import { all, type FakeNode, mountIn, settle } from "./fakeDom";

export interface Call {
  method: Method;
  path: string;
  body?: unknown;
}

export const SCANNED = "2026-11-20T10:00:00Z";

export const pick = (id: string, title: string, more: Partial<SetupPick> = {}): SetupPick => ({
  id,
  kind: "provider",
  title,
  summary: `${title} accounts.`,
  installed: false,
  suggested: false,
  ...more,
});

export const client = (id: string, name: string, more: Partial<SetupClient> = {}): SetupClient => ({
  id,
  name,
  installed: true,
  version: "2.4.1",
  config: `~/.${id}/settings.json`,
  state: "not-configured",
  ...more,
});

export const account = (id: string, more: Partial<Account> = {}): Account => ({
  id,
  source: "login",
  state: "active",
  refreshable: false,
  extension: "pluxy/claude",
  ...more,
});

export const CLAUDE_PICK = pick("pluxy/claude", "Claude", {
  summary: "Claude models with a Claude subscription or an Anthropic API key.",
});
export const CODEX_PICK = pick("pluxy/codex", "Codex");
export const CLAUDE_CODE = client("claude-code", "Claude Code");

export const IMPORT: SetupImport = {
  kind: "cliproxyapi",
  config: "~/.cli-proxy-api/config.yaml",
  auth_dir: "~/.cli-proxy-api/auths",
  accounts: 3,
};

export interface WorldInit {
  firstRun?: boolean;
  /** No scan kept yet (scanned_at absent). */
  unscanned?: boolean;
  scanning?: boolean;
  picks?: SetupPick[];
  clients?: SetupClient[];
  signIns?: SetupSignIn[];
  imports?: SetupImport[];
  registries?: number;
  reachable?: boolean;
  accounts?: Account[];
  packages?: Package[];
  /** Extensions core runs, for GET /status; by default every installed pick's. */
  running?: string[];
  /** Login methods by extension; by default one browser method for each provider pick. */
  methods?: { extension: string; methods: { method: string; kind: string; label: string }[] }[];
  address?: string;
  /** The host's prefs at the start. */
  prefs?: Record<string, string>;
  /** An answer to any call first; undefined leaves it to the defaults. */
  reply?: (call: Call) => Reply<unknown> | Promise<Reply<unknown>> | undefined;
}

const ok = (body: unknown = {}): Reply<unknown> => ({ status: 200, body });

export function world(init: WorldInit = {}) {
  const calls: Call[] = [];
  const state = {
    setup: {
      first_run: init.firstRun ?? true,
      scanning: init.scanning ?? false,
      scanned_at: init.unscanned ? undefined : SCANNED,
      found: {
        clients: init.clients ?? [CLAUDE_CODE],
        sign_ins: init.signIns ?? [],
        imports: init.imports ?? [],
        core: { listen: "127.0.0.1:8317", reachable_from_network: init.reachable ?? false },
      },
      picks: init.picks ?? [CLAUDE_PICK, CODEX_PICK],
      registries: init.registries ?? 1,
    } as Setup,
    accounts: [...(init.accounts ?? [])],
    packages: [...(init.packages ?? [])],
    running: init.running,
    made: 0,
  };
  const running = () =>
    state.running ?? [
      ...state.accounts.map((a) => a.extension ?? ""),
      ...state.setup.picks.filter((p) => p.installed).map((p) => p.id),
      ...state.packages.filter((p) => p.enabled).map((p) => p.id),
    ];
  const prefs = new Map<string, string>(Object.entries(init.prefs ?? {}));
  const answer = async (call: Call): Promise<Reply<unknown>> => {
    calls.push(call);
    const given = await init.reply?.(call);
    if (given) return given;
    const { method, path } = call;
    if (method === "GET" && path === "/setup") return ok(structuredClone(state.setup));
    if (method === "POST" && path === "/setup/scan") {
      state.setup.scanned_at = SCANNED;
      state.setup.scanning = false;
      return ok(structuredClone(state.setup));
    }
    if (method === "POST" && path === "/setup/apply") {
      const ids = (call.body as { picks: string[] }).picks;
      for (const id of ids) {
        const p = state.setup.picks.find((x) => x.id === id);
        if (p) p.installed = true;
      }
      return ok({ results: ids.map((id) => ({ id, status: "done" })) });
    }
    if (method === "POST" && path === "/setup/done") {
      state.setup.first_run = false;
      return ok({ first_run: false });
    }
    if (method === "POST" && path === "/setup/import")
      return ok({ dry_run: true, brought: [], changed: [], not_brought: [], needs: [] });
    if (method === "GET" && path === "/packages") return ok({ packages: state.packages });
    if (method === "GET" && path === "/status") return ok({ extensions: running().map((id) => ({ id })) });
    if (method === "GET" && path === "/accounts") return ok({ accounts: structuredClone(state.accounts) });
    if (method === "GET" && path === "/login-methods") {
      return ok({
        extensions:
          init.methods ??
          state.setup.picks
            .filter((p) => p.kind === "provider")
            .map((p) => ({
              extension: p.id,
              methods: [{ method: "browser", kind: "browser", label: `${p.title} sign-in` }],
            })),
      });
    }
    if (method === "POST" && path === "/accounts") {
      const body = call.body as { extension: string };
      const id = `acc_${String(state.accounts.length + 1).padStart(8, "0")}`;
      state.accounts.push(account(id, { extension: body.extension }));
      return ok({
        login: { id: "login-1", account: id, extension: body.extension, method: "browser", state: "done" },
      });
    }
    if (method === "GET" && /^\/accounts\/[^/]+\/models$/.test(path)) {
      return ok({
        models: [
          { id: "model-a", names: null },
          { id: "model-b", names: null },
          { id: "hidden", names: null, excluded: true },
        ],
      });
    }
    if (method === "POST" && /^\/accounts\/[^/]+\/test$/.test(path)) {
      return ok({ result: { ok: true, model: "model-a", latency_ms: 420 } });
    }
    if (method === "GET" && path.startsWith("/setup/clients/")) {
      const id = path.split("/")[3] ?? "";
      return ok({
        tool: id,
        name: id,
        base_url: "http://127.0.0.1:8317",
        key: "<client-key>",
        files: [
          {
            path: `~/.${id}/settings.json`,
            content: `{"url": "http://127.0.0.1:8317", "key": "<client-key>"}`,
          },
        ],
      });
    }
    if (method === "POST" && path === "/client-keys") {
      state.made++;
      const label = (call.body as { label?: string } | undefined)?.label;
      return {
        status: 201,
        body: {
          key: { id: `key-${state.made}`, masked: "sk-***11", label },
          value: `sk-secret-${state.made}`,
        },
      };
    }
    return { status: 404, error: { code: "not_found", message: "no such call" } };
  };
  const host: Host = {
    call: async <T,>(method: Method, path: string, body?: unknown) =>
      (await answer({ method, path, body })) as Reply<T>,
    signedIn: () => true,
    signIn: async () => undefined,
    signOut: () => {},
    prefs: {
      get: (name) => prefs.get(name) ?? null,
      set: (name, value) => {
        if (value === null) prefs.delete(name);
        else prefs.set(name, value);
      },
    },
    features: new Set<Feature>(["signOut"]),
    address: init.address ?? "http://127.0.0.1:8317",
  };
  const signedOut = { times: 0 };
  const changed = { times: 0 };
  /** What the page told the shell about the first run, in order. */
  const firstRun: boolean[] = [];
  const kit = {
    host,
    api: api(host),
    unauthorized: () => {
      signedOut.times++;
    },
    extensionsChanged: () => {
      changed.times++;
    },
    setupFirstRun: (first: boolean) => {
      firstRun.push(first);
    },
  } as Kit;
  const mount = async (tree: ComponentChildren) => {
    const m = await mountIn(<KitContext.Provider value={kit}>{tree}</KitContext.Provider>);
    await flush();
    return m;
  };
  const sent = (method: Method, path: string) => calls.filter((c) => c.method === method && c.path === path);
  return { calls, state, signedOut, changed, firstRun, prefs, host, mount, sent };
}

// ---- reading the page ---------------------------------------------------------------------------

export const attribute = (node: FakeNode, name: string) =>
  node.attributes.find((a) => a.name === name)?.value;
export const descend = (node: FakeNode): FakeNode[] => [node, ...node.childNodes.flatMap(descend)];
export const withPart = (root: FakeNode, part: string) =>
  descend(root).filter((n) => attribute(n, "data-part") === part);

/** The row of steps on a first run, or undefined afterwards. */
const stepper = (root: FakeNode) => withPart(root, "setup-stepper")[0];

/**
 * The three steps, in order: on a first run the items of the row of steps, each with its state;
 * afterwards the checklist's rows.
 */
export const steps = (root: FakeNode) => {
  const row = stepper(root);
  return row ? all(row, "li") : withPart(root, "setup-step");
};

/** The steps' titles, in order, as the row of steps or the checklist shows them. */
export const stepTitles = (root: FakeNode) =>
  stepper(root)
    ? steps(root).map(
        (li) =>
          descend(li).find((n) => attribute(n, "class") === "setup-stepper-title")?.childNodes[0]
            ?.textContent,
      )
    : steps(root).map((s) => all(s, "h2")[0]?.childNodes[0]?.textContent);

/**
 * Step n's content: on a first run the step on screen (it must be step n), afterwards its row in
 * the checklist.
 */
export function step(root: FakeNode, n: number): FakeNode {
  if (stepper(root)) {
    const shown = withPart(root, "setup-step").find((s) => attribute(s, "data-step") === String(n));
    if (!shown) throw new Error(`step ${n} is not on screen`);
    return shown;
  }
  const found = steps(root)[n - 1];
  if (!found) throw new Error(`no step ${n}`);
  return found;
}

/** The number of the open step, or undefined. */
export const openStep = (root: FakeNode) => {
  const at = steps(root).findIndex((s) => attribute(s, "data-state") === "open");
  return at < 0 ? undefined : at + 1;
};

/** Opens step n as the person would: its item in the row of steps, or its row's Open. */
export async function goTo(root: FakeNode, n: number) {
  const row = stepper(root);
  if (row) {
    const item = all(row, "li")[n - 1];
    await press(must(item && all(item, "button")[0], `step ${n} in the row of steps`));
  } else await press(buttonIn(step(root, n), "Open"));
}

export const heading = (root: FakeNode, n: number) => {
  const h = all(step(root, n), "h2")[0];
  if (!h) throw new Error(`no heading in step ${n}`);
  return h;
};

export const buttons = (root: FakeNode) => all(root, "button");

export function buttonIn(root: FakeNode, label: string): FakeNode {
  const found = all(root, "button").find((b) => b.textContent === label);
  if (!found) throw new Error(`no button ${label} in ${root.textContent.slice(0, 300)}`);
  return found;
}

export const hasButton = (root: FakeNode, label: string) =>
  all(root, "button").some((b) => b.textContent === label);

export function linkIn(root: FakeNode, label: string): FakeNode {
  const found = all(root, "a").find((a) => a.textContent === label);
  if (!found) throw new Error(`no link ${label} in ${root.textContent.slice(0, 300)}`);
  return found;
}

/** value, or a thrown error naming what was missing. */
export function must<T>(value: T | undefined | null, what = "value"): T {
  if (value === undefined || value === null) throw new Error(`no ${what}`);
  return value;
}

/** Settles the fake core's answers and the reads they lead to, a few rounds deep. */
export async function flush() {
  for (let i = 0; i < 4; i++) await settle();
}

export async function press(node: FakeNode) {
  await act(async () => {
    node.dispatchEvent(new Event("click"));
  });
  await flush();
}

export async function choose(select: FakeNode, value: string) {
  select.value = value;
  await act(async () => {
    select.dispatchEvent(new Event("change"));
  });
  await flush();
}

export async function tick(input: FakeNode, checked: boolean) {
  input.checked = checked;
  await act(async () => {
    input.dispatchEvent(new Event("change"));
  });
  await flush();
}

/** The kind rows of step 1, by their shown names. */
export const kindRows = (root: FakeNode) => withPart(root, "setup-pick");
export const kindName = (row: FakeNode) => all(row, "strong")[0]?.textContent;
export function kindRow(root: FakeNode, name: string): FakeNode {
  const row = kindRows(root).find((r) => kindName(r) === name);
  if (!row) throw new Error(`no kind ${name}`);
  return row;
}
export const box = (row: FakeNode) => all(row, "input")[0];

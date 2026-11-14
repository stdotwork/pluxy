// Fixtures for the native test's check of cross-package routing and import authorization
// (src-tauri/src/routing_native.rs). Every native tool is the disposable fixture tool
// (src-tauri/examples/program_fixture, which run.ts builds) in its own signed package; the
// manifests differ only in what they declare:
//
// - pluxy/target, releases 1.0.0 and 2.0.0: exports summary and wait (read), store (write) and
//   launch (launch), export contract 1, with a page;
// - pluxy/relay: imports pluxy/target's summary, wait and launch (contract 1, required);
// - pluxy/needs-v2: imports pluxy/target contract 2, which no release exports;
// - pluxy/cycle-a and pluxy/cycle-b: each imports the other (optional);
// - pluxy/guide: a curated web package, like Easy mode, importing pluxy/target's summary, wait,
//   store and launch, with a page;
// - example/hub-viewer: the same imports (summary only), unsigned, from a registry no root names:
//   Unverified;
// - for the jobs check (src-tauri/src/jobs_native.rs), which runs after the routing check:
//   pluxy/jobs-hub consumes kind pluxy/jobs-hub/items (schema 1) through its deliver operation
//   take; pluxy/jobs-source, releases 1.0.0 and 1.1.0, produces it at schema 1 through scan;
//   pluxy/jobs-other produces it at schema 2, which the hub does not accept; pluxy/jobs-thief
//   consumes only its own kind; pluxy/jobs-dup produces one kind twice and pluxy/jobs-outside
//   consumes another package's kind: both are refused at install.
//
// The curated registry lists the pluxy packages, each signed with a publisher key generated for
// this run; the private key never leaves this process. expect.json holds what the check compares
// with, worked out here and not by the app: each archive's SHA-256, the summary's input and answer,
// and the launch the launch-class calls name.

import { spawnSync } from "node:child_process";
import { generateKeyPairSync, type KeyObject } from "node:crypto";
import { chmodSync, copyFileSync, mkdirSync, rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { artifact, type Fixture, platform, writeTrustProfile } from "./admission";

const ITEMS = ["alpha", "be", "gamma-delta"];
const object = { type: "object" };

/** The page: says hello; on its parent's {routeCall} sends that call on the channel, and passes
 * every channel reply back to its parent as {routeReply}. It tells its parent once it has the
 * channel ({routeReady}). */
export const PAGE_SCRIPT = `let port;
addEventListener("message", (e) => {
  if (e.source !== parent) return;
  const d = e.data;
  if (d && d.pluxy === "host" && d.type === "welcome" && e.ports[0] && !port) {
    port = e.ports[0];
    port.onmessage = (r) => parent.postMessage({ routeReply: r.data }, "*");
    setInterval(() => parent.postMessage({ routeReady: true }, "*"), 200);
  } else if (d && d.routeCall && port) {
    port.postMessage({ v: 0, id: d.routeCall.id, method: d.routeCall.method, params: d.routeCall.params });
  }
});
parent.postMessage({ pluxy: "page", v: 0, type: "hello", versions: [0] }, "*");
`;

export const summary = {
  id: "summary",
  description: "Count the items and name the longest",
  effect: "read",
  input_schema: {
    type: "object",
    properties: { items: { type: "array", items: { type: "string", maxLength: 64 }, maxItems: 16 } },
    required: ["items"],
    additionalProperties: false,
  },
  output_schema: {
    type: "object",
    properties: { count: { type: "integer" }, longest: { type: "string" } },
    required: ["count", "longest"],
  },
};
const wait = {
  id: "wait",
  description: "Wait until cancelled",
  effect: "read",
  input_schema: { type: "object", additionalProperties: false },
  output_schema: object,
};
const store = {
  id: "store",
  description: "Store a value",
  effect: "write",
  input_schema: {
    type: "object",
    properties: { value: { type: "string", maxLength: 64 } },
    required: ["value"],
    additionalProperties: false,
  },
  output_schema: object,
};
const launch = {
  id: "launch",
  description: "Start a program",
  effect: "launch",
  input_schema: {
    type: "object",
    properties: {
      executable: { type: "string" },
      argv: { type: "array", items: { type: "string" } },
      cwd: { type: "string" },
    },
    required: ["executable", "argv", "cwd"],
    additionalProperties: false,
  },
  output_schema: object,
};
const relay = {
  id: "relay",
  description: "Call another package's operation through the host",
  effect: "read",
  input_schema: object,
  output_schema: object,
};

const jobsOp = {
  id: "jobs",
  description: "Ask the host's job service",
  effect: "read",
  input_schema: object,
  output_schema: object,
};
const take = {
  id: "take",
  description: "Take a delivery of a job",
  effect: "read",
  input_schema: object,
  output_schema: object,
};
const report = {
  id: "report",
  description: "Say what was delivered",
  effect: "read",
  input_schema: object,
  output_schema: object,
};
const scan = {
  id: "scan",
  description: "Send items as a job",
  effect: "read",
  input_schema: {
    type: "object",
    properties: {
      count: { type: "integer", minimum: 0, maximum: 64 },
      mode: { type: "string" },
      hold: { type: "integer", minimum: 0, maximum: 1000 },
    },
    additionalProperties: false,
  },
  output_schema: object,
};
const ITEMS_KIND = "pluxy/jobs-hub/items";

export interface Package {
  id: string;
  version: string;
  tier: "native" | "web";
  app: Record<string, unknown>;
  page?: boolean;
}

const TARGET_EXPORTS = { contract: 1, operations: ["summary", "wait", "store", "launch"] };
const targetImport = (operations: string[], contract = [1]) => ({
  package: "pluxy/target",
  contract,
  operations,
  required: true,
});

export function native(id: string, version: string, app: Record<string, unknown>, page = false): Package {
  return {
    id,
    version,
    tier: "native",
    page,
    app: {
      version: 0,
      tier: "native",
      host_versions: [0],
      programs: { [platform()]: "bin/tool" },
      state: { version: 0, readable_versions: [0] },
      ...app,
    },
  };
}

function web(id: string, imports: object[]): Package {
  return {
    id,
    version: "1.0.0",
    tier: "web",
    page: true,
    app: { version: 0, tier: "web", host_versions: [0], imports },
  };
}

const PACKAGES: Package[] = [
  native("pluxy/jobs-hub", "1.0.0", {
    operations: [jobsOp, take, report],
    consumes: [{ kind: ITEMS_KIND, schema: [1], deliver: "take" }],
  }),
  native("pluxy/jobs-source", "1.0.0", {
    operations: [scan],
    produces: [{ kind: ITEMS_KIND, schema: 1, operation: "scan" }],
  }),
  native("pluxy/jobs-source", "1.1.0", {
    operations: [scan],
    produces: [{ kind: ITEMS_KIND, schema: 1, operation: "scan" }],
  }),
  native("pluxy/jobs-other", "1.0.0", {
    operations: [scan],
    produces: [{ kind: ITEMS_KIND, schema: 2, operation: "scan" }],
  }),
  native("pluxy/jobs-thief", "1.0.0", {
    operations: [jobsOp, take],
    consumes: [{ kind: "pluxy/jobs-thief/items", schema: [1], deliver: "take" }],
  }),
  native("pluxy/jobs-dup", "1.0.0", {
    operations: [scan],
    produces: [
      { kind: ITEMS_KIND, schema: 1, operation: "scan" },
      { kind: ITEMS_KIND, schema: 1, operation: "scan" },
    ],
  }),
  native("pluxy/jobs-outside", "1.0.0", {
    operations: [take],
    consumes: [{ kind: ITEMS_KIND, schema: [1], deliver: "take" }],
  }),
  native(
    "pluxy/target",
    "1.0.0",
    { operations: [summary, wait, store, launch], exports: TARGET_EXPORTS },
    true,
  ),
  native(
    "pluxy/target",
    "2.0.0",
    { operations: [summary, wait, store, launch], exports: TARGET_EXPORTS },
    true,
  ),
  native("pluxy/relay", "1.0.0", {
    operations: [relay],
    imports: [targetImport(["summary", "wait", "launch"])],
  }),
  native("pluxy/needs-v2", "1.0.0", { operations: [relay], imports: [targetImport(["summary"], [2])] }),
  native("pluxy/cycle-a", "1.0.0", {
    operations: [relay],
    exports: { contract: 1, operations: ["relay"] },
    imports: [{ package: "pluxy/cycle-b", contract: [1], operations: ["relay"], required: false }],
  }),
  native("pluxy/cycle-b", "1.0.0", {
    operations: [relay],
    exports: { contract: 1, operations: ["relay"] },
    imports: [{ package: "pluxy/cycle-a", contract: [1], operations: ["relay"], required: false }],
  }),
  web("pluxy/guide", [targetImport(["summary", "wait", "store", "launch"])]),
  web("example/hub-viewer", [targetImport(["summary"])]),
];

/** Packs p into root, with program as a native package's program; returns its archive's name. */
export function pack(root: string, p: Package, program: string): string {
  const file = `${p.id.replace("/", "-")}-${p.version}.tar.gz`;
  const dir = join(root, `pkg-${file}`);
  mkdirSync(dir, { recursive: true });
  const app = { ...p.app };
  if (p.page) {
    mkdirSync(join(dir, "pages"));
    app.ui = {
      version: 0,
      dir: "pages",
      pages: [{ id: "page", title: p.id, short: "Page", path: "index.html" }],
    };
    writeFileSync(
      join(dir, "pages", "index.html"),
      `<!doctype html><meta charset="utf-8"><title>${p.id}</title><script src="page.js"></script><p>${p.id}</p>`,
    );
    writeFileSync(join(dir, "pages", "page.js"), PAGE_SCRIPT);
  }
  if (p.tier === "native") {
    mkdirSync(join(dir, "bin"));
    copyFileSync(program, join(dir, "bin", "tool"));
    chmodSync(join(dir, "bin", "tool"), 0o755);
  }
  writeFileSync(
    join(dir, "pluxy-extension.json"),
    JSON.stringify({ manifest_version: 1, id: p.id, version: p.version, app }),
  );
  const r = spawnSync("tar", ["-czf", join(root, file), "-C", dir, "."], { stdio: "inherit" });
  if (r.status !== 0) throw new Error(`tar exited with ${r.status}`);
  rmSync(dir, { recursive: true, force: true });
  return file;
}

export function index(entries: [Package, Record<string, unknown>][]): object {
  const packages: Record<string, { releases: object[] }> = {};
  for (const [p, entry] of entries) {
    const listed = packages[p.id] ?? { releases: [] };
    packages[p.id] = listed;
    listed.releases.push({ version: p.version, artifacts: { [platform()]: entry } });
  }
  return { registry_version: 0, packages };
}

export interface Routing {
  /** The registries file the app's config should hold. */
  registries: { name: string; url: string }[];
  /** PLUXY_TEST_TRUST_NONCE for the app: the marker's nonce. */
  nonce: string;
}

/** Builds the fixtures in root, with program (the built fixture tool) as every native package's
 * program, and the test trust profile in home (the app's HOME). */
export function buildRoutingFixtures(root: string, home: string, program: string): Routing {
  const { privateKey, publicKey } = generateKeyPairSync("ed25519");
  const curated: [Package, Record<string, unknown>][] = [];
  const untrusted: [Package, Record<string, unknown>][] = [];
  const sha256: Record<string, string> = {};
  for (const p of PACKAGES) {
    const file = pack(root, p, program);
    const f: Fixture = { file, id: p.id, version: p.version, tier: p.tier };
    const reserved = p.id.startsWith("pluxy/");
    const a = artifact(root, file, f, publicKey, reserved ? (privateKey as KeyObject) : undefined);
    (reserved ? curated : untrusted).push([p, a.entry]);
    sha256[`${p.id} ${p.version}`] = a.sha256;
  }
  const curatedFile = join(root, "curated.json");
  const untrustedFile = join(root, "untrusted.json");
  writeFileSync(curatedFile, JSON.stringify(index(curated)));
  writeFileSync(untrustedFile, JSON.stringify(index(untrusted)));
  // What the launch-class calls name: a regular file to run, its arguments and folder.
  const executable = join(root, "launch-me");
  writeFileSync(executable, "#!/bin/sh\nexit 0\n", { mode: 0o755 });
  const longest = ITEMS.reduce((a, b) => (b.length > a.length ? b : a), "");
  writeFileSync(
    join(root, "expect.json"),
    JSON.stringify({
      sha256,
      items: ITEMS,
      count: ITEMS.length,
      longest,
      launch: { executable, argv: ["launch-me", "--routing-check"], cwd: root },
    }),
  );
  const nonce = writeTrustProfile(home, [curatedFile], publicKey);
  return {
    registries: [
      { name: "curated", url: curatedFile },
      { name: "untrusted", url: untrustedFile },
    ],
    nonce,
  };
}

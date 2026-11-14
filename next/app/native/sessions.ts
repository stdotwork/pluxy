// Fixtures for the native test's check of the read-only Codex session reader
// (src-tauri/src/sessions_native.rs): the reader program and its page from next/app-tools/codex-sessions
// (which run.ts builds) in a signed native package, a curated registry listing it, the run's test
// trust profile, and a Codex home in the run's HOME (`.codex`, the reader's default) holding:
//
// - sqlite/codex-dev.db: automation_runs (one row newer than its thread, one archived);
// - sqlite/threads.db, in WAL mode with no -wal or -shm left: threads with the optional columns;
// - state_5.sqlite, the legacy database: threads with only id, name and updated_at (seconds);
// - sqlite/live.db, in WAL mode with a -wal that still holds a committed row (as Codex leaves it
//   when it stops before copying its -wal back) and no -shm: the reader reads that row too and
//   adds no file beside it;
// - sqlite/broken.db: not a database;
// - rollouts in sessions/ (active) and archived_sessions/ (archived): one whose thread is in a
//   database (dropped from the list, read for its detail), one named only by session_index.jsonl,
//   one with neither (its modification time), one with a database thread's ID behind `local:`, and a
//   symbolic link (skipped);
// - session_index.jsonl, with a broken line;
// - config.toml and auth.json holding canary strings no page and no log may show.
//
// The test archive adds one page to the reader's manifest: routing.ts's relay page (`page`), which
// sends the calls the check posts it on its channel, as the routing and vault checks do. The reader's
// own page stays as it is.
//
// expect.json holds what the check compares with, written out by hand here, never worked out by
// the reader's code: each list page, each detail page and the canaries. When the reader has not
// been built (its program is missing), the registry lists nothing and the check fails at its
// first step, installing the reader.

import { Database } from "bun:sqlite";
import { spawnSync } from "node:child_process";
import { generateKeyPairSync, type KeyObject, randomBytes } from "node:crypto";
import {
  chmodSync,
  copyFileSync,
  cpSync,
  existsSync,
  mkdirSync,
  readFileSync,
  realpathSync,
  rmSync,
  statSync,
  symlinkSync,
  utimesSync,
  writeFileSync,
} from "node:fs";
import { join } from "node:path";
import { artifact, type Fixture, platform, writeTrustProfile } from "./admission";
import { PAGE_SCRIPT } from "./routing";

export const SESSIONS_ID = "pluxy/codex-sessions";

const T1 = "019a0000-0000-7000-8000-000000000001";
const T2 = "019a0000-0000-7000-8000-000000000002";
const R1 = "019a0000-0000-7000-8000-0000000000a1";
const R2 = "019a0000-0000-7000-8000-0000000000a2";
const R3 = "019a0000-0000-7000-8000-0000000000a3";

export interface Sessions {
  /** The registries file the app's config should hold. */
  registries: { name: string; url: string }[];
  /** PLUXY_TEST_TRUST_NONCE for the app: the marker's nonce. */
  nonce: string;
  /** Strings that must never reach a page, a webview or the app's output. */
  canaries: string[];
}

/** Writes rows into a new database at path, in the journal mode given. */
function database(path: string, sql: string, wal = false) {
  const db = new Database(path, { create: true });
  if (wal) db.run("PRAGMA journal_mode = WAL");
  db.run(sql);
  db.close();
  for (const side of ["-wal", "-shm", "-journal"]) {
    if (existsSync(path + side)) throw new Error(`${path}${side} was left behind`);
  }
}

/** A WAL database at dest whose row wal-only is only in its -wal, with no -shm: made in source,
 * then the database and its -wal copied while its connection still holds them. */
function liveDatabase(source: string, dest: string) {
  mkdirSync(source, { recursive: true });
  const path = join(source, "live.db");
  const db = new Database(path, { create: true });
  db.run("PRAGMA journal_mode = WAL");
  db.run("CREATE TABLE threads (id TEXT PRIMARY KEY, title TEXT, updated_at_ms INTEGER)");
  db.run("INSERT INTO threads VALUES ('wal-base', 'Checkpointed', 1753000000000)");
  db.run("PRAGMA wal_checkpoint(TRUNCATE)");
  db.run("PRAGMA wal_autocheckpoint = 0");
  db.run("INSERT INTO threads VALUES ('wal-only', 'Only in the WAL', 1753500000000)");
  copyFileSync(path, dest);
  copyFileSync(`${path}-wal`, `${dest}-wal`);
  db.close();
  if (statSync(`${dest}-wal`).size === 0) throw new Error(`${dest}-wal holds no row`);
  if (existsSync(`${dest}-shm`)) throw new Error(`${dest}-shm should not exist`);
}

/** The JSONL lines of T1's rollout, by line number. */
function rolloutLines(): string[] {
  const record = (type: string, payload: object, timestamp?: string) =>
    JSON.stringify(timestamp ? { timestamp, type, payload } : { type, payload });
  return [
    record(
      "session_meta",
      { id: T1, cwd: "/work/parser", model_provider: "openai" },
      "2025-08-04T10:00:00.000Z",
    ),
    record("turn_context", { model: "gpt-5-codex", cwd: "/work/parser" }),
    record(
      "response_item",
      {
        type: "message",
        role: "user",
        content: [{ type: "input_text", text: "Please fix <b>the</b> parser" }],
      },
      "2025-08-04T10:00:01.000Z",
    ),
    record("response_item", {
      type: "reasoning",
      summary: [{ type: "summary_text", text: "Looking at the parser" }],
    }),
    record("response_item", { type: "reasoning", encrypted_content: "gAAAAB-opaque" }),
    record("response_item", {
      type: "function_call",
      name: "shell",
      arguments: '{"cmd":"ls"}',
      call_id: "call_1",
    }),
    record("response_item", { type: "function_call_output", call_id: "call_1", output: "parser.rs" }),
    record("event_msg", { type: "agent_message", message: "not a response item" }),
    "not JSON",
    record(
      "response_item",
      { type: "message", role: "assistant", content: [{ type: "output_text", text: "Done." }] },
      "2025-08-04T10:00:09.000Z",
    ),
    record("turn_context", { model: "gpt-5.1-codex" }),
    record("response_item", {
      type: "message",
      role: "assistant",
      content: [{ type: "output_text", text: "x".repeat(10000) }],
    }),
  ];
}

/** Makes the Codex home in home/.codex; gives its canaries and the size of T1's rollout. */
function codexHome(home: string, outside: string) {
  const codex = join(home, ".codex");
  for (const d of ["sqlite", "sessions/2026/08/04", "sessions/2026/08/05", "archived_sessions"]) {
    mkdirSync(join(codex, d), { recursive: true });
  }
  const t1Rollout = join(codex, "sessions/2026/08/04", `rollout-2026-08-04T10-00-00-${T1}.jsonl`);
  const t1Bytes = `${rolloutLines().join("\n")}\n`;
  writeFileSync(t1Rollout, t1Bytes);
  writeFileSync(
    join(codex, "sessions/2026/08/05", `rollout-2026-08-05T09-00-00-${R1}.jsonl`),
    "not parsed\n",
  );
  const r2 = join(codex, "archived_sessions", `rollout-2026-07-01T08-00-00-${R2}.jsonl`);
  writeFileSync(r2, "not parsed\n");
  // 2025-07-01T08:00:00Z: R2 has no index entry, so the list shows its modification time.
  utimesSync(r2, 1751356800, 1751356800);
  // T2's thread is in threads.db (as local:T2): this rollout is dropped from the list.
  writeFileSync(join(codex, "archived_sessions", `rollout-2026-08-02T00-00-00-${T2}.jsonl`), "not parsed\n");
  // A link is skipped, wherever it points.
  mkdirSync(outside, { recursive: true });
  const linked = join(outside, `rollout-2026-08-06T00-00-00-${R3}.jsonl`);
  writeFileSync(linked, "not parsed\n");
  symlinkSync(linked, join(codex, "sessions", `rollout-2026-08-06T00-00-00-${R3}.jsonl`));
  writeFileSync(join(codex, "sessions", "notes.txt"), "not a rollout\n");
  writeFileSync(
    join(codex, "session_index.jsonl"),
    [
      JSON.stringify({ id: R1, thread_name: "Indexed rollout", updated_at: "2025-08-05T09:00:00Z" }),
      "{broken",
      JSON.stringify({ id: "019a0000-0000-7000-8000-0000000000ff", thread_name: "No rollout" }),
      "",
    ].join("\n"),
  );

  liveDatabase(join(outside, "live-source"), join(codex, "sqlite", "live.db"));
  database(
    join(codex, "sqlite", "codex-dev.db"),
    `CREATE TABLE automation_runs (thread_id TEXT PRIMARY KEY, status TEXT, thread_title TEXT, source_cwd TEXT, updated_at INTEGER);
     INSERT INTO automation_runs VALUES ('${T1}', 'running', 'Fix the parser again', '/work/parser-new', 1754305200);
     INSERT INTO automation_runs VALUES ('auto-only', 'archived', 'Automation only', '/work/auto', 1754100000);`,
  );
  database(
    join(codex, "sqlite", "threads.db"),
    `CREATE TABLE threads (id TEXT PRIMARY KEY, rollout_path TEXT, title TEXT, name TEXT, cwd TEXT, model_provider TEXT, model TEXT, archived INTEGER, updated_at_ms INTEGER);
     INSERT INTO threads VALUES ('${T1}', '${t1Rollout}', 'Fix the parser', '', '/work/parser', 'openai', 'gpt-5', 0, 1754301600000);
     INSERT INTO threads VALUES ('local:${T2}', NULL, '', 'Named thread', '/work/two', 'openai', '', 1, 1754200000000);
     INSERT INTO threads VALUES ('thread-db-only', NULL, 'Database only', NULL, '/work/three', 'azure', NULL, 0, NULL);`,
    true,
  );
  database(
    join(codex, "state_5.sqlite"),
    `CREATE TABLE threads (id TEXT PRIMARY KEY, name TEXT, updated_at INTEGER);
     INSERT INTO threads VALUES ('legacy-1', 'Legacy thread', 1754000000);`,
  );
  writeFileSync(join(codex, "sqlite", "broken.db"), "not a sqlite database\n");

  const config = `canary-config-${randomBytes(12).toString("hex")}`;
  const auth = `canary-auth-${randomBytes(12).toString("hex")}`;
  writeFileSync(join(codex, "config.toml"), `model_provider = "${config}"\n`);
  writeFileSync(join(codex, "auth.json"), JSON.stringify({ OPENAI_API_KEY: auth }), { mode: 0o600 });
  return { codex, t1Rollout, t1Size: Buffer.byteLength(t1Bytes), canaries: [config, auth] };
}

/** What the list and detail pages must say, by hand. */
function expected(codex: string, t1Rollout: string, t1Size: number) {
  const db = (name: string) => join(codex, name);
  const devDb = db("sqlite/codex-dev.db");
  const liveDb = db("sqlite/live.db");
  const threadsDb = db("sqlite/threads.db");
  const legacyDb = db("state_5.sqlite");
  const s = (
    id: string,
    title: string,
    cwd: string,
    provider: string,
    archived: boolean,
    updated_ms: number | null,
    database: string,
  ) => ({ id, title, cwd, provider, archived, updated_ms, database });
  const all = [
    s(R1, "Indexed rollout", "", "", false, 1754384400000, ""),
    s(T1, "Fix the parser again", "/work/parser-new", "openai", false, 1754305200000, devDb),
    s(`local:${T2}`, "Named thread", "/work/two", "openai", true, 1754200000000, threadsDb),
    s("auto-only", "Automation only", "/work/auto", "", true, 1754100000000, devDb),
    s("legacy-1", "Legacy thread", "", "", false, 1754000000000, legacyDb),
    s("wal-only", "Only in the WAL", "", "", false, 1753500000000, liveDb),
    s("wal-base", "Checkpointed", "", "", false, 1753000000000, liveDb),
    s(R2, "", "", "", true, 1751356800000, ""),
    s("thread-db-only", "Database only", "/work/three", "azure", false, null, threadsDb),
  ];
  const page = (offset: number, limit: number, has_more: boolean, active: number, archived: number) => ({
    input: offset === 0 && limit === 50 ? {} : { offset, limit },
    output: {
      codex_home: codex,
      databases: [devDb, liveDb, threadsDb, legacyDb],
      sessions: all.slice(offset, offset + limit),
      total: 9,
      offset,
      limit,
      has_more,
      active,
      archived,
    },
  });
  const m = (line: number, role: string, content: string, extra: object = {}) => ({
    line,
    role,
    timestamp: null,
    content,
    truncated: false,
    call_id: null,
    ...extra,
  });
  const messages = [
    m(2, "user", "Please fix <b>the</b> parser", { timestamp: "2025-08-04T10:00:01.000Z" }),
    m(3, "reasoning", "Looking at the parser"),
    m(4, "reasoning", "[Encrypted reasoning block]"),
    m(5, "tool", 'shell({"cmd":"ls"})', { call_id: "call_1" }),
    m(6, "tool", "parser.rs", { call_id: "call_1" }),
    m(9, "assistant", "Done.", { timestamp: "2025-08-04T10:00:09.000Z" }),
    m(11, "assistant", "x".repeat(8192), { truncated: true }),
  ];
  const detail = (input: object, offset: number, limit: number, next_offset: number | null) => ({
    input,
    output: {
      id: T1,
      title: "Fix the parser",
      cwd: "/work/parser",
      provider: "openai",
      model: "gpt-5",
      archived: false,
      updated_ms: 1754301600000,
      rollout: realpathSync(t1Rollout),
      database: threadsDb,
      stats: { lines: 12, messages: 5, user: 1, assistant: 2, tool: 2, reasoning: 2, size: t1Size },
      messages: messages.slice(offset, offset + limit),
      offset,
      total: 7,
      next_offset,
    },
  });
  return {
    list: [
      page(0, 50, false, 6, 3),
      page(0, 2, true, 2, 0),
      page(2, 2, true, 0, 2),
      page(6, 2, true, 1, 1),
      page(8, 2, false, 1, 0),
    ],
    // Each list page has one warning, about broken.db; its wording is SQLite's.
    warning_names: db("sqlite/broken.db"),
    detail: [detail({ id: T1 }, 0, 50, null), detail({ id: T1, offset: 3, limit: 2 }, 3, 2, 5)],
  };
}

/** Packs and signs the reader from tools (next/app-tools), with the relay page added; undefined when
 * the reader was not built. */
function packReader(
  root: string,
  tools: string,
  program: string,
  publicKey: KeyObject,
  privateKey: KeyObject,
) {
  const source = join(tools, "codex-sessions");
  const manifestPath = join(source, "pluxy-extension.json");
  if (!existsSync(program) || !existsSync(manifestPath)) return undefined;
  const manifest = JSON.parse(readFileSync(manifestPath, "utf8"));
  const dir = join(root, "pkg-codex-sessions");
  mkdirSync(join(dir, "bin"), { recursive: true });
  const ui = manifest.app.ui;
  cpSync(join(source, ui.dir), join(dir, ui.dir), { recursive: true });
  ui.pages = [...ui.pages, { id: "page", title: "Probe", short: "Probe", path: "probe.html" }];
  writeFileSync(
    join(dir, ui.dir, "probe.html"),
    '<!doctype html><meta charset="utf-8"><title>Probe</title><script src="probe.js"></script><p>Probe</p>',
  );
  writeFileSync(join(dir, ui.dir, "probe.js"), PAGE_SCRIPT);
  const bin = "bin/codex-sessions";
  manifest.app.programs = { [platform()]: bin };
  copyFileSync(program, join(dir, bin));
  chmodSync(join(dir, bin), 0o755);
  writeFileSync(join(dir, "pluxy-extension.json"), JSON.stringify(manifest));
  const f: Fixture = {
    file: `codex-sessions-${manifest.version}.tar.gz`,
    id: SESSIONS_ID,
    version: manifest.version,
    tier: "native",
  };
  const r = spawnSync("tar", ["-czf", join(root, f.file), "-C", dir, "."], { stdio: "inherit" });
  if (r.status !== 0) throw new Error(`tar exited with ${r.status}`);
  rmSync(dir, { recursive: true, force: true });
  return { version: manifest.version as string, bin, ...artifact(root, f.file, f, publicKey, privateKey) };
}

/** Builds the fixtures in root, the Codex home and the test trust profile in home (the app's
 * HOME), with program (the built reader) from tools (next/app-tools). */
export function buildSessionsFixtures(root: string, home: string, tools: string, program: string): Sessions {
  const { privateKey, publicKey } = generateKeyPairSync("ed25519");
  const reader = packReader(root, tools, program, publicKey, privateKey);
  const curated = join(root, "curated.json");
  writeFileSync(
    curated,
    JSON.stringify({
      registry_version: 0,
      packages: reader
        ? {
            [SESSIONS_ID]: {
              releases: [{ version: reader.version, artifacts: { [platform()]: reader.entry } }],
            },
          }
        : {},
    }),
  );
  const { codex, t1Rollout, t1Size, canaries } = codexHome(home, join(root, "outside"));
  writeFileSync(
    join(root, "expect.json"),
    JSON.stringify({
      id: SESSIONS_ID,
      version: reader?.version ?? "0.1.0",
      sha256: reader?.sha256 ?? null,
      bin: reader?.bin ?? "bin/codex-sessions",
      codex,
      canaries,
      ...expected(codex, t1Rollout, t1Size),
    }),
  );
  const nonce = writeTrustProfile(home, [curated], publicKey);
  return { registries: [{ name: "curated", url: curated }], nonce, canaries };
}

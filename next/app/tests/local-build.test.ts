// The local build's folder rules (package.ts local): which folders it refuses to build into
// (checkOutDir), how it puts a finished build in place (swapIn), what a failed or stopped build
// leaves (the command itself, with stand-in commands that fail or wait), and its version.
import { afterEach, describe, expect, test } from "bun:test";
import { spawn, spawnSync } from "node:child_process";
import {
  chmodSync,
  existsSync,
  lstatSync,
  mkdirSync,
  mkdtempSync,
  readdirSync,
  readFileSync,
  readlinkSync,
  renameSync,
  rmSync,
  symlinkSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { checkOutDir, LOCAL_RECORD, localVersion, swapIn } from "../package";

const app = join(import.meta.dir, "..");
const made: string[] = [];
afterEach(() => {
  for (const dir of made.splice(0)) rmSync(dir, { recursive: true, force: true });
});

function tempDir(): string {
  const dir = mkdtempSync(join(tmpdir(), "pluxy-local-build-test-"));
  made.push(dir);
  return dir;
}

/** Every file, folder and link under dir, with each file's bytes and each link's target. */
function tree(dir: string): Record<string, string> {
  const out: Record<string, string> = {};
  const walk = (rel: string) => {
    for (const name of readdirSync(join(dir, rel)).sort()) {
      const path = join(rel, name);
      const st = lstatSync(join(dir, path));
      if (st.isSymbolicLink()) out[path] = `link ${readlinkSync(join(dir, path))}`;
      else if (st.isDirectory()) {
        out[path] = "folder";
        walk(path);
      } else out[path] = `file ${readFileSync(join(dir, path)).toString("base64")}`;
    }
  };
  walk("");
  return out;
}

/** A local build's folder as the command writes it, with a home folder holding an account. */
function localBuildAt(outDir: string, record: Record<string, unknown> = {}) {
  mkdirSync(join(outDir, "bin"), { recursive: true });
  mkdirSync(join(outDir, "home", ".local", "share"), { recursive: true });
  writeFileSync(join(outDir, "bin", "pluxy-app-local"), "old app");
  writeFileSync(join(outDir, "home", ".local", "share", "account.json"), '{"account":1}');
  writeFileSync(
    join(outDir, LOCAL_RECORD),
    JSON.stringify({
      program: join(outDir, "bin", "pluxy-app-local"),
      identifier: "dev.pluxy.desktop.local",
      product_name: "pluxy Local",
      bundled_core: { version: "0.1.0+local.0123456789ab", packages: ["pluxy/claude"] },
      ...record,
    }),
  );
}

/** A /proc stand-in with no process, so no check reads the real one. */
function noProcesses(): string {
  const proc = join(tempDir(), "proc");
  mkdirSync(proc);
  return proc;
}

describe("checkOutDir", () => {
  test("lets a missing folder, an empty folder and a local build through", () => {
    const root = tempDir();
    const proc = noProcesses();
    expect(() => checkOutDir(join(root, "new"), proc)).not.toThrow();
    mkdirSync(join(root, "empty"));
    expect(() => checkOutDir(join(root, "empty"), proc)).not.toThrow();
    localBuildAt(join(root, "built"), { added_later: true });
    expect(() => checkOutDir(join(root, "built"), proc)).not.toThrow();
  });

  const notLocal =
    "holds files that are not a local build. Choose an empty or new folder: make app-local LOCAL=<folder>";
  const refusals: [string, (outDir: string) => void, string][] = [
    ["a folder with files and no record", (d) => writeFileSync(join(d, "notes"), "mine"), notLocal],
    [
      "a folder whose record is someone else's",
      (d) => {
        writeFileSync(join(d, "notes"), "mine");
        writeFileSync(join(d, LOCAL_RECORD), '{"name":"my own project"}');
      },
      notLocal,
    ],
    ["a record that is not JSON", (d) => writeFileSync(join(d, LOCAL_RECORD), "{program:"), notLocal],
    [
      "a record of the release's identity",
      (d) => localBuildAt(d, { identifier: "dev.pluxy.desktop" }),
      notLocal,
    ],
    ["a record with no core version", (d) => localBuildAt(d, { bundled_core: { packages: [] } }), notLocal],
    [
      "a record whose program is outside the folder",
      (d) => localBuildAt(d, { program: "/usr/bin/true" }),
      notLocal,
    ],
    [
      "a record whose program is the folder's neighbour",
      (d) => localBuildAt(d, { program: `${d}-x/bin/app` }),
      notLocal,
    ],
    [
      "a record whose program is relative",
      (d) => localBuildAt(d, { program: "bin/pluxy-app-local" }),
      notLocal,
    ],
  ];
  for (const [name, fill, said] of refusals) {
    test(`refuses ${name}, leaving every file`, () => {
      const outDir = join(tempDir(), "out");
      mkdirSync(outDir);
      fill(outDir);
      const before = tree(outDir);
      expect(() => checkOutDir(outDir, noProcesses())).toThrow(`not building into ${outDir}: it ${said}`);
      expect(tree(outDir)).toEqual(before);
    });
  }

  test("refuses a link, even to a local build", () => {
    const root = tempDir();
    localBuildAt(join(root, "real"));
    symlinkSync(join(root, "real"), join(root, "out"));
    expect(() => checkOutDir(join(root, "out"), noProcesses())).toThrow(
      `not building into ${join(root, "out")}: it is a link. Name the folder itself: make app-local LOCAL=<folder>`,
    );
    expect(existsSync(join(root, "real", "home", ".local", "share", "account.json"))).toBe(true);
  });

  test("refuses a file", () => {
    const outDir = join(tempDir(), "out");
    writeFileSync(outDir, "mine");
    expect(() => checkOutDir(outDir, noProcesses())).toThrow(
      `not building into ${outDir}: it is not a folder`,
    );
    expect(readFileSync(outDir, "utf8")).toBe("mine");
  });

  test("refuses while a .old folder is left from an earlier build, whatever the folder holds", () => {
    const root = tempDir();
    const outDir = join(root, "out");
    localBuildAt(`${outDir}.old`);
    const before = tree(`${outDir}.old`);
    const said = `${outDir}.old is left from an earlier build and may hold its home folder: move out what you need, delete it, then build again`;
    expect(() => checkOutDir(outDir, noProcesses())).toThrow(said);
    localBuildAt(outDir);
    expect(() => checkOutDir(outDir, noProcesses())).toThrow(said);
    expect(tree(`${outDir}.old`)).toEqual(before);
  });

  test("refuses while a program runs from the folder: the app, or a core in its home folder", () => {
    const root = tempDir();
    const outDir = join(root, "out");
    localBuildAt(outDir);
    const proc = noProcesses();
    mkdirSync(join(proc, "4242"));
    mkdirSync(join(proc, "self"));
    symlinkSync("/usr/bin/sleep", join(proc, "4242", "exe"));
    // A program elsewhere, a process it may not read and a name that is no process: all pass.
    mkdirSync(join(proc, "4343"));
    expect(() => checkOutDir(outDir, proc)).not.toThrow();
    const said = `pluxy (local build) is running from ${outDir}: quit it from its tray menu, then build again`;
    for (const exe of [
      join(outDir, "bin", "pluxy-app-local"),
      `${join(outDir, "home", ".local", "share", "cores", "pluxy")} (deleted)`,
    ]) {
      mkdirSync(join(proc, "4444"));
      symlinkSync(exe, join(proc, "4444", "exe"));
      expect(() => checkOutDir(outDir, proc)).toThrow(said);
      rmSync(join(proc, "4444"), { recursive: true });
    }
  });
});

describe("swapIn", () => {
  /** A finished build in a stage beside outDir, and an old build with a home folder in outDir. */
  function staged() {
    const root = tempDir();
    const outDir = join(root, "out");
    localBuildAt(outDir);
    writeFileSync(join(outDir, "left-by-old"), "old");
    const stage = mkdtempSync(`${outDir}.stage-`);
    mkdirSync(join(stage, "bin"));
    writeFileSync(join(stage, "bin", "pluxy-app-local"), "new app");
    writeFileSync(join(stage, LOCAL_RECORD), "{}");
    return { root, outDir, stage };
  }

  test("renames the stage in when there is no folder yet", () => {
    const root = tempDir();
    const stage = join(root, "out.stage-1");
    mkdirSync(stage);
    writeFileSync(join(stage, "x"), "new");
    swapIn(stage, join(root, "out"));
    expect(readdirSync(root)).toEqual(["out"]);
    expect(readFileSync(join(root, "out", "x"), "utf8")).toBe("new");
  });

  test("puts the new build in place and keeps the home folder; nothing else of the old build stays", () => {
    const { root, outDir, stage } = staged();
    const home = tree(join(outDir, "home"));
    swapIn(stage, outDir);
    expect(readdirSync(root)).toEqual(["out"]);
    expect(readFileSync(join(outDir, "bin", "pluxy-app-local"), "utf8")).toBe("new app");
    expect(tree(join(outDir, "home"))).toEqual(home);
    expect(existsSync(join(outDir, "left-by-old"))).toBe(false);
  });

  test("a failed rename of the stage puts the old build back as it was", () => {
    const { outDir, stage } = staged();
    const before = tree(outDir);
    const move = (from: string, to: string) => {
      if (from === stage) throw new Error("EXDEV: cross-device link not permitted");
      renameSync(from, to);
    };
    expect(() => swapIn(stage, outDir, move)).toThrow(
      `could not put the new build in place (EXDEV: cross-device link not permitted); the previous build is back in ${outDir}`,
    );
    expect(tree(outDir)).toEqual(before);
    expect(existsSync(`${outDir}.old`)).toBe(false);
  });

  test("when putting the old build back fails too, it stays whole in .old and the message says so", () => {
    const { outDir, stage } = staged();
    const before = tree(outDir);
    const move = (from: string, to: string) => {
      if (from === stage || from === `${outDir}.old`) throw new Error("EIO");
      renameSync(from, to);
    };
    expect(() => swapIn(stage, outDir, move)).toThrow(
      `could not put the new build in place (EIO); the previous build and its home folder are in ${outDir}.old: move it back to ${outDir}`,
    );
    expect(tree(`${outDir}.old`)).toEqual(before);
  });

  test("a failed move of the home folder keeps .old, home folder and all", () => {
    const { outDir, stage } = staged();
    const home = tree(join(outDir, "home"));
    const move = (from: string, to: string) => {
      if (from.endsWith(`.old${"/"}home`)) throw new Error("EACCES");
      renameSync(from, to);
    };
    expect(() => swapIn(stage, outDir, move)).toThrow(
      `the new build is in ${outDir}, but its home folder could not be moved there (EACCES): it is in ${outDir}.old/home`,
    );
    expect(tree(join(`${outDir}.old`, "home"))).toEqual(home);
    expect(readFileSync(join(outDir, "bin", "pluxy-app-local"), "utf8")).toBe("new app");
  });

  test("a failed first rename leaves everything as it was", () => {
    const { outDir, stage } = staged();
    const before = tree(outDir);
    const move = (from: string, to: string) => {
      if (from === outDir) throw new Error("EBUSY");
      renameSync(from, to);
    };
    expect(() => swapIn(stage, outDir, move)).toThrow(
      `could not put the new build in place (EBUSY); ${outDir} is as it was`,
    );
    expect(tree(outDir)).toEqual(before);
  });
});

describe("the command", () => {
  /** A folder of stand-in commands for PATH: go as given; nothing else. */
  function standIns(go: string): string {
    const bin = join(tempDir(), "bin");
    mkdirSync(bin);
    writeFileSync(join(bin, "go"), `#!/bin/sh\n${go}\n`);
    chmodSync(join(bin, "go"), 0o755);
    return bin;
  }

  test("a build that fails leaves the old build as it was and no stage", () => {
    const root = tempDir();
    const outDir = join(root, "out");
    localBuildAt(outDir);
    const before = tree(outDir);
    const r = spawnSync(process.execPath, ["package.ts", "local", outDir], {
      cwd: app,
      env: { ...process.env, PATH: standIns("echo go is broken here >&2; exit 3") },
      encoding: "utf8",
    });
    expect(r.status).toBe(1);
    expect(r.stderr).toContain("package local: go run ./cmd/pluxy-pack");
    expect(r.stderr).toContain(`package local: the build failed; ${outDir} is as it was`);
    expect(tree(outDir)).toEqual(before);
    expect(readdirSync(root)).toEqual(["out"]);
  });

  test("a folder that is not a local build is refused before anything runs", () => {
    const root = tempDir();
    const outDir = join(root, "out");
    mkdirSync(outDir);
    writeFileSync(join(outDir, LOCAL_RECORD), "{}");
    writeFileSync(join(outDir, "notes"), "mine");
    const before = tree(outDir);
    const r = spawnSync(process.execPath, ["package.ts", "local", outDir], {
      cwd: app,
      env: { ...process.env, PATH: standIns(`: > ${join(root, "go-ran")}`) },
      encoding: "utf8",
    });
    expect(r.status).toBe(1);
    expect(r.stderr).toContain(
      `package local: not building into ${outDir}: it holds files that are not a local build`,
    );
    expect(tree(outDir)).toEqual(before);
    expect(readdirSync(root)).toEqual(["out"]);
  });

  test("SIGINT stops the build: the command ends with 130, the stage is gone, the old build stays", async () => {
    const root = tempDir();
    const outDir = join(root, "out");
    localBuildAt(outDir);
    const before = tree(outDir);
    const started = join(root, "go-started");
    const child = spawn(process.execPath, ["package.ts", "local", outDir], {
      cwd: app,
      env: { ...process.env, PATH: standIns(`: > ${started}; exec /bin/sleep 30`) },
      stdio: ["ignore", "ignore", "pipe"],
    });
    let stderr = "";
    child.stderr.on("data", (b) => {
      stderr += b;
    });
    const ended = new Promise<number | null>((done) => child.on("close", (code) => done(code)));
    for (let i = 0; i < 400 && !existsSync(started); i++) await Bun.sleep(50);
    expect(existsSync(started)).toBe(true);
    expect(readdirSync(root).some((n) => n.startsWith("out.stage-"))).toBe(true);
    const t0 = Date.now();
    child.kill("SIGINT");
    expect(await ended).toBe(130);
    expect(Date.now() - t0).toBeLessThan(10_000);
    expect(stderr).toContain(`package local: the build failed; ${outDir} is as it was`);
    expect(readdirSync(root).sort()).toEqual(["go-started", "out"]);
    expect(tree(outDir)).toEqual(before);
  }, 30_000);
});

describe("localVersion", () => {
  test("is the core version the app names with 12 random hex digits as build metadata, new on each call", () => {
    const core: string = JSON.parse(readFileSync(join(app, "core-release.json"), "utf8")).version;
    const form = new RegExp(`^${core.replaceAll(".", "\\.")}\\+local\\.[0-9a-f]{12}$`);
    const a = localVersion();
    const b = localVersion();
    expect(a).toMatch(form);
    expect(b).toMatch(form);
    expect(a).not.toBe(b);
  });
});

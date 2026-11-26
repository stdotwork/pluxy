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

/** A test that needs file modes to hold: root reads and removes past them. */
const asUser = test.skipIf(process.getuid?.() === 0);

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
  mkdirSync(join(outDir, "lib", "pluxy Local", "core", "bundled"), { recursive: true });
  mkdirSync(join(outDir, "home", ".local", "share"), { recursive: true });
  writeFileSync(join(outDir, "bin", "pluxy-app-local"), "old app");
  writeFileSync(join(outDir, "bin", "pluxy"), "old core");
  writeFileSync(join(outDir, "lib", "pluxy Local", "core", "bundled", "index.json"), '{"packages":{}}');
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
    [
      "a record whose program is another file in the folder",
      (d) => {
        writeFileSync(join(d, "notes"), "mine");
        localBuildAt(d, { program: join(d, "notes") });
      },
      notLocal,
    ],
    [
      "a build whose app is gone",
      (d) => {
        localBuildAt(d);
        rmSync(join(d, "bin", "pluxy-app-local"));
      },
      notLocal,
    ],
    [
      "a build whose core is gone",
      (d) => {
        localBuildAt(d);
        rmSync(join(d, "bin", "pluxy"));
      },
      notLocal,
    ],
    [
      "a build whose providers are gone",
      (d) => {
        localBuildAt(d);
        rmSync(join(d, "lib"), { recursive: true });
      },
      notLocal,
    ],
    [
      "a build whose app is a link",
      (d) => {
        localBuildAt(d);
        rmSync(join(d, "bin", "pluxy-app-local"));
        symlinkSync("/usr/bin/true", join(d, "bin", "pluxy-app-local"));
      },
      notLocal,
    ],
    [
      "a build whose bin folder is a link",
      (d) => {
        localBuildAt(d);
        renameSync(join(d, "bin"), `${d}-bin`);
        symlinkSync(`${d}-bin`, join(d, "bin"));
      },
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

  test("refuses a folder under a link, naming its own path", () => {
    const root = tempDir();
    mkdirSync(join(root, "real"));
    localBuildAt(join(root, "real", "out"));
    symlinkSync(join(root, "real"), join(root, "via"));
    const before = tree(join(root, "real"));
    for (const outDir of [join(root, "via", "out"), join(root, "via", "new", "out")]) {
      expect(() => checkOutDir(outDir, noProcesses())).toThrow(
        `not building into ${outDir}: ${join(root, "via")} is a link or inside one. Name the folder by its own path: make app-local LOCAL=${outDir.replace(join(root, "via"), join(root, "real"))}`,
      );
    }
    expect(tree(join(root, "real"))).toEqual(before);
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
    // A program elsewhere, a process with no program (ended, a zombie) and a name that is no
    // process: all pass.
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

  test("refuses when it cannot list the processes", () => {
    const outDir = join(tempDir(), "out");
    localBuildAt(outDir);
    const before = tree(outDir);
    const proc = join(tempDir(), "no-proc");
    expect(() => checkOutDir(outDir, proc)).toThrow(
      `cannot tell whether pluxy (local build) is running: ${proc} cannot be listed (ENOENT)`,
    );
    expect(tree(outDir)).toEqual(before);
  });

  test("refuses when it cannot read the program of a process of this user's with no capability", () => {
    const outDir = join(tempDir(), "out");
    localBuildAt(outDir);
    const proc = noProcesses();
    // An exe that is no link cannot be read as one; the entry is this user's, as a process's is.
    const entry = (pid: string, caps: string) => {
      mkdirSync(join(proc, pid));
      writeFileSync(join(proc, pid, "exe"), "");
      writeFileSync(join(proc, pid, "status"), `Name:\tx\nCapInh:\t0000000000000000\nCapPrm:\t${caps}\n`);
    };
    // One holding a capability (as systemd --user does) cannot be the build's: it passes.
    entry("4646", "0000000800000000");
    expect(() => checkOutDir(outDir, proc)).not.toThrow();
    entry("4545", "0000000000000000");
    expect(() => checkOutDir(outDir, proc)).toThrow(
      `cannot tell whether pluxy (local build) is running: ${join(proc, "4545", "exe")} cannot be read (EINVAL)`,
    );
  });

  test("lets a build through on the real /proc, whose processes it may not read are not the build's", () => {
    // Process 1 is root's; a desktop session's systemd --user holds a capability and its (sd-pam)
    // gave up its dump rights: the kernel hides the program of each.
    const outDir = join(tempDir(), "out");
    localBuildAt(outDir);
    expect(() => checkOutDir(outDir)).not.toThrow();
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

  test("renames the stage in when there is no folder yet, or an empty one", () => {
    for (const empty of [false, true]) {
      const root = tempDir();
      const stage = join(root, "out.stage-1");
      mkdirSync(stage);
      writeFileSync(join(stage, "x"), "new");
      if (empty) mkdirSync(join(root, "out"));
      swapIn(stage, join(root, "out"));
      expect(readdirSync(root)).toEqual(["out"]);
      expect(readFileSync(join(root, "out", "x"), "utf8")).toBe("new");
    }
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

  asUser("an old build that cannot be removed is named, with the new build and home folder in place", () => {
    const { outDir, stage } = staged();
    const home = tree(join(outDir, "home"));
    mkdirSync(join(outDir, "locked"));
    writeFileSync(join(outDir, "locked", "file"), "x");
    chmodSync(join(outDir, "locked"), 0o555);
    let said = "";
    try {
      swapIn(stage, outDir);
    } catch (e) {
      said = (e as Error).message;
    } finally {
      chmodSync(join(`${outDir}.old`, "locked"), 0o755);
    }
    expect(said).toStartWith(
      `the new build is in ${outDir} with its home folder, but the previous build could not be removed (EACCES`,
    );
    expect(said).toEndWith(`: delete ${outDir}.old, then build again`);
    expect(readFileSync(join(outDir, "bin", "pluxy-app-local"), "utf8")).toBe("new app");
    expect(tree(join(outDir, "home"))).toEqual(home);
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

describe("the final phase", () => {
  /** Runs localBuild on outDir in a process of its own, as the command does, with a build step that
   * writes a new build into the stage, the real rename, and no processes. when says what else
   * happens: "build" sends the process SIGTERM as the build step ends, "swap" sends it once the old
   * build is renamed, and "replace" puts another folder in outDir's place just before that rename. */
  function finalPhase(outDir: string, when: "build" | "swap" | "replace") {
    const script = join(tempDir(), "final-phase.ts");
    writeFileSync(
      script,
      `import { mkdirSync, renameSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { interrupted, localBuild } from ${JSON.stringify(join(app, "package.ts"))};
const [outDir, when, proc] = process.argv.slice(2) as [string, string, string];
try {
  await localBuild(outDir, {
    proc,
    build: async (stage, version, program) => {
      mkdirSync(join(stage, "bin"));
      mkdirSync(join(stage, "lib", "pluxy Local", "core", "bundled"), { recursive: true });
      writeFileSync(join(stage, "bin", "pluxy-app-local"), "new app");
      writeFileSync(join(stage, "bin", "pluxy"), "new core");
      writeFileSync(join(stage, "lib", "pluxy Local", "core", "bundled", "index.json"), "{}");
      writeFileSync(
        join(stage, "local-build.json"),
        JSON.stringify({ program, identifier: "dev.pluxy.desktop.local", product_name: "pluxy Local", bundled_core: { version, packages: [] } }),
      );
      if (when === "build") process.kill(process.pid, "SIGTERM");
      return { version, manifest: "", dir: "", packages: [], addOns: [] };
    },
    move: (from, to) => {
      if (when === "replace" && from === outDir) {
        renameSync(outDir, \`\${outDir}-aside\`);
        mkdirSync(outDir);
        writeFileSync(join(outDir, "notes"), "mine");
      }
      renameSync(from, to);
      if (when === "swap" && to === \`\${outDir}.old\`) process.kill(process.pid, "SIGTERM");
    },
  });
} catch (e) {
  console.error(\`package local: \${(e as Error).message}\`);
  process.exitCode = interrupted ? 130 : 1;
}
`,
    );
    return spawnSync(process.execPath, [script, outDir, when, noProcesses()], { encoding: "utf8" });
  }

  test("SIGTERM as the build ends, before the swap: exit 130, the old build stays, no stage", () => {
    const root = tempDir();
    const outDir = join(root, "out");
    localBuildAt(outDir);
    const before = tree(outDir);
    const r = finalPhase(outDir, "build");
    expect(r.stderr).toContain("package local: stopped by SIGTERM");
    expect(r.stderr).toContain(`package local: the build failed; ${outDir} is as it was`);
    expect(r.status).toBe(130);
    expect(tree(outDir)).toEqual(before);
    expect(readdirSync(root)).toEqual(["out"]);
  });

  test("SIGTERM during the swap waits for it: exit 130, saying the new build is in place, home folder kept", () => {
    const root = tempDir();
    const outDir = join(root, "out");
    localBuildAt(outDir);
    const home = tree(join(outDir, "home"));
    const r = finalPhase(outDir, "swap");
    expect(r.stderr).toMatch(
      new RegExp(
        `package local: stopped by SIGTERM once the new build was in place: ${outDir} holds the new build, pluxy \\S+\\+local\\.[0-9a-f]{12}\\n`,
      ),
    );
    expect(r.status).toBe(130);
    expect(readdirSync(root)).toEqual(["out"]);
    expect(readFileSync(join(outDir, "bin", "pluxy-app-local"), "utf8")).toBe("new app");
    expect(tree(join(outDir, "home"))).toEqual(home);
  });

  test("a folder put in the build's place after its last check is not deleted: it and the build stay", () => {
    const root = tempDir();
    const outDir = join(root, "out");
    localBuildAt(outDir);
    const build = tree(outDir);
    const r = finalPhase(outDir, "replace");
    expect(r.stderr).toContain(
      `package local: not putting the new build in place: ${outDir} is no longer a local build (it changed while the build ran); it is as it was`,
    );
    expect(r.status).toBe(1);
    expect(tree(outDir)).toEqual({ notes: `file ${Buffer.from("mine").toString("base64")}` });
    expect(tree(`${outDir}-aside`)).toEqual(build);
    expect(readdirSync(root).sort()).toEqual(["out", "out-aside"]);
  });
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
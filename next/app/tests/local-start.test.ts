// The local build's start script (local-start.sh, copied into each build as start): the home
// folder it runs the app in, the trust profile and add-on source it writes, and what it refuses
// before starting anything. A stand-in app records the HOME and environment it was started with.
import { afterEach, describe, expect, test } from "bun:test";
import { spawnSync } from "node:child_process";
import {
  chmodSync,
  copyFileSync,
  existsSync,
  mkdirSync,
  mkdtempSync,
  readdirSync,
  readFileSync,
  realpathSync,
  rmSync,
  statSync,
  symlinkSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

/** A test that needs file modes and owners to hold: root passes them. */
const asUser = test.skipIf(process.getuid?.() === 0);

const script = join(import.meta.dir, "..", "local-start.sh");
const made: string[] = [];
afterEach(() => {
  for (const dir of made.splice(0)) {
    chmodSync(dir, 0o700);
    rmSync(dir, { recursive: true, force: true });
  }
});

function tempDir(): string {
  const dir = realpathSync(mkdtempSync(join(tmpdir(), "pluxy-local-start-test-")));
  made.push(dir);
  return dir;
}

const keyId = "local-build-00000000-0000-4000-8000-000000000000";
const publicKey = "ab".repeat(32);

/** A local build's folder as start needs it, in a folder of its own named name: start, a stand-in
 * app that writes down what it was given, and the add-ons' index and publisher.json. */
function buildAt(name = "build"): string {
  const build = join(tempDir(), name);
  const tools = join(build, "lib", "pluxy Local", "app-tools");
  mkdirSync(join(build, "bin"), { recursive: true });
  mkdirSync(tools, { recursive: true });
  copyFileSync(script, join(build, "start"));
  chmodSync(join(build, "start"), 0o755);
  writeFileSync(
    join(build, "bin", "pluxy-app-local"),
    `#!/bin/sh\nenv > "$0.ran"\nprintf '%s\\n' "$@" > "$0.args"\n`,
  );
  chmodSync(join(build, "bin", "pluxy-app-local"), 0o755);
  writeFileSync(join(tools, "index.json"), '{"registry_version":0,"packages":{}}\n');
  writeFileSync(
    join(tools, "publisher.json"),
    `${JSON.stringify({ key_id: keyId, public_key: publicKey }, null, 2)}\n`,
  );
  return build;
}

/** Runs build's start with args and HOME home, in an environment with no XDG folders but those in
 * more. */
function start(build: string, home: string, args: string[] = [], more: Record<string, string> = {}) {
  const env: Record<string, string> = { PATH: process.env.PATH ?? "/usr/bin:/bin", HOME: home, ...more };
  const r = spawnSync(join(build, "start"), args, { env, encoding: "utf8" });
  return { status: r.status, stderr: r.stderr, stdout: r.stdout };
}

/** What the stand-in app was started with, or undefined when it was not started. */
function ran(build: string): Record<string, string> | undefined {
  const file = join(build, "bin", "pluxy-app-local.ran");
  if (!existsSync(file)) return undefined;
  const env: Record<string, string> = {};
  for (const line of readFileSync(file, "utf8").split("\n")) {
    const at = line.indexOf("=");
    if (at > 0) env[line.slice(0, at)] = line.slice(at + 1);
  }
  return env;
}

/** A home folder for --real-home, private unless mode says otherwise. */
function homeAt(mode = 0o700): string {
  const home = join(tempDir(), "home");
  mkdirSync(home);
  chmodSync(home, mode);
  return home;
}

const instead = ", or run start without --real-home to use the build's own home folder.";

describe("start", () => {
  test("runs the app in the build's own home folder, private, with the add-ons trusted from there", () => {
    const build = buildAt();
    const elsewhere = homeAt();
    const r = start(build, elsewhere, ["--some-flag"]);
    expect(r.stderr).toBe("");
    expect(r.status).toBe(0);
    const home = join(build, "home");
    const env = ran(build);
    expect(env?.HOME).toBe(home);
    expect(env?.XDG_CONFIG_HOME).toBe(join(home, ".config"));
    expect(env?.XDG_DATA_HOME).toBe(join(home, ".local", "share"));
    expect(env?.XDG_CACHE_HOME).toBe(join(home, ".cache"));
    expect(env?.XDG_STATE_HOME).toBe(join(home, ".local", "state"));
    expect(env?.PLUXY_TEST_TRUST_NONCE).toMatch(/^[0-9a-f]{64}$/);
    expect(readFileSync(join(build, "bin", "pluxy-app-local.args"), "utf8")).toBe("--some-flag\n");
    expect(statSync(home).mode & 0o777).toBe(0o700);
    const index = join(build, "lib", "pluxy Local", "app-tools", "index.json");
    expect(JSON.parse(readFileSync(join(home, ".pluxy-test-trust-roots.json"), "utf8"))).toEqual({
      roots: [{ kind: "test", publisher: "pluxy", source: index, key_id: keyId, public_key: publicKey }],
      updater: null,
    });
    expect(JSON.parse(readFileSync(join(home, ".pluxy-test-trust-profile.json"), "utf8"))).toEqual({
      format: 1,
      home,
      purpose: "pluxy-artifact-test-trust",
      nonce: env?.PLUXY_TEST_TRUST_NONCE,
    });
    for (const f of [".pluxy-test-trust-roots.json", ".pluxy-test-trust-profile.json"]) {
      expect(statSync(join(home, f)).mode & 0o077).toBe(0);
    }
    expect(
      JSON.parse(
        readFileSync(join(home, ".config", "dev.pluxy.desktop.local", "device-registries.json"), "utf8"),
      ),
    ).toEqual({ version: 0, registries: [{ name: "local-build", url: index }] });
    // Nothing of the HOME it was given is touched.
    expect(readdirSync(elsewhere)).toEqual([]);
  });

  test("keeps the nonce across starts, so an app already running keeps its trust", () => {
    const build = buildAt();
    expect(start(build, homeAt()).status).toBe(0);
    const first = ran(build)?.PLUXY_TEST_TRUST_NONCE;
    expect(start(build, homeAt()).status).toBe(0);
    expect(ran(build)?.PLUXY_TEST_TRUST_NONCE).toBe(first);
  });

  asUser("--real-home runs the app on a home folder only you can open, and says first what it writes", () => {
    const build = buildAt();
    const home = homeAt(0o700);
    const r = start(build, home, ["--real-home"]);
    expect(r.status).toBe(0);
    const config = join(home, ".config", "dev.pluxy.desktop.local");
    const data = join(home, ".local", "share", "dev.pluxy.desktop.local");
    expect(r.stderr).toBe(
      [
        `start: starting pluxy (local build) on your own home folder, ${home}.`,
        "It writes:",
        `  ${home}/.pluxy-test-trust-profile.json`,
        `  ${home}/.pluxy-test-trust-roots.json`,
        "      the trust profile: this build's add-on signing key, for its add-on index only",
        `  ${config}/`,
        "      the app's settings, with device-registries.json naming this build's add-ons",
        `  ${data}/`,
        "      the app's data: its pluxy, providers, installed add-ons, and the backups the tool pages keep",
        "and, only when you confirm Connect, Disconnect or Restore backup on a tool's page, that tool's own",
        "settings, such as ~/.claude/settings.json or ~/.codex/config.toml. Connect points that tool at",
        "this build's pluxy, away from any other pluxy it used.",
        "To remove it: press Disconnect on each tool page you connected (its backups are in the app's data),",
        "quit the app, then:",
        `  rm -f '${home}/.pluxy-test-trust-profile.json' '${home}/.pluxy-test-trust-roots.json'`,
        `  rm -rf '${config}' '${data}'`,
        "",
      ].join("\n"),
    );
    const env = ran(build);
    expect(env?.HOME).toBe(home);
    expect(env?.XDG_CONFIG_HOME).toBeUndefined();
    expect(existsSync(join(home, ".pluxy-test-trust-roots.json"))).toBe(true);
    expect(existsSync(join(config, "device-registries.json"))).toBe(true);
    expect(existsSync(join(build, "home"))).toBe(false);
  });

  /** start refused: exit 1, the message, the app not started and nothing written. */
  function refused(
    build: string,
    home: string,
    args: string[],
    said: string,
    more: Record<string, string> = {},
  ) {
    const folder = existsSync(home) && statSync(home).isDirectory();
    const before = folder ? readdirSync(home) : [];
    const r = start(build, home, args, more);
    expect(r.stderr).toBe(`start: ${said}\n`);
    expect(r.status).toBe(1);
    expect(ran(build)).toBeUndefined();
    if (folder) expect(readdirSync(home)).toEqual(before);
  }

  asUser("--real-home refuses a home folder other users can open, naming the fix", () => {
    for (const mode of [0o755, 0o750, 0o701]) {
      const build = buildAt();
      const home = homeAt(mode);
      const shown = mode.toString(8);
      refused(
        build,
        home,
        ["--real-home"],
        `not starting: other users can open your home folder ${home} (mode ${shown}). The add-ons are trusted only from a home only you can open: run chmod 700 '${home}'${instead}`,
      );
    }
  });

  asUser("--real-home refuses a home folder that is a link, even to a private folder", () => {
    const build = buildAt();
    const home = homeAt(0o700);
    const link = join(tempDir(), "link");
    symlinkSync(home, link);
    refused(
      build,
      link,
      ["--real-home"],
      `not starting: your home folder ${link} is a link or not a folder${instead}`,
    );
    expect(readdirSync(home)).toEqual([]);
  });

  asUser("--real-home refuses a home folder that is not a folder", () => {
    const build = buildAt();
    const home = join(tempDir(), "file");
    writeFileSync(home, "");
    refused(
      build,
      home,
      ["--real-home"],
      `not starting: your home folder ${home} is a link or not a folder${instead}`,
    );
  });

  asUser("--real-home refuses a home folder that belongs to another user", () => {
    // A folder of root's that this user cannot change.
    const home = ["/usr", "/etc", "/"].find((p) => statSync(p).uid !== process.getuid?.());
    expect(home).toBeDefined();
    const build = buildAt();
    refused(
      build,
      home as string,
      ["--real-home"],
      `not starting: your home folder ${home} belongs to another user${instead}`,
    );
  });

  test("--real-home refuses a HOME that is not an absolute path", () => {
    const build = buildAt();
    refused(
      build,
      "relative/home",
      ["--real-home"],
      `not starting: your home folder (relative/home) is not an absolute path${instead}`,
    );
  });

  test("refuses a build folder whose path holds a quote, a backslash or a control character", () => {
    for (const name of ['with"quote', "with\\backslash", "with\ttab", "with\nnewline"]) {
      const build = buildAt(name);
      refused(
        build,
        homeAt(),
        [],
        `not starting: the build's path (${build}) holds a quote, backslash or control character: build it into another folder with make app-local LOCAL=<folder>`,
      );
      // Refused before it makes the build's home folder.
      expect(existsSync(join(build, "home"))).toBe(false);
    }
  });

  asUser(
    "--real-home refuses a home folder whose path holds a quote, a backslash or a control character",
    () => {
      for (const name of ['with"quote', "with\\backslash", "with\u0001control"]) {
        const build = buildAt();
        const home = join(tempDir(), name);
        mkdirSync(home, { mode: 0o700 });
        refused(
          build,
          home,
          ["--real-home"],
          `not starting: the path of your home folder (${home}) holds a quote, backslash or control character${instead}`,
        );
      }
    },
  );

  test("leaves an add-on source file that lists other sources as it is, and says what to add", () => {
    const build = buildAt();
    const config = join(build, "home", ".config", "dev.pluxy.desktop.local");
    mkdirSync(config, { recursive: true });
    const file = join(config, "device-registries.json");
    const other = '{"version":0,"registries":[{"name":"mine","url":"/somewhere/index.json"}]}\n';
    writeFileSync(file, other);
    const r = start(build, homeAt());
    const index = join(build, "lib", "pluxy Local", "app-tools", "index.json");
    expect(r.stderr).toBe(
      `start: this build's add-ons will not show in Add-ons: ${file} lists other add-on sources. Add {"name":"local-build","url":"${index}"} to its "registries", then start again.\n`,
    );
    expect(r.status).toBe(0);
    expect(readFileSync(file, "utf8")).toBe(other);
    expect(ran(build)).toBeDefined();
  });

  test("refuses a build whose add-ons are missing, before writing anything", () => {
    const build = buildAt();
    rmSync(join(build, "lib", "pluxy Local", "app-tools", "publisher.json"));
    refused(
      build,
      homeAt(),
      [],
      `${join(build, "lib", "pluxy Local", "app-tools")} is incomplete: build again with make app-local`,
    );
    expect(existsSync(join(build, "home"))).toBe(false);
  });
  /** A folder name that a shell would act on if it read it: a command substitution, a backquoted
   * command, a variable, spaces and a single quote. Each command would make a file in the folder it
   * runs in. */
  const hostile = "it's a $(touch dollar) `touch tick` $HOME end";

  /** Runs each of commands with sh in a folder of its own, with HOME set to a decoy folder holding
   * the same names the commands name, and returns that folder and the decoy. */
  function runPrinted(commands: string[]) {
    const cwd = tempDir();
    const decoy = tempDir();
    mkdirSync(join(decoy, ".config", "dev.pluxy.desktop.local"), { recursive: true });
    writeFileSync(join(decoy, ".pluxy-test-trust-roots.json"), "decoy");
    for (const c of commands) {
      const r = spawnSync("sh", ["-c", c], {
        cwd,
        env: { PATH: process.env.PATH ?? "/usr/bin:/bin", HOME: decoy },
        encoding: "utf8",
      });
      expect(r.stderr).toBe("");
      expect(r.status).toBe(0);
    }
    return { cwd, decoy };
  }

  /** What a command run by runPrinted left: no file made by a command in a path, the decoy as it was. */
  function inert(ran: { cwd: string; decoy: string }) {
    expect(readdirSync(ran.cwd)).toEqual([]);
    expect(readdirSync(ran.decoy).sort()).toEqual([".config", ".pluxy-test-trust-roots.json"]);
    expect(existsSync(join(ran.decoy, ".config", "dev.pluxy.desktop.local"))).toBe(true);
  }

  asUser(
    "--real-home prints removal commands that, run, remove exactly what it wrote, whatever the paths hold",
    () => {
      for (const xdg of [false, true]) {
        const build = buildAt();
        const home = join(tempDir(), hostile);
        mkdirSync(home, { mode: 0o700 });
        writeFileSync(join(home, "keep"), "mine");
        const more: Record<string, string> = {};
        if (xdg) {
          more.XDG_CONFIG_HOME = join(tempDir(), `config ${hostile}`);
          more.XDG_DATA_HOME = join(tempDir(), `data ${hostile}`);
        }
        const r = start(build, home, ["--real-home"], more);
        expect(r.status).toBe(0);
        const config = join(more.XDG_CONFIG_HOME ?? join(home, ".config"), "dev.pluxy.desktop.local");
        const data = join(more.XDG_DATA_HOME ?? join(home, ".local", "share"), "dev.pluxy.desktop.local");
        // The app makes its data folder; the stand-in does not.
        mkdirSync(data, { recursive: true });
        writeFileSync(join(data, "cores"), "");
        expect(existsSync(join(config, "device-registries.json"))).toBe(true);
        const commands = r.stderr.split("\n").filter((l) => l.startsWith("  rm "));
        expect(commands).toHaveLength(2);
        inert(runPrinted(commands));
        for (const gone of [
          join(home, ".pluxy-test-trust-profile.json"),
          join(home, ".pluxy-test-trust-roots.json"),
          config,
          data,
        ]) {
          expect(existsSync(gone)).toBe(false);
        }
        expect(readFileSync(join(home, "keep"), "utf8")).toBe("mine");
      }
    },
  );

  asUser(
    "--real-home's chmod for a home others can open is one quoted word: run, it fixes that folder only",
    () => {
      const build = buildAt();
      const home = join(tempDir(), hostile);
      mkdirSync(home);
      chmodSync(home, 0o755);
      const r = start(build, home, ["--real-home"]);
      expect(r.status).toBe(1);
      const command = /: run (chmod 700 '.*'), or run start/.exec(r.stderr)?.[1];
      expect(command).toBe(`chmod 700 '${home.replaceAll("'", "'\\''")}'`);
      inert(runPrinted([command as string]));
      expect(statSync(home).mode & 0o777).toBe(0o700);
      expect(ran(build)).toBeUndefined();
    },
  );

  asUser(
    "--real-home refuses an XDG settings or data folder whose path holds a quote, a backslash or a control character, before printing anything",
    () => {
      for (const name of ["XDG_CONFIG_HOME", "XDG_DATA_HOME"]) {
        for (const bad of ['with"quote', "with\\backslash", "with\ttab"]) {
          const build = buildAt();
          const home = homeAt();
          const path = join(tempDir(), bad);
          refused(
            build,
            home,
            ["--real-home"],
            `not starting: the path in ${name} (${path}) holds a quote, backslash or control character${instead}`,
            { [name]: path },
          );
          expect(existsSync(path)).toBe(false);
        }
      }
    },
  );

  asUser(
    "--real-home takes a relative XDG folder as the app does: not at all, the folder under HOME instead",
    () => {
      const build = buildAt();
      const home = homeAt();
      const r = start(build, home, ["--real-home"], {
        XDG_CONFIG_HOME: "relative/config",
        XDG_DATA_HOME: "relative/data",
      });
      expect(r.status).toBe(0);
      const config = join(home, ".config", "dev.pluxy.desktop.local");
      const data = join(home, ".local", "share", "dev.pluxy.desktop.local");
      expect(r.stderr).toContain(`  rm -rf '${config}' '${data}'\n`);
      expect(existsSync(join(config, "device-registries.json"))).toBe(true);
    },
  );

  /** Runs start on a build whose app already has an add-on source file holding text, and returns
   * what start said, with the file's path and the build's index. */
  function withSources(text: (index: string) => string) {
    const build = buildAt();
    const index = join(build, "lib", "pluxy Local", "app-tools", "index.json");
    const config = join(build, "home", ".config", "dev.pluxy.desktop.local");
    mkdirSync(config, { recursive: true });
    const file = join(config, "device-registries.json");
    writeFileSync(file, text(index));
    const r = start(build, homeAt());
    expect(r.status).toBe(0);
    expect(readFileSync(file, "utf8")).toBe(text(index));
    expect(ran(build)).toBeDefined();
    return { said: r.stderr, file, index };
  }

  test("reads the add-on source file's registries as JSON: the index named only in other fields is not a source", () => {
    const r = withSources((index) =>
      JSON.stringify({
        version: 0,
        note: index,
        registries: [{ name: index, url: "/elsewhere/index.json", mirror: { url: index } }],
        old: { registries: [{ name: "local-build", url: index }] },
      }),
    );
    expect(r.said).toBe(
      `start: this build's add-ons will not show in Add-ons: ${r.file} lists other add-on sources. Add {"name":"local-build","url":"${r.index}"} to its "registries", then start again.\n`,
    );
  });

  test("says nothing when one of the file's registries has the build's index as its url, however the file is laid out", () => {
    for (const text of [
      (index: string) =>
        `${JSON.stringify(
          {
            version: 0,
            registries: [
              { name: "mine", url: "https://example.invalid/index.json" },
              { url: index, name: "here" },
            ],
          },
          null,
          2,
        )}\n`,
      (index: string) =>
        `{"registries":[{"name":"local-build","url":"${index.replaceAll("/", "\\/")}"}],"version":0}`,
    ]) {
      expect(withSources(text).said).toBe("");
    }
  });

  test("says so when the add-on source file is not JSON", () => {
    const r = withSources((index) => `{"version":0,"registries":[{"name":"local-build","url":"${index}"}`);
    expect(r.said).toBe(
      `start: this build's add-ons will not show in Add-ons: ${r.file} cannot be read as JSON. Fix it or move it away, then start again.\n`,
    );
  });
});

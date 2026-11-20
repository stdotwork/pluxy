// The environment block's logic (src/environment.ts): the selection kept in the window's storage,
// pasted WSL paths, the launcher's history key, paths in a WSL home and the refusals the pages
// recognise.
import { describe, expect, test } from "bun:test";
import {
  type EnvRecord,
  encodeSelection,
  historyKey,
  inHome,
  linuxBuildMissing,
  linuxPath,
  linuxPlatform,
  notInWsl,
  parseSelection,
  prefKey,
  recordOf,
  sameSelection,
  usersInOrder,
} from "../src/environment";

const ubuntu = { wsl: { distro: "Ubuntu", user: "alice" } };

const record = (distro: string, user: string, home = `/home/${user}`): EnvRecord => ({
  distro,
  user,
  uid: 1000,
  home,
  arch: "amd64",
  wsl: 2,
  checked_at: 1,
});

describe("the kept selection", () => {
  test("is kept per client package under environment.<package>", () => {
    expect(prefKey("pluxy/claude-code")).toBe("environment.pluxy/claude-code");
    expect(prefKey("pluxy/codex")).toBe("environment.pluxy/codex");
  });

  test("a WSL selection round-trips; Windows local is kept as nothing", () => {
    const raw = encodeSelection(ubuntu);
    expect(raw).not.toBeNull();
    expect(JSON.parse(raw as string)).toEqual(ubuntu);
    expect(parseSelection(raw)).toEqual(ubuntu);
    expect(encodeSelection(null)).toBeNull();
    expect(parseSelection(null)).toBeNull();
  });

  test("anything else in storage reads as Windows local", () => {
    for (const raw of [
      "",
      "not json",
      "null",
      "[]",
      '"Ubuntu"',
      "{}",
      '{"wsl":null}',
      '{"wsl":{"distro":"Ubuntu"}}',
      '{"wsl":{"distro":"","user":"alice"}}',
      '{"wsl":{"distro":"Ubuntu","user":""}}',
      '{"wsl":{"distro":"Ubuntu","user":7}}',
    ]) {
      expect(parseSelection(raw)).toBeNull();
    }
  });

  test("only the distribution and user are kept, nothing else stored beside them", () => {
    expect(parseSelection('{"wsl":{"distro":"Ubuntu","user":"alice","home":"/x"},"extra":1}')).toEqual(
      ubuntu,
    );
  });

  test("two selections are the same when they name the same distribution and user", () => {
    expect(sameSelection(ubuntu, { wsl: { distro: "Ubuntu", user: "alice" } })).toBe(true);
    expect(sameSelection(ubuntu, { wsl: { distro: "Ubuntu", user: "bob" } })).toBe(false);
    expect(sameSelection(ubuntu, { wsl: { distro: "Debian", user: "alice" } })).toBe(false);
    expect(sameSelection(ubuntu, null)).toBe(false);
    expect(sameSelection(null, null)).toBe(true);
  });

  test("the host's record of the selection, if it has one", () => {
    const records = [record("Debian", "alice"), record("Ubuntu", "bob"), record("Ubuntu", "alice")];
    expect(recordOf(records, ubuntu)).toBe(records[2]);
    expect(recordOf(records, { wsl: { distro: "Ubuntu", user: "carol" } })).toBeUndefined();
    expect(recordOf(records, null)).toBeUndefined();
    expect(recordOf(undefined, ubuntu)).toBeUndefined();
  });
});

describe("a pasted WSL path", () => {
  const converted = (path: string) => ({ path, converted: true });

  test("for the selected distribution becomes its Linux path", () => {
    expect(linuxPath("\\\\wsl.localhost\\Ubuntu\\home\\alice\\.claude\\settings.json", "Ubuntu")).toEqual(
      converted("/home/alice/.claude/settings.json"),
    );
    expect(linuxPath("\\\\wsl$\\Ubuntu\\home\\alice\\project", "Ubuntu")).toEqual(
      converted("/home/alice/project"),
    );
  });

  test("in any case and with either slash", () => {
    expect(linuxPath("//WSL.LOCALHOST/Ubuntu/home/alice", "Ubuntu")).toEqual(converted("/home/alice"));
    expect(linuxPath("\\\\Wsl$/Ubuntu\\home/alice", "Ubuntu")).toEqual(converted("/home/alice"));
    expect(linuxPath("\\\\wsl.localhost\\ubuntu\\home\\alice", "Ubuntu")).toEqual(converted("/home/alice"));
  });

  test("keeps the Linux path's own case and drops empty parts and surrounding spaces", () => {
    expect(linuxPath("  \\\\wsl$\\Ubuntu\\Home\\Alice\\\\My Project\\  ", "Ubuntu")).toEqual(
      converted("/Home/Alice/My Project"),
    );
  });

  test("of the distribution itself is its root folder", () => {
    expect(linuxPath("\\\\wsl.localhost\\Ubuntu", "Ubuntu")).toEqual(converted("/"));
    expect(linuxPath("\\\\wsl$\\Ubuntu\\", "Ubuntu")).toEqual(converted("/"));
  });

  test("of another distribution, or any other UNC path, is refused", () => {
    for (const p of [
      "\\\\wsl.localhost\\Debian\\home\\alice",
      "\\\\wsl$\\Ubuntu-22.04\\home\\alice",
      "\\\\server\\share\\settings.json",
      "//server/share",
      "\\\\wsl.localhost",
      "\\\\wsl$\\",
      "\\\\?\\UNC\\wsl.localhost\\Ubuntu\\home",
      "\\\\.\\C:\\Users",
    ]) {
      expect(linuxPath(p, "Ubuntu")).toEqual({ refused: true });
    }
  });

  test("a Linux path, or anything that is not a UNC path, is left as it is", () => {
    for (const p of [
      "/home/alice/project",
      "~/project",
      "relative/file.json",
      "",
      "C:\\Users\\alice",
      "\\home",
    ]) {
      expect(linuxPath(p, "Ubuntu")).toEqual({ path: p, converted: false });
    }
  });
});

describe("paths in the selected environment", () => {
  test("recent folders are keyed by the client, and in WSL by distribution and user too", () => {
    expect(historyKey("Claude Code", null)).toBe("Claude Code");
    expect(historyKey("Claude Code", ubuntu)).toBe("Claude Code (WSL Ubuntu/alice)");
  });

  test("a path in a WSL user's home", () => {
    expect(inHome("/home/alice", ".claude/settings.json")).toBe("/home/alice/.claude/settings.json");
    expect(inHome("/home/alice/", ".claude/settings.json")).toBe("/home/alice/.claude/settings.json");
    expect(inHome("/root", "project")).toBe("/root/project");
    expect(inHome("/", "project")).toBe("/project");
  });

  test("the Linux build a record's distribution needs", () => {
    expect(linuxPlatform(record("Ubuntu", "alice"))).toBe("linux/amd64");
    expect(linuxPlatform({ ...record("Ubuntu", "alice"), arch: "arm64" })).toBe("linux/arm64");
  });

  test("users are listed with root last", () => {
    const u = (name: string, uid: number, root = false) => ({
      name,
      uid,
      home: root ? "/root" : `/home/${name}`,
      default: false,
      root,
    });
    const users = [u("root", 0, true), u("bob", 1001), u("alice", 1000)];
    expect(usersInOrder(users).map((x) => x.name)).toEqual(["bob", "alice", "root"]);
    expect(users.map((x) => x.name)).toEqual(["root", "bob", "alice"]);
  });
});

describe("refusals the pages recognise", () => {
  test("a missing Linux build of the package", () => {
    expect(linuxBuildMissing("pluxy/claude-code's Linux build for linux/amd64 is not installed")).toBe(true);
    expect(
      linuxBuildMissing("The Linux build of pluxy/claude-code is not installed: install it for WSL"),
    ).toBe(true);
  });

  test("other refusals are not a missing Linux build", () => {
    expect(linuxBuildMissing(undefined)).toBe(false);
    expect(linuxBuildMissing("pluxy/claude-code is not installed on this device")).toBe(false);
    expect(linuxBuildMissing("pluxy has no Linux build for riscv64")).toBe(false);
    expect(linuxBuildMissing("pluxy/claude-code does not run in WSL")).toBe(false);
  });

  test("a package that does not run in WSL", () => {
    expect(notInWsl("pluxy/claude-desktop does not run in WSL")).toBe(true);
    expect(notInWsl("pluxy/claude-code is not installed on this device")).toBe(false);
    expect(notInWsl(undefined)).toBe(false);
  });
});

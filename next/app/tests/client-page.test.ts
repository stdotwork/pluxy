// What the tool pages share (src/clientPage.tsx): the keys a page offers from the vault, the key
// Connect sends, how an add-on stands, and whether a tool counts as installed.
import { describe, expect, test } from "bun:test";
import { addonState, installedOf, keyFor, keysOf, readingState, usableKeys } from "../src/clientPage";

describe("keys", () => {
  test("lists plain secrets and this package's client keys from vault_secrets' answer", () => {
    const body = {
      secrets: [
        { id: "a", label: "Plain" },
        { id: "b", label: "Ours", client_key: { client: "pluxy/claude-code" } },
        { id: "c", label: "Codex's", client_key: { client: "pluxy/codex-setup" } },
      ],
    };
    expect(keysOf("pluxy/claude-code", body).map((s) => s.id)).toEqual(["a", "b"]);
    expect(keysOf("pluxy/codex-setup", body).map((s) => s.id)).toEqual(["a", "c"]);
  });

  test("lists nothing while the answer is missing or empty", () => {
    expect(keysOf("pluxy/claude-code", undefined)).toEqual([]);
    expect(keysOf("pluxy/claude-code", { secrets: [] })).toEqual([]);
    expect(keysOf("pluxy/claude-code", {})).toEqual([]);
  });
});

describe("the key Connect sends", () => {
  const HERE = "http://127.0.0.1:8317";
  const bound = (id: string, created: number, origin = HERE, client = "pluxy/claude-code") => ({
    id,
    label: "Claude Code",
    created,
    client_key: { client, core: { mode: "local", origin } },
  });
  const plain = { id: "plain", label: "My key" };
  const secrets = [
    plain,
    bound("old", 1),
    bound("new", 2),
    bound("away", 3, "http://127.0.0.1:9999"),
    bound("codex", 4, HERE, "pluxy/codex-setup"),
  ];

  test("only keys bound to this add-on and this pluxy, and plain secrets, can be used", () => {
    expect(usableKeys("pluxy/claude-code", HERE, secrets).map((s) => s.id)).toEqual(["plain", "old", "new"]);
    // The same pluxy, written another way, is the same origin.
    expect(usableKeys("pluxy/claude-code", `${HERE}/`, secrets).map((s) => s.id)).toEqual([
      "plain",
      "old",
      "new",
    ]);
    expect(usableKeys("pluxy/claude-code", undefined, secrets).map((s) => s.id)).toEqual(["plain"]);
  });

  test("the chosen key while usable, else the newest bound one; a plain secret only when chosen", () => {
    expect(keyFor("pluxy/claude-code", HERE, secrets, "")?.id).toBe("new");
    expect(keyFor("pluxy/claude-code", HERE, secrets, "old")?.id).toBe("old");
    expect(keyFor("pluxy/claude-code", HERE, secrets, "plain")?.id).toBe("plain");
    expect(keyFor("pluxy/claude-code", HERE, secrets, "away")?.id).toBe("new");
    expect(keyFor("pluxy/claude-code", HERE, [plain], "")).toBeUndefined();
  });
});

describe("the add-on", () => {
  const catalog = (enabled: boolean, authorized = true, safe = false) =>
    ({
      safe_mode: safe,
      packages: [{ id: "pluxy/claude-code", enabled, authorized, tier: "native" }],
    }) as never;

  test("on, off, not yet allowed, missing, paused, or not read yet", () => {
    expect(addonState(catalog(true), "pluxy/claude-code")).toBe("on");
    expect(addonState(catalog(false), "pluxy/claude-code")).toBe("off");
    expect(addonState(catalog(true, false), "pluxy/claude-code")).toBe("off");
    expect(addonState(catalog(true), "pluxy/codex-setup")).toBe("missing");
    expect(addonState(catalog(true, true, true), "pluxy/claude-code")).toBe("safe");
    expect(addonState(undefined, "pluxy/claude-code")).toBe("loading");
  });
});

describe("installed, and K3's state word", () => {
  test("settings to read mean installed; otherwise pluxy's look, or nobody knows", () => {
    for (const state of ["configured", "needs-update", "refused"] as const) {
      expect(installedOf(undefined, state)).toBe(true);
      expect(installedOf(false, state)).toBe(true);
    }
    expect(installedOf(true, "not-configured")).toBe(true);
    expect(installedOf(false, "not-configured")).toBe(false);
    expect(installedOf(undefined, "not-configured")).toBeUndefined();
  });

  test("each reading reads as one of K3's states", () => {
    expect(readingState(undefined, { state: "configured" })).toBe("configured");
    expect(readingState(undefined, { state: "refused" })).toBe("refused");
    expect(readingState(true, { state: "not-configured" })).toBe("not-configured");
    expect(readingState(false, { state: "not-configured" })).toBe("not_installed");
    expect(readingState(undefined, { state: "not-configured" })).toBe("unchecked");
  });
});

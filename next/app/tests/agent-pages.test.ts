// The agent client pages Beginner Mode can set up (src/agentPages.ts): exactly the eight clients,
// each naming its tool's package, in the window's page order, with none listed twice.
// And what the Setup page gets from them: each client of core's scan has a page here, and "Set up"
// is offered for the ones Beginner Mode would list.
import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { agentPages, SETUP_CLIENT_PAGES, setupClientAction, setupOffersNeeded } from "../src/agentPages";
import type { DeviceCatalog, DevicePackage } from "../src/device";

describe("agentPages", () => {
  test("holds every set-up client, each with its tool's package ID, in the window's order", () => {
    expect(agentPages().map((p) => [p.id, p.package])).toEqual([
      ["claude-code", "pluxy/claude-code"],
      ["codex", "pluxy/codex-setup"],
      ["claude-desktop", "pluxy/claude-desktop"],
      ["opencode", "pluxy/opencode"],
      ["omp", "pluxy/omp-setup"],
      ["grok-build", "pluxy/grok-setup"],
      ["hermes", "pluxy/hermes-setup"],
      ["pi", "pluxy/pi-setup"],
    ]);
  });

  test("no page ID and no package ID is listed twice", () => {
    const pages = agentPages();
    expect(new Set(pages.map((p) => p.id)).size).toBe(pages.length);
    expect(new Set(pages.map((p) => p.package)).size).toBe(pages.length);
  });
});

describe("setupClientAction", () => {
  const catalog = (...ids: string[]): DeviceCatalog => ({
    safe_mode: false,
    packages: ids.map(
      (id) => ({ id, enabled: true, authorized: true, tier: "native" }) as unknown as DevicePackage,
    ),
  });

  test("every client of core's scan names a page that exists", () => {
    // The clients core's scan looks for, read from its table (clientSpecs), so a client core adds
    // or renames fails here until this window names its page.
    const source = readFileSync(join(import.meta.dir, "../../internal/core/setup.go"), "utf8");
    const table = /\nvar clientSpecs = \[\]clientProbeSpec\{\n([\s\S]*?)\n\}\n/.exec(source)?.[1] ?? "";
    const scanned = [...table.matchAll(/^\t\tid:\s+"([^"]+)",$/gm)].map((m) => m[1]);
    expect(scanned.length).toBeGreaterThan(0);
    expect(Object.keys(SETUP_CLIENT_PAGES).sort()).toEqual(scanned.sort());
    const ids = agentPages().map((p) => p.id);
    for (const page of Object.values(SETUP_CLIENT_PAGES)) expect(ids).toContain(page);
  });

  test("Set up opens the page of a client whose tool is installed or offered, and no other", () => {
    const agents = agentPages();
    const offers = [{ name: "curated", packages: [{ id: "pluxy/codex-setup", versions: ["1.0.0"] }] }];
    const { clientAction, clientActionHint } = setupClientAction(
      agents,
      catalog("pluxy/claude-code"),
      offers,
    );
    expect(clientAction?.("claude-code")).toEqual({ label: "Set up", page: "claude-code" });
    expect(clientAction?.("codex")).toEqual({ label: "Set up", page: "codex" });
    // Pi's tool is neither installed nor offered; core may name a client this window has no page for.
    expect(clientAction?.("pi")).toBeUndefined();
    expect(clientAction?.("some-new-client")).toBeUndefined();
    expect(clientAction?.("constructor")).toBeUndefined();
    expect(clientActionHint).toContain("Set up opens the client's own page");
    // With no tool on this device and no registry, nothing is offered.
    expect(setupClientAction(agents).clientAction?.("claude-code")).toBeUndefined();
  });
});

describe("setupOffersNeeded", () => {
  test("the registries are read only for a signed-in window whose core runs on this computer", () => {
    expect(setupOffersNeeded({ target: "http://127.0.0.1:1", signed_in: true, mode: "managed" })).toBe(true);
    expect(setupOffersNeeded({ target: "http://127.0.0.1:1", signed_in: true, mode: "local" })).toBe(true);
    // Not signed in or not connected: the window shows no Setup page.
    expect(setupOffersNeeded({ target: "http://127.0.0.1:1", signed_in: false, mode: "local" })).toBe(false);
    expect(setupOffersNeeded({ signed_in: true, mode: "local" })).toBe(false);
    // Core on another machine: Setup offers no "Set up" there.
    expect(setupOffersNeeded({ target: "https://core.example", signed_in: true, mode: "remote" })).toBe(
      false,
    );
  });
});

// The agent client pages Beginner Mode can set up (src/agentPages.ts): each names a package that
// a tool's manifest declares, with none listed twice.
// And what the Setup page gets from them: each client of core's scan has a page here, and "Set up"
// is offered for the ones Beginner Mode would list.
import { describe, expect, test } from "bun:test";
import { existsSync, readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import type { VNode } from "preact";
import type { SetupClient } from "../../ui/kit/api";
import {
  agentPages,
  SETUP_CLIENT_PAGES,
  setupClientAction,
  setupClientPage,
  setupOffersNeeded,
} from "../src/agentPages";
import type { DeviceCatalog, DevicePackage } from "../src/device";

describe("agentPages", () => {
  test("each page names the package of a tool manifest in app-tools", () => {
    // The tools' own manifests are the reference: a page naming a package no tool declares would
    // ask the device for a program that is never there.
    const tools = join(import.meta.dir, "../../app-tools");
    const declared = readdirSync(tools, { withFileTypes: true })
      .filter((d) => d.isDirectory() && existsSync(join(tools, d.name, "pluxy-extension.json")))
      .map((d) => JSON.parse(readFileSync(join(tools, d.name, "pluxy-extension.json"), "utf8")).id as string);
    expect(declared.length).toBeGreaterThan(0);
    const pages = agentPages();
    expect(pages.length).toBeGreaterThan(0);
    for (const page of pages) expect(declared).toContain(page.package);
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
    const found = (id: string, state: SetupClient["state"] = "not-configured") => ({ id, state });
    const page = (id: string, state?: SetupClient["state"]) =>
      setupClientPage(found(id, state), agents, catalog("pluxy/claude-code"), offers);
    expect(page("claude-code")).toBe("claude-code");
    expect(page("codex")).toBe("codex");
    // Pi's tool is neither installed nor offered; core may name a client this window has no page for.
    expect(page("pi")).toBeUndefined();
    expect(page("some-new-client")).toBeUndefined();
    expect(page("constructor")).toBeUndefined();
    // A file pluxy refused to change: the page here would not write it either.
    expect(page("claude-code", "refused")).toBeUndefined();
    // With no tool on this device and no registry, nothing is offered.
    expect(setupClientPage(found("claude-code"), agents)).toBeUndefined();
  });

  test("the Setup page's tool button is a Set up link to that page, and nothing where there is none", () => {
    const { toolAction } = setupClientAction(agentPages(), catalog("pluxy/claude-code"));
    const client = (id: string): SetupClient => ({
      id,
      name: id,
      installed: true,
      config: "",
      state: "not-configured",
    });
    const link = toolAction?.(client("claude-code")) as VNode<{ href: string; children: string }>;
    expect(link.type).toBe("a");
    expect(link.props.href).toBe("#/claude-code");
    expect(link.props.children).toBe("Set up");
    expect(toolAction?.(client("pi"))).toBeUndefined();
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

// The coding tools' setup pages (src/agentPages.ts): each names a package that a tool's manifest
// declares, with none listed twice. And what the Tools page and the setup's step 2 get from them:
// every tool of core's scan has a page here, and each row's button follows its add-on.
import { describe, expect, test } from "bun:test";
import { existsSync, readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import type { VNode } from "preact";
import type { SetupClient } from "../../ui/kit/api";
import {
  agentPages,
  movedTo,
  type RowAction,
  rowAction,
  SETUP_CLIENT_PAGES,
  setupOffersNeeded,
  toolAction,
} from "../src/agentPages";
import type { DeviceCatalog, DevicePackage } from "../src/device";
import { Checking } from "../src/toolRows";

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

describe("the tool pages", () => {
  test("none is a topic of any page: each opens under Tools", () => {
    for (const page of agentPages()) {
      expect(page.kind).toBeUndefined();
      expect(page.under).toBe("tools");
      expect(page.dest).toBe("tools");
    }
  });
});

describe("rows and their add-ons", () => {
  const catalog = (...packages: [string, boolean][]): DeviceCatalog => ({
    safe_mode: false,
    packages: packages.map(
      ([id, enabled]) => ({ id, enabled, authorized: true, tier: "native" }) as unknown as DevicePackage,
    ),
  });
  const agents = agentPages();
  const client = (id: string, state: SetupClient["state"] = "not-configured", installed = true) =>
    ({ id, name: id, installed, config: "", state }) as SetupClient;
  const offers = [{ name: "curated", packages: [{ id: "pluxy/codex-setup", versions: ["1.0.0"] }] }];
  const action = (c: SetupClient, view: Parameters<typeof rowAction>[2]): RowAction =>
    rowAction(c, agents, view);
  const on = catalog(["pluxy/claude-code", true]);

  test("every tool of core's scan, and the three the app reads, names a page that exists", () => {
    // The tools core's scan looks for, read from its table (clientSpecs), so a tool core adds or
    // renames fails here until this window names its page.
    const source = readFileSync(join(import.meta.dir, "../../internal/core/setup.go"), "utf8");
    const table = /\nvar clientSpecs = \[\]clientProbeSpec\{\n([\s\S]*?)\n\}\n/.exec(source)?.[1] ?? "";
    const scanned = [...table.matchAll(/^\t\tid:\s+"([^"]+)",$/gm)].map((m) => m[1]);
    expect(scanned.length).toBeGreaterThan(0);
    expect(Object.keys(SETUP_CLIENT_PAGES).sort()).toEqual(scanned.sort());
    expect(Object.keys(SETUP_CLIENT_PAGES)).toHaveLength(8);
    const ids = agents.map((p) => p.id);
    for (const page of Object.values(SETUP_CLIENT_PAGES)) expect(ids).toContain(page);
  });

  test("with the add-on on, the row opens the tool's page: Open when Connected, else Set up", () => {
    const view = { catalog: on, offers: [] };
    expect(action(client("claude-code", "configured"), view)).toEqual({
      kind: "link",
      page: "claude-code",
      label: "Open",
    });
    for (const state of ["not-configured", "needs-update", "unchecked"] as const)
      expect(action(client("claude-code", state), view)).toMatchObject({ kind: "link", label: "Set up" });
    // A tool the person has not installed is not Connected, whatever its settings say.
    expect(action(client("claude-code", "configured", false), view)).toMatchObject({ label: "Set up" });
  });

  test("a row whose settings pluxy refused still leads to its page", () => {
    expect(action(client("claude-code", "refused"), { catalog: on, offers: [] })).toEqual({
      kind: "link",
      page: "claude-code",
      label: "Set up",
    });
  });

  test("an add-on installed but off is turned on; one a registry offers is installed", () => {
    const off = catalog(["pluxy/claude-code", false]);
    expect(action(client("claude-code"), { catalog: off, offers: [] })).toEqual({
      kind: "turn-on",
      pkg: "pluxy/claude-code",
    });
    expect(action(client("codex"), { catalog: on, offers })).toEqual({
      kind: "install",
      pkg: "pluxy/codex-setup",
      registry: "curated",
      version: "1.0.0",
    });
  });

  test("while the device's add-ons or the registries are read, the row says it is checking", () => {
    expect(action(client("claude-code"), {})).toEqual({ kind: "checking" });
    expect(action(client("codex"), { catalog: on })).toEqual({ kind: "checking" });
    // An add-on that is there needs no registry.
    expect(action(client("claude-code"), { catalog: on })).toMatchObject({ kind: "link" });
  });

  test("with no add-on to be had, the app gives no button, and neither for a tool it has no page for", () => {
    expect(action(client("codex"), { catalog: on, offers: [] })).toEqual({ kind: "none" });
    expect(action(client("some-new-tool"), { catalog: on, offers })).toEqual({ kind: "none" });
    expect(toolAction(agents, { catalog: on, offers: [] }, () => {})(client("codex"))).toBeUndefined();
  });

  test("in safe mode, or when the add-ons cannot be read, only the page: it says what is needed", () => {
    const safe = { ...catalog(["pluxy/claude-code", false]), safe_mode: true };
    for (const view of [
      { catalog: safe, offers },
      { catalog: safe, offers: [] },
      { catalog: on, unread: true, offers },
    ])
      for (const id of ["claude-code", "codex"])
        expect(action(client(id), view)).toMatchObject({ kind: "link", page: id });
  });

  test("the button is a link to the page, quiet, and a checking row shows no button", () => {
    const run = toolAction(agents, { catalog: on, offers: [] }, () => {});
    const link = run(client("claude-code", "configured")) as VNode<{
      href: string;
      children: string;
      "data-variant": string;
    }>;
    expect(link.type).toBe("a");
    expect(link.props.href).toBe("#/claude-code");
    expect(link.props.children).toBe("Open");
    expect(link.props["data-variant"]).toBe("quiet");
    expect((toolAction(agents, {}, () => {})(client("pi")) as VNode).type).toBe(Checking);
  });
});

describe("old addresses", () => {
  test("Beginner Mode's address goes to the setup and Manage Clients' to Tools", () => {
    expect(movedTo("#/easy-mode")).toBe("#/setup");
    expect(movedTo("#easy-mode")).toBe("#/setup");
    expect(movedTo("#/clients")).toBe("#/tools");
    expect(movedTo("#/tools")).toBeUndefined();
    expect(movedTo("#/claude-code")).toBeUndefined();
    expect(movedTo("")).toBeUndefined();
  });
});

describe("setupOffersNeeded", () => {
  test("the registries are read only for a signed-in window whose core runs on this computer", () => {
    expect(setupOffersNeeded({ target: "http://127.0.0.1:1", signed_in: true, mode: "managed" })).toBe(true);
    expect(setupOffersNeeded({ target: "http://127.0.0.1:1", signed_in: true, mode: "local" })).toBe(true);
    // Not signed in or not connected: the window shows no Tools page.
    expect(setupOffersNeeded({ target: "http://127.0.0.1:1", signed_in: false, mode: "local" })).toBe(false);
    expect(setupOffersNeeded({ signed_in: true, mode: "local" })).toBe(false);
    // A pluxy elsewhere: no add-on is offered for its tools.
    expect(setupOffersNeeded({ target: "https://core.example", signed_in: true, mode: "remote" })).toBe(
      false,
    );
  });
});

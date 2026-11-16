// The agent client pages Beginner Mode can set up (src/agentPages.ts): exactly the eight clients,
// each naming its tool's package, in the window's page order, with none listed twice.
import { describe, expect, test } from "bun:test";
import { agentPages } from "../src/agentPages";

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

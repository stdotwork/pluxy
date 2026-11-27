// The Pi page's form logic (src/piForm.ts): the preview input for each place the files can be.
import { describe, expect, test } from "bun:test";
import { pluginNewer, selectionFor, validModel } from "../src/piForm";

describe("selection", () => {
  test("the default folder sends nothing, a selected folder itself", () => {
    expect(selectionFor("default", "/x")).toEqual({});
    expect(selectionFor("folder", " /home/p/.pi-root ")).toEqual({ folder: "/home/p/.pi-root" });
  });

  test("linked files are the three files in the folder, selected one by one", () => {
    expect(selectionFor("links", "/home/p/.pi-stow/")).toEqual({
      targets: [
        { file: "/home/p/.pi-stow/cliproxyapi.json" },
        { file: "/home/p/.pi-stow/settings.json" },
        { file: "/home/p/.pi-stow/cliproxyapi-models.json" },
      ],
    });
  });

  test("an empty folder sends empty paths, never files at the root", () => {
    expect(selectionFor("links", "  ")).toEqual({ targets: [{ file: "" }, { file: "" }, { file: "" }] });
  });
});

describe("model", () => {
  test("takes one model ID, without spaces or control characters, of at most 240 characters", () => {
    for (const ok of ["gpt-6.1-sol", "qwen3:8b", "x".repeat(240)]) expect(validModel(ok)).toBe(true);
    for (const bad of ["", "a b", "a\u0000b", "a\u0007b", "a\u0085b", "x".repeat(241)])
      expect(validModel(bad)).toBe(false);
  });
});

describe("the plugin", () => {
  test("Update plugin is for a newer copy only: never for the same or an older one", () => {
    expect(pluginNewer("1.4.24-pluxy.1", "1.4.25-pluxy.1")).toBe(true);
    expect(pluginNewer("1.4.25", "1.4.25-pluxy.1")).toBe(true);
    expect(pluginNewer("1.4.25-pluxy.1", "1.4.25-pluxy.2")).toBe(true);
    expect(pluginNewer("1.3.99-pluxy.9", "1.4.0-pluxy.1")).toBe(true);
    expect(pluginNewer("1.4.25-pluxy.1", "1.4.25-pluxy.1")).toBe(false);
    expect(pluginNewer("1.4.25-pluxy.2", "1.4.25-pluxy.1")).toBe(false);
    expect(pluginNewer("1.10.0-pluxy.1", "1.9.0-pluxy.1")).toBe(false);
    expect(pluginNewer("2.0.0", "1.4.25-pluxy.1")).toBe(false);
  });

  test("a copy whose version is not known can be updated", () => {
    expect(pluginNewer(null, "1.4.25-pluxy.1")).toBe(true);
    expect(pluginNewer("an old build", "1.4.25-pluxy.1")).toBe(true);
  });
});

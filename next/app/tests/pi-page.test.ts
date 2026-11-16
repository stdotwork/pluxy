// The Pi page's form logic (src/piForm.ts): the preview input for each place the files can be.
import { describe, expect, test } from "bun:test";
import { selectionFor } from "../src/piForm";

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

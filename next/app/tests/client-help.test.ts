// What the client pages say is missing (src/clientHelp.tsx, src/clientList.tsx): where a key comes
// from, why a button is off, when no raw code shows, and what the Manage Clients count counts.
import { describe, expect, test } from "bun:test";
import { folderWait, toolMissing } from "../src/clientHelp";
import { listedText } from "../src/clientList";

describe("a missing tool", () => {
  test("the native side's line for a tool this device does not have is covered by the notice", () => {
    expect(
      toolMissing(
        "extension_unavailable: pluxy/claude-code is not installed on this device",
        "pluxy/claude-code",
      ),
    ).toBe(true);
  });

  test("another tool's line, or any other error, is not", () => {
    expect(
      toolMissing(
        "extension_unavailable: pluxy/codex-setup is not installed on this device",
        "pluxy/claude-code",
      ),
    ).toBe(false);
    expect(toolMissing("refused: the file is not JSON", "pluxy/claude-code")).toBe(false);
    expect(toolMissing(undefined, "pluxy/claude-code")).toBe(false);
  });
});

describe("Start's wait", () => {
  test("names the folder while it is empty", () => {
    expect(folderWait("")).toContain("folder");
    expect(folderWait("  ")).toContain("folder");
    expect(folderWait("/work")).toBeUndefined();
  });
});

describe("Manage Clients count", () => {
  test("says the count is of clients in the navigation", () => {
    expect(listedText(0)).toBe("0 clients in the navigation");
    expect(listedText(1)).toBe("1 client in the navigation");
    expect(listedText(8)).toBe("8 clients in the navigation");
  });
});

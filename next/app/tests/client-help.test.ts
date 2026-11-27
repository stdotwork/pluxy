// What the tool pages say Start waits for (src/clientHelp.tsx).
import { describe, expect, test } from "bun:test";
import { folderWait } from "../src/clientHelp";

describe("Start's wait", () => {
  test("names the folder while it is empty", () => {
    expect(folderWait("")).toContain("folder");
    expect(folderWait("  ")).toContain("folder");
    expect(folderWait("/work")).toBeUndefined();
  });
});

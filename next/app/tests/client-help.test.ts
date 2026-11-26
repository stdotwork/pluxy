// What the tool pages say Start waits for (src/clientHelp.tsx), and what the Manage Clients count
// counts (src/clientList.tsx).
import { describe, expect, test } from "bun:test";
import { folderWait } from "../src/clientHelp";
import { listedText } from "../src/clientList";

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

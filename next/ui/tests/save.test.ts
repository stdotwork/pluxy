// Saving a file through a host that has its own way: the desktop app's window cannot download.
import { expect, test } from "bun:test";
import { saveFile, shownPath } from "../kit/save";

test("a host with its own way to save gets the file, and its answer or its reason comes back", async () => {
  const got: string[][] = [];
  const host = {
    saveFile: async (name: string, text: string) => {
      got.push([name, text]);
      return `/home/me/Downloads/${name}`;
    },
  };
  // No document here: a browser download would throw.
  expect(await saveFile(host, "nightfall.json", "{}\n", "application/json")).toBe(
    "/home/me/Downloads/nightfall.json",
  );
  expect(got).toEqual([["nightfall.json", "{}\n"]]);
  const refusing = { saveFile: () => Promise.reject(new Error("the Downloads folder is read-only")) };
  expect(saveFile(refusing, "a.json", "{}", "application/json")).rejects.toThrow(
    "the Downloads folder is read-only",
  );
});

test("in privacy mode the saved file's path names its folder and not the ones above, which hold the user name", () => {
  expect(shownPath("/home/me/Downloads/usage.csv", false)).toBe("/home/me/Downloads/usage.csv");
  expect(shownPath("/home/me/Downloads/usage.csv", true)).toBe("…/Downloads/usage.csv");
  expect(shownPath("C:\\Users\\me\\Downloads\\usage.csv", true)).toBe("…\\Downloads\\usage.csv");
  // Nothing above the folder to leave out.
  expect(shownPath("/Downloads/usage.csv", true)).toBe("/Downloads/usage.csv");
});

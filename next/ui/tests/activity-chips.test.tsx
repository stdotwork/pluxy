import { afterEach, expect, test } from "bun:test";
import { FilterMenu } from "../kit/pages/activityChips";
import { all, type FakeNode, mountIn, restoreDom } from "./fakeDom";

afterEach(restoreDom);

const choices = [
  {
    filter: "account" as const,
    kind: "Account",
    every: "Every account",
    options: [{ value: "studio-max", label: "studio-max" }],
    value: "",
  },
];

test("the Filter panel hears Escape as soon as it shows, before the next paint", async () => {
  const { document, root } = await mountIn(<FilterMenu choices={choices} onPick={() => {}} />);
  const filter = all(root, "button").find((b) => b.textContent === "Filter") as FakeNode;
  filter.dispatchEvent(new Event("click"));
  for (let i = 0; i < 10; i++) await Promise.resolve();
  expect(all(root, "fieldset")).toHaveLength(1);
  expect(document.count("keydown")).toBe(1);
  expect(document.count("pointerdown")).toBe(1);
});

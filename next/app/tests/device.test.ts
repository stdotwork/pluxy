import { expect, test } from "bun:test";
import { newest } from "../src/device";

test("a catalog load that answers after a newer one is dropped", async () => {
  const applied: string[] = [];
  const load = newest<string>((v) => applied.push(v));
  let first: (v: string) => void = () => {};
  let second: (v: string) => void = () => {};
  load(new Promise((r) => (first = r)));
  load(new Promise((r) => (second = r)));
  second("newer");
  await Bun.sleep(0);
  first("older");
  await Bun.sleep(0);
  expect(applied).toEqual(["newer"]);
  // A load after both still applies.
  load(Promise.resolve("latest"));
  await Bun.sleep(0);
  expect(applied).toEqual(["newer", "latest"]);
});

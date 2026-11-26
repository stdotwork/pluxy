// What the panel's status block says (panel/status.ts): the address the panel was served from,
// and whether pluxy answered its last call.
import { expect, test } from "bun:test";
import { panelStatus, type Reach, ReachWatch } from "../panel/status";

const at = { host: "127.0.0.1:8317", origin: "http://127.0.0.1:8317" };

test("it names the address it was served from and offers it to copy", () => {
  expect(panelStatus("running", at)).toEqual({
    state: "running",
    word: "Running",
    where: "127.0.0.1:8317",
    menu: [{ kind: "address", value: "http://127.0.0.1:8317" }],
  });
});

test("a pluxy that does not answer is not called stopped: the panel cannot know why", () => {
  expect(panelStatus("down", at)).toMatchObject({ state: "down", word: "Not answering" });
  expect(panelStatus("unknown", at)).toMatchObject({ state: "unknown", word: "Checking" });
});

test("watchers hear the last known answer at once, then each change, and nothing after they stop", () => {
  const watch = new ReachWatch();
  const host = { signedIn: () => false, call: async () => ({ status: 200 }) };
  watch.heard(true);
  const heard: Reach[] = [];
  const stop = watch.watch(host as never, (r) => heard.push(r));
  watch.heard(true); // no change: nothing is said twice
  watch.heard(false);
  watch.heard(true);
  stop();
  watch.heard(false);
  expect(heard).toEqual(["running", "down", "running"]);
});

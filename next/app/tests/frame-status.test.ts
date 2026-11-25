// What the frame's status block says in the desktop app (src/frameStatus.ts), for each state of
// the app's connection. The expected words are written out by hand.
import { expect, test } from "bun:test";
import { connectionStatus } from "../src/frameStatus";
import type { Connection } from "../src/host";

const base: Connection = { signed_in: false, remembered: false };

test("connected to its own core: running on this computer, with the address and the Connection page", () => {
  const c: Connection = { ...base, mode: "managed", target: "http://127.0.0.1:35247", signed_in: true };
  expect(connectionStatus(c)).toEqual({
    state: "running",
    word: "Running",
    where: "this computer :35247",
    menu: [
      { kind: "address", value: "http://127.0.0.1:35247" },
      { kind: "link", label: "Connection", href: "#/connection" },
    ],
  });
});

test("a remote core is named by its address, not as this computer", () => {
  const c: Connection = { ...base, mode: "remote", target: "https://box.example:8317", signed_in: true };
  expect(connectionStatus(c)).toMatchObject({ state: "running", where: "box.example:8317" });
});

test("not connected: each reason has its own word, and no menu", () => {
  const says = (c: Connection) => {
    const s = connectionStatus(c);
    expect(s.menu).toBeUndefined();
    return [s.state, s.word, s.where];
  };
  expect(says(base)).toEqual(["unknown", "Not connected", "not set up"]);
  expect(says({ ...base, mode: "managed", core: { state: "stopped" } })).toEqual([
    "stopped",
    "Stopped",
    "this computer",
  ]);
  expect(says({ ...base, mode: "managed", core: { state: "paused" } })[1]).toBe("Stopped");
  expect(says({ ...base, mode: "managed", core: { state: "missing" } })[1]).toBe("Not installed");
  expect(says({ ...base, mode: "managed", core: { state: "recovering" } })).toEqual([
    "down",
    "Restarting",
    "this computer",
  ]);
  expect(says({ ...base, mode: "local", url: "http://127.0.0.1:8317" })).toEqual([
    "down",
    "Not answering",
    "this computer :8317",
  ]);
  expect(says({ ...base, mode: "remote", url: "https://box.example:8317" })).toEqual([
    "down",
    "Not answering",
    "box.example:8317",
  ]);
  expect(says({ ...base, mode: "remote", target: "https://box.example:8317" })).toEqual([
    "unknown",
    "Not signed in",
    "box.example:8317",
  ]);
});

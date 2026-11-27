// What the frame's status block says in the desktop app (src/frameStatus.ts), for each state of
// the app's connection. The expected words are written out by hand.
import { expect, test } from "bun:test";
import { connectionStatus } from "../src/frameStatus";
import type { Connection } from "../src/host";

const base: Connection = { signed_in: false, remembered: false };

test("the app's own pluxy, signed in: Running, with its address, Restart and Where pluxy runs", () => {
  const c: Connection = { ...base, mode: "managed", target: "http://127.0.0.1:35247", signed_in: true };
  const s = connectionStatus(c);
  expect([s.state, s.word, s.where]).toEqual(["running", "Running", "this computer :35247"]);
  expect(s.menu?.map((i) => [i.kind, i.kind === "address" ? i.value : i.label])).toEqual([
    ["address", "http://127.0.0.1:35247"],
    ["action", "Restart pluxy"],
    ["link", "Where pluxy runs"],
  ]);
  const link = s.menu?.[2];
  expect(link?.kind === "link" && link.href).toBe("#/connection");
});

test("Restart in the menu restarts the app's own pluxy and hands the new connection over", async () => {
  const c: Connection = { ...base, mode: "managed", target: "http://127.0.0.1:35247", signed_in: true };
  const calls: string[] = [];
  const next: Connection = { ...c, error: "exited" };
  (globalThis as unknown as { window: unknown }).window = {
    __TAURI_INTERNALS__: {
      invoke: (cmd: string) => {
        calls.push(cmd);
        return Promise.resolve(next);
      },
      transformCallback: () => 1,
    },
  };
  const got: Connection[] = [];
  const action = connectionStatus(c, (n) => got.push(n)).menu?.find((i) => i.kind === "action");
  await (action?.kind === "action" ? action.run() : Promise.reject("no restart in the menu"));
  expect(calls).toEqual(["core_restart"]);
  expect(got).toEqual([next]);
  Reflect.deleteProperty(globalThis, "window");
});

test("a pluxy the app only connects to is Connected, not Running: the app does not check that it answers", () => {
  const remote: Connection = { ...base, mode: "remote", target: "https://box.example:8317", signed_in: true };
  const r = connectionStatus(remote);
  expect([r.state, r.word, r.where]).toEqual(["unknown", "Connected", "box.example:8317"]);
  expect(r.menu?.map((i) => i.kind)).toEqual(["address", "link"]);
  const local: Connection = { ...base, mode: "local", target: "http://127.0.0.1:8317", signed_in: true };
  const l = connectionStatus(local);
  expect([l.word, l.where]).toEqual(["Connected", "this computer :8317"]);
  expect(l.menu?.map((i) => i.kind)).toEqual(["address", "link"]);
});

test("not running: each reason has its own word, and a click opens Where pluxy runs", () => {
  const says = (c: Connection) => {
    const s = connectionStatus(c);
    expect(s.menu).toBeUndefined();
    return [s.state, s.word, s.where, s.href];
  };
  const open = "#/connection";
  expect(says(base)).toEqual(["unknown", "Not connected", "not set up", undefined]);
  expect(says({ ...base, mode: "managed", core: { state: "stopped" } })).toEqual([
    "stopped",
    "Stopped",
    "this computer",
    open,
  ]);
  expect(says({ ...base, mode: "managed", core: { state: "paused" } })[1]).toBe("Stopped");
  expect(says({ ...base, mode: "managed", core: { state: "missing" } })[1]).toBe("Not installed");
  expect(says({ ...base, mode: "managed", core: { state: "recovering" } })).toEqual([
    "down",
    "Restarting",
    "this computer",
    open,
  ]);
  // No address: it cannot connect.
  expect(says({ ...base, mode: "local", url: "http://127.0.0.1:8317" })).toEqual([
    "down",
    "Not connected",
    "this computer :8317",
    open,
  ]);
  expect(says({ ...base, mode: "remote", url: "https://box.example:8317" })).toEqual([
    "down",
    "Not connected",
    "box.example:8317",
    open,
  ]);
  // An address and no key: not signed in, unless what the app last tried got no answer.
  const target = "https://box.example:8317";
  expect(says({ ...base, mode: "remote", target })).toEqual([
    "unknown",
    "Not signed in",
    "box.example:8317",
    open,
  ]);
  expect(says({ ...base, mode: "remote", target, error: "This management key was not accepted." })[1]).toBe(
    "Not signed in",
  );
  expect(says({ ...base, mode: "remote", target, error: "The connection to pluxy timed out." })).toEqual([
    "down",
    "Not answering",
    "box.example:8317",
    open,
  ]);
});

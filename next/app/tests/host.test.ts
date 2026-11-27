import { expect, test } from "bun:test";
import { appHost, type Connection, coreAction, coreRestart, coreStart } from "../src/host";

const connection = (over: Partial<Connection> = {}): Connection => ({
  signed_in: true,
  remembered: false,
  ...over,
});

test("its address is the core the app talks to, without a trailing slash", () => {
  const host = appHost(connection({ mode: "local", target: "http://127.0.0.1:8400/" }), () => {});
  expect(host.address).toBe("http://127.0.0.1:8400");
});

test("with no target, it has no address", () => {
  const host = appHost(connection({ mode: "managed" }), () => {});
  expect(host.address).toBeUndefined();
});

test("the core button restarts only a running core, and otherwise starts one without replacing it", () => {
  const managed = (state: NonNullable<Connection["core"]>["state"]) =>
    connection({ mode: "managed", core: { state } });
  expect(coreAction(managed("running"))).toBe(coreRestart);
  for (const state of ["stopped", "recovering", "paused"] as const)
    expect(coreAction(managed(state))).toBe(coreStart);
});

/** Answers the window's native calls with answer, for one test. */
function nativeAnswers(answer: (cmd: string) => Promise<unknown>) {
  (globalThis as unknown as { window: unknown }).window = {
    __TAURI_INTERNALS__: { invoke: (cmd: string) => answer(cmd), transformCallback: () => 1 },
  };
}

test("the app asks before replacing the management key itself, so the page does not ask too", () => {
  const host = appHost(connection({ mode: "local", target: "http://127.0.0.1:8400" }), () => {});
  expect(host.features.has("rotateManagementKey")).toBe(true);
  expect(host.features.has("confirmsKeyReplacement")).toBe(true);
});

test("a rotation the native side refused before starting comes back with its own reason, not as a lost reply", async () => {
  const host = appHost(connection({ mode: "local", target: "http://127.0.0.1:8400" }), () => {});
  for (const refusal of ["finish native key entry first", "a rotation is already in progress"]) {
    nativeAnswers((cmd) =>
      cmd === "management_key_rotate" ? Promise.reject(refusal) : Promise.resolve(null),
    );
    const reply = await host.call("POST", "/management-key/rotate");
    expect(reply.status).toBe(409);
    expect(reply.error?.message).toBe(refusal);
  }
  // A rotation that failed on its way may have replaced the key: that one is a lost reply.
  nativeAnswers((cmd) =>
    cmd === "management_key_rotate" ? Promise.reject("native rotation failed") : Promise.resolve(null),
  );
  expect((await host.call("POST", "/management-key/rotate")).status).toBe(0);
  Reflect.deleteProperty(globalThis, "window");
});

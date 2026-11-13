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

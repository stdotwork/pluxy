import { expect, test } from "bun:test";
import { appHost, type Connection } from "../src/host";

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

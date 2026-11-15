import { expect, test } from "bun:test";
import { lifetimeFor } from "../src/launcherForm";

test("a terminal launch is always detached, whatever was chosen before the kind changed", () => {
  expect(lifetimeFor("terminal", "managed")).toBe("detached");
  expect(lifetimeFor("terminal", "detached")).toBe("detached");
});

test("a direct launch keeps the lifetime chosen, managed included", () => {
  expect(lifetimeFor("direct", "managed")).toBe("managed");
  expect(lifetimeFor("direct", "detached")).toBe("detached");
});

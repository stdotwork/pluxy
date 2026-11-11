import { expect, test } from "bun:test";
import { soonestRecovery } from "../kit/pages/quota";

test("soonest recovery uses the earliest reset across every limit, unknown last", () => {
  expect(
    soonestRecovery({
      limits: [
        { id: "weekly", resets_at: 400 },
        { id: "daily", resets_at: 100 },
      ],
    }),
  ).toBe(100);
  expect(soonestRecovery({ limits: [{ id: "other", resets_at: 200 }] })).toBe(200);
  expect(soonestRecovery({ limits: [{ id: "balance", remaining: 0 }] })).toBe(Number.POSITIVE_INFINITY);
});

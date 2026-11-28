import { expect, test } from "bun:test";
import type { QuotaLimit } from "../kit/api";
import { windowName } from "../kit/parts";

const WEEK = 7 * 86400;

test("a scoped window keeps the provider's scoped name; an unscoped one gets the named label", () => {
  const codeReview: QuotaLimit = {
    id: "code_review_seven_day",
    label: "Code review weekly limit",
    window_seconds: WEEK,
    feature: true,
  };
  const fable: QuotaLimit = {
    id: "seven_day_fable",
    label: "Weekly Fable limit",
    window_seconds: WEEK,
    models: ["claude-fable-5-2"],
  };
  expect(windowName(codeReview)).toBe("Code review weekly limit");
  expect(windowName(fable)).toBe("Weekly Fable limit");
  expect(windowName({ id: "seven_day", label: "7 days", window_seconds: WEEK })).toBe("Weekly limit");
  expect(windowName({ id: "scoped", window_seconds: WEEK, feature: true })).toBe("Weekly limit");
});

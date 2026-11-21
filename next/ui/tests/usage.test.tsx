import { expect, test } from "bun:test";
import { renderToString } from "preact-render-to-string";
import type { UsageEvent } from "../kit/api";
import { fallbackStep } from "../kit/pages/usage";

const event = (fields: Partial<UsageEvent> = {}): UsageEvent => ({
  seq: 1,
  timestamp: "2026-01-01T00:00:00Z",
  model: "fake-model",
  failed: false,
  status: 200,
  latency_ms: 12,
  tokens: { total_tokens: 3 },
  ...fields,
});

const html = (e: UsageEvent) => {
  const mark = fallbackStep(e);
  return mark ? renderToString(mark) : "";
};

test("a request answered on a later step of its fallback chain is marked with that step", () => {
  expect(html(event({ chain_step: 2 }))).toBe(
    '<span title="Answered by step 2 of this model\'s fallback chain">' +
      '<span class="badge" data-part="badge" data-tone="muted">Fallback step 2</span></span>',
  );
  expect(html(event({ chain_step: 5 }))).toContain(">Fallback step 5</span>");
});

test("the first step and a model with no chain carry no mark", () => {
  expect(html(event({ chain_step: 1 }))).toBe("");
  expect(html(event())).toBe("");
});

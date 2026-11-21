import { expect, test } from "bun:test";
import { renderToString } from "preact-render-to-string";
import type { UsageEvent } from "../kit/api";
import { accountNames, fallbackStep, tries, UsageDetail } from "../kit/pages/usage";
import { privacyMasks } from "../kit/privacy";

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
    '<span title="Ended on step 2 of this model\'s fallback chain">' +
      '<span class="badge" data-part="badge" data-tone="muted">Fallback step 2</span></span>',
  );
  expect(html(event({ chain_step: 5 }))).toContain(">Fallback step 5</span>");
});

test("the first step and a model with no chain carry no mark", () => {
  expect(html(event({ chain_step: 1 }))).toBe("");
  expect(html(event())).toBe("");
});

test("a request sent more than once is marked with its number of tries", () => {
  const mark = (e: UsageEvent) => {
    const m = tries(e);
    return m ? renderToString(m) : "";
  };
  expect(mark(event({ attempts: 3 }))).toContain(">3 tries</span>");
  expect(mark(event({ attempts: 1 }))).toBe("");
  // A record from before core counted them.
  expect(mark(event())).toBe("");
});

test("a request's detail lists each try in order, by the account's own ID", () => {
  const e = event({
    account: "second",
    attempts: 2,
    tried: [
      { account: "acc_00000001", provider: "test/local", model: "fake-model", result: "quota" },
      { account: "acc_00000002", provider: "test/local", model: "fake-model", result: "ok" },
    ],
  });
  const name = accountNames([
    { id: "first", public_id: "acc_00000001", source: "login", state: "active", refreshable: false },
    { id: "second", public_id: "acc_00000002", source: "login", state: "active", refreshable: false },
  ]);
  const detail = (mask = privacyMasks(false).masker([])) =>
    renderToString(<UsageDetail event={e} name={name} mask={mask} onClose={() => {}} />);
  const html = detail();
  expect(html.indexOf("first")).toBeLessThan(html.indexOf("failed: quota"));
  expect(html.indexOf("failed: quota")).toBeLessThan(html.indexOf("answered"));
  expect(html).toContain('<span class="mono">second</span>');
  // An account core no longer lists keeps its public ID.
  expect(accountNames([])("acc_00000009")).toBe("acc_00000009");
  // Privacy mode masks the accounts' IDs here as in the list.
  const masked = detail(privacyMasks(true).masker(["first", "second"]));
  expect(masked).not.toContain(">first<");
  expect(masked).not.toContain(">second<");
});

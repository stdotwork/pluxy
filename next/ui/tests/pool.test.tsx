// Home's pool of one provider's accounts (parts/pool.ts) and the parts drawn beside it: the
// provider's mark, the compact request row's "2nd", and limits a provider does not report.

import { expect, test } from "bun:test";
import { renderToString } from "preact-render-to-string";
import type { Account } from "../kit/api/accounts";
import type { QuotaLimit } from "../kit/api/quota";
import type { UsageEvent } from "../kit/api/usageEvents";
import { baseUrlFor, Limits, PoolCard, ProviderMark, poolOf, RequestRow, seriesOf } from "../kit/parts";
import type { Windows } from "../kit/parts/hooks";

const NOW = Date.parse("2026-11-27T12:00:00Z");
const HOUR = 3600;

const account = (id: string, extra: Partial<Account> = {}): Account =>
  ({
    id,
    extension: "pluxy/claude",
    source: "login",
    state: "active",
    refreshable: true,
    ...extra,
  }) as Account;

const window = (seconds: number, used: number, resetIn?: number): QuotaLimit => ({
  id: `w${seconds}`,
  window_seconds: seconds,
  used_percent: used,
  resets_at: resetIn === undefined ? undefined : NOW / 1000 + resetIn,
});

const read = (...limits: QuotaLimit[]): Windows => ({ state: "read", limits });

test("the pool is the mean left of the accounts that serve, with one segment per account not turned off", () => {
  const list = [
    account("a"),
    account("b"),
    account("c", { state: "reauth_required" }),
    account("d", { disabled: true }),
  ];
  const windows = new Map([
    ["a", read(window(5 * HOUR, 20, 2 * HOUR), window(7 * 24 * HOUR, 50))],
    ["b", read(window(5 * HOUR, 60, HOUR))],
  ]);
  const pool = poolOf("pluxy/claude", list, windows, undefined, NOW);
  expect(pool.provider).toBe("Claude");
  expect(pool.window).toBe("5-hour limit");
  // (80 + 40) / 2
  expect(pool.left).toBe(60);
  expect(pool.serving).toBe(2);
  expect(pool.total).toBe(3);
  expect(pool.segments).toEqual([
    { id: "a", kind: "left", left: 80 },
    { id: "b", kind: "left", left: 40 },
    { id: "c", kind: "out" },
  ]);
  // b starts again first, in an hour: its 60 used come back, over the two counted.
  expect(pool.back).toEqual({ gain: 30, in: HOUR * 1000 });
});

test("the pool counts the window most serving accounts share, and an account without it is unknown", () => {
  const windows = new Map([
    ["a", read(window(5 * HOUR, 10))],
    ["b", read(window(5 * HOUR, 30))],
    ["c", read(window(24 * HOUR, 0))],
    ["d", { state: "failed", limits: [] } as Windows],
  ]);
  const pool = poolOf(
    "pluxy/claude",
    [account("a"), account("b"), account("c"), account("d")],
    windows,
    undefined,
    NOW,
  );
  expect(pool.window).toBe("5-hour limit");
  expect(pool.left).toBe(80);
  expect(pool.segments.map((s) => s.kind)).toEqual(["left", "left", "unknown", "unknown"]);
  expect(pool.back).toBeUndefined();
});

test("a provider that reports a balance in place of a limit adds it up; one that reports neither has no number", () => {
  const usd = (remaining: number): QuotaLimit => ({ id: "prepaid", unit: "usd", remaining });
  const balance = poolOf(
    "pluxy/xai",
    [account("a", { extension: "pluxy/xai" }), account("b", { extension: "pluxy/xai" })],
    new Map([
      ["a", read(usd(40))],
      ["b", read(usd(2.1))],
    ]),
    undefined,
    NOW,
  );
  expect(balance.left).toBeUndefined();
  expect(balance.balance).toBeCloseTo(42.1);
  const html = renderToString(<PoolCard pool={balance} />);
  expect(html).toContain("$42.10");
  expect(html).not.toContain("pool-bar");

  const none = poolOf("pluxy/xai", [account("a", { extension: "pluxy/xai" })], new Map(), undefined, NOW);
  expect(none.left).toBeUndefined();
  expect(none.balance).toBeUndefined();
  expect(renderToString(<PoolCard pool={none} />)).toContain("Not reported by xAI");
});

test("a pool with nothing serving says so, and when the first account is back", () => {
  const until = new Date(NOW + 2 * HOUR * 1000).toISOString();
  const list = [
    account("a", { health: { state: "exhausted", until } } as Partial<Account>),
    account("b", { state: "reauth_required" }),
  ];
  const pool = poolOf("pluxy/claude", list, new Map(), undefined, NOW);
  expect(pool.serving).toBe(0);
  expect(pool.firstBack).toBe(2 * HOUR * 1000);
  const html = renderToString(<PoolCard pool={pool} />);
  expect(html).toContain("Nothing is serving");
  expect(html).toContain("0 of 2 serving");
  expect(html).toContain("The first is back in 2h");
});

test("the pool's number is in text ink when green and takes the warning or danger tone when its bar does", () => {
  const html = (used: number) =>
    renderToString(
      <PoolCard
        pool={poolOf("pluxy/claude", [account("a")], new Map([["a", read(window(5 * HOUR, used))]]))}
      />,
    );
  expect(html(10)).not.toMatch(/pool-number[^>]*data-tone="(warn|danger)"/);
  expect(html(50)).toMatch(/pool-number[^>]*data-tone="warn"/);
  expect(html(90)).toMatch(/pool-number[^>]*data-tone="danger"/);
});

test("a known provider has its mark, any other its initial in a colour that stays with its ID", () => {
  const claude = renderToString(<ProviderMark extension="pluxy/claude" />);
  expect(claude).toContain('data-provider="claude"');
  expect(claude).toContain("<svg");
  expect(claude).toContain('aria-hidden="true"');
  const other = renderToString(<ProviderMark extension="demo/aurora" />);
  expect(other).toContain(">A</span>");
  expect(other).toContain(`data-series="${seriesOf("aurora")}"`);
  expect(seriesOf("aurora")).toBe(seriesOf("aurora"));
  // Named, for a row whose words do not say the provider.
  expect(renderToString(<ProviderMark extension="pluxy/codex" named />)).toContain(
    'role="img" aria-label="Codex"',
  );
});

test("a provider that reports no limits says so by name", () => {
  const none: Windows = { state: "none", limits: [] };
  expect(renderToString(<Limits windows={none} account="a" provider="xAI" />)).toContain(
    "Not reported by xAI",
  );
});

test("the compact request row says on which try a backup answered, and a failure as a word", () => {
  const event = (extra: Partial<UsageEvent>): UsageEvent =>
    ({
      seq: 1,
      id: "t1",
      timestamp: new Date(NOW).toISOString(),
      model: "opus-5",
      provider: "pluxy/claude",
      account: "acc",
      failed: false,
      latency_ms: 900,
      ...extra,
    }) as UsageEvent;
  const second = renderToString(
    <RequestRow
      compact
      event={event({
        tried: [
          { account: "a", provider: "pluxy/claude", result: "rate_limited" },
          { account: "b", provider: "pluxy/claude", result: "ok" },
        ],
      } as Partial<UsageEvent>)}
    />,
  );
  expect(second).toContain(">2nd<");
  expect(second).toContain('data-provider="claude"');
  const plain = renderToString(<RequestRow compact event={event({})} />);
  expect(plain).not.toContain("request-chip");
});

test("each API a tool speaks takes its base URL from pluxy's address", () => {
  expect(baseUrlFor("http://127.0.0.1:8317", "openai")).toBe("http://127.0.0.1:8317/v1");
  expect(baseUrlFor("http://127.0.0.1:8317/", "claude")).toBe("http://127.0.0.1:8317");
  expect(baseUrlFor("http://127.0.0.1:8317", "gemini")).toBe("http://127.0.0.1:8317");
});

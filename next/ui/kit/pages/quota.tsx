import { useEffect, useRef, useState } from "preact/hooks";
import { type Account, problem, type Quota, type QuotaLimit, type QuotaReply } from "../api";
import { Meter } from "../meter";
import { usePrivacy } from "../privacy";
import type { PageDef } from "../shell";
import { rich, strings } from "../strings";
import { Button, Empty, Facts, List, Notice, PageHead, useKit, useLoad, when } from "../ui";

const t = strings("quota", {
  title: "Quota Management",
  short: "Quota",
  tip: "API keys & limits",
  account: "Account",
  quota: "Quota",
  refresh: "Refresh Quota",
  refreshAll: "Refresh all credentials",
  search: "Search accounts",
  sort: "Sort",
  default: "Default",
  soonest: "Soonest recovery",
  empty: "No quota-capable credentials",
  noMatch: "No matching accounts",
  idle: "Click here to refresh quota",
  loading: "Loading quota...",
  plan: "Plan",
  ends: "Subscription ends {time}",
  manualResets: "Manual resets",
  fetched: "Last updated",
  noLimits: "No quota data available",
  unlimited: "Unlimited",
  percent: "{percent}%",
  amount: "{value} {unit}",
  usedLimit: "{used} / {limit}",
  used: "{amount} used",
  remaining: "{amount} remaining",
  limit: "Limit: {amount}",
  reset: "Reset time",
  resetsIn: "resets in {hint}",
  window: "Window",
  days: { one: "{count} day", other: "{count} days" },
  hours: { one: "{count} hour", other: "{count} hours" },
  minutes: { one: "{count} minute", other: "{count} minutes" },
  seconds: { one: "{count} second", other: "{count} seconds" },
});

function duration(seconds: number): string {
  if (seconds >= 86400) return t("days", { count: Math.ceil(seconds / 86400) });
  if (seconds >= 3600) return t("hours", { count: Math.ceil(seconds / 3600) });
  if (seconds >= 60) return t("minutes", { count: Math.ceil(seconds / 60) });
  return t("seconds", { count: Math.max(0, Math.ceil(seconds)) });
}

export function soonestRecovery(quota?: Quota): number {
  return (quota?.limits ?? []).reduce(
    (soonest, limit) => (limit.resets_at ? Math.min(soonest, limit.resets_at) : soonest),
    Number.POSITIVE_INFINITY,
  );
}

function Timestamp({ value }: { value: string | number }) {
  const date = new Date(typeof value === "number" ? value * 1000 : value);
  if (!Number.isFinite(date.getTime())) return null;
  return <time dateTime={date.toISOString()}>{when(value)}</time>;
}

function LimitAmounts({ limit }: { limit: QuotaLimit }) {
  const amount = (value: number) =>
    t("amount", { value: value.toLocaleString(), unit: limit.unit ?? "" }).trim();
  const details: string[] = [];
  if (limit.unlimited) details.push(t("unlimited"));
  if (limit.used !== undefined && limit.limit !== undefined) {
    details.push(t("usedLimit", { used: limit.used.toLocaleString(), limit: amount(limit.limit) }));
  } else {
    if (limit.used !== undefined) details.push(t("used", { amount: amount(limit.used) }));
    if (limit.limit !== undefined) details.push(t("limit", { amount: amount(limit.limit) }));
  }
  if (limit.remaining !== undefined) details.push(t("remaining", { amount: amount(limit.remaining) }));
  return <span class="muted">{details.join(" · ")}</span>;
}

function LimitTiming({ limit }: { limit: QuotaLimit }) {
  return (
    <Facts
      rows={[
        [
          t("reset"),
          limit.resets_at ? (
            <>
              <Timestamp value={limit.resets_at} />{" "}
              <span class="muted">
                {t("resetsIn", { hint: duration(limit.resets_at - Date.now() / 1000) })}
              </span>
            </>
          ) : undefined,
        ],
        [t("window"), limit.window_seconds ? duration(limit.window_seconds) : undefined],
      ]}
    />
  );
}

function QuotaLimitBody({ limit }: { limit: QuotaLimit }) {
  const label = limit.label || limit.id;
  const percent =
    limit.used_percent ??
    (limit.limit && limit.limit > 0 && (limit.used !== undefined || limit.remaining !== undefined)
      ? ((limit.used ?? limit.limit - (limit.remaining ?? 0)) / limit.limit) * 100
      : undefined);
  return (
    <section class="card quota-limit" data-part="card" aria-label={label}>
      <div class="quota-limit-head">
        <strong>{label}</strong>
        {percent !== undefined && <span>{t("percent", { percent: Number(percent.toFixed(2)) })}</span>}
      </div>
      {percent !== undefined && !limit.unlimited && <Meter label={label} percent={percent} />}
      <LimitAmounts limit={limit} />
      <LimitTiming limit={limit} />
    </section>
  );
}

function QuotaSummary({ data }: { data: QuotaReply }) {
  const end = data.quota.plan_ends_at;
  const resets = data.quota.limits.find((limit) => limit.id === "reset_credits");
  return (
    <>
      {end ? (
        <p data-part="subscription-end">{rich(t("ends"), { time: <Timestamp value={end} /> })}</p>
      ) : null}
      <Facts
        rows={[
          [t("plan"), data.quota.plan],
          [t("manualResets"), resets?.remaining?.toLocaleString()],
          [t("fetched"), <Timestamp value={data.fetched_at} />],
        ]}
      />
    </>
  );
}

interface QuotaState {
  data?: QuotaReply;
  error?: string;
  busy?: boolean;
  unsupported?: boolean;
}

function QuotaBody({ state, refresh, disabled }: { state?: QuotaState; refresh(): void; disabled: boolean }) {
  const p = usePrivacy();
  return (
    <section class="card quota-body" data-part="card" aria-busy={state?.busy ?? false}>
      <div>
        <Button onClick={refresh} disabled={disabled}>
          {t("refresh")}
        </Button>
      </div>
      {state?.busy && <Notice tone="muted">{t("loading")}</Notice>}
      {state?.error && <Notice tone="danger">{p.text(state.error)}</Notice>}
      {state?.data ? (
        <>
          <QuotaSummary data={state.data} />
          {state.data.quota.limits.length ? (
            state.data.quota.limits
              .filter((limit) => limit.id !== "reset_credits")
              .map((limit) => <QuotaLimitBody key={limit.id} limit={limit} />)
          ) : (
            <Empty>{t("noLimits")}</Empty>
          )}
        </>
      ) : (
        !state?.busy && !state?.error && <Empty>{t("idle")}</Empty>
      )}
    </section>
  );
}

export function QuotaPage() {
  const { api, unauthorized } = useKit();
  const p = usePrivacy();
  const accounts = useLoad(api.accounts);
  const [states, setStates] = useState<Record<string, QuotaState>>({});
  const [search, setSearch] = useState("");
  const [sort, setSort] = useState("default");
  const [batch, setBatch] = useState(false);
  const inFlight = useRef(new Set<string>());
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);
  const update = (id: string, state: QuotaState) => {
    if (mounted.current) setStates((all) => ({ ...all, [id]: { ...all[id], ...state } }));
  };
  const refresh = async (id: string) => {
    if (inFlight.current.has(id) || !mounted.current) return;
    inFlight.current.add(id);
    update(id, { busy: true, error: undefined });
    try {
      const r = await api.quota(id);
      if (r.status === 401) {
        unauthorized();
        return;
      }
      update(id, {
        busy: false,
        data: r.body,
        error: problem(r),
        unsupported: r.status === 422 && r.error?.code === "unsupported",
      });
    } finally {
      inFlight.current.delete(id);
    }
  };
  const all = (accounts.data?.accounts ?? []).filter((a) => !states[a.id]?.unsupported);
  const refreshAll = async () => {
    setBatch(true);
    let next = 0;
    const worker = async () => {
      while (next < all.length && mounted.current) {
        const a = all[next++];
        if (a) await refresh(a.id);
      }
    };
    try {
      await Promise.all(Array.from({ length: Math.min(3, all.length) }, worker));
    } finally {
      if (mounted.current) setBatch(false);
    }
  };
  const query = search.trim().toLocaleLowerCase();
  const shown = all.filter((a) =>
    [a.id, a.label, a.extension].some((value) => value?.toLocaleLowerCase().includes(query)),
  );
  if (sort === "soonest")
    shown.sort(
      (a, b) => soonestRecovery(states[a.id]?.data?.quota) - soonestRecovery(states[b.id]?.data?.quota),
    );
  return (
    <>
      <PageHead
        title={t("title")}
        actions={
          <Button onClick={() => void refreshAll()} disabled={batch || accounts.loading || all.length === 0}>
            {t("refreshAll")}
          </Button>
        }
      />
      {accounts.error && <Notice tone="danger">{p.text(accounts.error)}</Notice>}
      <div class="quota-controls">
        <label class="field" data-part="field">
          <span class="label">{t("search")}</span>
          <input
            type="search"
            aria-label={t("search")}
            value={search}
            onInput={(e) => setSearch(e.currentTarget.value)}
          />
        </label>
        <label class="field" data-part="field">
          <span class="label">{t("sort")}</span>
          <select aria-label={t("sort")} value={sort} onChange={(e) => setSort(e.currentTarget.value)}>
            <option value="default">{t("default")}</option>
            <option value="soonest">{t("soonest")}</option>
          </select>
        </label>
      </div>
      {shown.length === 0 && !accounts.loading ? (
        <Empty>{t(query ? "noMatch" : "empty")}</Empty>
      ) : (
        <List<Account>
          label={t("title")}
          items={shown}
          id={(a) => a.id}
          columns={[
            {
              label: t("account"),
              value: (a) => (
                <>
                  {p.text(a.id, [a.label])}
                  <small class="quota-identity muted">
                    {p.id(a.label)}
                    {a.extension && <> · {a.extension}</>}
                  </small>
                </>
              ),
            },
            {
              label: t("quota"),
              value: (a) => (
                <QuotaBody
                  state={states[a.id]}
                  refresh={() => void refresh(a.id)}
                  disabled={batch || !!states[a.id]?.busy}
                />
              ),
            },
          ]}
        />
      )}
    </>
  );
}

export const quotaPage = (): PageDef => ({
  id: "quota",
  group: "observe",
  title: t("title"),
  short: t("short"),
  tip: t("tip"),
  render: () => <QuotaPage />,
});

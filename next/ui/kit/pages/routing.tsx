import { useEffect, useRef, useState } from "preact/hooks";
import { type Account, problem } from "../api";
import type { CoreSettings } from "../api/settings";
import type { Reply } from "../host";
import type { PageDef } from "../shell";
import { strings } from "../strings";
import { Badge, Button, Card, Empty, List, Notice, PageHead, useKit } from "../ui";
import {
  changeSetting,
  type Field,
  fields,
  SettingControl,
  settingsChange,
  type Value,
} from "./settingsFields";

const t = strings("routing", {
  title: "Routing",
  short: "Routing",
  tip: "Priority tiers and failover",
  refresh: "Refresh",
  loading: "Loading routing…",
  empty: "Nothing to route yet",
  preferred: "Preferred",
  fallback: "Fallback {index}",
  priority: "Priority {priority}",
  account: "Account",
  state: "State",
  actions: "Actions",
  config: "Set in config",
  weight: "Weight {weight}",
  cooling: "Cooling {time}",
  seconds: { one: "{count}s", other: "{count}s" },
  minutes: { one: "{count}m", other: "{count}m" },
  hours: { one: "{count}h", other: "{count}h" },
  unavailable: "Unavailable",
  up: "Move up a tier",
  down: "Move down a tier",
  setWeight: "Set weight…",
  weightHint: "Only used by weighted round-robin. Empty counts as 1; 0 takes no share.",
  policy: "Gateway policy",
  policyHint: "Applies to every tier.",
  strategy: "Within a tier",
  roundRobin: "Round-robin",
  weighted: "Weighted round-robin",
  fillFirst: "Fill first",
  leastActive: "Least active",
  roundRobinHint: "Each credential in the tier takes requests in turn.",
  weightedHint: "Requests are split across the tier by weight.",
  fillFirstHint: "One credential takes everything until it is unavailable, then the next.",
  leastActiveHint: "The credential with the fewest running requests takes the next request.",
  affinity: "Session affinity",
  affinityHint: "Requests of one session stick to one credential, failing over when it becomes unavailable.",
  ttl: "Session Affinity TTL",
  ttlHint: "A duration such as 1h or 30m; enable session affinity before changing its TTL.",
  failover: "Failover",
  retry: "Retry rounds",
  retryHint: "Extra rounds after every credential fails in the first round.",
  credentials: "Credentials per round",
  credentialsHint: "Most credentials tried per round; 0 tries all.",
  interval: "Max Retry Interval (seconds)",
  coolingTitle: "Cooling",
  disableCooling: "Disable cooling",
  transient: "Transient error cooldown (seconds)",
  transientHint: "0 uses core's backoff; -1 disables transient error cooling.",
  quota: "Report usage limit on quota exhaustion",
  quotaHint: "When off, quota exhaustion is reported as retryable.",
  prefix: "Force model prefix",
});

export interface Tier {
  priority: number;
  accounts: Account[];
}
export function groupTiers(accounts: Account[]): Tier[] {
  const groups = new Map<number, Account[]>();
  for (const account of accounts) {
    const priority = account.priority ?? 0;
    const members = groups.get(priority) ?? [];
    members.push(account);
    groups.set(priority, members);
  }
  return [...groups]
    .sort(([a], [b]) => b - a)
    .map(([priority, members]) => ({ priority, accounts: members }));
}
export function movePriority(tiers: Tier[], priority: number, direction: "up" | "down"): number {
  const index = tiers.findIndex((tier) => tier.priority === priority);
  return tiers[index + (direction === "up" ? -1 : 1)]?.priority ?? priority + (direction === "up" ? 1 : -1);
}
function coolingTime(account: Account): string | undefined {
  const until = Math.max(
    0,
    ...[account.cooldown_until, ...(account.cooldowns ?? []).map((c) => c.until)]
      .map((value) => (value ? Date.parse(value) : 0))
      .filter(Number.isFinite),
  );
  const seconds = Math.ceil((until - Date.now()) / 1000);
  if (seconds <= 0) return undefined;
  return seconds >= 3600
    ? t("hours", { count: Math.ceil(seconds / 3600) })
    : seconds >= 60
      ? t("minutes", { count: Math.ceil(seconds / 60) })
      : t("seconds", { count: seconds });
}
function WeightEditor({
  account,
  busy,
  save,
}: {
  account: Account;
  busy: boolean;
  save: (weight: number | null) => void;
}) {
  const [editing, setEditing] = useState(false);
  const [value, setValue] = useState("");
  const commit = (input: HTMLInputElement) => {
    if (busy || !input.reportValidity()) return;
    const weight = value === "" ? null : Number(value);
    setEditing(false);
    if (weight !== (account.weight ?? null)) save(weight);
  };
  return (
    <>
      <Button
        disabled={busy || account.source === "config"}
        onClick={() => {
          setValue(account.weight === undefined ? "" : String(account.weight));
          setEditing(true);
        }}
      >
        {t("setWeight")}
      </Button>
      {editing && (
        <label class="field routing-weight" data-part="field">
          <span class="label">{t("setWeight")}</span>
          <input
            type="number"
            aria-label={t("setWeight")}
            min="0"
            step="1"
            value={value}
            disabled={busy}
            onInput={(e) => setValue(e.currentTarget.value)}
            onBlur={(e) => commit(e.currentTarget)}
            onKeyDown={(e) => {
              if (e.key === "Enter") {
                e.preventDefault();
                commit(e.currentTarget);
              }
              if (e.key === "Escape") setEditing(false);
            }}
          />
          <small class="hint">{t("weightHint")}</small>
        </label>
      )}
    </>
  );
}
export function RoutingPage() {
  const { api, unauthorized } = useKit();
  const [settings, setSettings] = useState<CoreSettings>();
  const [accounts, setAccounts] = useState<Account[]>([]);
  const [busy, setBusy] = useState(true);
  const [error, setError] = useState<string>();
  const saving = useRef(false);
  const mounted = useRef(true);
  const load = async () => {
    const [s, a] = await Promise.all([api.coreSettings(), api.accounts()]);
    if (!mounted.current) return;
    if (s.status === 401 || a.status === 401) return unauthorized();
    if (s.body) setSettings(s.body);
    if (a.body) setAccounts(a.body.accounts);
    const why = problem(s) ?? problem(a);
    if (why) setError(why);
  };
  const run = async (call?: () => Promise<Reply<unknown>>) => {
    if (saving.current) return;
    saving.current = true;
    setBusy(true);
    setError(undefined);
    try {
      if (call) {
        const reply = await call();
        if (!mounted.current) return;
        if (reply.status === 401) return unauthorized();
        setError(problem(reply));
      }
      if (mounted.current) await load();
    } finally {
      saving.current = false;
      if (mounted.current) setBusy(false);
    }
  };
  useEffect(() => {
    mounted.current = true;
    void run();
    return () => {
      mounted.current = false;
    };
  }, []);
  const tiers = groupTiers(accounts);
  const change = (field: Field, value: Value) => {
    if (!settings) return;
    void run(() =>
      api.changeCoreSettings(settingsChange(settings, changeSetting(settings, field.key, value))),
    );
  };
  const labels: Record<string, { label: string; hint?: string }> = {
    "routing.strategy": { label: t("strategy") },
    "routing.retry.request-retry": { label: t("retry"), hint: t("retryHint") },
    "routing.retry.max-retry-credentials": { label: t("credentials"), hint: t("credentialsHint") },
    "routing.retry.max-retry-interval": { label: t("interval") },
    "routing.session-affinity": { label: t("affinity"), hint: t("affinityHint") },
    "routing.session-affinity-ttl": { label: t("ttl"), hint: t("ttlHint") },
    "routing.force-model-prefix": { label: t("prefix") },
    "routing.cooldown.disable-cooling": { label: t("disableCooling") },
    "routing.cooldown.transient-error-cooldown-seconds": { label: t("transient"), hint: t("transientHint") },
    "routing.cooldown.quota-response-mode": { label: t("quota"), hint: t("quotaHint") },
  };
  const strategies = {
    "round-robin": t("roundRobin"),
    "weighted-round-robin": t("weighted"),
    "fill-first": t("fillFirst"),
    "least-active": t("leastActive"),
  };
  const strategyHints = {
    "round-robin": t("roundRobinHint"),
    "weighted-round-robin": t("weightedHint"),
    "fill-first": t("fillFirstHint"),
    "least-active": t("leastActiveHint"),
  };
  const network = fields().network.map((field) => ({ ...field, ...labels[field.key] }));
  const controls = (filter: (f: Field) => boolean) =>
    network
      .filter(filter)
      .map(
        (field) =>
          settings && (
            <SettingControl
              key={field.key}
              field={field}
              settings={settings}
              immediate
              strategies={strategies}
              set={(value) => change(field, value)}
            />
          ),
      );
  return (
    <>
      <PageHead
        title={t("title")}
        actions={
          <Button disabled={busy} onClick={() => void run()}>
            {t("refresh")}
          </Button>
        }
      />
      {error && <Notice tone="danger">{error}</Notice>}
      {!settings ? (
        <Notice tone="muted">{t("loading")}</Notice>
      ) : (
        <div class="routing-layout">
          <div class="stack routing-tiers">
            {!tiers.length && <Empty>{t("empty")}</Empty>}
            {tiers.map((tier, index) => {
              const name = index === 0 ? t("preferred") : t("fallback", { index });
              return (
                <section key={tier.priority} data-part="tier" aria-label={name}>
                  <header class="routing-tier-head">
                    <h2>{name}</h2>
                    <span class="muted">{t("priority", { priority: tier.priority })}</span>
                  </header>
                  <List<Account>
                    label={name}
                    items={tier.accounts}
                    id={(a) => a.id}
                    columns={[
                      {
                        label: t("account"),
                        value: (a) => (
                          <>
                            {a.id}
                            <small class="routing-identity muted">
                              {a.label}
                              {a.extension && <> · {a.extension}</>}
                            </small>
                            {a.source === "config" && <Badge tone="muted">{t("config")}</Badge>}
                          </>
                        ),
                      },
                      {
                        label: t("state"),
                        value: (a) => {
                          const cooling = coolingTime(a);
                          return (
                            <>
                              <Badge tone="muted">{t("weight", { weight: a.weight ?? 1 })}</Badge>
                              {cooling && (
                                <span class="routing-state">
                                  <Badge tone="warn">{t("cooling", { time: cooling })}</Badge>
                                </span>
                              )}
                              {(a.disabled || a.state !== "active" || a.weight === 0) && (
                                <span class="routing-state">
                                  <Badge tone="muted">{t("unavailable")}</Badge>
                                </span>
                              )}
                            </>
                          );
                        },
                      },
                      {
                        label: t("actions"),
                        value: (a) => (
                          <div class="routing-actions">
                            <Button
                              disabled={busy || a.source === "config"}
                              onClick={() =>
                                void run(() =>
                                  api.changeAccountRouting(a.id, {
                                    priority: movePriority(tiers, tier.priority, "up"),
                                  }),
                                )
                              }
                            >
                              {t("up")}
                            </Button>
                            <Button
                              disabled={busy || a.source === "config"}
                              onClick={() =>
                                void run(() =>
                                  api.changeAccountRouting(a.id, {
                                    priority: movePriority(tiers, tier.priority, "down"),
                                  }),
                                )
                              }
                            >
                              {t("down")}
                            </Button>
                            <WeightEditor
                              account={a}
                              busy={busy}
                              save={(weight) => void run(() => api.changeAccountRouting(a.id, { weight }))}
                            />
                          </div>
                        ),
                      },
                    ]}
                  />
                </section>
              );
            })}
          </div>
          <aside class="routing-policy" data-part="card" aria-label={t("policy")}>
            <Card>
              <h2>{t("policy")}</h2>
              <p class="muted">{t("policyHint")}</p>
              <fieldset disabled={busy}>
                {controls((f) => f.kind === "strategy")}
                <small class="hint">{strategyHints[settings.routing.strategy]}</small>
                {controls((f) => f.key.startsWith("routing.session-affinity"))}
              </fieldset>
            </Card>
            <Card>
              <h3>{t("failover")}</h3>
              <fieldset disabled={busy}>{controls((f) => f.key.startsWith("routing.retry."))}</fieldset>
            </Card>
            <Card>
              <h3>{t("coolingTitle")}</h3>
              <fieldset disabled={busy}>{controls((f) => f.key.startsWith("routing.cooldown."))}</fieldset>
            </Card>
            <Card>
              <fieldset disabled={busy}>{controls((f) => f.key === "routing.force-model-prefix")}</fieldset>
            </Card>
          </aside>
        </div>
      )}
    </>
  );
}
export const routingPage = (): PageDef => ({
  id: "routing",
  group: "gateway",
  title: t("title"),
  short: t("short"),
  tip: t("tip"),
  render: () => <RoutingPage />,
});

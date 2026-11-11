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
  upFor: "Move up a tier: {id}",
  downFor: "Move down a tier: {id}",
  setWeightFor: "Set weight…: {id}",
  invalid: "{label}: {reason}",
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
/** Whether a move changes anything: a sole member of the first or last tier would only renumber it. */
export function canMove(tiers: Tier[], priority: number, direction: "up" | "down"): boolean {
  const index = tiers.findIndex((tier) => tier.priority === priority);
  const tier = tiers[index];
  if (!tier) return false;
  const edge = direction === "up" ? index === 0 : index === tiers.length - 1;
  return !(edge && tier.accounts.length === 1);
}
/** What identifies a focused control across a re-render that replaces its element. */
const focusKey = (element: Element | null) =>
  element instanceof HTMLElement && element !== document.body
    ? element.id
      ? `#${element.id}`
      : (element.getAttribute("aria-label") ?? (element.tagName === "BUTTON" ? element.textContent : "") ?? "")
    : "";
/** Puts focus back on the control with this key, or on the row's other button when it is disabled. */
function focusByKey(key: string) {
  if (!key) return;
  const found = [...document.querySelectorAll<HTMLElement>("button, input, select")].find(
    (element) => focusKey(element) === key,
  );
  const target =
    found && !(found as HTMLButtonElement).disabled
      ? found
      : found?.closest(".routing-actions")?.querySelector<HTMLElement>("button:not(:disabled)");
  target?.focus();
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
function WeightEditor({ account, save }: { account: Account; save: (weight: number | null) => void }) {
  const [editing, setEditing] = useState(false);
  const [value, setValue] = useState("");
  // Set once the editor is done with its value, so the blur that follows removing the input cannot save again.
  const closed = useRef(true);
  const refocus = useRef("");
  const name = t("setWeightFor", { id: account.id });
  useEffect(() => {
    if (editing || !refocus.current) return;
    focusByKey(refocus.current);
    refocus.current = "";
  }, [editing]);
  const commit = (input: HTMLInputElement) => {
    if (closed.current || !input.reportValidity()) return;
    closed.current = true;
    const weight = value === "" ? null : Number(value);
    setEditing(false);
    if (weight !== (account.weight ?? null)) save(weight);
  };
  return (
    <>
      <Button
        disabled={account.source === "config"}
        label={name}
        onClick={() => {
          setValue(account.weight === undefined ? "" : String(account.weight));
          closed.current = false;
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
            aria-label={name}
            min="0"
            step="1"
            value={value}
            onInput={(e) => setValue(e.currentTarget.value)}
            onBlur={(e) => commit(e.currentTarget)}
            onKeyDown={(e) => {
              if (e.key === "Enter") {
                e.preventDefault();
                commit(e.currentTarget);
              }
              if (e.key === "Escape") {
                closed.current = true;
                refocus.current = focusKey(e.currentTarget);
                setEditing(false);
              }
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
  const mounted = useRef(true);
  const latest = useRef<CoreSettings>();
  const turns = useRef(0);
  const chain = useRef<Promise<void>>(Promise.resolve());
  const refocus = useRef("");
  const load = async () => {
    const [s, a] = await Promise.all([api.coreSettings(), api.accounts()]);
    if (!mounted.current) return;
    if (s.status === 401 || a.status === 401) return unauthorized();
    if (s.body) {
      latest.current = s.body;
      setSettings(s.body);
    }
    if (a.body) setAccounts(a.body.accounts);
    const why = problem(s) ?? problem(a);
    if (why) setError(why);
  };
  /** Runs a change, then reloads. A change made while another is saving waits for its turn. */
  const run = (call?: () => Promise<Reply<unknown>>) => {
    turns.current++;
    setBusy(true);
    chain.current = chain.current.then(async () => {
      try {
        if (!mounted.current) return;
        refocus.current = focusKey(document.activeElement);
        setError(undefined);
        if (call) {
          const reply = await call();
          if (!mounted.current) return;
          if (reply.status === 401) return unauthorized();
          setError(problem(reply));
        }
        await load();
      } catch (e) {
        if (mounted.current) setError(e instanceof Error ? e.message : String(e));
      } finally {
        turns.current--;
        if (mounted.current && turns.current === 0) setBusy(false);
      }
    });
  };
  useEffect(() => {
    mounted.current = true;
    run();
    return () => {
      mounted.current = false;
    };
  }, []);
  // Saving re-renders rows into other tiers, and a removed control drops focus to the page; give it back.
  useEffect(() => {
    if (busy || !refocus.current) return;
    const key = refocus.current;
    refocus.current = "";
    if (!document.activeElement || document.activeElement === document.body) focusByKey(key);
  }, [busy]);
  const tiers = groupTiers(accounts);
  const weighted = settings?.routing.strategy === "weighted-round-robin";
  const change = (field: Field, value: Value) =>
    run(() => {
      const base = latest.current;
      if (!base) throw new Error(t("loading"));
      return api.changeCoreSettings(settingsChange(base, changeSetting(base, field.key, value)));
    });
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
              invalid={(reason) => setError(t("invalid", { label: field.label, reason }))}
            />
          ),
      );
  return (
    <>
      <PageHead
        title={t("title")}
        actions={
          <Button disabled={busy} onClick={() => run()}>
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
                              {weighted && (
                                <Badge tone="muted">{t("weight", { weight: a.weight ?? 1 })}</Badge>
                              )}
                              {cooling && (
                                <span class="routing-state">
                                  <Badge tone="warn">{t("cooling", { time: cooling })}</Badge>
                                </span>
                              )}
                              {(a.disabled || a.state !== "active" || (weighted && a.weight === 0)) && (
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
                            {(["up", "down"] as const).map((direction) => (
                              <Button
                                key={direction}
                                disabled={a.source === "config" || !canMove(tiers, tier.priority, direction)}
                                label={t(direction === "up" ? "upFor" : "downFor", { id: a.id })}
                                onClick={() => {
                                  const priority = movePriority(tiers, tier.priority, direction);
                                  run(() => api.changeAccountRouting(a.id, { priority }));
                                }}
                              >
                                {t(direction)}
                              </Button>
                            ))}
                            <WeightEditor
                              account={a}
                              save={(weight) => run(() => api.changeAccountRouting(a.id, { weight }))}
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
          <aside class="routing-policy" aria-label={t("policy")}>
            <Card>
              <h2>{t("policy")}</h2>
              <p class="muted">{t("policyHint")}</p>
              <fieldset>
                {controls((f) => f.kind === "strategy")}
                <small class="hint">{strategyHints[settings.routing.strategy]}</small>
                {controls((f) => f.key.startsWith("routing.session-affinity"))}
              </fieldset>
            </Card>
            <Card>
              <h3>{t("failover")}</h3>
              <fieldset>{controls((f) => f.key.startsWith("routing.retry."))}</fieldset>
            </Card>
            <Card>
              <h3>{t("coolingTitle")}</h3>
              <fieldset>{controls((f) => f.key.startsWith("routing.cooldown."))}</fieldset>
            </Card>
            <Card>
              <fieldset>{controls((f) => f.key === "routing.force-model-prefix")}</fieldset>
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

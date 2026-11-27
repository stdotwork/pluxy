// Order & fallback: which of a provider's accounts pluxy tries first, what it tries when no account
// can serve a model, and the rules by which accounts share requests. Moves and rules save at once;
// a fallback is saved from its editor.

import { useEffect, useMemo, useRef, useState } from "preact/hooks";
import { type Account, problem, runningExtensions } from "../api";
import type { Reply } from "../host";
import { Icon } from "../icons";
import {
  expireWindows,
  type Outcome,
  Phases,
  refreshWord,
  StaleNotice,
  StatePane,
  tryAgainWord,
  useShownAfter,
  useWindows,
} from "../parts";
import { accountIdentities, usePrivacy } from "../privacy";
import type { PageDef } from "../shell";
import { strings } from "../strings";
import { relativeTime } from "../time";
import { BUSY_SHOWS_AFTER_MS, Button, Notice, PageHead, useKit, useLoad } from "../ui";
import { Fallbacks } from "./routingFallbacks";
import { AccountOrder } from "./routingOrder";
import { Rules } from "./routingRules";

const t = strings("routing", {
  title: "Order & fallback",
  tab: "Order & fallback",
  tip: "Which account goes first, and what to try when none can answer",
  updated: "Updated {when}",
  loading: "Loading the order…",
  failed: "Could not read the order.",
  orderHeading: "Account order",
  rulesHeading: "Rules",
  loadingRules: "Loading the rules…",
  failedRules: "Could not read the rules.",
  loadingOrder: "Loading the order…",
});

/** An account as a move or a new weight left it: no weight (null) is the account's default. */
const place = (a: Account, to: { priority?: number; weight?: number | null }): Account => ({
  ...a,
  ...(to.priority !== undefined ? { priority: to.priority } : {}),
  ...(to.weight !== undefined ? { weight: to.weight ?? undefined } : {}),
});

/** A clock that moves every interval, for "Updated 2m ago". */
function useNow(every = 15_000) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), every);
    return () => clearInterval(timer);
  }, [every]);
  return now;
}

/** When the page last read pluxy, as the location bar says it. */
function Freshness(props: { at?: number }) {
  const now = useNow();
  if (props.at === undefined) return null;
  return (
    <span class="routing-fresh" title={new Date(props.at).toLocaleString()}>
      {t("updated", { when: relativeTime(new Date(props.at), Math.max(now, props.at)) })}
    </span>
  );
}

/** The page in its final two-column shape while the first load runs. */
function RoutingSkeleton() {
  const shown = useShownAfter(BUSY_SHOWS_AFTER_MS);
  if (!shown) return null;
  return (
    <div class="routing-skeleton" aria-hidden="true">
      <div class="routing-skeleton-main">
        <i class="skeleton-provider" />
        <i class="skeleton-fallbacks" />
      </div>
      <div class="routing-skeleton-side">
        <i class="skeleton-rules" />
      </div>
    </div>
  );
}

export function RoutingPage() {
  const { api, unauthorized } = useKit();
  const p = usePrivacy();
  const accounts = useLoad(api.accounts);
  const settings = useLoad(api.coreSettings);
  const chains = useLoad(api.chains);
  const models = useLoad(api.servedModels);
  const config = useLoad(api.config);
  const status = useLoad(api.status);
  const [error, setError] = useState<string>();
  const [yaml, setYaml] = useState(false);
  const [refreshed, setRefreshed] = useState(0);
  const [lastAt, setLastAt] = useState<number>();
  // What core said about moves not yet read back, so a row sits where it was put at once.
  // Each belongs to the read it was made over: a newer read has it in, so it is not used again.
  const [placed, setPlaced] = useState<{
    over: unknown;
    by: Record<string, { priority?: number; weight?: number | null }>;
  }>({
    over: undefined,
    by: {},
  });
  const [saving, setSaving] = useState(0);
  const read = useRef(accounts.data);
  read.current = accounts.data;

  useEffect(() => {
    if (accounts.phase === "ready" || settings.phase === "ready" || chains.phase === "ready") {
      setLastAt(Date.now());
    }
  }, [accounts.phase, accounts.data, settings.phase, settings.data, chains.phase, chains.data]);

  const list = useMemo(
    () =>
      accounts.data?.accounts.map((a) =>
        placed.over === accounts.data && placed.by[a.id] ? place(a, placed.by[a.id] ?? {}) : a,
      ) ?? [],
    [accounts.data, placed],
  );
  const running = useMemo(() => runningExtensions(status.data), [status.data]);
  const windows = useWindows(accounts.data ? list : undefined, running, refreshed);

  // One save at a time, in the order they were asked for, so two on the same account never cross.
  const queue = useRef<Promise<unknown>>(Promise.resolve());
  const save = async (id: string, patch: { priority?: number; weight?: number | null }) => {
    setError(undefined);
    try {
      const reply = await api.changeAccountRouting(id, patch);
      if (reply.status === 401) return unauthorized();
      const why = problem(reply);
      if (why !== undefined) return setError(why);
      setPlaced((now) => {
        const by = now.over === read.current ? now.by : {};
        return { over: read.current, by: { ...by, [id]: { ...by[id], ...patch } } };
      });
    } finally {
      setSaving((n) => n - 1);
      accounts.reload();
    }
  };
  const change = (id: string, patch: { priority?: number; weight?: number | null }) => {
    setSaving((n) => n + 1);
    const done = queue.current.then(() => save(id, patch));
    queue.current = done.catch(() => {});
    return done;
  };
  const fixed = async (reply: Promise<Reply<unknown>>): Promise<Outcome> => {
    const r = await reply;
    if (problem(r) === undefined) accounts.reload();
    return r;
  };
  const turnOn = (id: string) => fixed(api.setDisabled(id, false));
  const clearCooldown = (id: string) => fixed(api.resetCooldown(id));

  const reloadAll = () => {
    expireWindows();
    setRefreshed((n) => n + 1);
    for (const load of [accounts, settings, chains, models, config, status]) load.reload();
  };

  const all = [accounts, settings, chains];
  const nothingYet = all.every((l) => l.phase === "loading");
  const nothingRead = all.every((l) => l.phase === "failed" && l.data === undefined);
  const stale = all.find((l) => l.phase === "failed" && l.data !== undefined);
  const weighted = settings.data?.routing.strategy === "weighted-round-robin";
  return (
    <>
      <PageHead
        title={t("title")}
        actions={
          <>
            <Freshness at={lastAt} />
            <Button variant="quiet" onClick={reloadAll}>
              <Icon name="refresh" size={14} />
              {refreshWord()}
            </Button>
          </>
        }
      />
      {nothingYet ? (
        <>
          <StatePane kind="loading" title={t("loading")} rows={0} />
          <RoutingSkeleton />
        </>
      ) : nothingRead ? (
        <StatePane
          kind="failed"
          title={t("failed")}
          details={accounts.error}
          primary={{ label: tryAgainWord(), onClick: reloadAll }}
        />
      ) : (
        <div class="routing-page" data-stale={stale ? "" : undefined}>
          {stale && lastAt !== undefined && (
            <StaleNotice at={lastAt} error={stale.error} onRetry={reloadAll} />
          )}
          <div class="routing-layout">
            <div class="routing-main">
              <section class="routing-order" aria-labelledby="routing-order-h">
                <h2 id="routing-order-h">{t("orderHeading")}</h2>
                {error && <Notice tone="danger">{p.text(error, list.flatMap(accountIdentities))}</Notice>}
                <Phases
                  load={accounts}
                  loading={t("loadingOrder")}
                  failed={{ title: t("failed") }}
                  isEmpty={() => false}
                  empty={null}
                >
                  {() => (
                    <AccountOrder
                      accounts={list}
                      windows={windows}
                      running={running}
                      weighted={weighted}
                      busy={saving > 0}
                      change={change}
                      onTurnOn={turnOn}
                      onClearCooldown={clearCooldown}
                    />
                  )}
                </Phases>
              </section>
              <Fallbacks
                chains={chains}
                accounts={list}
                models={models.data?.models}
                running={running}
                yaml={yaml || config.data?.format === "yaml"}
                onYaml={() => setYaml(true)}
              />
            </div>
            <aside class="routing-rules-pane" aria-labelledby="routing-rules-h">
              <h2 id="routing-rules-h">{t("rulesHeading")}</h2>
              <Phases
                load={settings}
                loading={t("loadingRules")}
                failed={{ title: t("failedRules") }}
                isEmpty={() => false}
                empty={null}
              >
                {(data) => <Rules settings={data} reload={settings.reload} />}
              </Phases>
            </aside>
          </div>
        </div>
      )}
    </>
  );
}

export const routingPage = (): PageDef => ({
  id: "routing",
  dest: "accounts",
  kind: "tab",
  label: t("tab"),
  title: t("title"),
  tip: t("tip"),
  render: () => <RoutingPage />,
});

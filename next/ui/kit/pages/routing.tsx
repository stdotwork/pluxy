// Order & fallback: which of a provider's accounts pluxy tries first, what it tries when no account
// can serve a model, and the rules by which accounts share requests. Moves and rules save at once;
// a fallback is saved from its editor.

import { useMemo, useRef, useState } from "preact/hooks";
import { type Account, problem, runningExtensions } from "../api";
import { Phases, refreshWord, StatePane, tryAgainWord } from "../parts";
import { accountIdentities, usePrivacy } from "../privacy";
import type { PageDef } from "../shell";
import { strings } from "../strings";
import { Button, Notice, PageHead, useKit, useLoad } from "../ui";
import { Fallbacks } from "./routingFallbacks";
import { AccountOrder } from "./routingOrder";
import { Rules } from "./routingRules";

const t = strings("routing", {
  title: "Order & fallback",
  tab: "Order & fallback",
  tip: "Which account goes first, and what to try when none can answer",
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

  const list = useMemo(
    () =>
      accounts.data?.accounts.map((a) =>
        placed.over === accounts.data && placed.by[a.id] ? place(a, placed.by[a.id] ?? {}) : a,
      ) ?? [],
    [accounts.data, placed],
  );
  const running = useMemo(() => runningExtensions(status.data), [status.data]);

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
  const reloadAll = () => {
    for (const load of [accounts, settings, chains, models, config, status]) load.reload();
  };

  const all = [accounts, settings, chains];
  const nothingYet = all.every((l) => l.phase === "loading");
  const nothingRead = all.every((l) => l.phase === "failed" && l.data === undefined);
  const weighted = settings.data?.routing.strategy === "weighted-round-robin";
  return (
    <>
      <PageHead
        title={t("title")}
        actions={
          <Button variant="quiet" onClick={reloadAll}>
            {refreshWord()}
          </Button>
        }
      />
      {nothingYet ? (
        <StatePane kind="loading" title={t("loading")} rows={4} />
      ) : nothingRead ? (
        <StatePane
          kind="failed"
          title={t("failed")}
          details={accounts.error}
          primary={{ label: tryAgainWord(), onClick: reloadAll }}
        />
      ) : (
        <div class="routing-page">
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
                      running={running}
                      weighted={weighted}
                      busy={saving > 0}
                      change={change}
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

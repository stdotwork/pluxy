import { useCallback, useEffect, useMemo, useRef, useState } from "preact/hooks";
import { type Account, problem, runningExtensions } from "../api";
import type { Reply } from "../host";
import { Icon } from "../icons";
import {
  AccountRow,
  accountCondition,
  accountFix,
  accountName,
  conditionUnchecked,
  GROUPS,
  type Group,
  groupWord,
  type Outcome,
  PoolCard,
  ProviderMark,
  poolOf,
  providerName,
  StatePane,
  staleWords,
  tryAgainWord,
  uncheckedWord,
  useNaming,
  useShownAfter,
  useWindows,
} from "../parts";
import { accountIdentities, usePrivacy } from "../privacy";
import type { PageDef } from "../shell";
import { strings } from "../strings";
import { BUSY_SHOWS_AFTER_MS, Button, Empty, Notice, PageHead, useKit, usePoll, useThemeState } from "../ui";
import {
  type BatchAction,
  batchTargets,
  countsLine,
  groupAccounts,
  matchesSearch,
  providerCounts,
  type StateCounts,
  stateCounts,
} from "./accounts.helpers";
import { ConfigDialog } from "./configDialog";
import { type Read, staleOf } from "./home.helpers";

const t = strings("accounts", {
  title: "Accounts",
  tab: "All accounts",
  tip: "Every account, by what it needs from you",
  add: "Add an account",
  loading: "Loading accounts…",
  failed: "Could not read the accounts.",
  empty: "No accounts yet.",
  emptyText: "An account is a subscription or an API key that pluxy sends your requests through.",
  find: "Find an account",
  allProviders: "All",
  filterProvider: "Filter by provider",
  view: "Account view",
  viewList: "List",
  viewCards: "Cards",
  noMatch: 'No account matches "{search}".',
  noMatchProvider: 'No {provider} account matches "{search}".',
  clearSearch: "Clear search",
  select: "Select",
  selectAll: "Select all",
  clearSelection: "Clear",
  selectOne: "Select {name}",
  selected: { one: "{count} selected", other: "{count} selected" },
  selectedHidden: "{count} selected, {hidden} hidden",
  hiddenOne: "The search hides it.",
  hiddenMany: { one: "The search hides {count} of them.", other: "The search hides {count} of them." },
  hiddenFilterOne: "The filter hides it.",
  hiddenFilterMany: { one: "The filter hides {count} of them.", other: "The filter hides {count} of them." },
  statusUnread: "Could not check which providers are running.",
  weekUnread: "Could not read this week's requests.",
  actions: "Actions for the selected accounts",
  turnOn: "Turn on",
  turnOff: "Turn off",
  clearCooldown: "Clear cooldown",
  remove: "Remove…",
  removeNow: "Remove",
  done: "Done",
  cancel: "Cancel",
  removeOne: "Remove {name}?",
  removeMany: { one: "Remove {count} account?", other: "Remove {count} accounts?" },
  removeText: "pluxy deletes its saved sign-in. To use it again, add it again.",
  skipped: {
    one: "Skipped {count} account set in pluxy's settings file.",
    other: "Skipped {count} accounts set in pluxy's settings file.",
  },
  failure: "{name}: {error}",
  group: "{group} ({count})",
});

/** How often the list is read again while the page shows. */
export const ACCOUNTS_POLL_MS = 10_000;

const unreachable = (error: unknown): Reply<never> => ({
  status: 0,
  error: { code: "unreachable", message: error instanceof Error ? error.message : String(error) },
});

/** What a read comes to: the data when it worked, else the failure over the data read before. */
function settleRead<T>(prev: Read<T>, r: Reply<T>, at: number): Read<T> {
  const error = problem(r);
  if (error === undefined) return { phase: "ready", data: r.body, at };
  return { phase: "failed", data: prev.data, error, status: r.status, at: prev.at };
}

export type Reads<T> = { [K in keyof T]: Read<T[K]> };

/**
 * A page's reads taken together in rounds, one round at a time, every ms while active: the page
 * draws once all of a round's replies are in, so the accounts never show before the status that
 * says which providers run. run reads one round now (or joins the one under way); again reads one
 * more after it, for a change that worked. A failed read keeps the data read before.
 */
export function useRound<T extends Record<string, unknown>>(
  loads: { [K in keyof T]: () => Promise<Reply<T[K]>> },
  ms: number,
  active = true,
) {
  const { unauthorized } = useKit();
  const [reads, setReads] = useState<Reads<T>>(
    () => Object.fromEntries(Object.keys(loads).map((k) => [k, { phase: "loading" }])) as Reads<T>,
  );
  const loadsRef = useRef(loads);
  loadsRef.current = loads;
  const inflight = useRef<Promise<void>>(undefined);
  const live = useRef(true);
  useEffect(() => {
    live.current = true;
    return () => {
      live.current = false;
    };
  }, []);
  const run = useCallback((): Promise<void> => {
    if (inflight.current) return inflight.current;
    const p = (async () => {
      const now = loadsRef.current;
      const keys = Object.keys(now) as (keyof T & string)[];
      const replies = await Promise.all(keys.map((k) => now[k]().catch(unreachable)));
      if (!live.current) return;
      if (replies.some((r) => r.status === 401)) return unauthorized();
      const at = Date.now();
      setReads((prev) => {
        const next = { ...prev };
        keys.forEach((k, i) => {
          next[k] = settleRead(prev[k], replies[i] as Reply<T[typeof k]>, at);
        });
        return next;
      });
    })().finally(() => {
      inflight.current = undefined;
    });
    inflight.current = p;
    return p;
  }, [unauthorized]);
  const again = useCallback(async () => {
    await inflight.current;
    await run();
  }, [run]);
  useEffect(() => {
    void run();
  }, [run]);
  usePoll(run, ms, active);
  return { reads, run, again };
}

/**
 * One notice for a round that did not all work, with one Try again: "Could not refresh" when a
 * read failed over data read before (K3's stale notice), then what could not be read at all.
 * Nothing when every read worked. ids mask core's error, shown in the title.
 */
export function RoundNotice(props: {
  reads: Read<unknown>[];
  unread: string[];
  ids: readonly string[];
  onRetry: () => unknown;
}) {
  const p = usePrivacy();
  const stale = staleOf(props.reads);
  if (!stale && props.unread.length === 0) return null;
  const error = stale?.error ?? props.reads.find((r) => r.phase === "failed")?.error;
  const words = [stale && staleWords(stale.at), ...props.unread].filter(Boolean).join(" ");
  return (
    <div class="stale-notice" data-part="round-notice" title={p.text(error, props.ids)}>
      <Notice tone="warn">
        {words} <Button onClick={props.onRetry}>{tryAgainWord()}</Button>
      </Notice>
    </div>
  );
}

/** Above this many accounts the list offers Find an account. */
const SEARCH_ABOVE = 6;

export const ACCOUNTS_VIEW_KEY = "pluxy.accounts.view";

function accountsStorage(): Storage | undefined {
  try {
    return (
      (typeof window !== "undefined" ? (window as { localStorage?: Storage }).localStorage : undefined) ??
      globalThis.localStorage
    );
  } catch {
    return undefined;
  }
}

function readStoredView(): "list" | "cards" | undefined {
  try {
    const value = accountsStorage()?.getItem(ACCOUNTS_VIEW_KEY);
    return value === "list" || value === "cards" ? value : undefined;
  } catch {
    return undefined;
  }
}

function writeStoredView(view: "list" | "cards"): void {
  try {
    accountsStorage()?.setItem(ACCOUNTS_VIEW_KEY, view);
  } catch {}
}

function AddButton(props: { quiet?: boolean }) {
  return (
    <a class="btn" data-part="button" data-variant={props.quiet ? "quiet" : "primary"} href="#/add-account">
      <Icon name="plus" />
      {t("add")}
    </a>
  );
}

function ViewIcon(props: { kind: "list" | "cards" }) {
  return (
    <svg
      class="icon"
      data-part="icon"
      viewBox="0 0 16 16"
      width="14"
      height="14"
      fill="none"
      stroke="currentColor"
      stroke-width="1.25"
      stroke-linecap="round"
      stroke-linejoin="round"
      aria-hidden="true"
      focusable="false"
    >
      {props.kind === "list" ? (
        <path d="M2.5 4.25h11M2.5 8h11M2.5 11.75h11" />
      ) : (
        <path d="M2.5 2.5h4.5v4.5H2.5zM9 2.5h4.5v4.5H9zM2.5 9h4.5v4.5H2.5zM9 9h4.5v4.5H9z" />
      )}
    </svg>
  );
}

function StateBar(props: { counts: StateCounts }) {
  const c = props.counts;
  const parts = (["serving", "waiting", "needs", "unchecked", "off"] as const).filter((g) => c[g] > 0);
  if (parts.length === 0) return null;
  return (
    <div class="accounts-state-bar" aria-hidden="true">
      {parts.map((g) => (
        <span key={g} data-group={g} style={{ flexGrow: c[g] }} />
      ))}
    </div>
  );
}

function AccountsSkeleton(props: { view: "list" | "cards" }) {
  const shown = useShownAfter(BUSY_SHOWS_AFTER_MS);
  if (!shown) return null;
  if (props.view === "cards") {
    return (
      <div class="accounts-skeleton" data-view="cards" aria-hidden="true">
        <i class="accounts-skeleton-bar" />
        <div class="accounts-skeleton-cards">
          {[0, 1, 2, 3, 4, 5].map((i) => (
            <i key={i} class="accounts-skeleton-card" />
          ))}
        </div>
      </div>
    );
  }
  return (
    <div class="accounts-skeleton" data-view="list" aria-hidden="true">
      <i class="accounts-skeleton-bar" />
      <div class="accounts-skeleton-group">
        <i class="accounts-skeleton-head" />
        {[0, 1, 2, 3, 4].map((i) => (
          <i key={i} class="accounts-skeleton-row" />
        ))}
      </div>
    </div>
  );
}

interface Report {
  skipped: number;
  errors: string[];
}

export function AccountsPage() {
  const { api, unauthorized } = useKit();
  const themes = useThemeState();
  const p = usePrivacy();
  const [selecting, setSelecting] = useState(false);
  const [busy, setBusy] = useState(false);
  // Paused while Select is on and during a batch, so rows do not move under the person's hand.
  const { reads, run, again } = useRound(
    { accounts: api.accounts, status: api.status, week: () => api.usage("since=7d") },
    ACCOUNTS_POLL_MS,
    !selecting && !busy,
  );
  const { accounts, status, week } = reads;
  const list = accounts.data?.accounts;
  const naming = useNaming(list);
  const running = useMemo(() => runningExtensions(status.data), [status.data]);
  // A status never read: whether each provider runs is not known, so no account reads Serving.
  const unchecked = status.phase === "failed" && status.data === undefined;
  const [provider, setProvider] = useState("");
  const [query, setQuery] = useState("");
  const [checked, setChecked] = useState<Set<string>>(new Set());
  const [asking, setAsking] = useState(false);
  const [report, setReport] = useState<Report>();
  const themeList = themes?.resolved?.layout?.list;
  const [storedView, setStoredView] = useState<"list" | "cards" | undefined>(readStoredView);
  const view: "list" | "cards" = storedView ?? (themeList === "cards" ? "cards" : "list");
  const switchView = (next: "list" | "cards") => {
    if (next === view) return;
    setStoredView(next);
    writeStoredView(next);
  };

  // A ticked account that is gone is no longer selected.
  useEffect(() => {
    if (list) setChecked((old) => new Set([...old].filter((id) => list.some((a) => a.id === id))));
  }, [list]);

  const providers = useMemo(() => providerCounts(list ?? []), [list]);
  useEffect(() => {
    if (provider && list && !providers.some((pr) => pr.extension === provider)) setProvider("");
  }, [provider, list, providers]);

  const now = Date.now();
  const sections = useMemo(() => {
    const scoped = provider ? (list ?? []).filter((a) => a.extension === provider) : (list ?? []);
    const all = groupAccounts(scoped, running, now);
    const shown = (rows: typeof all.needs) =>
      rows.filter((x) => matchesSearch(x.account, x.c, query)).map((x) => x.account);
    // The accounts whose condition waits on a status never read: one group of their own, by name,
    // in the place of Waiting and Serving.
    const waits = (x: (typeof all.needs)[number]) => unchecked && conditionUnchecked(x.account, x.c, running);
    const notChecked = shown(
      [...all.waiting, ...all.serving]
        .filter(waits)
        .sort(
          (x, y) =>
            accountName(x.account).localeCompare(accountName(y.account)) ||
            x.account.id.localeCompare(y.account.id),
        ),
    );
    const out: { key: Group | "unchecked"; rows: Account[] }[] = [];
    for (const g of GROUPS) {
      if (g === "waiting" && notChecked.length) out.push({ key: "unchecked", rows: notChecked });
      const rows = shown(all[g].filter((x) => !waits(x)));
      if (rows.length) out.push({ key: g, rows });
    }
    return out;
  }, [list, running, provider, query, unchecked]);

  const ordered = useMemo(() => sections.flatMap((s) => s.rows), [sections]);
  const providerAccounts = useMemo(
    () => (provider && list ? list.filter((a) => a.extension === provider) : []),
    [provider, list],
  );
  const forWindows = useMemo(() => {
    if (!list) return undefined;
    if (!provider) return ordered;
    const seen = new Set(ordered.map((a) => a.id));
    return [...ordered, ...providerAccounts.filter((a) => !seen.has(a.id))];
  }, [list, provider, ordered, providerAccounts]);
  const windows = useWindows(forWindows, running);

  const firstRepairId = useMemo(() => {
    const needs = sections.find((s) => s.key === "needs")?.rows ?? [];
    return needs.find((a) => accountFix(a, accountCondition(a, running, now))?.variant === "primary")?.id;
  }, [sections, running]);

  // The week's requests once read: an account with none this week reads 0.
  const requests = new Map<string, number>();
  for (const g of week.data?.accounts ?? []) requests.set(g.name, g.total_requests);
  const weekCount = (id: string) => (week.data ? (requests.get(id) ?? 0) : undefined);
  const selected = (list ?? []).filter((a) => checked.has(a.id));
  const shownIds = new Set(ordered.map((a) => a.id));
  const hidden = selected.filter((a) => !shownIds.has(a.id)).length;
  const allVisibleSelected = ordered.length > 0 && ordered.every((a) => checked.has(a.id));

  const settle = (r: Outcome): string | undefined => {
    if (typeof r === "string") return r || undefined;
    if (!r) return undefined;
    if (r.status === 401) {
      unauthorized();
      return undefined;
    }
    const why = problem(r);
    if (why === undefined) void again();
    return why;
  };
  const turnOn = async (id: string) => settle(await api.setDisabled(id, false));
  const clearCooldown = async (id: string) => settle(await api.resetCooldown(id));

  // One account after another; a failure is one line and the rest still run.
  const runBatch = async (action: BatchAction) => {
    setAsking(false);
    setBusy(true);
    setReport(undefined);
    const targets = batchTargets(selected, action);
    const errors: string[] = [];
    const removed = new Set<string>();
    for (const a of targets) {
      let why: string | undefined;
      try {
        const r =
          action === "remove"
            ? await api.removeAccount(a.id)
            : action === "clearCooldown"
              ? await api.resetCooldown(a.id)
              : await api.setDisabled(a.id, action === "turnOff");
        if (r.status === 401) {
          unauthorized();
          return;
        }
        why = problem(r);
      } catch (error) {
        why = String(error);
      }
      if (why !== undefined)
        errors.push(p.text(t("failure", { name: naming.of(a), error: why }), accountIdentities(a)));
      else if (action === "remove") removed.add(a.id);
    }
    setChecked((old) => new Set([...old].filter((id) => !removed.has(id))));
    setReport({ skipped: selected.length - targets.length, errors });
    setBusy(false);
    void again();
  };

  const stopSelecting = () => {
    setSelecting(false);
    setChecked(new Set());
    setReport(undefined);
  };
  const tick = (id: string, on: boolean) =>
    setChecked((old) => {
      const next = new Set(old);
      if (on) next.add(id);
      else next.delete(id);
      return next;
    });
  const toggleVisible = () =>
    setChecked((old) => {
      const next = new Set(old);
      if (allVisibleSelected) {
        for (const a of ordered) next.delete(a.id);
      } else {
        for (const a of ordered) next.add(a.id);
      }
      return next;
    });

  const headQuiet =
    firstRepairId !== undefined ||
    accounts.phase === "loading" ||
    !list ||
    (accounts.phase === "failed" && list.length === 0) ||
    list.length === 0;
  const head = <PageHead title={t("title")} actions={<AddButton quiet={headQuiet} />} />;
  if (accounts.phase === "loading") {
    return (
      <>
        {head}
        <div class="accounts-loading">
          <StatePane kind="loading" title={t("loading")} rows={0} />
          <AccountsSkeleton view={view} />
        </div>
      </>
    );
  }
  if (!list || (accounts.phase === "failed" && list.length === 0)) {
    // Whether the list is still empty is not known after a failure.
    return (
      <>
        {head}
        <div class="accounts-pane card">
          <StatePane
            kind="failed"
            title={t("failed")}
            details={accounts.error}
            primary={{ label: tryAgainWord(), onClick: run }}
          />
        </div>
      </>
    );
  }
  if (list.length === 0) {
    return (
      <>
        {head}
        <div class="accounts-pane card">
          <StatePane kind="empty" title={t("empty")} primary={{ label: t("add"), href: "#/add-account" }}>
            <p>{t("emptyText")}</p>
          </StatePane>
        </div>
      </>
    );
  }

  const one = selected.length === 1 ? selected[0] : undefined;
  const stale = staleOf([accounts, status, week]);
  const unread = [
    unchecked ? t("statusUnread") : "",
    week.phase === "failed" && week.data === undefined ? t("weekUnread") : "",
  ].filter(Boolean);
  const summaryList = provider ? providerAccounts : list;
  const counts = stateCounts(summaryList, running, now, unchecked);
  const selectedPool =
    provider && !unchecked && providerAccounts.length > 0
      ? poolOf(provider, providerAccounts, windows, running, now)
      : undefined;
  const hiddenBySearch = query.trim() !== "";

  return (
    <>
      {head}
      <RoundNotice
        reads={[accounts, status, week]}
        unread={unread}
        ids={list.flatMap(accountIdentities)}
        onRetry={run}
      />
      <div
        class="accounts-page"
        data-view={view}
        data-selecting={selecting ? "" : undefined}
        data-stale={stale ? "" : undefined}
      >
        <div class="accounts-summary">
          <p class="accounts-counts muted">
            {/* A long line wraps between its counts, never inside one. */}
            {countsLine(summaryList, running, now, unchecked, provider ? providerName(provider) : undefined)
              .split(" · ")
              .map((part, i, all) => (
                <span key={part}>
                  <span class="accounts-count">{i < all.length - 1 ? `${part} ·` : part}</span>
                  {i < all.length - 1 && " "}
                </span>
              ))}
          </p>
          <StateBar counts={counts} />
        </div>

        <div class="accounts-toolbar">
          {providers.length >= 2 && (
            <fieldset class="accounts-providers" aria-label={t("filterProvider")}>
              <button
                type="button"
                class="accounts-chip"
                aria-pressed={provider === ""}
                onClick={() => setProvider("")}
              >
                <span>{t("allProviders")}</span>
                <span class="accounts-chip-count num">{list.length}</span>
              </button>
              {providers.map((pr) => (
                <button
                  key={pr.extension}
                  type="button"
                  class="accounts-chip"
                  aria-pressed={provider === pr.extension}
                  onClick={() => setProvider(pr.extension)}
                >
                  <ProviderMark extension={pr.extension} size="small" />
                  <span>{pr.name}</span>
                  <span class="accounts-chip-count num">{pr.count}</span>
                </button>
              ))}
            </fieldset>
          )}

          <div class="accounts-controls">
            {(list.length > SEARCH_ABOVE || query !== "") && (
              <label class="accounts-find">
                <Icon name="search" size={14} />
                <span class="sr-only">{t("find")}</span>
                <input
                  data-slot="input"
                  type="search"
                  placeholder={t("find")}
                  value={query}
                  onInput={(e) => setQuery(e.currentTarget.value)}
                />
              </label>
            )}
            <fieldset class="accounts-view" aria-label={t("view")}>
              <button
                type="button"
                class="accounts-view-btn"
                aria-pressed={view === "list"}
                onClick={() => switchView("list")}
              >
                <ViewIcon kind="list" />
                <span>{t("viewList")}</span>
              </button>
              <button
                type="button"
                class="accounts-view-btn"
                aria-pressed={view === "cards"}
                onClick={() => switchView("cards")}
              >
                <ViewIcon kind="cards" />
                <span>{t("viewCards")}</span>
              </button>
            </fieldset>
            {list.length >= 2 && !selecting && (
              <Button onClick={() => setSelecting(true)}>{t("select")}</Button>
            )}
          </div>
        </div>

        {report && (report.skipped > 0 || report.errors.length > 0) && (
          <div class="accounts-report">
            {report.skipped > 0 && <Notice tone="muted">{t("skipped", { count: report.skipped })}</Notice>}
            {report.errors.map((error) => (
              <Notice key={error} tone="danger">
                {error}
              </Notice>
            ))}
          </div>
        )}

        <div class="accounts-fade">
          {selectedPool && selectedPool.total > 0 && (
            <div class="accounts-pool">
              <PoolCard pool={selectedPool} />
            </div>
          )}

          {sections.length === 0 ? (
            <div class="accounts-empty card">
              <Empty onClear={() => setQuery("")} clearLabel={t("clearSearch")}>
                {provider
                  ? t("noMatchProvider", { provider: providerName(provider), search: query })
                  : t("noMatch", { search: query })}
              </Empty>
            </div>
          ) : (
            <div class="accounts-groups">
              {sections.map(({ key, rows }) => {
                const word = key === "unchecked" ? uncheckedWord() : groupWord(key);
                return (
                  <section key={key} class="accounts-group" data-group={key} aria-label={word}>
                    <h2 class="accounts-group-head">
                      <span class="accounts-group-name">{word}</span>{" "}
                      <span class="accounts-group-count num">{rows.length}</span>
                    </h2>
                    <ul class="accounts-rows">
                      {rows.map((a) => (
                        <li
                          key={a.id}
                          class="accounts-item"
                          data-selected={checked.has(a.id) ? "" : undefined}
                        >
                          {selecting && (
                            <label class="accounts-pick hit">
                              <input
                                type="checkbox"
                                data-part="select"
                                aria-label={t("selectOne", { name: naming.of(a) })}
                                checked={checked.has(a.id)}
                                disabled={busy}
                                onChange={(e) => tick(a.id, e.currentTarget.checked)}
                              />
                            </label>
                          )}
                          <AccountRow
                            account={a}
                            windows={windows.get(a.id)}
                            wide
                            requests={weekCount(a.id)}
                            running={running}
                            unchecked={key === "unchecked"}
                            grouped={provider !== ""}
                            quietFix={a.id !== firstRepairId}
                            onTurnOn={turnOn}
                            onClearCooldown={clearCooldown}
                          />
                        </li>
                      ))}
                    </ul>
                  </section>
                );
              })}
            </div>
          )}
        </div>

        {selecting && (
          <div class="accounts-batch" data-part="batch" role="toolbar" aria-label={t("actions")}>
            <span class="accounts-batch-count num" aria-live="polite">
              {hidden > 0
                ? t("selectedHidden", { count: selected.length, hidden })
                : t("selected", { count: selected.length })}
            </span>
            <div class="accounts-batch-actions">
              {ordered.length > 0 && (
                <Button variant="ghost" disabled={busy} onClick={toggleVisible}>
                  {allVisibleSelected ? t("clearSelection") : t("selectAll")}
                </Button>
              )}
              <Button disabled={busy || !selected.length} onClick={() => runBatch("turnOn")}>
                {t("turnOn")}
              </Button>
              <Button disabled={busy || !selected.length} onClick={() => runBatch("turnOff")}>
                {t("turnOff")}
              </Button>
              <Button disabled={busy || !selected.length} onClick={() => runBatch("clearCooldown")}>
                {t("clearCooldown")}
              </Button>
              <Button variant="danger" disabled={busy || !selected.length} onClick={() => setAsking(true)}>
                {t("remove")}
              </Button>
              <Button disabled={busy} onClick={stopSelecting}>
                {t("done")}
              </Button>
            </div>
          </div>
        )}
      </div>
      {asking && (
        <ConfigDialog
          title={one ? t("removeOne", { name: naming.of(one) }) : t("removeMany", { count: selected.length })}
          close={() => setAsking(false)}
        >
          <p>{t("removeText")}</p>
          {hidden > 0 && (
            <p>
              {hiddenBySearch
                ? one
                  ? t("hiddenOne")
                  : t("hiddenMany", { count: hidden })
                : one
                  ? t("hiddenFilterOne")
                  : t("hiddenFilterMany", { count: hidden })}
            </p>
          )}
          <div class="actions">
            <Button variant="danger" onClick={() => runBatch("remove")}>
              {t("removeNow")}
            </Button>
            <Button onClick={() => setAsking(false)}>{t("cancel")}</Button>
          </div>
        </ConfigDialog>
      )}
    </>
  );
}

export const accountsPage = (): PageDef => ({
  id: "accounts",
  dest: "accounts",
  kind: "tab",
  title: t("title"),
  label: t("tab"),
  tip: t("tip"),
  // The Quota page's old address: limits are on every account's row and page now.
  addresses: ["quota"],
  render: () => <AccountsPage />,
});

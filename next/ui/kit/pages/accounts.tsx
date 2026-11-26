// All accounts: every account in the group of what it needs from the person (Needs you, Waiting,
// Serving, Turned off), each a wide AccountRow with its limits and its week's requests. Select
// turns on a checkbox per row and a bar of actions for the ones ticked. One account's own page is
// account.tsx.

import { useEffect, useMemo, useState } from "preact/hooks";
import { problem, runningExtensions } from "../api";
import { Icon } from "../icons";
import {
  AccountRow,
  type Group,
  groupWord,
  type Outcome,
  Phases,
  StatePane,
  useNaming,
  useWindows,
} from "../parts";
import { accountIdentities, usePrivacy } from "../privacy";
import type { PageDef } from "../shell";
import { strings } from "../strings";
import { Button, Empty, Notice, PageHead, useKit, useLoad, usePoll } from "../ui";
import {
  type BatchAction,
  batchTargets,
  countsLine,
  groupAccounts,
  matchesSearch,
  shownGroups,
} from "./accounts.helpers";
import { ConfigDialog } from "./configDialog";

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
  noMatch: 'No account matches "{search}".',
  clearSearch: "Clear search",
  select: "Select",
  selectOne: "Select {name}",
  selected: { one: "{count} selected", other: "{count} selected" },
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
/** Above this many accounts the list offers Find an account. */
const SEARCH_ABOVE = 6;

/** The button that leads to Add an account. */
function AddButton() {
  return (
    <a class="btn" data-part="button" data-variant="primary" href="#/add-account">
      <Icon name="plus" />
      {t("add")}
    </a>
  );
}

interface Report {
  skipped: number;
  errors: string[];
}

export function AccountsPage() {
  const { api, unauthorized } = useKit();
  const p = usePrivacy();
  const accounts = useLoad(api.accounts);
  const status = useLoad(api.status);
  const week = useLoad(() => api.usage("since=7d"));
  const list = accounts.data?.accounts;
  const naming = useNaming(list);
  const running = useMemo(() => runningExtensions(status.data), [status.data]);
  const [query, setQuery] = useState("");
  const [selecting, setSelecting] = useState(false);
  const [checked, setChecked] = useState<Set<string>>(new Set());
  const [busy, setBusy] = useState(false);
  const [asking, setAsking] = useState(false);
  const [report, setReport] = useState<Report>();

  const reload = () => {
    accounts.reload();
    status.reload();
    week.reload();
  };
  // Paused while Select is on and during a batch, so rows do not move under the person's hand.
  usePoll(reload, ACCOUNTS_POLL_MS, !selecting && !busy);
  // A ticked account that is gone is no longer selected.
  useEffect(() => {
    if (list) setChecked((old) => new Set([...old].filter((id) => list.some((a) => a.id === id))));
  }, [list]);

  const now = Date.now();
  const groups = useMemo(() => {
    const all = groupAccounts(list ?? [], running, now);
    for (const g of Object.keys(all) as Group[])
      all[g] = all[g].filter((x) => matchesSearch(x.account, x.c, query));
    return all;
    // now moves on every render; the groups follow the list, the providers and the search.
  }, [list, running, query]);
  const shown = shownGroups(groups);
  // The accounts in the order the page shows them: the first ones get their limits read.
  const ordered = useMemo(() => shown.flatMap((g) => groups[g].map((x) => x.account)), [groups]);
  const windows = useWindows(list ? ordered : undefined, running);
  const requests = new Map<string, number>();
  for (const g of week.data?.accounts ?? []) requests.set(g.name, g.total_requests);
  const selected = (list ?? []).filter((a) => checked.has(a.id));

  const settle = (r: Outcome): string | undefined => {
    if (typeof r === "string") return r || undefined;
    if (!r) return undefined;
    if (r.status === 401) {
      unauthorized();
      return undefined;
    }
    const why = problem(r);
    if (why === undefined) reload();
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
    reload();
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

  const one = selected.length === 1 ? selected[0] : undefined;
  return (
    <>
      <PageHead title={t("title")} actions={<AddButton />} />
      <Phases
        load={accounts}
        loading={t("loading")}
        failed={{ title: t("failed") }}
        isEmpty={(d) => d.accounts.length === 0}
        rows={6}
        empty={
          <StatePane kind="empty" title={t("empty")} primary={{ label: t("add"), href: "#/add-account" }}>
            <p>{t("emptyText")}</p>
          </StatePane>
        }
      >
        {(d) => (
          <div class="accounts-page" data-selecting={selecting ? "" : undefined}>
            <div class="accounts-top">
              <p class="accounts-counts muted">
                {/* A long line wraps between its counts, never inside one. */}
                {countsLine(d.accounts, running, now)
                  .split(" · ")
                  .map((part, i, all) => (
                    <span key={part}>
                      <span class="accounts-count">{i < all.length - 1 ? `${part} ·` : part}</span>
                      {i < all.length - 1 && " "}
                    </span>
                  ))}
              </p>
              <div class="accounts-tools">
                {(d.accounts.length > SEARCH_ABOVE || query !== "") && (
                  <label class="accounts-find">
                    <Icon name="search" />
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
                {d.accounts.length >= 2 && !selecting && (
                  <Button onClick={() => setSelecting(true)}>{t("select")}</Button>
                )}
              </div>
            </div>
            {selecting && (
              <div class="accounts-batch" data-part="batch" role="toolbar" aria-label={t("actions")}>
                <span class="accounts-batch-count" aria-live="polite">
                  {t("selected", { count: selected.length })}
                </span>
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
            )}
            {report && (report.skipped > 0 || report.errors.length > 0) && (
              <div class="accounts-report">
                {report.skipped > 0 && (
                  <Notice tone="muted">{t("skipped", { count: report.skipped })}</Notice>
                )}
                {report.errors.map((error) => (
                  <Notice key={error} tone="danger">
                    {error}
                  </Notice>
                ))}
              </div>
            )}
            {shown.length === 0 ? (
              <Empty onClear={() => setQuery("")} clearLabel={t("clearSearch")}>
                {t("noMatch", { search: query })}
              </Empty>
            ) : (
              shown.map((g) => (
                <section key={g} class="accounts-group" data-group={g} aria-label={groupWord(g)}>
                  <h2 class="accounts-group-head">
                    {groupWord(g)} <span class="muted">{groups[g].length}</span>
                  </h2>
                  <ul class="accounts-rows">
                    {groups[g].map(({ account: a }) => (
                      <li key={a.id} class="accounts-item">
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
                          requests={requests.get(a.id)}
                          running={running}
                          onTurnOn={turnOn}
                          onClearCooldown={clearCooldown}
                        />
                      </li>
                    ))}
                  </ul>
                </section>
              ))
            )}
          </div>
        )}
      </Phases>
      {asking && (
        <ConfigDialog
          title={one ? t("removeOne", { name: naming.of(one) }) : t("removeMany", { count: selected.length })}
          close={() => setAsking(false)}
        >
          <p>{t("removeText")}</p>
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

// The Accounts list; accountDetail.tsx owns one account's details.
import { useEffect, useMemo, useState } from "preact/hooks";
import { type Account, problem } from "../api";
import { one } from "../api/path";
import type { PageDef } from "../shell";
import { strings } from "../strings";
import { Tabs } from "../tabs";
import { Button, Empty, List, MasterDetail, Notice, PageHead, useKit, useLoad } from "../ui";
import { AccountDetail, AccountState } from "./accountDetail";
import { ConfigDialog } from "./configDialog";

const t = strings("accounts", {
  title: "Accounts",
  short: "Accts",
  tip: "Auth files & credentials",
  refresh: "Refresh",
  empty: "No accounts yet. Add them in core's config, or through a provider extension's login.",
  noResults: "No results",
  list: "Accounts",
  account: "Account",
  extension: "Extension",
  state: "State",
  label: "Label",
  credential: "Credential",
  from: "From",
  fromConfig: "AI providers",
  fromLogin: "Auth files",
  search: "Search credentials by name, account, type, provider, or error message",
  searchPlaceholder:
    "Search name, account, type, provider, or error (403, 429, invalid). Use * as a wildcard",
  providers: "Providers",
  tabCount: "{name} ({count})",
  all: "All",
  status: "Status Filter",
  enabled: "Enabled",
  disabled: "Disabled",
  problem: "Problem",
  source: "Source",
  sort: "Sort",
  default: "Default",
  name: "A-Z Name",
  priority: "Priority",
  perPage: "Per page",
  previous: "Previous",
  next: "Next",
  page: {
    one: "Page {current} / {total} · {count} account",
    other: "Page {current} / {total} · {count} accounts",
  },
  selected: { one: "{count} selected", other: "{count} selected" },
  select: "Select {id}",
  selectColumn: "Select",
  selectPage: "Select page",
  selectFiltered: "Select filtered",
  deselect: "Deselect",
  batch: "Batch actions",
  enable: "Enable",
  disable: "Disable",
  delete: "Delete",
  confirmDelete: {
    one: "Are you sure you want to delete {count} account?",
    other: "Are you sure you want to delete {count} accounts?",
  },
  cancel: "Cancel",
  clearCooldown: "Clear cooldown",
  skipped: {
    one: "Skipped {count} config account; change it in Config Panel.",
    other: "Skipped {count} config accounts; change them in Config Panel.",
  },
  completed: { one: "{count} account updated", other: "{count} accounts updated" },
  failure: "{id}: {error}",
  check: "Check",
  checking: "Checking",
  ok: "OK",
  failed: "Failed",
  notChecked: "Not checked",
  cooldown: "Cooldown",
  cooling: "Cooling {time}",
  seconds: "{count}s",
  minutes: "{count}m",
  hours: "{count}h",
  lastError: "Last error",
  requests: "Requests now",
  credentials: { one: "{count} credential", other: "{count} credentials" },
  active: { one: "{count} active", other: "{count} active" },
  problems: { one: "{count} problem", other: "{count} problem" },
  meta: "{credentials} · {active} · {problem}",
});

export interface AccountFilters {
  search?: string;
  extension?: string;
  status?: string;
  source?: string;
  sort?: string;
}

function coolingUntil(a: Account): number {
  return Math.max(
    Date.parse(a.cooldown_until ?? "") || 0,
    ...(a.cooldowns ?? []).map((c) => Date.parse(c.until) || 0),
  );
}

export function hasProblem(a: Account, now = Date.now()): boolean {
  return (
    a.state === "reauth_required" || a.check?.state === "failed" || !!a.last_error || coolingUntil(a) > now
  );
}

/** Case insensitive search: * matches any text; other characters are literal. */
export function filterAccounts(accounts: Account[], filters: AccountFilters, now = Date.now()): Account[] {
  const terms = filters.search?.trim().toLowerCase().split("*").filter(Boolean) ?? [];
  const matches = (text: string) => {
    const value = text.toLowerCase();
    let offset = 0;
    for (const term of terms) {
      const index = value.indexOf(term, offset);
      if (index < 0) return false;
      offset = index + term.length;
    }
    return true;
  };
  const result = accounts.filter((a) => {
    if (filters.extension && a.extension !== filters.extension) return false;
    if (filters.source && a.source !== filters.source) return false;
    if (filters.status === "enabled" && a.disabled) return false;
    if (filters.status === "disabled" && !a.disabled) return false;
    if (filters.status === "problem" && !hasProblem(a, now)) return false;
    return (
      terms.length === 0 ||
      [
        a.id,
        a.label,
        a.kind,
        a.extension,
        a.reason,
        a.last_error?.code,
        a.last_error?.message,
        a.check?.error?.code,
        a.check?.error?.message,
        a.renewal_error?.message,
      ].some((s) => s && matches(s))
    );
  });
  if (filters.sort === "name") result.sort((a, b) => a.id.localeCompare(b.id));
  if (filters.sort === "priority") result.sort((a, b) => (b.priority ?? 0) - (a.priority ?? 0));
  return result;
}

function coolingTime(until: number, now: number): string {
  const seconds = Math.ceil((until - now) / 1000);
  if (seconds < 60) return t("seconds", { count: seconds });
  if (seconds < 3600) return t("minutes", { count: Math.ceil(seconds / 60) });
  return t("hours", { count: Math.ceil(seconds / 3600) });
}

type BatchAction = "enable" | "disable" | "delete" | "cooldown";

export function AccountsPage() {
  const { api, host, unauthorized } = useKit();
  const accounts = useLoad(api.accounts);
  const [selected, setSelected] = useState<string>();
  const [filters, setFilters] = useState<AccountFilters>({});
  const [page, setPage] = useState(1);
  const [perPage, setPerPage] = useState(25);
  const [checked, setChecked] = useState<Set<string>>(new Set());
  const [busy, setBusy] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [report, setReport] = useState<{ success: number; skipped: number; errors: string[] }>();
  const [now, setNow] = useState(Date.now());
  const list = accounts.data?.accounts ?? [];
  const chosen = list.find((a) => a.id === selected);
  const filtered = useMemo(() => filterAccounts(list, filters, now), [list, filters, now]);
  const total = Math.max(1, Math.ceil(filtered.length / perPage));
  const current = Math.min(page, total);
  const pageItems = filtered.slice((current - 1) * perPage, current * perPage);
  const selectedAccounts = list.filter((a) => checked.has(a.id));
  const extensions = [...new Set(list.map((a) => a.extension).filter((e): e is string => !!e))].sort();

  useEffect(() => {
    const update = () => {
      if (!document.hidden) setNow(Date.now());
    };
    const timer = setInterval(update, 1000);
    document.addEventListener("visibilitychange", update);
    return () => {
      clearInterval(timer);
      document.removeEventListener("visibilitychange", update);
    };
  }, []);
  useEffect(() => {
    if (accounts.data)
      setChecked((old) => new Set([...old].filter((id) => accounts.data?.accounts.some((a) => a.id === id))));
  }, [accounts.data]);

  const change = (key: keyof AccountFilters, value: string) => {
    setFilters((f) => ({ ...f, [key]: value }));
    setPage(1);
  };
  const selectItems = (items: Account[]) => setChecked((old) => new Set([...old, ...items.map((a) => a.id)]));
  const runBatch = async (action: BatchAction) => {
    setConfirmDelete(false);
    setBusy(true);
    setReport(undefined);
    const targets = selectedAccounts.filter((a) => action === "cooldown" || a.source !== "config");
    const skipped = selectedAccounts.length - targets.length;
    const errors: string[] = [];
    const succeeded = new Set<string>();
    let cursor = 0;
    await Promise.all(
      Array.from({ length: Math.min(3, targets.length) }, async () => {
        while (cursor < targets.length) {
          const a = targets[cursor++];
          if (!a) break;
          try {
            const reply =
              action === "delete"
                ? await api.removeAccount(a.id)
                : action === "cooldown"
                  ? await host.call("POST", `/accounts/${one(a.id)}/cooldown/reset`)
                  : await api.setDisabled(a.id, action === "disable");
            if (reply.status === 401) unauthorized();
            const error = problem(reply);
            if (error) errors.push(t("failure", { id: a.id, error }));
            else succeeded.add(a.id);
          } catch (error) {
            errors.push(t("failure", { id: a.id, error: String(error) }));
          }
        }
      }),
    );
    if (action === "delete") setChecked((old) => new Set([...old].filter((id) => !succeeded.has(id))));
    setReport({ success: succeeded.size, skipped, errors });
    setBusy(false);
    accounts.reload();
  };
  const toolbarActions = [{ id: "refresh", label: t("refresh"), run: accounts.reload }];

  return (
    <>
      <PageHead title={t("title")} />
      <p class="muted accounts-meta" data-part="account-meta">
        {t("meta", {
          credentials: t("credentials", { count: list.length }),
          active: t("active", {
            count: list.filter((a) => !a.disabled && a.state === "active" && !hasProblem(a, now)).length,
          }),
          problem: t("problems", { count: list.filter((a) => hasProblem(a, now)).length }),
        })}
      </p>
      {accounts.error && <Notice tone="danger">{accounts.error}</Notice>}
      <Tabs
        label={t("providers")}
        active={filters.extension || "all"}
        onChange={(id) => change("extension", id === "all" ? "" : id)}
        tabs={[
          { id: "all", label: t("tabCount", { name: t("all"), count: list.length }) },
          ...extensions.map((id) => ({
            id,
            label: t("tabCount", { name: id, count: list.filter((a) => a.extension === id).length }),
          })),
        ]}
      >
        <div class="accounts-toolbar" data-part="toolbar">
          <label class="field accounts-search" data-part="field">
            <span class="label">{t("search")}</span>
            <input
              type="text"
              placeholder={t("searchPlaceholder")}
              value={filters.search ?? ""}
              onInput={(e) => change("search", e.currentTarget.value)}
            />
          </label>
          <label class="field" data-part="field">
            <span class="label">{t("status")}</span>
            <select value={filters.status ?? "all"} onChange={(e) => change("status", e.currentTarget.value)}>
              <option value="all">{t("all")}</option>
              <option value="enabled">{t("enabled")}</option>
              <option value="disabled">{t("disabled")}</option>
              <option value="problem">{t("problem")}</option>
            </select>
          </label>
          <label class="field" data-part="field">
            <span class="label">{t("source")}</span>
            <select value={filters.source ?? ""} onChange={(e) => change("source", e.currentTarget.value)}>
              <option value="">{t("all")}</option>
              <option value="login">{t("fromLogin")}</option>
              <option value="config">{t("fromConfig")}</option>
            </select>
          </label>
          <label class="field" data-part="field">
            <span class="label">{t("sort")}</span>
            <select value={filters.sort ?? "default"} onChange={(e) => change("sort", e.currentTarget.value)}>
              <option value="default">{t("default")}</option>
              <option value="name">{t("name")}</option>
              <option value="priority">{t("priority")}</option>
            </select>
          </label>
          {toolbarActions.map((action) => (
            <Button key={action.id} onClick={action.run} disabled={busy || accounts.loading}>
              {action.label}
            </Button>
          ))}
        </div>
        <fieldset class="accounts-batch" data-part="batch" aria-label={t("batch")}>
          <span>{t("selected", { count: selectedAccounts.length })}</span>
          <Button disabled={busy || !pageItems.length} onClick={() => selectItems(pageItems)}>
            {t("selectPage")}
          </Button>
          <Button disabled={busy || !filtered.length} onClick={() => selectItems(filtered)}>
            {t("selectFiltered")}
          </Button>
          <Button disabled={busy || !selectedAccounts.length} onClick={() => setChecked(new Set())}>
            {t("deselect")}
          </Button>
          {selectedAccounts.length > 0 && (
            <>
              <Button disabled={busy} onClick={() => runBatch("enable")}>
                {t("enable")}
              </Button>
              <Button disabled={busy} onClick={() => runBatch("disable")}>
                {t("disable")}
              </Button>
              <Button disabled={busy} onClick={() => runBatch("cooldown")}>
                {t("clearCooldown")}
              </Button>
              <Button variant="danger" disabled={busy} onClick={() => setConfirmDelete(true)}>
                {t("delete")}
              </Button>
            </>
          )}
        </fieldset>
        {report && (
          <>
            <Notice tone="ok">
              {t("completed", { count: report.success })}
              {report.skipped > 0 && <> · {t("skipped", { count: report.skipped })}</>}
            </Notice>
            {report.errors.map((error) => (
              <Notice key={error} tone="danger">
                {error}
              </Notice>
            ))}
          </>
        )}
        <MasterDetail
          list={
            accounts.data && list.length === 0 ? (
              <Empty>{t("empty")}</Empty>
            ) : filtered.length === 0 && !accounts.loading ? (
              <Empty>{t("noResults")}</Empty>
            ) : (
              <List<Account>
                label={t("list")}
                items={pageItems}
                id={(a) => a.id}
                selected={selected}
                onSelect={setSelected}
                columns={[
                  { label: t("account"), value: (a) => a.id, mono: true },
                  {
                    label: t("selectColumn"),
                    value: (a) => (
                      <input
                        type="checkbox"
                        data-part="select"
                        aria-label={t("select", { id: a.id })}
                        checked={checked.has(a.id)}
                        disabled={busy}
                        onClick={(e) => e.stopPropagation()}
                        onChange={(e) => {
                          const on = e.currentTarget.checked;
                          setChecked((old) => {
                            const next = new Set(old);
                            if (on) next.add(a.id);
                            else next.delete(a.id);
                            return next;
                          });
                        }}
                      />
                    ),
                  },
                  { label: t("extension"), value: (a) => a.extension ?? "", mono: true },
                  { label: t("state"), value: (a) => <AccountState a={a} /> },
                  { label: t("label"), value: (a) => a.label ?? "" },
                  { label: t("credential"), value: (a) => a.kind ?? "" },
                  {
                    label: t("from"),
                    value: (a) => (a.source === "config" ? t("fromConfig") : t("fromLogin")),
                  },
                  {
                    label: t("check"),
                    value: (a) => (
                      <span title={a.check?.error?.message ?? a.check?.detail}>
                        {a.check?.state === "ok"
                          ? t("ok")
                          : a.check?.state === "checking"
                            ? t("checking")
                            : a.check?.state === "failed"
                              ? t("failed")
                              : t("notChecked")}
                      </span>
                    ),
                  },
                  {
                    label: t("cooldown"),
                    value: (a) =>
                      coolingUntil(a) > now ? t("cooling", { time: coolingTime(coolingUntil(a), now) }) : "",
                  },
                  { label: t("lastError"), value: (a) => a.last_error?.message ?? "" },
                  { label: t("requests"), value: (a) => a.active_requests ?? 0 },
                ]}
              />
            )
          }
          detail={
            chosen && (
              <AccountDetail
                account={chosen}
                onClose={() => setSelected(undefined)}
                onChanged={accounts.reload}
              />
            )
          }
        />
        <div class="accounts-pager" data-part="pager">
          <label class="field" data-part="field">
            <span class="label">{t("perPage")}</span>
            <select
              value={perPage}
              onChange={(e) => {
                setPerPage(Number(e.currentTarget.value));
                setPage(1);
              }}
            >
              {[10, 25, 50, 100].map((n) => (
                <option key={n} value={n}>
                  {n}
                </option>
              ))}
            </select>
          </label>
          <Button disabled={current <= 1} onClick={() => setPage(current - 1)}>
            {t("previous")}
          </Button>
          <span>{t("page", { current, total, count: filtered.length })}</span>
          <Button disabled={current >= total} onClick={() => setPage(current + 1)}>
            {t("next")}
          </Button>
        </div>
      </Tabs>
      {confirmDelete && (
        <ConfigDialog title={t("delete")} close={() => setConfirmDelete(false)}>
          <p>{t("confirmDelete", { count: selectedAccounts.length })}</p>
          <div class="actions">
            <Button variant="danger" onClick={() => runBatch("delete")}>
              {t("delete")}
            </Button>
            <Button onClick={() => setConfirmDelete(false)}>{t("cancel")}</Button>
          </div>
        </ConfigDialog>
      )}
    </>
  );
}

export const accountsPage = (): PageDef => ({
  id: "accounts",
  group: "gateway",
  title: t("title"),
  short: t("short"),
  tip: t("tip"),
  render: () => <AccountsPage />,
});

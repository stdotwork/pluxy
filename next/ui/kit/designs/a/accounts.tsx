// Direction A's Accounts: every account as one dense row, and one account's detail beside it.
// The detail answers, in order: can it serve, if not why and what fixes it, how its usage windows
// are going, what it did lately; then today's settings and tools for the account, unchanged.

import { useEffect, useMemo, useRef, useState } from "preact/hooks";
import type { Account, UsageSummary } from "../../api";
import { AccountSettings } from "../../pages/accountSettings";
import { AccountTools } from "../../pages/accountTools";
import { accountIdentities, usePrivacy } from "../../privacy";
import { useKit } from "../../ui";
import { usePoll } from "./data";
import { FixButton, type Fleet, type Row, useFix, useFleet } from "./fleet";
import { Icon } from "./icons";
import { useKeys } from "./keys";
import {
  ago,
  type BoardState,
  clock,
  compact,
  countBoard,
  latency,
  matchScore,
  NEEDS_YOU,
  percent,
  shortProvider,
  stamp,
  wait,
} from "./lib";
import { WindowsCell } from "./overview";
import { Bars, Blank, Btn, ConfirmBtn, Fault, Kbd, Skeleton, StateTag } from "./parts";

type Show = "all" | "serving" | "waiting" | "need-you" | "off";
const SHOWS: { id: Show; label: string }[] = [
  { id: "all", label: "all" },
  { id: "need-you", label: "need you" },
  { id: "waiting", label: "waiting" },
  { id: "serving", label: "serving" },
  { id: "off", label: "off" },
];

function shows(state: BoardState, show: Show): boolean {
  if (show === "all") return true;
  if (show === "serving") return state === "serving";
  if (show === "off") return state === "off";
  if (show === "need-you") return NEEDS_YOU.includes(state);
  return state === "cooling" || state === "exhausted";
}

/** Rows that match what was typed (name, label, provider, kind, state, last error) and the state shown. */
export function filterRows(rows: Row[], typed: string, show: Show): Row[] {
  return rows.filter((r) => {
    if (!shows(r.standing.state, show)) return false;
    const a = r.account;
    return (
      matchScore(
        [
          a.id,
          a.label,
          a.extension,
          a.kind,
          r.standing.label,
          a.last_error?.code,
          a.last_error?.message,
          a.last_error?.status,
        ]
          .filter(Boolean)
          .join(" "),
        typed,
      ) > 0
    );
  });
}

export function Accounts(props: { rest: string }) {
  const fleet = useFleet();
  const fix = useFix(fleet.reload);
  const [typed, setTyped] = useState("");
  const [show, setShow] = useState<Show>("all");
  const filter = useRef<HTMLInputElement>(null);
  const table = useRef<HTMLDivElement>(null);
  const [selectedId, action] = props.rest.split("/").map(decodeURIComponent);
  const selected = fleet.rows.find((r) => r.account.id === selectedId);
  const rows = useMemo(() => filterRows(fleet.rows, typed, show), [fleet.rows, typed, show]);
  const counts = countBoard(fleet.rows.map((r) => r.standing));
  const open = (id?: string) => {
    location.hash = id ? `#/accounts/${encodeURIComponent(id)}` : "#/accounts";
  };
  const move = (by: number) => {
    if (rows.length === 0) return;
    const at = rows.findIndex((r) => r.account.id === selectedId);
    const next =
      rows[Math.max(0, Math.min(rows.length - 1, at < 0 ? (by > 0 ? 0 : rows.length - 1) : at + by))];
    if (next) open(next.account.id);
  };
  // The selected row stays in view as j and k move through the list.
  useEffect(() => {
    table.current?.querySelector('[aria-current="true"]')?.scrollIntoView({ block: "nearest" });
  }, [selectedId]);
  useKeys([
    { keys: "j", label: "Next account", run: () => move(1), keyOnly: true, group: "Accounts" },
    { keys: "k", label: "Previous account", run: () => move(-1), keyOnly: true, group: "Accounts" },
    { keys: "/", label: "Filter accounts", run: () => filter.current?.focus(), group: "Accounts" },
    {
      keys: "r",
      label: "Refresh accounts and windows",
      run: () => {
        fleet.reload();
        fleet.refreshWindows();
      },
      group: "Accounts",
    },
    { keys: "a", label: "Add account", run: () => (location.hash = "#/add-account"), group: "Accounts" },
    ...(selectedId
      ? [{ keys: "Escape", label: "Close the account", run: () => open(), keyOnly: true, group: "Accounts" }]
      : []),
  ]);
  const data = fleet.accounts.data;
  return (
    <div class="da-accounts" data-open={selectedId ? "" : undefined}>
      <section class="da-pane da-list-pane" aria-label="Accounts">
        <header class="da-pane-head da-toolbar">
          <label class="da-filter">
            <Icon name="search" size={14} />
            <input
              ref={filter}
              type="search"
              placeholder="Filter: name, provider, state, error"
              aria-label="Filter accounts"
              value={typed}
              onInput={(e) => setTyped(e.currentTarget.value)}
              onKeyDown={(e) => {
                if (e.key === "Escape") {
                  setTyped("");
                  e.currentTarget.blur();
                }
              }}
            />
            <Kbd keys="/" />
          </label>
          <fieldset class="da-seg" aria-label="Show">
            {SHOWS.map((s) => {
              const n =
                s.id === "all"
                  ? counts.total
                  : s.id === "need-you"
                    ? counts.needYou
                    : s.id === "waiting"
                      ? counts.waiting
                      : s.id === "serving"
                        ? counts.serving
                        : counts.off;
              return (
                <button
                  key={s.id}
                  type="button"
                  aria-pressed={show === s.id}
                  onClick={() => setShow(s.id)}
                  disabled={!data}
                >
                  {s.label}
                  {data && <span class="da-num da-seg-count">{n}</span>}
                </button>
              );
            })}
          </fieldset>
          <span class="da-grow" />
          <Btn
            small
            variant="ghost"
            href="#/classic/accounts"
            title="Today's Accounts page: select many, import auth files"
          >
            Bulk edit
          </Btn>
          <Btn small variant="primary" icon="plus" href="#/add-account" keys="a">
            Add account
          </Btn>
        </header>
        {fix.error && <Fault what="" error={fix.error} retry={fix.clearError} />}
        {fleet.accounts.error && (
          <Fault
            what="Accounts could not be read."
            error={fleet.accounts.error}
            retry={fleet.reload}
            stale={!!data}
          />
        )}
        {fleet.accounts.loading ? (
          <Skeleton rows={12} label="Reading accounts" />
        ) : data && fleet.rows.length === 0 ? (
          <Blank
            title="No accounts yet"
            action={
              <Btn variant="primary" icon="plus" href="#/add-account">
                Add account
              </Btn>
            }
          >
            Add a subscription sign-in or an API key. Each account gets a row here with its state, usage
            windows and last error.
          </Blank>
        ) : data && rows.length === 0 ? (
          <Blank
            title="No account matches"
            action={
              <Btn
                onClick={() => {
                  setTyped("");
                  setShow("all");
                }}
              >
                Clear the filter
              </Btn>
            }
          >
            {typed ? `Nothing matches “${typed}”` : "No account is in this state"}
            {show !== "all" && typed ? ` among the ${SHOWS.find((s) => s.id === show)?.label} accounts` : ""}.
          </Blank>
        ) : (
          <div class="da-table-wrap" ref={table}>
            <table class="da-table da-accounts-table" aria-label="Accounts">
              <thead>
                <tr>
                  <th scope="col">state</th>
                  <th scope="col">account</th>
                  <th scope="col" class="da-col-provider">
                    provider
                  </th>
                  <th scope="col" class="da-col-windows">
                    windows <span class="da-quiet">used · resets in</span>
                  </th>
                  <th
                    scope="col"
                    class="da-right da-col-order"
                    title="Priority (higher is tried first) and weight"
                  >
                    order
                  </th>
                  <th scope="col" class="da-right da-col-now" title="Requests in flight">
                    now
                  </th>
                  <th scope="col" class="da-col-error">
                    last error
                  </th>
                  <th scope="col" class="da-col-fix">
                    <span class="da-sr">Fix</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {rows.map((row) => (
                  <AccountRow
                    key={row.account.id}
                    row={row}
                    fleet={fleet}
                    fix={fix}
                    selected={row.account.id === selectedId}
                  />
                ))}
              </tbody>
            </table>
            <p class="da-table-foot da-quiet da-num">
              {rows.length === fleet.rows.length
                ? `${rows.length} accounts`
                : `${rows.length} of ${fleet.rows.length} accounts`}
              {fleet.accounts.at ? ` · read ${clock(fleet.accounts.at)}` : ""}
            </p>
          </div>
        )}
      </section>
      {selectedId &&
        (selected ? (
          <AccountPane
            key={selected.account.id}
            row={selected}
            fleet={fleet}
            fix={fix}
            signIn={action === "sign-in"}
            onClose={() => open()}
          />
        ) : (
          <aside class="da-pane da-detail" aria-label="Account">
            <header class="da-pane-head">
              <h2 class="da-clip">{selectedId}</h2>
              <span class="da-grow" />
              <Btn small variant="ghost" icon="close" label="Close" keys="Escape" onClick={() => open()} />
            </header>
            {fleet.accounts.loading ? (
              <Skeleton rows={8} label="Reading the account" />
            ) : (
              <Blank title="No such account" action={<Btn href="#/accounts">All accounts</Btn>}>
                Core has no account with this name. It may have been removed or renamed.
              </Blank>
            )}
          </aside>
        ))}
    </div>
  );
}

function AccountRow(props: { row: Row; fleet: Fleet; fix: ReturnType<typeof useFix>; selected: boolean }) {
  const { row, fleet } = props;
  const p = usePrivacy();
  const a = row.account;
  const s = row.standing;
  const ids = [a.label, a.id];
  const err = a.last_error;
  const why = s.state !== "serving" && s.state !== "off" ? s.why : undefined;
  return (
    <tr
      data-tone={s.tone}
      aria-current={props.selected ? "true" : undefined}
      onClick={(e) => {
        // A click anywhere on the row opens it, as its name does; buttons and links keep their own.
        if ((e.target as HTMLElement).closest("a,button")) return;
        location.hash = `#/accounts/${encodeURIComponent(a.id)}`;
      }}
    >
      <td>
        <StateTag standing={s} now={fleet.now} />
      </td>
      <th scope="row">
        <a href={`#/accounts/${encodeURIComponent(a.id)}`} class="da-rowlink da-clip" title={row.name}>
          {row.name}
        </a>
        {row.label && <span class="da-sub da-clip">{row.label}</span>}
      </th>
      <td class="da-col-provider">
        <span class="da-clip" title={a.extension}>
          {shortProvider(a.extension)}
        </span>
        <span class="da-sub">{a.kind ?? (a.source === "config" ? "config" : "")}</span>
      </td>
      <td class="da-col-windows">
        <WindowsCell row={row} fleet={fleet} />
      </td>
      <td class="da-num da-right da-col-order">
        {a.priority ?? 0}
        {a.weight !== undefined && a.weight !== 1 && <span class="da-quiet"> ×{a.weight}</span>}
      </td>
      <td class="da-num da-right da-col-now" data-zero={a.active_requests ? undefined : ""}>
        {a.active_requests ?? 0}
      </td>
      <td class="da-col-error">
        {why ? (
          <span class="da-clip" title={p.text(why, ids)}>
            {p.text(why, ids)}
          </span>
        ) : err ? (
          <span class="da-clip da-quiet" title={p.text(err.message, ids)}>
            <span class="da-num">{err.status ?? err.code}</span> {p.text(err.message, ids)}
          </span>
        ) : (
          <span class="da-quiet">–</span>
        )}
      </td>
      <td class="da-col-fix">
        <FixButton row={row} fix={props.fix} small />
      </td>
    </tr>
  );
}

function Fact(props: { k: string; children: preact.ComponentChildren; title?: string }) {
  return (
    <div class="da-fact" title={props.title}>
      <dt>{props.k}</dt>
      <dd>{props.children}</dd>
    </div>
  );
}

/** What the account did in the last 24 hours, from the usage record. */
function Activity(props: { account: Account; fleet: Fleet }) {
  const { api } = useKit();
  const id = props.account.public_id;
  const usage = usePoll<UsageSummary>(
    () => api.usage(`since=24h&bucket=20m&buckets=72&account=${encodeURIComponent(id ?? "")}`),
    30_000,
    id,
  );
  const u = usage.data;
  if (!id) return <p class="da-quiet">Core has no usage record for an account without a public ID.</p>;
  if (usage.loading) return <Skeleton rows={3} label="Reading the account's usage" />;
  if (!u) return <Fault what="Its usage could not be read." error={usage.error ?? ""} retry={usage.reload} />;
  if (u.enabled === false) return <p class="da-quiet">Usage recording is off.</p>;
  if (u.total_requests === 0)
    return <p class="da-quiet">No requests went to this account in the last 24 hours.</p>;
  return (
    <>
      <dl class="da-facts da-facts-row">
        <Fact k="requests">
          <span class="da-num">{compact(u.total_requests)}</span>
        </Fact>
        <Fact k="success">
          <span class="da-num" data-tone={u.failure_count ? "warn" : undefined}>
            {percent(u.success_rate)}
          </span>
          {u.failure_count > 0 && <span class="da-quiet da-num"> {compact(u.failure_count)} failed</span>}
        </Fact>
        <Fact k="tokens">
          <span class="da-num">{u.tokens ? compact(u.tokens.total_tokens) : "–"}</span>
        </Fact>
        <Fact k="latency">
          <span class="da-num">
            {u.average_latency_ms !== undefined ? latency(u.average_latency_ms) : "–"}
          </span>
        </Fact>
      </dl>
      <Bars
        series={u.series}
        bucketSeconds={u.bucket_seconds}
        now={props.fleet.now}
        height={40}
        label={`This account's requests over the last 24 hours: ${u.total_requests}, ${u.failure_count} failed`}
        summary="requests per 20 min"
      />
      <p class="da-more">
        <a href={`#/usage/list?account=${encodeURIComponent(id)}`}>Its requests</a>
      </p>
    </>
  );
}

function AccountPane(props: {
  row: Row;
  fleet: Fleet;
  fix: ReturnType<typeof useFix>;
  signIn: boolean;
  onClose: () => void;
}) {
  const { row, fleet } = props;
  const { api, unauthorized } = useKit();
  const p = usePrivacy();
  const a = row.account;
  const s = row.standing;
  const ids = accountIdentities(a);
  const tools = useRef<HTMLDivElement>(null);
  const pane = useRef<HTMLElement>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const w = fleet.windows.get(a.id);
  const cooldowns = (a.cooldowns ?? []).filter((c) => Date.parse(c.until) > fleet.now);
  // Signing in again is done with today's tool for it, further down: this brings it up.
  const signIn = () => {
    const button = [...(tools.current?.querySelectorAll("button") ?? [])].find(
      (b) => b.textContent?.trim() === "Sign in again",
    );
    button?.scrollIntoView({ block: "center" });
    button?.focus();
    button?.click();
  };
  useEffect(() => {
    pane.current?.focus();
    if (props.signIn) signIn();
  }, []);
  const act = async (call: () => Promise<{ status: number }>) => {
    setBusy(true);
    setError(undefined);
    const r = await call();
    setBusy(false);
    if (r.status === 401) return unauthorized();
    if (r.status < 200 || r.status >= 300)
      setError((r as { error?: { message: string } }).error?.message ?? `core answered ${r.status}`);
    fleet.reload();
  };
  const canChange = a.source !== "config" && s.state !== "broken";
  return (
    <aside class="da-pane da-detail" aria-label={`Account ${row.name}`} tabIndex={-1} ref={pane}>
      <header class="da-pane-head da-detail-head">
        <a class="da-back" href="#/accounts" aria-label="All accounts">
          <Icon name="chevronRight" size={14} />
        </a>
        <div class="da-detail-title">
          <h2 class="da-clip" title={row.name}>
            {row.name}
          </h2>
          <span class="da-sub da-clip">
            {[row.label, a.extension, a.kind, a.source === "config" ? "set in the config" : undefined]
              .filter(Boolean)
              .join(" · ")}
          </span>
        </div>
        <Btn small variant="ghost" icon="close" label="Close" keys="Escape" onClick={props.onClose} />
      </header>

      <div class="da-detail-body">
        <section class="da-verdict" data-tone={s.tone} aria-label="State">
          <div class="da-verdict-line">
            <StateTag standing={s} now={fleet.now} />
            <span class="da-grow" />
            <FixButton row={row} fix={props.fix} primary onSignIn={signIn} />
          </div>
          <p class="da-verdict-why">{sentence(row, fleet.now, p.text(s.why, ids))}</p>
          {(error ?? props.fix.error) && (
            <Fault what="" error={p.text(error ?? props.fix.error ?? "", ids)} />
          )}
          {cooldowns.length > 0 && (
            <ul class="da-cooldowns">
              {cooldowns.map((c) => (
                <li key={`${c.scope}:${c.model ?? ""}:${c.kind}`}>
                  <span class="da-num" data-tone="warn">
                    {wait(Date.parse(c.until) - fleet.now)}
                  </span>
                  <span>
                    {c.scope === "model"
                      ? (c.model ?? "one model")
                      : c.scope === "endpoint"
                        ? "one endpoint"
                        : "whole account"}
                  </span>
                  <span class="da-quiet da-clip">
                    {c.kind.replace(/_/g, " ")}
                    {c.backoff_level ? `, backoff ${c.backoff_level}` : ""}
                    {c.error ? `: ${p.text(c.error.message, ids)}` : ""}
                  </span>
                </li>
              ))}
            </ul>
          )}
        </section>

        <section aria-label="Usage windows">
          <h3>
            windows
            {w?.reply?.quota.plan && <span class="da-quiet"> · {w.reply.quota.plan}</span>}
            <span class="da-grow" />
            {w && <span class="da-quiet da-num">read {ago(fleet.now - w.at)} ago</span>}
            <Btn
              small
              variant="ghost"
              icon="refresh"
              label="Read the windows again"
              onClick={() => fleet.refreshWindows([a.id])}
            />
          </h3>
          <WindowsCell row={row} fleet={fleet} wide />
          {w?.reply?.quota.limits
            .filter((l) => l.remaining !== undefined && l.used_percent === undefined)
            .map((l) => (
              <p key={l.id} class="da-quiet">
                {l.label ?? l.id}: <span class="da-num">{l.remaining}</span> {l.unit ?? ""} left
              </p>
            ))}
        </section>

        <section aria-label="Last 24 hours">
          <h3>last 24h</h3>
          <Activity account={a} fleet={fleet} />
        </section>

        <section aria-label="Routing">
          <h3>routing</h3>
          <dl class="da-facts">
            <Fact k="priority" title="Higher is tried first">
              <span class="da-num">{a.priority ?? 0}</span>
            </Fact>
            <Fact k="weight">
              <span class="da-num">{a.weight ?? 1}</span>
            </Fact>
            <Fact k="in flight">
              <span class="da-num">
                {a.active_requests ?? 0}
                {a.max_concurrency ? ` of ${a.max_concurrency}` : ""}
              </span>
            </Fact>
            <Fact k="public id">
              <span class="da-num">{a.public_id ?? "–"}</span>
            </Fact>
            {a.last_error && (
              <Fact k="last error">
                <span class="da-num">{a.last_error.status ?? a.last_error.code}</span>{" "}
                {p.text(a.last_error.message, ids)}
                {a.last_error_at && (
                  <span class="da-quiet da-num"> · {stamp(a.last_error_at, fleet.now)}</span>
                )}
              </Fact>
            )}
            {a.check && (
              <Fact k="check">
                {a.check.state.replace(/_/g, " ")}
                {a.check.detail ? `: ${p.text(a.check.detail, ids)}` : ""}
                {a.check.at && <span class="da-quiet da-num"> · {stamp(a.check.at, fleet.now)}</span>}
              </Fact>
            )}
          </dl>
          {canChange && (
            <div class="da-actions">
              {s.state !== "off" && s.state !== "banned" && (
                <Btn small icon="off" disabled={busy} onClick={() => act(() => api.setDisabled(a.id, true))}>
                  Disable
                </Btn>
              )}
              <ConfirmBtn
                small
                variant="danger"
                icon="trash"
                disabled={busy}
                ask="Remove for good?"
                onConfirm={() => act(() => api.removeAccount(a.id)).then(props.onClose)}
              >
                Remove
              </ConfirmBtn>
            </div>
          )}
        </section>

        {s.state !== "broken" && (
          <section aria-label="Settings and tools">
            <h3>settings and tools</h3>
            <div class="da-legacy da-legacy-inline" ref={tools}>
              <AccountSettings account={a} />
              <AccountTools key={a.id} account={a} onChanged={fleet.reload} />
            </div>
          </section>
        )}
      </div>
    </aside>
  );
}

/** The state as a sentence: what is so, until when, and what to do. */
function sentence(row: Row, now: number, why?: string): string {
  const s = row.standing;
  const until =
    s.until && s.until > now ? ` It comes back at ${stamp(s.until, now)}, in ${wait(s.until - now)}.` : "";
  const because = why ? ` ${why.replace(/\.?$/, ".")}` : "";
  switch (s.state) {
    case "serving":
      return s.coolingModels
        ? "Serving. Some models are held back for a while; the rest go through."
        : "Serving. Requests can go to this account now.";
    case "cooling":
      return `Held back after an error, so requests go to the next account.${because}${until} Clearing the cooldown sends it requests again at once.`;
    case "exhausted":
      return `Its usage window is used up, so requests go to the next account.${until || " It comes back when the window resets."}`;
    case "sign-in":
      return `The provider no longer accepts its sign-in, so it gets no requests.${because} Sign in again to bring it back.`;
    case "banned":
      return `The provider closed or refused this account, so it gets no requests.${because} Enable it to have it tried again.`;
    case "broken":
      return `pluxy cannot read this account's credential file, so it gets no requests.${because} Fix the file and restart pluxy, or remove the account.`;
    case "no-provider":
      return `${because.trim()} No request can reach the account until the extension runs.`;
    case "off":
      return "Turned off. It gets no requests until it is enabled.";
  }
}

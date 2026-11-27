// Home: whether the person's coding tools can be served. The verdict first, naming the problem;
// the accounts that need the person in a band with their fixes; one pool per provider; every
// other account by provider; and beside them how to point a tool at pluxy, the last seven days and
// the latest requests. On a new install it shows the way to add the first account instead. Its
// page ID stays `status`: that is its address and the theme hook `data-page`.

import type { ComponentChildren } from "preact";
import { useCallback, useEffect, useRef, useState } from "preact/hooks";
import { type Account, type ClientKey, problem, runningExtensions, type Status } from "../api";
import type { Setup } from "../api/setup";
import type { UsageSummary } from "../api/usage";
import type { UsageEvent } from "../api/usageEvents";
import type { Reply } from "../host";
import { Icon } from "../icons";
import type { Outcome } from "../parts";
import {
  AccountRow,
  accountCondition,
  accountFix,
  byProvider,
  ConnectionCard,
  conditionUnchecked,
  groupWord,
  Placeholder,
  PoolCard,
  ProviderMark,
  poolOf,
  providerName,
  RequestRow,
  StaleNotice,
  StatePane,
  tryAgainWord,
  useNaming,
  useShownAfter,
  useWindows,
} from "../parts";
import { expireWindows } from "../parts/hooks";
import { accountIdentities, usePrivacy } from "../privacy";
import type { PageDef } from "../shell";
import { strings } from "../strings";
import { relativeTime } from "../time";
import { BUSY_SHOWS_AFTER_MS, Button, Notice, PageHead, useKit, usePoll } from "../ui";
import {
  countsLine,
  groupAccounts,
  hotspotOf,
  localDays,
  noAccountsTitle,
  providerGroups,
  type Read,
  staleOf,
  successPercent,
  toolLine,
  usageQuery,
  verdictOf,
  weekTotals,
} from "./home.helpers";
import { Days } from "./homeDays";
import { MAIN_KINDS } from "./setup.helpers";

const t = strings("home", {
  title: "Home",
  tip: "Whether your tools can be served",
  refresh: "Refresh",
  updated: "Updated {when}",
  checking: "Checking pluxy…",
  unreachable: "Cannot reach pluxy.",
  unreachableHelp: "Check that pluxy is running, then try again.",
  noList: "pluxy could not list its accounts.",
  noAccountsHelp:
    "Add the account you use for Claude, ChatGPT, Gemini or another service, and your coding tools can use it through pluxy.",
  addAccount: "Add an account",
  guidedSetup: "Guided setup",
  nextTool: "Next: connect a coding tool.",
  found: "Found: {tools}.",
  more: "And {count} more",
  moreKinds: "More",
  seeAll: "See all",
  accounts: "Accounts",
  allAccounts: "All accounts",
  pools: "Pools",
  groupCount: { one: "1 account · {serving} serving", other: "{count} accounts · {serving} serving" },
  groupCountUnchecked: {
    one: "1 account · {unchecked} not checked",
    other: "{count} accounts · {unchecked} not checked",
  },
  lastWeek: "Last 7 days",
  usage: "Usage",
  requestsKpi: { one: "request", other: "requests" },
  wentThrough: "went through",
  tokens: "tokens",
  seeThem: "See them",
  latest: "Latest requests",
  allRequests: "All requests",
  noRequests: "No requests in the last 7 days.",
  noneToList: "No recent requests to list.",
  connectIt: "Connect it",
  connectTool: "Connect a tool",
  recordOff: "pluxy is not keeping a record of requests, so there is nothing to show here.",
  recordOn: "Turn it on in Config",
  refused: "pluxy refused the last change to its settings: {error}. The settings from {time} still serve.",
  noReason: "no reason given",
  openConfig: "Open Config",
  weekFailed: "Could not read the last 7 days.",
  latestFailed: "Could not read the latest requests.",
  states: "Accounts by condition",
});

/** How often the page reads again while it is shown. */
export const HOME_POLL_MS = 60_000;
/** The most accounts Home lists under one provider; the rest are on Accounts. */
export const PROVIDER_SHOWN = 6;
/** The latest requests Home lists. */
export const LATEST_SHOWN = 10;

interface Round {
  accounts: Read<Account[]>;
  status: Read<Status>;
  week: Read<UsageSummary>;
  latest: Read<UsageEvent[]>;
}

const LOADING: Round = {
  accounts: { phase: "loading" },
  status: { phase: "loading" },
  week: { phase: "loading" },
  latest: { phase: "loading" },
};

/** What a read comes to: the data when it worked, else the failure over the data read before. */
function settle<T, B>(prev: Read<T>, r: Reply<B>, pick: (body?: B) => T, at: number): Read<T> {
  const error = problem(r);
  if (error === undefined) return { phase: "ready", data: pick(r.body), at };
  return { phase: "failed", data: prev.data, error, status: r.status, at: prev.at };
}

const unreachable = (): Reply<never> => ({ status: 0, error: { code: "unreachable", message: "" } });

/**
 * The page's reads: accounts, status, the seven days and the latest requests, taken together in
 * rounds, one round at a time, every minute while the page is shown. run reads one round now (or
 * joins the one under way); again reads one more after it, for a fix that changed something.
 */
function useRound() {
  const { api, unauthorized } = useKit();
  const [round, setRound] = useState<Round>(LOADING);
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
      const [a, s, w, l] = await Promise.all([
        api.accounts().catch(unreachable),
        api.status().catch(unreachable),
        api.usage(usageQuery(new Date())).catch(unreachable),
        api.usageEventsBefore({}, 0, LATEST_SHOWN).catch(unreachable),
      ]);
      if (!live.current) return;
      if ([a, s, w, l].some((r) => r.status === 401)) return unauthorized();
      const at = Date.now();
      setRound((prev) => ({
        accounts: settle(prev.accounts, a, (b) => b?.accounts ?? [], at),
        status: settle(prev.status, s, (b) => b as Status, at),
        week: settle(prev.week, w, (b) => b as UsageSummary, at),
        latest: settle(prev.latest, l, (b) => b?.events ?? [], at),
      }));
    })().finally(() => {
      inflight.current = undefined;
    });
    inflight.current = p;
    return p;
  }, [api, unauthorized]);
  const again = useCallback(async () => {
    await inflight.current;
    await run();
  }, [run]);
  useEffect(() => {
    void run();
  }, [run]);
  usePoll(run, HOME_POLL_MS);
  return { round, run, again };
}

/**
 * What the connection card and the line about coding tools need, read once when the page opens
 * and never polled: the coding tools core found (GET /setup) and the client keys. A read that
 * fails leaves its part out.
 */
function useOnce() {
  const { api } = useKit();
  const [setup, setSetup] = useState<Setup>();
  const [keys, setKeys] = useState<ClientKey[]>();
  useEffect(() => {
    let live = true;
    api.setup().then((r) => {
      if (live && problem(r) === undefined) setSetup(r.body);
    });
    api.clientKeys().then((r) => {
      if (live && problem(r) === undefined) setKeys(r.body?.keys);
    });
    return () => {
      live = false;
    };
  }, [api]);
  return { setup, keys };
}

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
    <span class="home-fresh" title={new Date(props.at).toLocaleString()}>
      {t("updated", { when: relativeTime(new Date(props.at), Math.max(now, props.at)) })}
    </span>
  );
}

/** A side section that could not be read: one line, and the way to read again. */
function SideFailed(props: { text: string; error?: string; onRetry: () => unknown }) {
  const p = usePrivacy();
  return (
    <p class="home-side-failed" role="alert" title={p.text(props.error)}>
      <span>{props.text}</span> <Button onClick={props.onRetry}>{tryAgainWord()}</Button>
    </p>
  );
}

/** A card of the side column: its heading, a link to the page with more, and what it holds. */
function Section(props: {
  title: string;
  link?: { label: string; href: string };
  children: ComponentChildren;
}) {
  return (
    <section class="home-section card" aria-label={props.title}>
      <header class="home-section-head">
        <h2>{props.title}</h2>
        {props.link && (
          <a href={props.link.href}>
            {props.link.label}
            <Icon name="chevron" size={12} />
          </a>
        )}
      </header>
      {props.children}
    </section>
  );
}

function Waiting() {
  return (
    <div role="status" aria-busy="true">
      <Placeholder rows={3} />
    </div>
  );
}

/** The page in its final shape with nothing in it yet, shown a moment after a load starts. */
function Skeleton() {
  const shown = useShownAfter(BUSY_SHOWS_AFTER_MS);
  if (!shown) return null;
  return (
    <div class="home-skeleton" aria-hidden="true">
      <div class="home-skeleton-main">
        <div class="home-pools">
          {[0, 1, 2].map((i) => (
            <i key={i} class="skeleton-card" />
          ))}
        </div>
        <i class="skeleton-list" />
      </div>
      <div class="home-skeleton-side">
        <i class="skeleton-card" />
        <i class="skeleton-card" />
      </div>
    </div>
  );
}

/** The thin bar beside the verdict: how many serve, wait, need the person, are unchecked and are turned off. */
function StateBar(props: {
  counts: { needs: number; waiting: number; serving: number; unchecked?: number; off: number };
}) {
  const c = props.counts;
  const parts = (["serving", "waiting", "needs", "unchecked", "off"] as const).filter((g) => (c[g] ?? 0) > 0);
  return (
    <div class="home-state-bar" aria-hidden="true">
      {parts.map((g) => (
        <span key={g} data-group={g} style={{ flexGrow: c[g] ?? 0 }} />
      ))}
    </div>
  );
}

/** Day one: the four kinds of account and More, each leading to Add an account. */
function KindTiles() {
  return (
    <ul class="home-kinds">
      {MAIN_KINDS.map((k) => (
        <li key={k.id}>
          <a class="home-kind" href="#/add-account">
            <ProviderMark extension={k.id} />
            <span>{k.title}</span>
          </a>
        </li>
      ))}
      <li>
        <a class="home-kind" data-more="" href="#/add-account">
          <span class="home-kind-more" aria-hidden="true">
            <Icon name="plus" />
          </span>
          <span>{t("moreKinds")}</span>
        </a>
      </li>
    </ul>
  );
}

/** A compact number for a figure tile: 8,412; 21.4M. */
const compact = (n: number) =>
  n >= 100_000
    ? n.toLocaleString(undefined, { notation: "compact", maximumFractionDigits: 1 })
    : n.toLocaleString();

export function HomePage() {
  const { api, host } = useKit();
  const p = usePrivacy();
  const { round, run, again } = useRound();
  const { setup, keys } = useOnce();
  const [refreshed, setRefreshed] = useState(0);
  const { accounts, status, week, latest } = round;
  const list = accounts.data;
  const running = runningExtensions(status.data);
  const statusFailed = status.phase === "failed" && status.data === undefined;
  const naming = useNaming(list);
  const now = new Date();
  const grouped = list && groupAccounts(list, running, now.getTime(), statusFailed);
  // The limits are read for the accounts that serve or wait, the waiting ones first.
  const windows = useWindows(
    grouped && [...grouped.groups.waiting, ...grouped.groups.serving, ...grouped.unchecked],
    running,
    refreshed,
  );

  const refresh = async () => {
    expireWindows();
    setRefreshed((n) => n + 1);
    await run();
  };
  const fixed = async (reply: Promise<Reply<unknown>>): Promise<Outcome> => {
    const r = await reply;
    if (problem(r) === undefined) await again();
    return r;
  };
  const turnOn = (id: string) => fixed(api.setDisabled(id, false));
  const clearCooldown = (id: string) => fixed(api.resetCooldown(id));

  const head = (
    <PageHead
      title={t("title")}
      actions={
        <>
          <Freshness at={accounts.at} />
          <Button onClick={refresh}>
            <Icon name="refresh" size={14} />
            {t("refresh")}
          </Button>
        </>
      }
    />
  );

  if (accounts.phase === "loading") {
    return (
      <>
        {head}
        <StatePane kind="loading" title={t("checking")} rows={0} />
        <Skeleton />
      </>
    );
  }
  if (accounts.phase === "failed" && (list === undefined || list.length === 0)) {
    const nothing = accounts.status === 0;
    return (
      <>
        {head}
        <StatePane
          kind="failed"
          title={nothing ? t("unreachable") : t("noList")}
          details={accounts.error}
          primary={{ label: tryAgainWord(), onClick: run }}
        >
          {nothing && t("unreachableHelp")}
        </StatePane>
      </>
    );
  }

  const record = week.data;
  const tools = setup?.found.clients.filter((c) => c.installed && c.state !== "unchecked") ?? [];
  const connection = <ConnectionCard address={host.address} keys={keys} clients={setup?.found.clients} />;
  if (!list || list.length === 0 || !grouped) {
    return (
      <>
        {head}
        <div class="home-body" data-empty="">
          <div class="home-cols">
            <div class="home-main">
              <StatePane
                kind="empty"
                title={noAccountsTitle()}
                primary={{ label: t("addAccount"), href: "#/add-account" }}
                secondary={{ label: t("guidedSetup"), href: "#/setup" }}
              >
                {t("noAccountsHelp")}
              </StatePane>
              <KindTiles />
              <p class="home-next">
                {tools.length > 0
                  ? t("found", { tools: tools.map((c) => c.name).join(", ") })
                  : t("nextTool")}
              </p>
            </div>
            <aside class="home-side">{connection}</aside>
          </div>
        </div>
      </>
    );
  }

  const days = record?.enabled ? localDays(record.series, now) : undefined;
  const totals = days && weekTotals(days);
  const verdict = verdictOf(status.data, grouped.counts, grouped.firstBack, now.getTime(), totals);
  const hotspot = record?.enabled && totals ? hotspotOf(record.models, totals.failed) : undefined;
  const stale = staleOf([accounts, status, week, latest]);
  const config = status.data;
  const refusedConfig =
    config && config.desired.revision !== config.active.revision ? config.desired : undefined;
  const ids = list.flatMap(accountIdentities);
  const events = latest.data ?? [];
  const quiet = record?.enabled === true && record.total_requests === 0;
  const line = quiet && setup ? toolLine(setup.found.clients) : undefined;
  const pools = byProvider(list)
    .map((g) => poolOf(g.extension, g.accounts, windows, running, now.getTime(), statusFailed))
    .filter((pool) => pool.total > 0);
  const listed = providerGroups(grouped, windows, running, now.getTime());
  const isUnchecked = (a: Account) =>
    statusFailed && conditionUnchecked(a, accountCondition(a, running, now.getTime()), running);

  // The view's one filled button: the first fix in the band that repairs something.
  const firstRepair = grouped.groups.needs.findIndex(
    (a) => accountFix(a, accountCondition(a, running, now.getTime()))?.variant === "primary",
  );
  const band =
    grouped.groups.needs.length > 0 ? (
      <section class="home-band" data-part="needs-band" aria-label={groupWord("needs")}>
        {grouped.groups.needs.map((a, i) => (
          <AccountRow
            key={a.id}
            account={a}
            running={running}
            quietFix={i !== firstRepair}
            onTurnOn={turnOn}
            onClearCooldown={clearCooldown}
          />
        ))}
      </section>
    ) : null;

  const accountsCard =
    listed.length > 0 ? (
      <section class="home-accounts card" aria-labelledby="home-accounts-title">
        <header class="home-section-head">
          <h2 id="home-accounts-title">
            {t("accounts")}{" "}
            <span class="home-group-count num">{listed.reduce((n, g) => n + g.accounts.length, 0)}</span>
          </h2>
          <a href="#/accounts">
            {t("allAccounts")}
            <Icon name="chevron" size={12} />
          </a>
        </header>
        {listed.map((g) => {
          const shown = g.accounts.slice(0, PROVIDER_SHOWN);
          const name = providerName(g.extension || undefined);
          return (
            <section class="home-group" key={g.extension} aria-label={name}>
              <h3 class="home-group-head">
                <ProviderMark extension={g.extension || undefined} size="small" />
                <span class="home-group-name">{name}</span>
                <span class="home-group-count">
                  {g.unchecked > 0 && g.serving === 0
                    ? t("groupCountUnchecked", { count: g.accounts.length, unchecked: g.unchecked })
                    : t("groupCount", { count: g.accounts.length, serving: g.serving })}
                </span>
              </h3>
              {shown.map((a) => (
                <AccountRow
                  key={a.id}
                  account={a}
                  windows={windows.get(a.id)}
                  running={running}
                  unchecked={isUnchecked(a)}
                  grouped
                  onTurnOn={turnOn}
                  onClearCooldown={clearCooldown}
                />
              ))}
              {shown.length < g.accounts.length && (
                <p class="home-more">
                  {t("more", { count: g.accounts.length - shown.length })}{" "}
                  <a href="#/accounts">{t("seeAll")}</a>
                </p>
              )}
            </section>
          );
        })}
      </section>
    ) : null;

  let side: ComponentChildren;
  if (record?.enabled === false) {
    side = (
      <p class="home-side-note card">
        {t("recordOff")} <a href="#/config/logging">{t("recordOn")}</a>
      </p>
    );
  } else if (record && totals?.total === 0) {
    side = (
      <div class="home-side-note card">
        <p>{t("noRequests")}</p>
        {line && (
          <p>
            {line.text}{" "}
            {line.link && <a href="#/tools">{line.link === "connect" ? t("connectIt") : t("connectTool")}</a>}
          </p>
        )}
      </div>
    );
  } else {
    const tokens = record?.tokens?.total_tokens;
    side = (
      <>
        <Section title={t("lastWeek")} link={{ label: t("usage"), href: "#/activity/usage" }}>
          {days && totals ? (
            <>
              <dl class="home-kpis">
                <div>
                  <dt>{t("requestsKpi", { count: totals.total })}</dt>
                  <dd class="num">{compact(totals.total)}</dd>
                </div>
                <div>
                  <dt>{t("wentThrough")}</dt>
                  <dd class="num">{successPercent(totals.succeeded, totals.total) ?? "—"}</dd>
                </div>
                {tokens !== undefined && tokens > 0 && (
                  <div>
                    <dt>{t("tokens")}</dt>
                    <dd class="num">{compact(tokens)}</dd>
                  </div>
                )}
              </dl>
              <Days days={days} now={now} />
              {hotspot && (
                <p class="home-hotspot">
                  {p.text(hotspot.text, ids)}{" "}
                  <a href={`#/activity?failed=1&model=${encodeURIComponent(hotspot.model)}`}>
                    {t("seeThem")}
                  </a>
                </p>
              )}
            </>
          ) : week.phase === "failed" ? (
            <SideFailed text={t("weekFailed")} error={week.error} onRetry={run} />
          ) : (
            <Waiting />
          )}
        </Section>
        <Section title={t("latest")} link={{ label: t("allRequests"), href: "#/activity" }}>
          {latest.data ? (
            events.length > 0 ? (
              <div class="home-latest">
                {events.map((e) => (
                  <RequestRow key={e.seq} event={e} name={naming.byId} compact />
                ))}
              </div>
            ) : (
              <p class="home-side-empty">{t("noneToList")}</p>
            )
          ) : latest.phase === "failed" ? (
            <SideFailed text={t("latestFailed")} error={latest.error} onRetry={run} />
          ) : (
            <Waiting />
          )}
        </Section>
      </>
    );
  }

  return (
    <>
      {head}
      <div class="home-body" data-stale={stale ? "" : undefined}>
        <div class="home-cols">
          <div class="home-main">
            <header class="home-strip" data-verdict={verdict.kind}>
              <div class="home-verdict">
                <h2 class="home-headline">{verdict.headline}</h2>
                {verdict.note && <p class="home-note">{verdict.note}</p>}
                <p class="home-counts">
                  {countsLine(grouped.counts, record?.enabled && totals ? totals : undefined)}
                </p>
              </div>
              <StateBar counts={grouped.counts} />
            </header>
            {stale ? (
              <StaleNotice at={stale.at} error={stale.error} onRetry={run} />
            ) : (
              refusedConfig &&
              config && (
                <Notice tone="warn">
                  {t("refused", {
                    error: p.text(refusedConfig.error, ids) ?? t("noReason"),
                    time: new Date(config.active.at).toLocaleTimeString(undefined, {
                      hour: "2-digit",
                      minute: "2-digit",
                      hourCycle: "h23",
                    }),
                  })}{" "}
                  <a href="#/config">{t("openConfig")}</a>
                </Notice>
              )
            )}
            <div class="home-fade">
              {band}
              {pools.length > 0 && (
                <section class="home-pools" aria-label={t("pools")}>
                  {pools.map((pool) => (
                    <PoolCard key={pool.extension} pool={pool} />
                  ))}
                </section>
              )}
              {accountsCard}
            </div>
          </div>
          <aside class="home-side">
            {connection}
            <div class="home-fade">{side}</div>
          </aside>
        </div>
      </div>
    </>
  );
}

export const homePage = (): PageDef => ({
  id: "status",
  dest: "home",
  title: t("title"),
  tip: t("tip"),
  render: () => <HomePage />,
});

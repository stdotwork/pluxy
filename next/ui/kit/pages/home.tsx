// Home: whether the person's coding tools can be served, every account with how much of its
// limits is left, the address for tools, the last seven days and the latest requests. On a new
// install it shows the one next step instead. Its page ID stays `status`: that is its address and
// the theme hook `data-page`.

import type { ComponentChildren } from "preact";
import { useCallback, useEffect, useRef, useState } from "preact/hooks";
import { type Account, problem, runningExtensions, type Status } from "../api";
import type { Setup } from "../api/setup";
import type { UsageSummary } from "../api/usage";
import type { UsageEvent } from "../api/usageEvents";
import type { Reply } from "../host";
import type { Outcome } from "../parts";
import {
  AccountRow,
  GROUPS,
  groupWord,
  Placeholder,
  RequestRow,
  StaleNotice,
  StatePane,
  tryAgainWord,
  useNaming,
  useWindows,
} from "../parts";
import { expireWindows } from "../parts/hooks";
import { accountIdentities, usePrivacy } from "../privacy";
import type { PageDef } from "../shell";
import { strings } from "../strings";
import { Button, Notice, PageHead, useCopy, useKit, usePoll } from "../ui";
import {
  countsLine,
  groupAccounts,
  hotspotOf,
  localDays,
  noAccountsTitle,
  type Read,
  servingOrder,
  staleOf,
  toolLine,
  usageQuery,
  verdictOf,
  weekTotals,
} from "./home.helpers";
import { Days } from "./homeDays";

const t = strings("home", {
  title: "Home",
  tip: "Whether your tools can be served",
  refresh: "Refresh",
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
  address: "Address for tools",
  copy: "Copy",
  copied: "Copied",
  notCopied: "Not copied",
  more: "And {count} more",
  seeAll: "See all",
  lastWeek: "Last 7 days",
  usage: "Usage",
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
});

/** How often the page reads again while it is shown. */
export const HOME_POLL_MS = 60_000;
/** The most serving accounts Home lists; the rest are on Accounts. */
export const SERVING_SHOWN = 12;
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

/** The address for tools and a button that copies it exactly. */
function AddressCopy(props: { address: string }) {
  const copy = useCopy(props.address);
  return (
    <div class="home-address">
      <span class="home-address-label">{t("address")}</span>
      <code class="mono" title={props.address}>
        {props.address}
      </code>
      <Button onClick={copy.copy}>
        {copy.state === "copied" ? t("copied") : copy.state === "failed" ? t("notCopied") : t("copy")}
      </Button>
    </div>
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

function Section(props: {
  title: string;
  link?: { label: string; href: string };
  children: ComponentChildren;
}) {
  return (
    <section class="home-section" aria-label={props.title}>
      <header class="home-section-head">
        <h2>{props.title}</h2>
        {props.link && <a href={props.link.href}>{props.link.label}</a>}
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

export function HomePage() {
  const { api, host } = useKit();
  const p = usePrivacy();
  const { round, run, again } = useRound();
  const [refreshed, setRefreshed] = useState(0);
  const { accounts, status, week, latest } = round;
  const list = accounts.data;
  const running = runningExtensions(status.data);
  const naming = useNaming(list);
  const now = new Date();
  const grouped = list && groupAccounts(list, running, now.getTime());
  // The limits are read for the accounts that serve or wait, the waiting ones first.
  const windows = useWindows(
    grouped && [...grouped.groups.waiting, ...grouped.groups.serving],
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

  // One read of the coding tools when there is nothing else to say about the requests, never polled.
  const record = week.data;
  const quiet = list?.length === 0 || (record?.enabled === true && record.total_requests === 0);
  const [setup, setSetup] = useState<Setup>();
  const asked = useRef(false);
  useEffect(() => {
    if (!quiet || asked.current) return;
    asked.current = true;
    api.setup().then((r) => {
      if (problem(r) === undefined) setSetup(r.body);
    });
  }, [quiet, api]);

  const head = <PageHead title={t("title")} actions={<Button onClick={refresh}>{t("refresh")}</Button>} />;

  if (accounts.phase === "loading") {
    return (
      <>
        {head}
        <StatePane kind="loading" title={t("checking")} />
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

  const tools = setup?.found.clients.filter((c) => c.installed && c.state !== "unchecked") ?? [];
  if (!list || list.length === 0 || !grouped) {
    return (
      <>
        {head}
        <StatePane
          kind="empty"
          title={noAccountsTitle()}
          primary={{ label: t("addAccount"), href: "#/add-account" }}
          secondary={{ label: t("guidedSetup"), href: "#/setup" }}
        >
          {t("noAccountsHelp")}
        </StatePane>
        <p class="home-next">
          {tools.length > 0 ? t("found", { tools: tools.map((c) => c.name).join(", ") }) : t("nextTool")}
        </p>
      </>
    );
  }

  const verdict = verdictOf(status.data, grouped.counts, grouped.firstBack, now.getTime());
  const days = record?.enabled ? localDays(record.series, now) : undefined;
  const totals = days && weekTotals(days);
  const hotspot = record?.enabled && totals ? hotspotOf(record.models, totals.failed) : undefined;
  const stale = staleOf([accounts, status, week, latest]);
  const config = status.data;
  const refusedConfig =
    config && config.desired.revision !== config.active.revision ? config.desired : undefined;
  const ids = list.flatMap(accountIdentities);
  const serving = servingOrder(grouped.groups.serving, windows);
  const events = latest.data ?? [];
  const line = quiet && setup ? toolLine(setup.found.clients) : undefined;

  const rows = (group: (typeof GROUPS)[number]) => {
    const all = group === "serving" ? serving : grouped.groups[group];
    if (all.length === 0) return null;
    const shown = group === "serving" ? all.slice(0, SERVING_SHOWN) : all;
    return (
      <section class="home-group" key={group} aria-label={groupWord(group)}>
        <h2 class="home-group-head">
          {groupWord(group)} <span class="home-group-count">{all.length}</span>
        </h2>
        {shown.map((a) => (
          <AccountRow
            key={a.id}
            account={a}
            windows={windows.get(a.id)}
            running={running}
            onTurnOn={(id) => fixed(api.setDisabled(id, false))}
            onClearCooldown={(id) => fixed(api.resetCooldown(id))}
          />
        ))}
        {shown.length < all.length && (
          <p class="home-more">
            {t("more", { count: all.length - shown.length })} <a href="#/accounts">{t("seeAll")}</a>
          </p>
        )}
      </section>
    );
  };

  let side: ComponentChildren;
  if (record?.enabled === false) {
    side = (
      <p class="home-side-note">
        {t("recordOff")} <a href="#/config/logging">{t("recordOn")}</a>
      </p>
    );
  } else if (record && totals?.total === 0) {
    side = (
      <div class="home-side-note">
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
    side = (
      <>
        <Section title={t("lastWeek")} link={{ label: t("usage"), href: "#/activity/usage" }}>
          {days && totals ? (
            <>
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
              <p class="home-side-note">{t("noneToList")}</p>
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
      <header class="home-strip" data-verdict={verdict.kind}>
        <div class="home-verdict">
          <h2 class="home-headline">{verdict.headline}</h2>
          {verdict.note && <p class="home-note">{verdict.note}</p>}
          <p class="home-counts">
            {countsLine(grouped.counts, record?.enabled && totals ? totals : undefined)}
          </p>
        </div>
        {host.address && <AddressCopy address={host.address} />}
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
      <div class="home-body">
        <div class="home-cols">
          <div class="home-accounts">{GROUPS.map(rows)}</div>
          <aside class="home-side">{side}</aside>
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

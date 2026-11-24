// The first screen. It is written like a short letter: the date, one sentence in large type that
// is the verdict, what needs the person with the one button that fixes each thing, what is waiting
// and will come back by itself, then this week's use and the usage windows. On a wide window the
// letter is the left page and the figures are the right one.

import type { Account, ClientKey, UsageGroup, UsageSummary } from "../../api";
import { useKit } from "../../ui";
import { accountHref, useCore } from "./core";
import { useRead, useWindows, WINDOWS_AT_ONCE } from "./data";
import { FixButton } from "./fix";
import {
  clock,
  conditionSentence,
  dateline,
  duration,
  moment,
  num,
  percent,
  plural,
  providerName,
  short,
  usedPercent,
} from "./lib";
import {
  Asking,
  type BarItem,
  BarList,
  Btn,
  Note,
  Onward,
  Section,
  StateMark,
  TimeBars,
  Trouble,
  tightest,
  WindowBrief,
} from "./parts";

const WEEK = "since=7d&bucket=24h&buckets=7";
const EVERY_MS = 60_000;

const bars = (groups: UsageGroup[], name: (g: UsageGroup) => string, href?: (g: UsageGroup) => string) =>
  groups
    .filter((g) => g.total_requests > 0)
    .sort((a, b) => b.total_requests - a.total_requests)
    .map(
      (g): BarItem => ({
        id: g.name,
        name: name(g),
        value: g.total_requests,
        failed: g.failure_count,
        href: href?.(g),
      }),
    );

/** A client key as people know it: its masked value, or its ID while the keys are not read. */
export const keyName = (id: string, keys: ClientKey[] | undefined) => {
  const key = keys?.find((k) => k.id === id);
  return key ? `Key ${key.masked.replace(/\*+/, "…")}` : id ? `Key ${id.slice(0, 6)}…` : "No key";
};

/** This week in a sentence or two: how much, how much of it went through, and where it did not. */
export function weekWords(u: UsageSummary): string {
  if (u.total_requests === 0) return "No requests in the last seven days.";
  const said = [
    `${plural(u.total_requests, "request")} in the last seven days, and ${percent(u.success_count, u.total_requests)} went through.`,
  ];
  if (u.failure_count > 0) {
    const worst = [...u.models].sort((a, b) => b.failure_count - a.failure_count)[0];
    if (worst && worst.failure_count * 2 >= u.failure_count && u.models.length > 1) {
      said.push(
        worst.failure_count === u.failure_count
          ? `All ${num(u.failure_count)} that failed asked for ${worst.name}.`
          : `${num(worst.failure_count)} of the ${num(u.failure_count)} that failed asked for ${worst.name}.`,
      );
    }
  }
  return said.join(" ");
}

export function Home() {
  const { api, host } = useKit();
  const core = useCore();
  const week = useRead(() => api.usage(WEEK), EVERY_MS);
  const keys = useRead(api.clientKeys);
  const list = core.list;
  const { now, naming } = core;

  // The accounts' windows: those that serve or wait, the first few of a large fleet.
  const asked = (list ?? []).filter((a) => core.conditionOf(a).group !== "off").slice(0, WINDOWS_AT_ONCE);
  const { windows } = useWindows(list ? asked : undefined);

  if (!list) {
    if (core.accounts.error) {
      return (
        <article class="db-home">
          <header class="db-opening">
            <p class="db-dateline">{dateline(now)}</p>
            <h1 class="db-verdict" data-tone="needs">
              pluxy is not answering.
            </h1>
            <p class="db-lead">{naming.mask(core.accounts.error)}</p>
            <div class="db-opening-actions">
              <Btn kind="primary" icon="refresh" onClick={core.reload} disabled={core.accounts.busy}>
                Ask again
              </Btn>
            </div>
          </header>
        </article>
      );
    }
    return (
      <article class="db-home">
        <header class="db-opening">
          <p class="db-dateline">{dateline(now)}</p>
          <h1 class="db-verdict" data-tone="unknown" aria-live="polite">
            Asking pluxy how it is doing…
          </h1>
          <div class="db-ghost" aria-hidden="true">
            <i />
            <i />
          </div>
        </header>
      </article>
    );
  }

  const verdict = core.verdict;
  const needs = list.filter((a) => core.conditionOf(a).group === "needs");
  const waiting = list.filter((a) => {
    const c = core.conditionOf(a);
    return c.group === "waiting" || (c.group === "serving" && c.resting.length > 0);
  });
  const s = core.status.data;
  const refused = s && s.desired.revision !== s.active.revision ? s.desired : undefined;
  const u = week.data;

  // Usage windows, by provider; within one, the account closest to a limit first.
  const byProvider = new Map<string, Account[]>();
  for (const a of asked) {
    if (!windows.has(a.id)) continue;
    const key = a.extension ?? "";
    byProvider.set(key, [...(byProvider.get(key) ?? []), a]);
  }
  const tight = (a: Account) => {
    const l = tightest(windows.get(a.id));
    return l ? (usedPercent(l) ?? -1) : -1;
  };
  const PER_PROVIDER = 5;

  return (
    <article class="db-home">
      <div class="db-letter">
        <header class="db-opening">
          <p class="db-dateline">
            {dateline(now)}
            {core.accounts.at && <span>, checked at {clock(core.accounts.at)}</span>}
          </p>
          <h1 class="db-verdict" data-tone={verdict?.tone}>
            {verdict?.headline}
          </h1>
          {verdict?.rest && <p class="db-lead">{verdict.rest}</p>}
          {list.length === 0 && (
            <div class="db-opening-actions">
              <Btn kind="primary" icon="plus" href="#/add-account">
                Add an account
              </Btn>
            </div>
          )}
          {core.accounts.error && (
            <Trouble retry={core.reload} quiet>
              This is what pluxy last said. It is not answering now: {naming.mask(core.accounts.error)}
            </Trouble>
          )}
          {refused && s && (
            <Note tone="warn">
              pluxy refused the last change to its config ({naming.mask(refused.error ?? "no reason given")}),
              so the config from {moment(Date.parse(s.active.at), now)} still serves.{" "}
              <a href="#/config">Open the config</a>
            </Note>
          )}
        </header>

        {needs.length > 0 && (
          <Section title="Needs you" class="db-needs">
            <ul class="db-people">
              {needs.map((a, i) => {
                const c = core.conditionOf(a);
                return (
                  <li key={a.id} class="db-need">
                    <StateMark c={c} />
                    <div class="db-need-text">
                      <a class="db-name" href={accountHref(a.id)}>
                        {naming.name(a.id)}
                      </a>
                      <p>{conditionSentence(a, c, now)}</p>
                    </div>
                    <FixButton account={a} c={c} primary={i === 0} />
                  </li>
                );
              })}
            </ul>
          </Section>
        )}

        {waiting.length > 0 && (
          <Section title="Waiting, nothing to do" class="db-waits">
            <ul class="db-people">
              {waiting.map((a) => {
                const c = core.conditionOf(a);
                const resting = c.resting[0];
                return (
                  <li key={a.id} class="db-wait">
                    <StateMark c={c} />
                    <p>
                      <a class="db-name" href={accountHref(a.id)}>
                        {naming.name(a.id)}
                      </a>{" "}
                      {c.group === "serving" && resting
                        ? `serves, but rests on ${c.resting.length === 1 ? resting.model : plural(c.resting.length, "model")} for ${duration(Math.max(...c.resting.map((r) => r.until)) - now)}.`
                        : c.kind === "exhausted"
                          ? `has used up its limit${c.until && c.until > now ? ` and is back at ${moment(c.until, now)}, in ${duration(c.until - now)}.` : "."}`
                          : `is cooling down${c.until && c.until > now ? ` for ${duration(c.until - now)}.` : "."}`}
                    </p>
                  </li>
                );
              })}
            </ul>
          </Section>
        )}

        {needs.length === 0 && waiting.length === 0 && list.length > 0 && (
          <p class="db-allclear">
            Nothing here needs you. <Onward href="#/accounts">See the accounts</Onward>
          </p>
        )}
      </div>

      <div class="db-figures">
        <Section title="This week" aside={<Onward href="#/usage">All usage</Onward>}>
          {!u && week.loading && <Asking lines={4}>Adding up the week…</Asking>}
          {!u && week.error && <Trouble retry={week.reload}>{naming.mask(week.error)}</Trouble>}
          {u && !u.enabled && (
            <Note tone="warn">
              pluxy is not recording requests, so there is nothing to add up.{" "}
              <a href="#/config/logging">Turn on usage statistics</a>
            </Note>
          )}
          {u?.enabled && (
            <>
              <p class="db-said">{weekWords(u)}</p>
              {u.total_requests > 0 && (
                <>
                  <TimeBars
                    small
                    label={`Requests on each of the last seven days: ${u.series.map((b) => b.requests).join(", ")}`}
                    buckets={u.series}
                    tick={(d) => d.toLocaleDateString("en-GB", { weekday: "short", timeZone: "UTC" })}
                    span={(d) =>
                      d.toLocaleDateString("en-GB", {
                        weekday: "long",
                        day: "numeric",
                        month: "long",
                        timeZone: "UTC",
                      })
                    }
                  />
                  <div class="db-three">
                    <div>
                      <h3>By provider</h3>
                      <BarList
                        label="Requests by provider"
                        max={4}
                        items={bars(u.providers, (g) => providerName(g.name))}
                      />
                    </div>
                    <div>
                      <h3>By model</h3>
                      <BarList label="Requests by model" max={4} items={bars(u.models, (g) => g.name)} />
                    </div>
                    <div>
                      <h3>By client key</h3>
                      <BarList
                        label="Requests by client key"
                        max={4}
                        items={bars(u.keys, (g) => keyName(g.name, keys.data?.keys))}
                      />
                    </div>
                  </div>
                  <p class="db-small">
                    {short(u.tokens?.total_tokens ?? 0)} tokens in all
                    {u.average_latency_ms
                      ? `, a reply took ${(u.average_latency_ms / 1000).toFixed(1)} s on average`
                      : ""}
                    .
                  </p>
                </>
              )}
            </>
          )}
        </Section>

        {asked.length > 0 && (
          <Section title="Usage windows" aside={<Onward href="#/accounts">All accounts</Onward>}>
            {byProvider.size === 0 && <Asking lines={3}>Asking the providers what is left…</Asking>}
            {[...byProvider].map(([extension, accounts]) => {
              const sorted = [...accounts].sort((a, b) => tight(b) - tight(a));
              const shown = sorted.slice(0, PER_PROVIDER);
              return (
                <div class="db-provider" key={extension}>
                  <h3>{providerName(extension)}</h3>
                  {shown.map((a) => (
                    <div class="db-provider-account" key={a.id}>
                      <a class="db-name" href={accountHref(a.id)} title={naming.name(a.id)}>
                        {naming.name(a.id)}
                      </a>
                      <WindowBrief w={windows.get(a.id)} now={now} account={naming.name(a.id)} />
                    </div>
                  ))}
                  {sorted.length > shown.length && (
                    <p class="db-small">
                      and {plural(sorted.length - shown.length, "more account")} with more left.
                    </p>
                  )}
                </div>
              );
            })}
            {list.length > asked.length && (
              <p class="db-small">
                The first {asked.length} accounts are read here; each account's page reads its own.
              </p>
            )}
          </Section>
        )}
      </div>

      <footer class="db-colophon">
        {s && (
          <p>
            pluxy {s.version}
            {host.address ? `, listening at ${host.address}` : ""}. Config revision {s.active.revision}, in
            use since {moment(Date.parse(s.active.at), now)}.{" "}
            {s.extensions.filter((e) => e.current).length > 0 &&
              `${plural(s.extensions.filter((e) => e.current).length, "provider")} running.`}
          </p>
        )}
      </footer>
    </article>
  );
}

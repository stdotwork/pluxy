// The request list: each request on a line (when, what was asked for, who answered, how it went)
// under the day it came on, and opened, the story of why it went where it did, in a sentence.

import { useState } from "preact/hooks";
import type { ClientKey, UsageEvent } from "../../api";
import { accountHref } from "./core";
import type { Naming } from "./data";
import { keyName } from "./home";
import { Icon } from "./icons";
import { clock, dayName, latency, num, providerName, triedResult, whyOf } from "./lib";

function Request(props: {
  e: UsageEvent;
  naming: Naming;
  keys: ClientKey[] | undefined;
  open: boolean;
  onToggle: () => void;
  own?: string;
}) {
  const { e, naming } = props;
  const why = whyOf(e, naming.name);
  const account = e.account ? naming.find(e.account) : undefined;
  const tokens = e.tokens.total_tokens;
  const id = `db-req-${e.seq}`;
  return (
    <li class="db-request" data-failed={e.failed ? "" : undefined} data-open={props.open ? "" : undefined}>
      <button
        type="button"
        class="db-request-line"
        aria-expanded={props.open}
        aria-controls={id}
        onClick={props.onToggle}
      >
        <time dateTime={e.timestamp}>{clock(e.timestamp)}</time>
        <span class="db-request-model" title={e.model}>
          {e.model}
        </span>
        <span class="db-request-to">
          {e.failed ? (
            <span class="db-failed-text">
              <Icon name="needsYou" /> Failed{e.status ? ` (${e.status})` : ""}
            </span>
          ) : (
            e.account !== props.own && (
              <span title={e.account ? naming.name(e.account) : undefined}>
                {e.account ? naming.name(e.account) : "—"}
              </span>
            )
          )}
          {why.note && <small> {why.note}</small>}
        </span>
        <span class="db-request-n">{e.failed && e.latency_ms === 0 ? "" : latency(e.latency_ms)}</span>
        <span class="db-request-n">{tokens !== undefined ? `${num(tokens)} tokens` : ""}</span>
        <Icon name="caretDown" />
      </button>
      {props.open && (
        <div class="db-request-more" id={id}>
          <p class="db-request-story">{why.story}</p>
          {(e.tried?.length ?? 0) > 1 && (
            <ol class="db-tried" aria-label="What pluxy tried, in order">
              {e.tried?.map((t, i) => (
                <li key={i} data-ok={t.result === "ok" ? "" : undefined}>
                  <b>{naming.name(t.account)}</b> {triedResult(t)}
                  <small>
                    {" "}
                    {providerName(t.provider)}
                    {t.model && t.model !== e.model ? `, as ${t.model}` : ""}
                  </small>
                </li>
              ))}
            </ol>
          )}
          <dl class="db-facts db-facts-inline">
            {e.provider && (
              <div>
                <dt>Provider</dt>
                <dd>{providerName(e.provider)}</dd>
              </div>
            )}
            {account && (
              <div>
                <dt>Account</dt>
                <dd>
                  <a href={accountHref(account.id)}>{naming.name(account.id)}</a>
                </dd>
              </div>
            )}
            {e.served_model && e.served_model !== e.model && (
              <div>
                <dt>Answered as</dt>
                <dd>{e.served_model}</dd>
              </div>
            )}
            <div>
              <dt>Client key</dt>
              <dd>{e.key_id ? keyName(e.key_id, props.keys) : "None"}</dd>
            </div>
            {e.endpoint && (
              <div>
                <dt>Came to</dt>
                <dd class="db-code">
                  {e.endpoint}
                  {e.stream ? " (streamed)" : ""}
                </dd>
              </div>
            )}
            {tokens !== undefined && (
              <div>
                <dt>Tokens</dt>
                <dd>
                  {num(e.tokens.input_tokens ?? 0)} in, {num(e.tokens.output_tokens ?? 0)} out
                  {e.tokens.cached_tokens ? `, ${num(e.tokens.cached_tokens)} read from cache` : ""}
                  {e.tokens.reasoning_tokens
                    ? `, ${num(e.tokens.reasoning_tokens)} of the output reasoning`
                    : ""}
                </dd>
              </div>
            )}
            {e.id && (
              <div>
                <dt>Trace</dt>
                <dd>
                  <a class="db-code" href={`#/logs/${encodeURIComponent(e.id)}`}>
                    {e.id}
                  </a>
                </dd>
              </div>
            )}
          </dl>
        </div>
      )}
    </li>
  );
}

/** Requests, newest first, under the day each came on. One is open at a time. */
export function RequestList(props: {
  events: UsageEvent[];
  naming: Naming;
  keys: ClientKey[] | undefined;
  now: number;
  /** The account whose page the list is on: its lines do not repeat its name. */
  own?: string;
}) {
  const [open, setOpen] = useState<number>();
  const days: { day: string; events: UsageEvent[] }[] = [];
  for (const e of props.events) {
    const day = dayName(e.timestamp, props.now);
    const last = days[days.length - 1];
    if (last && last.day === day) last.events.push(e);
    else days.push({ day, events: [e] });
  }
  return (
    <div class="db-requests" data-own={props.own ? "" : undefined}>
      {days.map((d) => (
        <section key={d.day} aria-label={d.day}>
          <h3 class="db-day">{d.day}</h3>
          <ol>
            {d.events.map((e) => (
              <Request
                key={e.seq}
                e={e}
                naming={props.naming}
                keys={props.keys}
                own={props.own}
                open={open === e.seq}
                onToggle={() => setOpen(open === e.seq ? undefined : e.seq)}
              />
            ))}
          </ol>
        </section>
      ))}
    </div>
  );
}

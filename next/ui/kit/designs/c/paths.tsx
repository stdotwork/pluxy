// A request drawn as the path it took: each account pluxy tried, in order, with what the try came
// to, ending at the one that answered or at the error that ended it. And the list of requests
// built from those paths.

import { useState } from "preact/hooks";
import type { UsageEvent, UsageTried } from "../../api";
import type { Fleet } from "./data";
import { Icon } from "./icons";
import { ago, clockExact, compact, count, latency } from "./lib";
import { Btn } from "./parts";

/** An error code as people say it. */
const RESULT_WORDS: Record<string, string> = {
  ok: "answered",
  rate_limit: "rate limited",
  quota: "limit used up",
  auth: "sign-in refused",
  upstream: "provider error",
  not_found: "no account serves it",
  account_closed: "account closed",
  timeout: "timed out",
  canceled: "client left",
  bad_request: "request refused",
  no_account: "no account free",
};

export const resultWords = (code: string | undefined) =>
  code ? (RESULT_WORDS[code] ?? code.replace(/_/g, " ")) : "failed";

/** The tries of a request: core lists them only when there were several or it failed. */
function triesOf(e: UsageEvent): UsageTried[] {
  if (e.tried && e.tried.length > 0) return e.tried;
  if (!e.account) return [];
  return [
    {
      account: e.account,
      provider: e.provider ?? "",
      model: e.upstream_model,
      result: e.failed ? (e.error_code ?? "failed") : "ok",
    },
  ];
}

export function RequestPath(props: { e: UsageEvent; fleet: Fleet }) {
  const { e, fleet } = props;
  const tries = triesOf(e);
  const fellBack = (e.chain_step ?? 0) >= 2;
  return (
    <ol class="dc-path" aria-label="Path">
      {tries.map((t, i) => {
        const account = fleet.account(t.account);
        const name = fleet.mask(account?.id ?? t.account);
        const p = fleet.providers.of(t.provider || account?.extension);
        const ok = t.result === "ok";
        const last = i === tries.length - 1;
        return (
          <li key={i} class="dc-path-stop" data-ok={ok ? "" : undefined} data-slot-p={p.slot}>
            <span class="dc-path-dot" aria-hidden="true">
              {ok ? <Icon name="check" size={11} /> : <Icon name="close" size={10} />}
            </span>
            <span class="dc-path-text">
              {last && fellBack && <span class="dc-path-step">step {e.chain_step}</span>}
              <span class="dc-path-name" title={`${name} · ${p.name}${t.model ? ` · ${t.model}` : ""}`}>
                {name}
              </span>
              <span class="dc-path-result">
                {ok ? (tries.length > 1 ? "answered" : "") : resultWords(t.result)}
              </span>
            </span>
          </li>
        );
      })}
      {tries.length === 0 && (
        <li class="dc-path-stop" data-end="">
          <span class="dc-path-dot" aria-hidden="true">
            <Icon name="close" size={10} />
          </span>
          <span class="dc-path-text">
            <span class="dc-path-result">{resultWords(e.error_code)}</span>
          </span>
        </li>
      )}
    </ol>
  );
}

const tokenParts = (e: UsageEvent): [string, number | undefined][] => [
  ["Input", e.tokens.input_tokens],
  ["Output", e.tokens.output_tokens],
  ["Reasoning", e.tokens.reasoning_tokens],
  ["Read from cache", e.tokens.cached_tokens],
  ["Written to cache", e.tokens.cache_creation_tokens],
];

/** One request: when, from which key, for which model, its path, and how it ended. */
export function RequestRow(props: { e: UsageEvent; fleet: Fleet; now: number }) {
  const { e, fleet } = props;
  const [open, setOpen] = useState(false);
  const tries = triesOf(e);
  const total = e.tokens.total_tokens;
  return (
    <li class="dc-req" data-failed={e.failed ? "" : undefined} data-open={open ? "" : undefined}>
      <div class="dc-req-line">
        <time class="dc-req-when dc-num" dateTime={e.timestamp} title={clockExact(e.timestamp)}>
          {ago(e.timestamp, props.now)}
        </time>
        <span class="dc-req-key dc-mono" title="Client key">
          {e.key_id ? fleet.keyName(e.key_id) : "–"}
        </span>
        <span class="dc-req-model dc-ellipsis" title={e.model}>
          {e.model}
        </span>
        <RequestPath e={e} fleet={fleet} />
        <span class="dc-req-end">
          <span class="dc-req-status" data-failed={e.failed ? "" : undefined}>
            {e.failed && <Icon name="failed" size={14} />}
            {e.status ?? "–"}
          </span>
          <span class="dc-num dc-muted">{latency(e.latency_ms)}</span>
          <span class="dc-num dc-muted dc-req-tokens">
            {total !== undefined ? `${compact(total)} tok` : ""}
          </span>
        </span>
        <Btn
          kind="plain"
          size="small"
          icon={open ? "down" : "right"}
          label={open ? "Hide details" : "Show details"}
          pressed={open}
          onClick={() => setOpen(!open)}
        />
      </div>
      {open && (
        <div class="dc-req-more">
          <dl class="dc-facts">
            <div>
              <dt>Time</dt>
              <dd>{clockExact(e.timestamp)}</dd>
            </div>
            {e.endpoint && (
              <div>
                <dt>Endpoint</dt>
                <dd class="dc-mono">
                  {e.endpoint}
                  {e.stream ? " · stream" : ""}
                </dd>
              </div>
            )}
            {e.upstream_model && e.upstream_model !== e.model && (
              <div>
                <dt>Sent upstream as</dt>
                <dd>{e.upstream_model}</dd>
              </div>
            )}
            {e.error_code && (
              <div>
                <dt>Ended with</dt>
                <dd>
                  {resultWords(e.error_code)} <span class="dc-muted dc-mono">({e.error_code})</span>
                </dd>
              </div>
            )}
            {tokenParts(e)
              .filter(([, v]) => v !== undefined)
              .map(([k, v]) => (
                <div key={k}>
                  <dt>{k}</dt>
                  <dd class="dc-num">{count(v ?? 0)} tokens</dd>
                </div>
              ))}
            {e.id && (
              <div>
                <dt>Trace ID</dt>
                <dd class="dc-mono">
                  <a href={`#/logs/${encodeURIComponent(e.id)}`}>{e.id}</a>
                </dd>
              </div>
            )}
          </dl>
          {tries.length > 0 && (
            <ol class="dc-tries" aria-label="What pluxy tried, in order">
              {tries.map((t, i) => {
                const account = fleet.account(t.account);
                const p = fleet.providers.of(t.provider || account?.extension);
                const id = account?.id;
                const name = fleet.mask(id ?? t.account);
                return (
                  <li key={i}>
                    <span class="dc-num dc-muted">{i + 1}</span>
                    <span>
                      {id ? <a href={`#/accounts/${encodeURIComponent(id)}`}>{name}</a> : name}
                      <span class="dc-muted">
                        {" "}
                        · {p.name}
                        {t.model ? ` · ${t.model}` : ""}
                      </span>
                    </span>
                    <span data-ok={t.result === "ok" ? "" : undefined} class="dc-tries-result">
                      {resultWords(t.result)}
                    </span>
                  </li>
                );
              })}
            </ol>
          )}
        </div>
      )}
    </li>
  );
}

export function RequestList(props: { events: UsageEvent[]; fleet: Fleet; now: number; label: string }) {
  return (
    <ul class="dc-reqs" aria-label={props.label}>
      {props.events.map((e) => (
        <RequestRow key={e.seq} e={e} fleet={props.fleet} now={props.now} />
      ))}
    </ul>
  );
}

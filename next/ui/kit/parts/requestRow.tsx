// RequestRow: one request on one line, the same on Home and Activity. The full row is a button
// that opens the request in place: the page's routing story, the attempts in order and the facts.
// The compact row (Home) is a link to that request on Activity.

import type { ComponentChildren } from "preact";
import { useState } from "preact/hooks";
import type { ClientKey } from "../api/config";
import type { UsageEvent } from "../api/usageEvents";
import { Icon } from "../icons";
import { usePrivacy } from "../privacy";
import { failureWord } from "../states";
import { strings } from "../strings";
import { Facts, TraceId } from "../ui";
import { accountHref, keyName, providerName } from "./account";

const t = strings("parts.requestRow", {
  succeeded: "Succeeded",
  account: "Account",
  provider: "Provider",
  modelUsed: "Model used",
  key: "Key",
  path: "Path",
  tokens: "Tokens",
  trace: "Trace",
  tokenCounts: "in {input}, out {output}, cached {cached}",
  attempts: "Attempts",
  underMs: "<1 ms",
  ms: "{count} ms",
  seconds: "{count} s",
  minutes: "{minutes}m {seconds}s",
});

const n = (v: number | undefined) => (v ?? 0).toLocaleString();

/** How long a request took, as a person reads a wait: <1 ms, 420 ms, 2.2 s, 1m 5s. */
export function timeTaken(ms: number): string {
  if (Math.round(ms) < 1) return t("underMs");
  if (ms < 1000) return t("ms", { count: Math.round(ms) });
  if (ms < 60_000) return t("seconds", { count: (ms / 1000).toFixed(1) });
  return t("minutes", { minutes: Math.floor(ms / 60_000), seconds: Math.round((ms % 60_000) / 1000) });
}

const hhmm = (time: string, seconds: boolean) =>
  new Date(time).toLocaleTimeString(undefined, {
    hour: "2-digit",
    minute: "2-digit",
    second: seconds ? "2-digit" : undefined,
    hourCycle: "h23",
  });

/** A request's result: a mark named Succeeded, or a danger mark and the failure word, shown. */
export function RequestResult(props: { event: UsageEvent }) {
  if (!props.event.failed) {
    return (
      <span class="request-result" data-result="ok">
        <Icon name="check" label={t("succeeded")} />
      </span>
    );
  }
  return (
    <span class="request-result" data-result="failed">
      <Icon name="needsYou" />
      <span>{failureWord(props.event)}</span>
    </span>
  );
}

/** Where a request's trace leads on Activity; a request that reached no account has none. */
export const requestHref = (id?: string) =>
  id ? `#/activity?trace=${encodeURIComponent(id)}` : "#/activity";

/**
 * One request. name turns an account's ID or public ID into its name, masked (useNaming().byId);
 * without it the ID shows, masked. keys are the client keys (GET /client-keys), for the key's
 * name. note is the page's few words on a detour ("after 2 others"), story the page's routing
 * story, both masked by the page. trace is how the facts show the request's trace: a link to its
 * log (the default), or copyable text only, for a request whose log is not kept. compact (Home)
 * makes the row a link to the request on Activity, with no expanding. open and onToggle make the
 * expanding the page's own.
 */
export function RequestRow(props: {
  event: UsageEvent;
  name?: (account: string) => string;
  keys?: ClientKey[];
  note?: string;
  story?: ComponentChildren;
  trace?: "link" | "text";
  compact?: boolean;
  open?: boolean;
  onToggle?: (open: boolean) => void;
}) {
  const p = usePrivacy();
  const [ownOpen, setOwnOpen] = useState(false);
  const e = props.event;
  const name = props.name ?? ((id: string) => p.id(id));
  const account = e.account ? name(e.account) : undefined;
  // A model's name can hold an address or a person's name (an alias, a custom upstream's model).
  const model = p.text(e.model);
  const when = new Date(e.timestamp);
  const full = Number.isNaN(when.getTime()) ? e.timestamp : when.toLocaleString();
  if (props.compact) {
    return (
      <a
        class="request-row"
        data-part="request-row"
        data-compact=""
        data-failed={e.failed ? "" : undefined}
        href={requestHref(e.id)}
      >
        <time class="request-time" dateTime={e.timestamp} title={full}>
          {hhmm(e.timestamp, false)}
        </time>
        <span class="request-model" title={model}>
          {model}
        </span>
        <span class="request-account" title={e.failed ? undefined : account}>
          {e.failed ? failureWord(e) : account}
        </span>
        <span class="request-result" data-result={e.failed ? "failed" : "ok"}>
          {/* A failed row says its failure word in the account's place: the mark adds nothing to hear. */}
          <Icon name={e.failed ? "needsYou" : "check"} label={e.failed ? undefined : t("succeeded")} />
        </span>
      </a>
    );
  }
  const open = props.open ?? ownOpen;
  const toggle = () => {
    setOwnOpen(!open);
    props.onToggle?.(!open);
  };
  const key = keyName(e.key_id, props.keys, e.label, p.id);
  const tokens = e.tokens ?? {};
  const tried = e.tried ?? [];
  return (
    <div
      class="request-row"
      data-part="request-row"
      data-open={open ? "" : undefined}
      data-failed={e.failed ? "" : undefined}
    >
      <button type="button" class="request-line" aria-expanded={open} onClick={toggle}>
        <time class="request-time" dateTime={e.timestamp} title={full}>
          {hhmm(e.timestamp, true)}
        </time>
        <span class="request-model" title={model}>
          {model}
        </span>
        <span class="request-account" title={account}>
          {account}
          {props.note && <span class="request-note"> {props.note}</span>}
        </span>
        <span class="request-key" title={key}>
          {key}
        </span>
        <span class="request-tokens">{tokens.total_tokens === undefined ? "" : n(tokens.total_tokens)}</span>
        <span class="request-taken">{timeTaken(e.latency_ms)}</span>
        <RequestResult event={e} />
        <span class="request-chevron">
          <Icon name="chevron" />
        </span>
      </button>
      {open && (
        <div class="request-detail">
          {props.story && <div class="request-story">{props.story}</div>}
          {tried.length > 1 && (
            <div class="request-attempts">
              <h3>{t("attempts")}</h3>
              <ol>
                {tried.map((x, i) => (
                  <li key={i}>
                    <a href={accountHref(x.account)}>{name(x.account)}</a>{" "}
                    <span class="request-attempt-provider">{providerName(x.provider)}</span>{" "}
                    {x.model && (
                      <>
                        <span class="request-attempt-model mono">{p.text(x.model)}</span>{" "}
                      </>
                    )}
                    <span class="request-attempt-result" data-result={x.result === "ok" ? "ok" : "failed"}>
                      {x.result === "ok" ? t("succeeded") : failureWord(x.result)}
                    </span>
                  </li>
                ))}
              </ol>
            </div>
          )}
          <Facts
            rows={[
              [t("account"), e.account && <a href={accountHref(e.account)}>{account}</a>],
              [t("provider"), e.provider && providerName(e.provider)],
              [
                t("modelUsed"),
                e.served_model && e.served_model !== e.model ? (
                  <span class="mono">{p.text(e.served_model)}</span>
                ) : undefined,
              ],
              [t("key"), key],
              [t("path"), e.endpoint && <span class="mono">{e.endpoint}</span>],
              [
                t("tokens"),
                tokens.input_tokens === undefined && tokens.output_tokens === undefined
                  ? undefined
                  : t("tokenCounts", {
                      input: n(tokens.input_tokens),
                      output: n(tokens.output_tokens),
                      cached: n(tokens.cached_tokens),
                    }),
              ],
              [
                t("trace"),
                e.id && (
                  <TraceId
                    id={e.id}
                    href={props.trace === "text" ? undefined : `#/logs/${encodeURIComponent(e.id)}`}
                  />
                ),
              ],
            ]}
          />
        </div>
      )}
    </div>
  );
}

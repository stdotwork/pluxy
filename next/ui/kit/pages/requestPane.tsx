// The side pane of one request on Requests: the sentence that says what routing did, the attempts
// as a timeline, the facts, and the one next step. It is what a person opens a failed request to
// read, so it starts with the decision, not with the facts.

import type { ClientKey } from "../api/config";
import type { UsageEvent } from "../api/usageEvents";
import { accountHref, keyName, providerName, timeTaken } from "../parts";
import { usePrivacy } from "../privacy";
import { failureWord } from "../states";
import { strings } from "../strings";
import { Detail, Facts, TraceId } from "../ui";
import { decisionOf, shortCount, whyOf } from "./activity";
import { WordChip } from "./activityChips";

const t = strings("requests.pane", {
  succeeded: "Succeeded",
  tiles: "The request in figures",
  time: "Time taken",
  tokens: "Tokens",
  tokensTo: "{input} → {output}",
  tries: "Tries",
  step: "Fallback step",
  when: "Time",
  account: "Account",
  provider: "Provider",
  modelUsed: "Model used",
  key: "Key",
  path: "Path",
  tokenCounts: "in {input}, out {output}, cached {cached}",
  trace: "Trace",
  attempts: "Attempts",
  why: "Why this account",
  notTried: "pluxy did not try:",
  accounts: "Accounts",
  order: "Order & fallback",
  openAccount: "Open {account}",
  openLog: "Open its log",
});

const n = (v: number | undefined) => (v ?? 0).toLocaleString();

export function RequestPane(props: {
  event: UsageEvent;
  name: (account: string) => string;
  idOf: (account: string) => string;
  keys?: ClientKey[];
  /** How the trace shows: a link to its log, or text to copy (a request whose log is not kept). */
  trace: "link" | "text";
  onClose: () => void;
}) {
  const p = usePrivacy();
  const e = props.event;
  const name = props.name;
  const story = whyOf(e, name);
  const took = timeTaken(e.latency_ms);
  const decision = decisionOf(e, name, took);
  const model = p.text(e.model);
  const account = e.account ? name(e.account) : undefined;
  const key = keyName(e.key_id, props.keys, e.label, p.id);
  const tokens = e.tokens ?? {};
  const tried = e.tried ?? [];
  const when = new Date(e.timestamp);
  const full = Number.isNaN(when.getTime()) ? e.timestamp : when.toLocaleString();
  const reported = tokens.input_tokens !== undefined || tokens.output_tokens !== undefined;
  const lines = decision ? story.rest : story.lines;
  return (
    <Detail title={model} onClose={props.onClose}>
      <div class="request-chips">
        {e.provider && <span class="request-provider">{providerName(e.provider)}</span>}
        {account && <span class="request-account-name">{account}</span>}
        {e.failed ? (
          <WordChip tone="danger">{failureWord(e)}</WordChip>
        ) : (
          <WordChip>{t("succeeded")}</WordChip>
        )}
      </div>
      <dl class="request-tiles" aria-label={t("tiles")}>
        <div>
          <dt>{t("time")}</dt>
          <dd>{took}</dd>
        </div>
        <div>
          <dt>{t("tokens")}</dt>
          <dd>
            {reported
              ? t("tokensTo", {
                  input: shortCount(tokens.input_tokens ?? 0),
                  output: shortCount(tokens.output_tokens ?? 0),
                })
              : "—"}
          </dd>
        </div>
        <div>
          <dt>{e.chain_step && e.chain_step > 1 ? t("step") : t("tries")}</dt>
          <dd>
            {e.chain_step && e.chain_step > 1 ? e.chain_step : (e.attempts ?? Math.max(1, tried.length))}
          </dd>
        </div>
      </dl>
      <div class="request-story" data-part="request-story">
        {decision && <p class="request-decision">{decision}</p>}
        {lines.map((line) => (
          <p key={line}>
            {line}
            {story.noServe && line === lines[lines.length - 1] && (
              <>
                {" "}
                <a href="#/accounts">{t("accounts")}</a>
              </>
            )}
          </p>
        ))}
        {story.leftOut.length > 0 && tried.length > 0 && <p>{t("notTried")}</p>}
        {story.leftOut.length > 0 && (
          <ul class="request-left-out">
            {story.leftOut.map((l, i) => (
              <li key={i}>
                {l.account}: {l.text}
              </li>
            ))}
          </ul>
        )}
        {story.reason && (
          <p class="request-reason">
            {story.reason.text}
            {story.reason.order && (
              <>
                {" "}
                <a href="#/routing">{t("order")}</a>
              </>
            )}
          </p>
        )}
      </div>
      {tried.length > 1 && (
        <div class="request-attempts">
          <h3>{t("attempts")}</h3>
          <ol>
            {tried.map((x, i) => (
              <li key={i} data-result={x.result === "ok" ? "ok" : "failed"}>
                <span class="attempt-who">
                  <a href={accountHref(x.account)}>{name(x.account)}</a>
                  <span class="request-attempt-provider">
                    {providerName(x.provider)}
                    {x.model && (
                      <>
                        {" · "}
                        <span class="request-attempt-model mono">{p.text(x.model)}</span>
                      </>
                    )}
                  </span>
                </span>
                <span class="request-attempt-result" data-result={x.result === "ok" ? "ok" : "failed"}>
                  {x.result === "ok" ? t("succeeded") : failureWord(x.result, e.status)}
                </span>
              </li>
            ))}
          </ol>
        </div>
      )}
      <Facts
        rows={[
          [t("when"), full],
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
            reported
              ? t("tokenCounts", {
                  input: n(tokens.input_tokens),
                  output: n(tokens.output_tokens),
                  cached: n(tokens.cached_tokens),
                })
              : undefined,
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
      {story.next?.kind === "account" && (
        <p class="request-next">
          <a
            class="btn"
            data-part="button"
            data-slot="button"
            data-variant="primary"
            href={accountHref(props.idOf(story.next.account))}
          >
            <span class="btn-label">{t("openAccount", { account: name(story.next.account) })}</span>
          </a>
        </p>
      )}
      {story.next?.kind === "log" && e.id && (
        <p class="request-next">
          <a
            class="btn"
            data-part="button"
            data-slot="button"
            data-variant="primary"
            href={`#/logs/${encodeURIComponent(e.id)}`}
          >
            <span class="btn-label">{t("openLog")}</span>
          </a>
        </p>
      )}
      {!e.failed && e.account && (
        <p class="request-next">
          <a
            class="btn"
            data-part="button"
            data-slot="button"
            data-variant="quiet"
            href={accountHref(props.idOf(e.account))}
          >
            <span class="btn-label">{t("openAccount", { account: name(e.account) })}</span>
          </a>
        </p>
      )}
    </Detail>
  );
}

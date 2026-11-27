// PoolCard: one provider's pool on Home. Its mark and name and the limit it counts; the share left
// on average over the accounts that can serve, in text ink until the theme's meter thresholds
// make it amber or red; a bar with one text-free segment per account (hatched for one that cannot
// serve now); how many serve; and what comes back next. A provider that reports a balance in
// place of a limit shows the balance with no bar; one that reports neither says so in a sentence.

import type { ComponentChildren } from "preact";
import { meterTone, useThresholds } from "../meter";
import { strings } from "../strings";
import { shortDuration } from "../time";
import type { Pool } from "./pool";
import { ProviderMark, providerId } from "./providerMark";

const t = strings("parts.poolCard", {
  percent: "{percent}%",
  left: "left",
  notReported: "Not reported by {provider}",
  nothingServing: "Nothing is serving",
  servingOf: "{serving} of {total} serving",
  back: "+{gain}% back in {duration}",
  firstBack: "The first is back in {duration}",
  balance: "Balance",
});

const money = (n: number) =>
  n.toLocaleString(undefined, { style: "currency", currency: "USD", maximumFractionDigits: 2 });

export function PoolCard(props: { pool: Pool }) {
  const p = props.pool;
  const thresholds = useThresholds();
  const tone = p.left === undefined ? undefined : meterTone(p.left, thresholds);
  const nothing = p.serving === 0;
  let hero: ComponentChildren;
  if (nothing) {
    hero = (
      <span class="pool-sentence" data-tone="danger">
        {t("nothingServing")}
      </span>
    );
  } else if (p.left !== undefined) {
    hero = (
      <>
        <span class="pool-number num" data-tone={tone}>
          {t("percent", { percent: p.left })}
        </span>{" "}
        <span class="pool-unit">{t("left")}</span>
      </>
    );
  } else if (p.balance !== undefined) {
    hero = (
      <>
        <span class="pool-number num">{money(p.balance)}</span> <span class="pool-unit">{t("left")}</span>
      </>
    );
  } else {
    hero = <span class="pool-sentence">{t("notReported", { provider: p.provider })}</span>;
  }
  const after = p.back
    ? t("back", { gain: p.back.gain, duration: shortDuration(p.back.in / 1000) })
    : nothing && p.firstBack !== undefined
      ? t("firstBack", { duration: shortDuration(p.firstBack / 1000) })
      : undefined;
  return (
    <section
      class="pool-card"
      data-part="pool-card"
      data-provider={providerId(p.extension)}
      aria-label={p.provider}
    >
      <header class="pool-head">
        <ProviderMark extension={p.extension} />
        <h3 class="pool-name">{p.provider}</h3>
        <span class="pool-window">{p.window ?? (p.balance !== undefined ? t("balance") : "")}</span>
      </header>
      <p class="pool-hero">{hero}</p>
      {p.balance === undefined || p.left !== undefined ? (
        <div class="pool-bar" aria-hidden="true">
          {p.segments.map((s) => (
            <span
              key={s.id}
              class="pool-segment"
              data-kind={s.kind}
              data-tone={s.kind === "left" ? meterTone(s.left as number, thresholds) : undefined}
            >
              {s.kind === "left" && <i style={{ width: `${s.left}%` }} />}
            </span>
          ))}
        </div>
      ) : null}
      <p class="pool-foot">
        <span>{t("servingOf", { serving: p.serving, total: p.total })}</span>
        {after && <span class="pool-back">{after}</span>}
      </p>
    </section>
  );
}

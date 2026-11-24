// Requests over time, as bars: one per step of the range, the failed part hatched on top. One
// axis and one measure at a time (requests or tokens). Each bar answers to the pointer with its
// numbers, and the same numbers are a table for a screen reader.

import { useState } from "preact/hooks";
import type { UsageBucket } from "../../api";
import { compact, count } from "./lib";

export type Measure = "requests" | "tokens";

/** A round number at or above peak, for the top of the axis. */
function axisTop(peak: number): number {
  if (peak <= 4) return 4;
  const pow = 10 ** Math.floor(Math.log10(peak));
  for (const m of [1, 2, 2.5, 4, 5, 8, 10]) if (m * pow >= peak) return m * pow;
  return 10 * pow;
}

function stepLabel(start: string, bucketSeconds: number): string {
  const d = new Date(start);
  if (Number.isNaN(d.getTime())) return "";
  if (bucketSeconds >= 86400) return d.toLocaleDateString(undefined, { month: "short", day: "numeric" });
  if (bucketSeconds >= 3600 * 6) {
    return d.toLocaleString(undefined, { month: "short", day: "numeric", hour: "numeric" });
  }
  return d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
}

export function SeriesChart(props: {
  series: UsageBucket[];
  bucketSeconds: number;
  measure: Measure;
  height?: number;
  label: string;
}) {
  const { series, bucketSeconds, measure } = props;
  const [at, setAt] = useState<number>();
  const H = props.height ?? 168;
  const W = 720;
  const left = 34;
  const top = 8;
  const bottom = 20;
  const plotH = H - top - bottom;
  const plotW = W - left;
  const value = (b: UsageBucket) => (measure === "requests" ? b.requests : b.total_tokens);
  const peak = Math.max(0, ...series.map(value));
  const max = axisTop(peak);
  const n = Math.max(1, series.length);
  const slot = plotW / n;
  const barW = Math.max(2, Math.min(26, slot - 2));
  const y = (v: number) => top + plotH - (v / max) * plotH;
  const ticks = [0, max / 2, max];
  const marks = n <= 1 ? [0] : [0, Math.floor((n - 1) / 2), n - 1];
  const shown = at !== undefined ? series[at] : undefined;
  return (
    <figure class="dc-chart">
      <svg
        viewBox={`0 0 ${W} ${H}`}
        role="img"
        aria-label={`${props.label}. The numbers are in the table that follows.`}
        onMouseLeave={() => setAt(undefined)}
      >
        {ticks.map((t) => (
          <g key={t}>
            <line class="dc-chart-grid" x1={left} x2={W} y1={y(t)} y2={y(t)} />
            <text class="dc-chart-tick" x={left - 6} y={y(t) + 4} text-anchor="end">
              {compact(t)}
            </text>
          </g>
        ))}
        {series.map((b, i) => {
          const v = value(b);
          const failed = measure === "requests" ? b.failure_count : 0;
          const x = left + i * slot + (slot - barW) / 2;
          const okTop = y(v - failed);
          const allTop = y(v);
          // A step with requests always shows: at least two pixels.
          const okH = v - failed > 0 ? Math.max(2, top + plotH - okTop) : 0;
          const failH = failed > 0 ? Math.max(2, okTop - allTop) : 0;
          return (
            <g key={b.start} data-at={at === i ? "" : undefined} class="dc-chart-step">
              {okH > 0 && (
                <rect class="dc-chart-bar" x={x} y={top + plotH - okH} width={barW} height={okH} rx={2} />
              )}
              {failH > 0 && (
                <rect
                  class="dc-chart-failed"
                  x={x}
                  y={top + plotH - okH - failH - (okH > 0 ? 2 : 0)}
                  width={barW}
                  height={failH}
                  rx={2}
                />
              )}
              {/* biome-ignore lint/a11y/noStaticElementInteractions: the pointer's target for one step; the table carries the numbers */}
              <rect
                class="dc-chart-hit"
                x={left + i * slot}
                y={top}
                width={slot}
                height={plotH}
                onMouseEnter={() => setAt(i)}
              />
            </g>
          );
        })}
        {marks.map((i) => {
          const b = series[i];
          if (!b) return null;
          return (
            <text
              key={i}
              class="dc-chart-tick"
              x={left + i * slot + slot / 2}
              y={H - 5}
              text-anchor={i === 0 ? "start" : i === n - 1 ? "end" : "middle"}
            >
              {stepLabel(b.start, bucketSeconds)}
            </text>
          );
        })}
      </svg>
      <figcaption class="dc-chart-read" aria-hidden="true">
        {shown ? (
          <>
            <strong>{stepLabel(shown.start, bucketSeconds)}</strong>
            <span class="dc-num">{count(shown.requests)} requests</span>
            {shown.failure_count > 0 && (
              <span class="dc-num dc-chart-read-failed">{count(shown.failure_count)} failed</span>
            )}
            <span class="dc-num">{compact(shown.total_tokens)} tokens</span>
          </>
        ) : (
          <>
            <span class="dc-legend">
              <svg width="12" height="12" aria-hidden="true" focusable="false">
                <rect width="12" height="12" rx="2" class="dc-chart-bar" />
              </svg>
              {measure === "requests" ? "Answered" : "Tokens"}
            </span>
            {measure === "requests" && (
              <span class="dc-legend">
                <svg width="12" height="12" aria-hidden="true" focusable="false">
                  <rect width="12" height="12" rx="2" class="dc-chart-failed" />
                </svg>
                Failed
              </span>
            )}
            <span class="dc-muted">Peak {compact(peak)} a step</span>
          </>
        )}
      </figcaption>
      <table class="dc-sr">
        <caption>{props.label}</caption>
        <thead>
          <tr>
            <th scope="col">From</th>
            <th scope="col">Requests</th>
            <th scope="col">Failed</th>
            <th scope="col">Tokens</th>
          </tr>
        </thead>
        <tbody>
          {series.map((b) => (
            <tr key={b.start}>
              <th scope="row">{stepLabel(b.start, bucketSeconds)}</th>
              <td>{b.requests}</td>
              <td>{b.failure_count}</td>
              <td>{b.total_tokens}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </figure>
  );
}

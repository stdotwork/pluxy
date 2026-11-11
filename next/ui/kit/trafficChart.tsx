// Requests over time as stacked bars of success and failure, with the same numbers as a table
// beside them. The arithmetic is plain functions so tests can check exact values.

import type { UsageBucket } from "./api";
import { strings } from "./strings";

const t = strings("trafficChart", {
  summary: "{total} requests over the last {minutes} minutes: {success} succeeded, {failed} failed.",
  bucket: "{time}: {success} succeeded, {failed} failed",
  empty: "No traffic recorded yet. Counters appear once the proxy has served its first request.",
  tableLabel: "Requests in each 10-minute bucket",
  window: "Window",
  success: "Success",
  failure: "Failure",
});

export interface BucketRow {
  start: string;
  success: number;
  failure: number;
  total: number;
}

/** Each step of core's series as its successes and failures, in core's order (oldest first). */
export function bucketRows(series: UsageBucket[]): BucketRow[] {
  return series.map((b) => ({
    start: b.start,
    success: b.requests - b.failure_count,
    failure: b.failure_count,
    total: b.requests,
  }));
}

/** Rounds up to a value on a 1, 1.5, 2, 2.5, 3, 4, 5, 6, 8 ladder times a power of ten. */
function niceCeil(value: number): number {
  if (value <= 0) return 1;
  const magnitude = 10 ** Math.floor(Math.log10(value));
  const normalized = value / magnitude;
  const step = [1, 1.5, 2, 2.5, 3, 4, 5, 6, 8, 10].find((s) => normalized <= s) ?? 10;
  return step * magnitude;
}

/** The chart's top: `intervals` equal whole-number steps, the smallest that reach `peak`. */
export function axisMax(peak: number, intervals: number): number {
  if (peak <= 0 || intervals <= 0) return Math.max(1, intervals);
  return Math.max(1, Math.ceil(niceCeil(peak / intervals))) * intervals;
}

/** The share of requests that succeeded, to one decimal and rounded down, so only all of them is 100%. */
export function successPercent(success: number, total: number): string | undefined {
  if (total <= 0) return undefined;
  return `${Math.floor((success * 1000) / total) / 10}%`;
}

const clock = (start: string) =>
  new Date(start).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });

const W = 640;
const H = 200;
const LEFT = 36;
const TOP = 8;
const BOTTOM = 22;
const GAP = 2;
const INTERVALS = 4;

export function TrafficChart(props: { rows: BucketRow[] }) {
  const { rows } = props;
  const total = rows.reduce((n, r) => n + r.total, 0);
  if (total === 0) return <p class="empty">{t("empty")}</p>;

  const failed = rows.reduce((n, r) => n + r.failure, 0);
  const max = axisMax(Math.max(...rows.map((r) => r.total)), INTERVALS);
  const plotW = W - LEFT;
  const plotH = H - TOP - BOTTOM;
  const slot = plotW / rows.length;
  const barW = slot * 0.7;
  const y = (n: number) => TOP + plotH - (n / max) * plotH;
  const minutes = rows.length * 10;
  const labelAt = [0, Math.floor((rows.length - 1) / 2), rows.length - 1];

  return (
    <div class="traffic">
      <div class="traffic-chart">
        <svg
          data-part="chart"
          viewBox={`0 0 ${W} ${H}`}
          role="img"
          aria-label={t("summary", { total, minutes, success: total - failed, failed })}
        >
          {Array.from({ length: INTERVALS + 1 }, (_, i) => {
            const v = (max / INTERVALS) * i;
            return (
              <g key={v}>
                <line class="chart-grid" x1={LEFT} x2={W} y1={y(v)} y2={y(v)} />
                <text class="chart-label" x={LEFT - 6} y={y(v)} text-anchor="end" dominant-baseline="middle">
                  {v}
                </text>
              </g>
            );
          })}
          {rows.map((r, i) => {
            const x = LEFT + i * slot + (slot - barW) / 2;
            const failTop = y(r.total);
            const successTop = y(r.success);
            const time = clock(r.start);
            return (
              <g key={r.start}>
                <title>{t("bucket", { time, success: r.success, failed: r.failure })}</title>
                <rect class="chart-hit" x={LEFT + i * slot} y={TOP} width={slot} height={plotH} />
                {r.success > 0 && (
                  <rect data-outcome="success" x={x} y={successTop} width={barW} height={y(0) - successTop} />
                )}
                {r.failure > 0 && (
                  <rect
                    data-outcome="failure"
                    x={x}
                    y={failTop}
                    width={barW}
                    height={Math.max(1, successTop - failTop - (r.success > 0 ? GAP : 0))}
                  />
                )}
              </g>
            );
          })}
          {labelAt.map((i, n) => (
            <text
              key={i}
              class="chart-label"
              x={LEFT + i * slot + slot / 2}
              y={H - 6}
              text-anchor={n === 0 ? "start" : n === 2 ? "end" : "middle"}
            >
              {clock(rows[i]?.start ?? "")}
            </text>
          ))}
        </svg>
        <p class="chart-legend">
          <span>
            <i class="chart-swatch" data-outcome="success" aria-hidden="true" />
            {t("success")}
          </span>
          <span>
            <i class="chart-swatch" data-outcome="failure" aria-hidden="true" />
            {t("failure")}
          </span>
        </p>
      </div>
      <div class="table-wrap traffic-table">
        <table class="table" aria-label={t("tableLabel")}>
          <thead>
            <tr>
              <th scope="col">{t("window")}</th>
              <th scope="col">{t("success")}</th>
              <th scope="col">{t("failure")}</th>
            </tr>
          </thead>
          <tbody>
            {[...rows].reverse().map((r) => (
              <tr key={r.start}>
                <th scope="row">
                  <time dateTime={r.start}>{clock(r.start)}</time>
                </th>
                <td>{r.success}</td>
                <td>{r.failure}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

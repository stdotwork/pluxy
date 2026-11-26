// The Usage tab's chart: one bar per hour or per local day, the requests that succeeded in ink and
// the ones that failed on top in the danger tone. A bar says its figures when hovered or focused;
// the arrow keys move between bars, so the chart is one stop for the keyboard, not thirty.

import { useRef, useState } from "preact/hooks";
import { strings } from "../strings";
import { Button, TableFrame } from "../ui";
import type { Bar } from "./activity";

const t = strings("usage.chart", {
  succeeded: "Succeeded",
  failed: "Failed",
  figures: "{day}: {requests} requests, {failed} failed",
  figuresOne: "{day}: 1 request, {failed} failed",
  sum: "{requests} requests in the last {range}, {failed} failed",
  sumOne: "1 request in the last {range}, {failed} failed",
  today: "Today",
  showNumbers: "Show the numbers",
  hideNumbers: "Hide the numbers",
  day: "Day",
  hour: "Hour",
  requests: "Requests",
  tokens: "Tokens",
});

const W = 600;
const H = 188;
const LEFT = 34;
const TOP = 14;
const BOTTOM = 22;
const GAP = 2;

const dayName = (d: Date) =>
  d.toLocaleDateString(undefined, { weekday: "short", day: "numeric", month: "short" });
const clock = (d: Date) =>
  d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit", hourCycle: "h23" });

/** A bar's name: Wed 25 Nov for a day, Wed 25 Nov, 14:00 for an hour. */
export const barName = (bar: Bar, unit: "hour" | "day") =>
  unit === "hour" ? `${dayName(bar.start)}, ${clock(bar.start)}` : dayName(bar.start);

/** What hovering or focusing a bar says: Wed 25 Nov: 138 requests, 9 failed. */
export const barFigures = (bar: Bar, unit: "hour" | "day") =>
  bar.requests === 1
    ? t("figuresOne", { day: barName(bar, unit), failed: bar.failed })
    : t("figures", { day: barName(bar, unit), requests: bar.requests, failed: bar.failed });

/** The label under a bar, or nothing when the bar goes without one. */
export function barLabel(bar: Bar, index: number, count: number, unit: "hour" | "day"): string | undefined {
  const fromEnd = count - 1 - index;
  if (unit === "hour") return bar.start.getHours() % 6 === 0 ? clock(bar.start) : undefined;
  if (fromEnd === 0) return t("today");
  if (count <= 7) return bar.start.toLocaleDateString(undefined, { weekday: "short" });
  return fromEnd % 7 === 0
    ? bar.start.toLocaleDateString(undefined, { day: "numeric", month: "short" })
    : undefined;
}

export function UsageChart(props: { bars: Bar[]; unit: "hour" | "day"; range: string }) {
  const { bars, unit } = props;
  const [shown, setShown] = useState<number>();
  const [numbers, setNumbers] = useState(false);
  const [stop, setStop] = useState(bars.length - 1);
  const refs = useRef<(SVGGElement | null)[]>([]);
  const requests = bars.reduce((n, b) => n + b.requests, 0);
  const failed = bars.reduce((n, b) => n + b.failed, 0);
  const peak = Math.max(1, ...bars.map((b) => b.requests));
  const plotH = H - TOP - BOTTOM;
  const slot = (W - LEFT) / bars.length;
  const barW = Math.min(slot * 0.7, 28);
  const y = (n: number) => TOP + plotH - (n / peak) * plotH;
  const sum =
    requests === 1
      ? t("sumOne", { range: props.range, failed })
      : t("sum", { requests, range: props.range, failed });
  const move = (to: number) => {
    const i = Math.max(0, Math.min(bars.length - 1, to));
    setStop(i);
    refs.current[i]?.focus();
  };
  const active = shown === undefined ? undefined : bars[shown];
  return (
    <div class="usage-chart" data-part="usage-chart">
      {/* biome-ignore lint/a11y/useSemanticElements: an SVG cannot be a fieldset; the group names the whole chart */}
      <svg viewBox={`0 0 ${W} ${H}`} role="group" aria-label={sum} onMouseLeave={() => setShown(undefined)}>
        <line class="chart-grid" x1={LEFT} x2={W} y1={y(peak)} y2={y(peak)} />
        <line class="chart-axis" x1={LEFT} x2={W} y1={y(0)} y2={y(0)} />
        <text class="chart-label" x={LEFT - 6} y={y(peak)} text-anchor="end" dominant-baseline="middle">
          {peak.toLocaleString()}
        </text>
        {bars.map((b, i) => {
          const x = LEFT + i * slot + (slot - barW) / 2;
          const ok = b.requests - b.failed;
          const okTop = y(ok);
          const failTop = y(b.requests);
          const label = barLabel(b, i, bars.length, unit);
          const text = barFigures(b, unit);
          return (
            // biome-ignore lint/a11y/noInteractiveElementToNoninteractiveRole: a bar is read, not pressed; it takes focus so the keyboard hears its figures, as chart libraries do
            <g
              key={b.start.getTime()}
              class="chart-bar"
              ref={(el) => {
                refs.current[i] = el;
              }}
              role="img"
              aria-label={text}
              tabIndex={i === stop ? 0 : -1}
              onMouseEnter={() => setShown(i)}
              onFocus={() => {
                setStop(i);
                setShown(i);
              }}
              onBlur={() => setShown(undefined)}
              onKeyDown={(e) => {
                const to =
                  e.key === "ArrowRight"
                    ? i + 1
                    : e.key === "ArrowLeft"
                      ? i - 1
                      : e.key === "Home"
                        ? 0
                        : e.key === "End"
                          ? bars.length - 1
                          : undefined;
                if (to === undefined) return;
                e.preventDefault();
                move(to);
              }}
            >
              <title>{text}</title>
              <rect class="chart-hit" x={LEFT + i * slot} y={TOP} width={slot} height={plotH} />
              {ok > 0 && <rect data-outcome="success" x={x} y={okTop} width={barW} height={y(0) - okTop} />}
              {b.failed > 0 && (
                <rect
                  data-outcome="failure"
                  x={x}
                  y={failTop}
                  width={barW}
                  height={Math.max(1, okTop - failTop - (ok > 0 ? GAP : 0))}
                />
              )}
              {label && (
                <text
                  class="chart-label"
                  x={i === bars.length - 1 && i > 0 ? W : x + barW / 2}
                  y={H - 6}
                  // The last label ends at the chart's edge, so a long one (Today) is not cut off.
                  text-anchor={i === bars.length - 1 && i > 0 ? "end" : "middle"}
                >
                  {label}
                </text>
              )}
            </g>
          );
        })}
      </svg>
      <p class="usage-readout" aria-hidden="true">
        {active ? barFigures(active, unit) : sum}
      </p>
      <div class="usage-chart-foot">
        <p class="chart-legend">
          <span>
            <i class="chart-swatch" data-outcome="success" aria-hidden="true" />
            {t("succeeded")}
          </span>
          <span>
            <i class="chart-swatch" data-outcome="failure" aria-hidden="true" />
            {t("failed")}
          </span>
        </p>
        <Button variant="ghost" onClick={() => setNumbers(!numbers)}>
          {numbers ? t("hideNumbers") : t("showNumbers")}
        </Button>
      </div>
      {numbers && (
        <TableFrame>
          <table class="table" data-part="usage-numbers">
            <thead>
              <tr>
                <th scope="col">{unit === "hour" ? t("hour") : t("day")}</th>
                <th scope="col" class="num">
                  {t("requests")}
                </th>
                <th scope="col" class="num">
                  {t("failed")}
                </th>
                <th scope="col" class="num">
                  {t("tokens")}
                </th>
              </tr>
            </thead>
            <tbody>
              {[...bars].reverse().map((b) => (
                <tr key={b.start.getTime()}>
                  <th scope="row">{barName(b, unit)}</th>
                  <td class="num">{b.requests.toLocaleString()}</td>
                  <td class="num">{b.failed.toLocaleString()}</td>
                  <td class="num">{b.tokens.toLocaleString()}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableFrame>
      )}
    </div>
  );
}

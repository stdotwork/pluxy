// The last seven local days as seven bars: what succeeded in the page's text colour with what
// failed on top in the danger tone, the top value as the only figure on the axis, and the day
// names under the bars. A bar says its day on hover and on focus; the chart's name sums them up.

import { useState } from "preact/hooks";
import { chartName, type Day, dayLabel, dayText, weekTotals } from "./home.helpers";

export function Days(props: { days: Day[]; now: Date }) {
  const { days, now } = props;
  const [active, setActive] = useState<number>();
  const top = Math.max(1, ...days.map((d) => d.total));
  const shown = active === undefined ? undefined : days[active];
  return (
    <figure class="days" data-part="days" aria-label={chartName(weekTotals(days))}>
      <div class="days-plot">
        <span class="days-top">{top.toLocaleString()}</span>
        <div class="days-bars">
          {days.map((d, i) => (
            <div
              key={d.start}
              class="day"
              role="img"
              // biome-ignore lint/a11y/noNoninteractiveTabindex: a bar takes focus so its figures show for a keyboard user too
              tabIndex={0}
              aria-label={dayText(d)}
              data-empty={d.total === 0 ? "" : undefined}
              onMouseEnter={() => setActive(i)}
              onMouseLeave={() => setActive(undefined)}
              onFocus={() => setActive(i)}
              onBlur={() => setActive(undefined)}
            >
              <span class="day-bar" style={{ height: `${(d.total / top) * 100}%` }}>
                {d.failed > 0 && <i class="day-failed" style={{ flexGrow: d.failed }} />}
                {d.succeeded > 0 && <i class="day-succeeded" style={{ flexGrow: d.succeeded }} />}
              </span>
            </div>
          ))}
        </div>
      </div>
      <div class="days-names" aria-hidden="true">
        {days.map((d) => (
          <span key={d.start} class="day-name">
            {dayLabel(d, now)}
          </span>
        ))}
      </div>
      <p class="days-readout" aria-hidden="true">
        {shown ? dayText(shown) : ""}
      </p>
    </figure>
  );
}

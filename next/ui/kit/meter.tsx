import { useContext, useEffect, useState } from "preact/hooks";
import { TOKENS } from "./theme/contract";
import { KitContext, type Tone } from "./ui";

/** Where a meter's colour changes, in percent left: under mid it warns, under low it is danger. */
export interface MeterThresholds {
  mid: number;
  low: number;
}

/** The thresholds a theme sets with meter.mid-below and meter.low-below. */
export const DEFAULT_THRESHOLDS: MeterThresholds = {
  mid: Number(TOKENS["meter.mid-below"].light),
  low: Number(TOKENS["meter.low-below"].light),
};

/** A meter's tone by the share left: ok at mid or more, warn under mid, danger under low or when used up. */
export function meterTone(percent: number, thresholds: MeterThresholds = DEFAULT_THRESHOLDS): Tone {
  if (percent <= 0 || percent < thresholds.low) return "danger";
  return percent < thresholds.mid ? "warn" : "ok";
}

/** The active theme's thresholds; the defaults outside the kit's shell. Re-renders on a theme change. */
function useThresholds(): MeterThresholds {
  const kit = useContext(KitContext);
  const themes = kit?.themes;
  const [, bump] = useState(0);
  useEffect(() => themes?.subscribe(() => bump((n) => n + 1)), [themes]);
  const tokens = themes?.resolved.tokens;
  if (!tokens) return DEFAULT_THRESHOLDS;
  return { mid: Number(tokens["meter.mid-below"]), low: Number(tokens["meter.low-below"]) };
}

/**
 * What is left of a limit, as a bar that is full when there is plenty, beside "{n}% left". The
 * label and any amounts are supplied by the caller.
 */
export function Meter(props: { label: string; percent: number; mode?: "left"; valueText?: string }) {
  const percent = Math.max(0, Math.min(100, props.percent));
  const thresholds = useThresholds();
  return (
    // biome-ignore lint/a11y/useSemanticElements: custom fill keeps theme styling consistent in browser and native webviews
    <div
      class="meter"
      data-part="meter"
      data-tone={meterTone(percent, thresholds)}
      data-mode="left"
      role="meter"
      aria-label={props.label}
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={percent}
      aria-valuetext={props.valueText}
    >
      <span aria-hidden="true" style={{ width: `${percent}%` }} />
    </div>
  );
}

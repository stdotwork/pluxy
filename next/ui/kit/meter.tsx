import type { Tone } from "./ui";

/** At or under this share left, a meter warns. */
export const LOW_LEFT_PERCENT = 10;

/** A meter's tone by the share left: warn at LOW_LEFT_PERCENT or less, danger when nothing is left. */
export function meterTone(percent: number): Tone {
  return percent <= 0 ? "danger" : percent <= LOW_LEFT_PERCENT ? "warn" : "ok";
}

/**
 * What is left of a limit, as a bar that is full when there is plenty, beside "{n}% left". The
 * label and any amounts are supplied by the caller.
 */
export function Meter(props: { label: string; percent: number; mode?: "left"; valueText?: string }) {
  const percent = Math.max(0, Math.min(100, props.percent));
  return (
    // biome-ignore lint/a11y/useSemanticElements: custom fill keeps theme styling consistent in browser and native webviews
    <div
      class="meter"
      data-part="meter"
      data-tone={meterTone(percent)}
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

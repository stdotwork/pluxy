import type { Tone } from "./ui";

/** At or under this share left, a "left" meter warns. */
export const LOW_LEFT_PERCENT = 10;

/**
 * A meter's tone. "used" (the default) fills by the share used: warn from 70%, danger from 90%.
 * "left" fills by the share left, so full is plenty: warn at LOW_LEFT_PERCENT or less, danger
 * when nothing is left.
 */
export function meterTone(percent: number, mode: "used" | "left" = "used"): Tone {
  if (mode === "left") return percent <= 0 ? "danger" : percent <= LOW_LEFT_PERCENT ? "warn" : "ok";
  return percent >= 90 ? "danger" : percent >= 70 ? "warn" : "ok";
}

/**
 * A share, as a bar; the label and any amounts are supplied by the caller. mode says what the
 * share is: used (the default), or left, as a limit's gauge shows it beside "{n}% left".
 */
export function Meter(props: { label: string; percent: number; mode?: "used" | "left"; valueText?: string }) {
  const percent = Math.max(0, Math.min(100, props.percent));
  const mode = props.mode ?? "used";
  return (
    // biome-ignore lint/a11y/useSemanticElements: custom fill keeps theme styling consistent in browser and native webviews
    <div
      class="meter"
      data-part="meter"
      data-tone={meterTone(percent, mode)}
      data-mode={mode}
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

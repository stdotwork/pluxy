import type { Tone } from "./ui";

/** A share used; the label and any amounts are supplied by the caller. */
export function Meter(props: { label: string; percent: number }) {
  const percent = Math.max(0, Math.min(100, props.percent));
  const tone: Tone = percent >= 90 ? "danger" : percent >= 70 ? "warn" : "ok";
  return (
    // biome-ignore lint/a11y/useSemanticElements: custom fill keeps theme styling consistent in browser and native webviews
    <div
      class="meter"
      data-part="meter"
      data-tone={tone}
      role="meter"
      aria-label={props.label}
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={percent}
    >
      <span aria-hidden="true" style={{ width: `${percent}%` }} />
    </div>
  );
}

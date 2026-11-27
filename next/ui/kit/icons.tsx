// The kit's icons: line drawings on a 16 unit grid, one line weight, no fill, in the text's
// colour. An icon marks a destination or a small action beside its word; it never stands alone
// without an accessible name on the control that holds it.

const ICONS = {
  home: "M2.5 7.5 8 2.5l5.5 5v6H10v-4H6v4H2.5z",
  accounts:
    "M8.25 5.25a2.5 2.5 0 1 1-5 0 2.5 2.5 0 0 1 5 0zM1.5 13.25c.5-2.1 2.1-3.25 4.25-3.25S9.5 11.15 10 13.25M10.75 2.9a2.5 2.5 0 0 1 0 4.7M12 10.25c1.3.45 2.15 1.5 2.5 3",
  activity: "M1.5 8H4l2-5 4 10 2-5h2.5",
  tools:
    "M2.5 3h11a1 1 0 0 1 1 1v8a1 1 0 0 1-1 1h-11a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1zM4.5 6.5l2 1.5-2 1.5M8.5 9.5H11",
  settings: "M2 5h6.5M11.5 5H14M2 11h2.5M7.5 11H14M10 3.25v3.5M6 9.25v3.5",
  copy: "M5.5 5.5h8v8h-8zM10.5 5.5v-3h-8v8h3",
  check: "M3 8.5 6.5 12 13 4.5",
  eye: "M1.5 8S3.9 3.5 8 3.5 14.5 8 14.5 8 12.1 12.5 8 12.5 1.5 8 1.5 8zM10 8a2 2 0 1 1-4 0 2 2 0 0 1 4 0z",
  eyeOff:
    "M6.4 3.7A6 6 0 0 1 8 3.5c4.1 0 6.5 4.5 6.5 4.5a11 11 0 0 1-1.6 2.1M11 11.6a5.9 5.9 0 0 1-3 .9C3.9 12.5 1.5 8 1.5 8a10.6 10.6 0 0 1 2.9-3.3M2.5 2.5l11 11",
  arrow: "M3 8h10M9 4l4 4-4 4",
  // An account's group, one shape each, so the groups differ without their colour: a triangle
  // with a mark (needs you), an hourglass (waiting), a ticked circle (serving), a circle with a
  // bar (turned off).
  needsYou: "M8 2 14.5 13.5h-13zM8 6.5v3M8 11.5v.01",
  waiting: "M4.5 2h7M4.5 14h7M5 2c0 3.5 6 4 6 6s-6 2.5-6 6M11 2c0 3.5-6 4-6 6s6 2.5 6 6",
  serving: "M14.5 8a6.5 6.5 0 1 1-13 0 6.5 6.5 0 0 1 13 0zM5.25 8.25l2 2 3.5-4",
  off: "M14.5 8a6.5 6.5 0 1 1-13 0 6.5 6.5 0 0 1 13 0zM5 8h6",
  search: "M11.5 7a4.5 4.5 0 1 1-9 0 4.5 4.5 0 0 1 9 0zM10.25 10.25 14 14",
  plus: "M8 3v10M3 8h10",
  refresh: "M13.5 8A5.5 5.5 0 1 1 11.9 4.1M12 1.75V4.5H9.25",
  chevron: "M6 3.5 10.5 8 6 12.5",
  grip: "M6 3.5v.01M6 8v.01M6 12.5v.01M10 3.5v.01M10 8v.01M10 12.5v.01",
  key: "M7 9.5a3.25 3.25 0 1 1-1.6-4.25M6.9 8.1l6.6-.1v2.5M11 8v2",
  // Where pluxy runs: this computer (a screen on its stand), another one (a globe).
  computer: "M2.5 3h11a1 1 0 0 1 1 1v6a1 1 0 0 1-1 1h-11a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1zM6 14h4M8 11v3",
  globe:
    "M14.5 8a6.5 6.5 0 1 1-13 0 6.5 6.5 0 0 1 13 0zM1.5 8h13M8 1.5C6.1 3.5 5.25 5.5 5.25 8S6.1 12.5 8 14.5M8 1.5c1.9 2 2.75 4 2.75 6.5S9.9 12.5 8 14.5",
} as const;

export type IconName = keyof typeof ICONS;

/**
 * An icon, 16px unless size is given. Decoration: the control around it carries the name. With
 * label, it is a picture with that name (role img), for a mark that says a state by itself.
 */
export function Icon(props: { name: IconName; size?: number; label?: string }) {
  const size = props.size ?? 16;
  return (
    <svg
      class="icon"
      data-part="icon"
      viewBox="0 0 16 16"
      width={size}
      height={size}
      fill="none"
      stroke="currentColor"
      stroke-width="1.25"
      stroke-linecap="round"
      stroke-linejoin="round"
      aria-hidden={props.label ? undefined : "true"}
      role={props.label ? "img" : undefined}
      aria-label={props.label}
      focusable="false"
    >
      <path d={ICONS[props.name]} />
    </svg>
  );
}

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
  signOut: "M6.5 2.5h-4v11h4M6.5 8H14M11 5l3 3-3 3",
  arrow: "M3 8h10M9 4l4 4-4 4",
} as const;

export type IconName = keyof typeof ICONS;

/** An icon, 16px unless size is given. Decoration: the control around it carries the name. */
export function Icon(props: { name: IconName; size?: number }) {
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
      aria-hidden="true"
      focusable="false"
    >
      <path d={ICONS[props.name]} />
    </svg>
  );
}

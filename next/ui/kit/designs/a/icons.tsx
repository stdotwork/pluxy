// Direction A's icons: a subset of Lucide (https://lucide.dev), version 1.78.0, ISC License,
// Copyright (c) Lucide Contributors (portions Copyright (c) Cole Bemis, Feather, MIT License).
// Each is the set's own 24-unit drawing with round caps and joins, inlined so the panel loads
// nothing for it. They are all drawn at one stroke: 1.75 units at any size.
//
// Generated from the set's SVG files; to add one, copy its shapes from the same version.

import { h, type VNode } from "preact";

type Shape = [tag: string, attrs: Record<string, string>];

const SHAPES = {
  // gauge
  overview: [["path", {"d": "m12 14 4-4"}], ["path", {"d": "M3.34 19a10 10 0 1 1 17.32 0"}]],
  // users
  accounts: [["path", {"d": "M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2"}], ["path", {"d": "M16 3.128a4 4 0 0 1 0 7.744"}], ["path", {"d": "M22 21v-2a4 4 0 0 0-3-3.87"}], ["circle", {"cx": "9", "cy": "7", "r": "4"}]],
  // arrow-left-right
  requests: [["path", {"d": "M8 3 4 7l4 4"}], ["path", {"d": "M4 7h16"}], ["path", {"d": "m16 21 4-4-4-4"}], ["path", {"d": "M20 17H4"}]],
  // split
  routing: [["path", {"d": "M16 3h5v5"}], ["path", {"d": "M8 3H3v5"}], ["path", {"d": "M12 22v-8.3a4 4 0 0 0-1.172-2.872L3 3"}], ["path", {"d": "m15 9 6-6"}]],
  // settings
  settings: [["path", {"d": "M9.671 4.136a2.34 2.34 0 0 1 4.659 0 2.34 2.34 0 0 0 3.319 1.915 2.34 2.34 0 0 1 2.33 4.033 2.34 2.34 0 0 0 0 3.831 2.34 2.34 0 0 1-2.33 4.033 2.34 2.34 0 0 0-3.319 1.915 2.34 2.34 0 0 1-4.659 0 2.34 2.34 0 0 0-3.32-1.915 2.34 2.34 0 0 1-2.33-4.033 2.34 2.34 0 0 0 0-3.831A2.34 2.34 0 0 1 6.35 6.051a2.34 2.34 0 0 0 3.319-1.915"}], ["circle", {"cx": "12", "cy": "12", "r": "3"}]],
  // command
  command: [["path", {"d": "M15 6v12a3 3 0 1 0 3-3H6a3 3 0 1 0 3 3V6a3 3 0 1 0-3 3h12a3 3 0 1 0-3-3"}]],
  // eye
  eye: [["path", {"d": "M2.062 12.348a1 1 0 0 1 0-.696 10.75 10.75 0 0 1 19.876 0 1 1 0 0 1 0 .696 10.75 10.75 0 0 1-19.876 0"}], ["circle", {"cx": "12", "cy": "12", "r": "3"}]],
  // eye-off
  eyeOff: [["path", {"d": "M10.733 5.076a10.744 10.744 0 0 1 11.205 6.575 1 1 0 0 1 0 .696 10.747 10.747 0 0 1-1.444 2.49"}], ["path", {"d": "M14.084 14.158a3 3 0 0 1-4.242-4.242"}], ["path", {"d": "M17.479 17.499a10.75 10.75 0 0 1-15.417-5.151 1 1 0 0 1 0-.696 10.75 10.75 0 0 1 4.446-5.143"}], ["path", {"d": "m2 2 20 20"}]],
  // sun
  sun: [["circle", {"cx": "12", "cy": "12", "r": "4"}], ["path", {"d": "M12 2v2"}], ["path", {"d": "M12 20v2"}], ["path", {"d": "m4.93 4.93 1.41 1.41"}], ["path", {"d": "m17.66 17.66 1.41 1.41"}], ["path", {"d": "M2 12h2"}], ["path", {"d": "M20 12h2"}], ["path", {"d": "m6.34 17.66-1.41 1.41"}], ["path", {"d": "m19.07 4.93-1.41 1.41"}]],
  // moon
  moon: [["path", {"d": "M20.985 12.486a9 9 0 1 1-9.473-9.472c.405-.022.617.46.402.803a6 6 0 0 0 8.268 8.268c.344-.215.825-.004.803.401"}]],
  // monitor
  monitor: [["rect", {"width": "20", "height": "14", "x": "2", "y": "3", "rx": "2"}], ["line", {"x1": "8", "x2": "16", "y1": "21", "y2": "21"}], ["line", {"x1": "12", "x2": "12", "y1": "17", "y2": "21"}]],
  // log-out
  signOut: [["path", {"d": "m16 17 5-5-5-5"}], ["path", {"d": "M21 12H9"}], ["path", {"d": "M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4"}]],
  // refresh-cw
  refresh: [["path", {"d": "M3 12a9 9 0 0 1 9-9 9.75 9.75 0 0 1 6.74 2.74L21 8"}], ["path", {"d": "M21 3v5h-5"}], ["path", {"d": "M21 12a9 9 0 0 1-9 9 9.75 9.75 0 0 1-6.74-2.74L3 16"}], ["path", {"d": "M8 16H3v5"}]],
  // plus
  plus: [["path", {"d": "M5 12h14"}], ["path", {"d": "M12 5v14"}]],
  // x
  close: [["path", {"d": "M18 6 6 18"}], ["path", {"d": "m6 6 12 12"}]],
  // chevron-right
  chevronRight: [["path", {"d": "m9 18 6-6-6-6"}]],
  // chevron-down
  chevronDown: [["path", {"d": "m6 9 6 6 6-6"}]],
  // corner-down-right
  fallback: [["path", {"d": "m15 10 5 5-5 5"}], ["path", {"d": "M4 4v7a4 4 0 0 0 4 4h12"}]],
  // search
  search: [["path", {"d": "m21 21-4.34-4.34"}], ["circle", {"cx": "11", "cy": "11", "r": "8"}]],
  // arrow-right
  arrowRight: [["path", {"d": "M5 12h14"}], ["path", {"d": "m12 5 7 7-7 7"}]],
  // check
  check: [["path", {"d": "M20 6 9 17l-5-5"}]],
  // copy
  copy: [["rect", {"width": "14", "height": "14", "x": "8", "y": "8", "rx": "2", "ry": "2"}], ["path", {"d": "M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2"}]],
  // triangle-alert
  warning: [["path", {"d": "m21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.73-3"}], ["path", {"d": "M12 9v4"}], ["path", {"d": "M12 17h.01"}]],
  // circle-alert
  alert: [["circle", {"cx": "12", "cy": "12", "r": "10"}], ["line", {"x1": "12", "x2": "12", "y1": "8", "y2": "12"}], ["line", {"x1": "12", "x2": "12.01", "y1": "16", "y2": "16"}]],
  // external-link
  external: [["path", {"d": "M15 3h6v6"}], ["path", {"d": "M10 14 21 3"}], ["path", {"d": "M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6"}]],
  // keyboard
  keyboard: [["path", {"d": "M10 8h.01"}], ["path", {"d": "M12 12h.01"}], ["path", {"d": "M14 8h.01"}], ["path", {"d": "M16 12h.01"}], ["path", {"d": "M18 8h.01"}], ["path", {"d": "M6 8h.01"}], ["path", {"d": "M7 16h10"}], ["path", {"d": "M8 12h.01"}], ["rect", {"width": "20", "height": "16", "x": "2", "y": "4", "rx": "2"}]],
  // filter
  filter: [["path", {"d": "M10 20a1 1 0 0 0 .553.895l2 1A1 1 0 0 0 14 21v-7a2 2 0 0 1 .517-1.341L21.74 4.67A1 1 0 0 0 21 3H3a1 1 0 0 0-.742 1.67l7.225 7.989A2 2 0 0 1 10 14z"}]],
  // trash-2
  trash: [["path", {"d": "M10 11v6"}], ["path", {"d": "M14 11v6"}], ["path", {"d": "M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6"}], ["path", {"d": "M3 6h18"}], ["path", {"d": "M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"}]],
  // download
  download: [["path", {"d": "M12 15V3"}], ["path", {"d": "M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"}], ["path", {"d": "m7 10 5 5 5-5"}]],
  // clock
  clock: [["circle", {"cx": "12", "cy": "12", "r": "10"}], ["path", {"d": "M12 6v6l4 2"}]],
  // arrow-up-right
  open: [["path", {"d": "M7 7h10v10"}], ["path", {"d": "M7 17 17 7"}]],
  // ellipsis
  more: [["circle", {"cx": "12", "cy": "12", "r": "1"}], ["circle", {"cx": "19", "cy": "12", "r": "1"}], ["circle", {"cx": "5", "cy": "12", "r": "1"}]],
  // circle-check
  serving: [["circle", {"cx": "12", "cy": "12", "r": "10"}], ["path", {"d": "m16 9-5.5 5.5L8 12"}]],
  // timer
  cooling: [["line", {"x1": "10", "x2": "14", "y1": "2", "y2": "2"}], ["line", {"x1": "12", "x2": "15", "y1": "14", "y2": "11"}], ["circle", {"cx": "12", "cy": "14", "r": "8"}]],
  // hourglass
  exhausted: [["path", {"d": "M5 22h14"}], ["path", {"d": "M5 2h14"}], ["path", {"d": "M17 22v-4.172a2 2 0 0 0-.586-1.414L12 12l-4.414 4.414A2 2 0 0 0 7 17.828V22"}], ["path", {"d": "M7 2v4.172a2 2 0 0 0 .586 1.414L12 12l4.414-4.414A2 2 0 0 0 17 6.172V2"}]],
  // log-in
  signIn: [["path", {"d": "m10 17 5-5-5-5"}], ["path", {"d": "M15 12H3"}], ["path", {"d": "M15 3h4a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2h-4"}]],
  // ban
  banned: [["circle", {"cx": "12", "cy": "12", "r": "10"}], ["path", {"d": "M4.929 4.929 19.07 19.071"}]],
  // file-warning
  broken: [["path", {"d": "M6 22a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h8a2.4 2.4 0 0 1 1.704.706l3.588 3.588A2.4 2.4 0 0 1 20 8v12a2 2 0 0 1-2 2z"}], ["path", {"d": "M12 9v4"}], ["path", {"d": "M12 17h.01"}]],
  // unplug
  noProvider: [["path", {"d": "m19 5 3-3"}], ["path", {"d": "m2 22 3-3"}], ["path", {"d": "M6.3 20.3a2.4 2.4 0 0 0 3.4 0L12 18l-6-6-2.3 2.3a2.4 2.4 0 0 0 0 3.4Z"}], ["path", {"d": "M7.5 13.5 10 11"}], ["path", {"d": "M10.5 16.5 13 14"}], ["path", {"d": "m12 6 6 6 2.3-2.3a2.4 2.4 0 0 0 0-3.4l-2.6-2.6a2.4 2.4 0 0 0-3.4 0Z"}]],
  // circle-slash
  off: [["circle", {"cx": "12", "cy": "12", "r": "10"}], ["line", {"x1": "9", "x2": "15", "y1": "15", "y2": "9"}]],
  // circle-x
  failed: [["circle", {"cx": "12", "cy": "12", "r": "10"}], ["path", {"d": "m15 9-6 6"}], ["path", {"d": "m9 9 6 6"}]],
  // list
  list: [["path", {"d": "M3 5h.01"}], ["path", {"d": "M3 12h.01"}], ["path", {"d": "M3 19h.01"}], ["path", {"d": "M8 5h13"}], ["path", {"d": "M8 12h13"}], ["path", {"d": "M8 19h13"}]],
  // scroll-text
  log: [["path", {"d": "M15 12h-5"}], ["path", {"d": "M15 8h-5"}], ["path", {"d": "M19 17V5a2 2 0 0 0-2-2H4"}], ["path", {"d": "M8 21h12a2 2 0 0 0 2-2v-1a1 1 0 0 0-1-1H11a1 1 0 0 0-1 1v1a2 2 0 1 1-4 0V5a2 2 0 1 0-4 0v2a1 1 0 0 0 1 1h3"}]],
  // chart-no-axes-column
  chart: [["path", {"d": "M5 21v-6"}], ["path", {"d": "M12 21V3"}], ["path", {"d": "M19 21V9"}]],
  // power
  power: [["path", {"d": "M12 2v10"}], ["path", {"d": "M18.4 6.6a9 9 0 1 1-12.77.04"}]],
  // key-round
  key: [["path", {"d": "M2.586 17.414A2 2 0 0 0 2 18.828V21a1 1 0 0 0 1 1h3a1 1 0 0 0 1-1v-1a1 1 0 0 1 1-1h1a1 1 0 0 0 1-1v-1a1 1 0 0 1 1-1h.172a2 2 0 0 0 1.414-.586l.814-.814a6.5 6.5 0 1 0-4-4z"}], ["circle", {"cx": "16.5", "cy": "7.5", "r": ".5", "fill": "currentColor"}]],
} satisfies Record<string, Shape[]>;

export type IconName = keyof typeof SHAPES;

/** An icon at size px (default 16), in the text colour. It is decoration: the control it sits in carries the name. */
export function Icon(props: { name: IconName; size?: number; class?: string }): VNode {
  const size = props.size ?? 16;
  return (
    <svg
      class={props.class ? `da-icon ${props.class}` : "da-icon"}
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      stroke-width="1.75"
      stroke-linecap="round"
      stroke-linejoin="round"
      aria-hidden="true"
      focusable="false"
    >
      {(SHAPES[props.name] as Shape[]).map(([tag, attrs], i) => h(tag, { key: i, ...attrs }))}
    </svg>
  );
}

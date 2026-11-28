import { useMemo } from "preact/hooks";
import { type Layout, type Theme, tokenProperty } from "../theme/contract";
import { type Resolved, resolve } from "../theme/engine";

interface Half {
  appearance: "light" | "dark";
  layout: Layout;
  style: string;
}

function halfOf(theme: Theme, appearance: "light" | "dark"): Half {
  const r: Resolved = resolve(
    theme,
    { appearance, layout: {}, css: "", safe: true },
    { systemDark: appearance === "dark", reducedMotion: false },
  );
  const style = Object.entries(r.tokens)
    .filter(([name]) => name.startsWith("color."))
    .map(([name, value]) => `${tokenProperty(name)}:${value};`)
    .join("");
  return {
    appearance: r.appearance,
    layout: r.layout,
    style,
  };
}

export function halves(theme: Theme): Half[] {
  return theme.appearance === "both"
    ? [halfOf(theme, "light"), halfOf(theme, "dark")]
    : [halfOf(theme, theme.appearance)];
}

function Frame(props: { half: Half }) {
  const { half } = props;
  return (
    <span class="mini" style={half.style} data-nav={half.layout.nav} data-frame={half.layout.frame}>
      <span class="mini-nav">
        <i class="mini-brand" />
        <i class="mini-entry" data-current="" />
        <i class="mini-entry" />
        <i class="mini-entry" />
      </span>
      <span class="mini-page">
        <i class="mini-title" />
        <span class="mini-row">
          <i class="mini-dot" data-tone="ok" />
          <i class="mini-name" />
          <i class="mini-bar" data-tone="ok">
            <b />
          </i>
        </span>
        <span class="mini-row">
          <i class="mini-dot" data-tone="warn" />
          <i class="mini-name" />
          <i class="mini-bar" data-tone="warn">
            <b />
          </i>
        </span>
        <i class="mini-action" />
      </span>
    </span>
  );
}

export function ThemePreview(props: { theme: Theme }) {
  const parts = useMemo(() => halves(props.theme), [props.theme]);
  return (
    <span class="mini-pair" aria-hidden="true" data-halves={parts.length}>
      {parts.map((half) => (
        <span key={half.appearance} class="mini-half" data-half={half.appearance}>
          <Frame half={half} />
        </span>
      ))}
    </span>
  );
}

export function PaletteDots(props: { theme: Theme }) {
  const first = useMemo(() => halves(props.theme)[0], [props.theme]);
  return (
    <span class="theme-dots" aria-hidden="true" style={first?.style}>
      <i />
      <i />
      <i />
      <i />
      <i />
    </span>
  );
}

// A theme's miniature: pluxy's own frame (the navigation, the page on its panel, two account
// rows with their limit bars and the one filled button) painted in the theme's own colours, with a
// light half and a dark half when the theme offers both. Each half carries the theme's colour
// tokens as custom properties of its own, so what a token is made from (a mix of other tokens)
// resolves against the theme and not against the window the person has on.

import { useMemo } from "preact/hooks";
import { type Layout, type Theme, tokenProperty } from "../theme/contract";
import { type Resolved, resolve } from "../theme/engine";

/** A half of the preview: the theme as it looks in one appearance. */
interface Half {
  appearance: "light" | "dark";
  layout: Layout;
  /** The theme's colour tokens as the declarations of a style attribute. */
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

/** The halves a theme shows: both for a theme that offers both appearances, else the one it has. */
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

/**
 * The miniature of a theme. A theme with both appearances shows the light one and, cut along a
 * slant, the dark one over it. It has no text: the card around it names the theme.
 */
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

/**
 * The five dots of a theme's palette (its ground, page, card, text and accent, in the first half's
 * colours). The dots take their colours from the tokens the style sets, in the stylesheet.
 */
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

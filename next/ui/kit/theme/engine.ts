// Turns a theme and the person's choices into what the page shows, and applies it to a document:
// tokens as custom properties, layout options as attributes on the root element, custom CSS as
// constructed stylesheets (no inline styles, so the panel's CSP can forbid them).

import {
  DEFAULT_LAYOUT,
  LAYOUT_OPTIONS,
  type Layout,
  type Theme,
  TOKENS,
  type TokenName,
  tokenProperty,
  tokenValueProblem,
} from "./contract";
import { cssProblem } from "./validate";

export type Appearance = "light" | "dark";
export type AppearanceChoice = "system" | Appearance;

/** What the person chose on the Themes page. Their choices override the theme's. */
export interface Choices {
  appearance: AppearanceChoice;
  layout: Partial<Layout>;
  density?: string;
  /** The person's own CSS, applied on top of any theme. */
  css: string;
  /** Safe mode: no custom CSS at all. */
  safe: boolean;
}

export const DEFAULT_CHOICES: Choices = { appearance: "system", layout: {}, css: "", safe: false };

export interface Environment {
  systemDark: boolean;
  reducedMotion: boolean;
}

export interface Resolved {
  appearance: Appearance;
  layout: Layout;
  tokens: Record<TokenName, string>;
  /** Custom CSS to apply, theme's first; empty in safe mode. */
  css: string[];
  warnings: string[];
}

export function resolve(theme: Theme, choices: Choices, env: Environment): Resolved {
  let appearance: Appearance =
    choices.appearance === "system" ? (env.systemDark ? "dark" : "light") : choices.appearance;
  if (theme.appearance !== "both") appearance = theme.appearance;
  const tokens = {} as Record<TokenName, string>;
  for (const [name, def] of Object.entries(TOKENS) as [TokenName, { light: string; dark?: string }][]) {
    tokens[name] = appearance === "dark" ? (def.dark ?? def.light) : def.light;
  }
  Object.assign(tokens, theme.tokens);
  if (appearance === "dark" && theme.dark) Object.assign(tokens, theme.dark);

  const warnings: string[] = [];
  if (choices.density !== undefined) {
    if (tokenValueProblem("density", choices.density))
      warnings.push("your density is not a number from 0.5 to 2");
    else tokens.density = choices.density;
  }
  if (env.reducedMotion) tokens["motion.duration"] = "0ms";

  const layout: Layout = { ...DEFAULT_LAYOUT, ...theme.layout };
  for (const option of LAYOUT_OPTIONS) {
    const v = choices.layout[option];
    if (v !== undefined) (layout as Record<string, string>)[option] = v;
  }

  const css: string[] = [];
  if (!choices.safe) {
    if (theme.css) css.push(theme.css);
    if (choices.css) {
      const problem = cssProblem(choices.css);
      if (problem) warnings.push(`your CSS ${problem}; it is not applied`);
      else css.push(choices.css);
    }
  }
  return { appearance, layout, tokens, css, warnings };
}

export function tokensCss(tokens: Record<string, string>): string {
  const decls = Object.entries(tokens).map(([name, value]) => `${tokenProperty(name)}:${value};`);
  return `:root{${decls.join("")}}`;
}

/** Applies resolved themes to one document. The kit's own stylesheet stays first. */
export class ThemeApplier {
  private readonly tokens = new CSSStyleSheet();
  private custom: CSSStyleSheet[] = [];

  constructor(private readonly doc: Document) {}

  apply(r: Resolved) {
    const root = this.doc.documentElement;
    root.dataset.appearance = r.appearance;
    for (const option of LAYOUT_OPTIONS) root.dataset[option] = r.layout[option];
    root.style.colorScheme = r.appearance;
    this.tokens.replaceSync(tokensCss(r.tokens));
    this.custom = r.css.map((text) => {
      const sheet = new CSSStyleSheet();
      sheet.replaceSync(text);
      return sheet;
    });
    this.doc.adoptedStyleSheets = [this.tokens, ...this.custom];
  }
}

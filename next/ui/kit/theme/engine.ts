// Turns a theme and the person's choices into what the page shows, and applies it to a document:
// tokens as custom properties, layout options as attributes on the root element, custom CSS as
// constructed stylesheets (no inline styles, so the panel's CSP can forbid them).

import {
  DEFAULT_LAYOUT,
  DURATION_TOKENS,
  LAYOUT_OPTIONS,
  type Layout,
  type Theme,
  TOKENS,
  type TokenName,
  tokenProperty,
  tokenValueProblem,
} from "./contract";
import { APP_THEME_VARIABLES, T3_ROLE_TO_TOKEN, type T3ColorRole, type T3RoleMap } from "./t3";
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
  /** Extra CSS custom properties (--app-theme-*) when the theme carries T3 Code roles. */
  variables: Record<string, string>;
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

  const variables: Record<string, string> = {};
  if (theme.t3) {
    const activeRoles: T3RoleMap = {};
    if (appearance === theme.t3.appearance) {
      if (theme.t3.generated) Object.assign(activeRoles, theme.t3.generated);
      if (theme.t3.colors) Object.assign(activeRoles, theme.t3.colors);
    }
    const variant = theme.t3.variants?.[appearance];
    if (variant) Object.assign(activeRoles, variant);

    for (const [role, value] of Object.entries(activeRoles) as [T3ColorRole, string | undefined][]) {
      if (value === undefined) continue;
      const mapped = T3_ROLE_TO_TOKEN[role];
      if (mapped) tokens[mapped] = value;
      variables[APP_THEME_VARIABLES[role]] = value;
    }
  }

  Object.assign(tokens, theme.tokens);
  if (appearance === "dark" && theme.dark) Object.assign(tokens, theme.dark);

  const warnings: string[] = [];
  if (choices.density !== undefined) {
    if (tokenValueProblem("density", choices.density))
      warnings.push("your density is not a number from 0.5 to 2");
    else tokens.density = choices.density;
  }
  if (env.reducedMotion) {
    for (const name of DURATION_TOKENS) tokens[name] = "0ms";
    tokens["motion.press-scale"] = "1";
  }

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
  return { appearance, layout, tokens, variables, css, warnings };
}

export function tokensCss(tokens: Record<string, string>, variables: Record<string, string> = {}): string {
  const decls = [
    ...Object.entries(tokens).map(([name, value]) => `${tokenProperty(name)}:${value};`),
    ...Object.entries(variables).map(([name, value]) => `${name}:${value};`),
  ];
  return `:root{${decls.join("")}}`;
}

/** Applies resolved themes to one document. The kit's own stylesheet stays first. */
export class ThemeApplier {
  private readonly tokens = new CSSStyleSheet();
  private custom: CSSStyleSheet[] = [];

  constructor(private readonly doc: Document) {}

  apply(r: Resolved) {
    const root = this.doc.documentElement;
    // Nothing animates into a new theme: transitions are off until the next frame has painted it.
    root.toggleAttribute("data-theme-switching", true);
    const raf = this.doc.defaultView?.requestAnimationFrame?.bind(this.doc.defaultView);
    if (raf) raf(() => raf(() => root.removeAttribute("data-theme-switching")));
    else root.removeAttribute("data-theme-switching");
    root.dataset.appearance = r.appearance;
    // The class CSS written for T3 Code picks the dark appearance by.
    root.classList.toggle("dark", r.appearance === "dark");
    for (const option of LAYOUT_OPTIONS) root.dataset[option] = r.layout[option];
    root.style.colorScheme = r.appearance;
    this.tokens.replaceSync(tokensCss(r.tokens, r.variables));
    this.custom = r.css.map((text) => {
      const sheet = new CSSStyleSheet();
      sheet.replaceSync(text);
      return sheet;
    });
    this.doc.adoptedStyleSheets = [this.tokens, ...this.custom];
  }
}

// The theme contract, version 0, as code. themes/README.md is its reference; tests/contract.test.ts
// checks that the two agree.

export const THEME_VERSION = 0;

/** Largest theme file, in bytes of UTF-8. */
export const MAX_THEME_BYTES = 256 * 1024;
/** Largest custom CSS, in bytes of UTF-8. */
export const MAX_CSS_BYTES = 128 * 1024;

export interface TokenDef {
  light: string;
  /** The dark default; absent means the light one. */
  dark?: string;
}

/** Every token and its defaults, in the order the README lists them. */
export const TOKENS = {
  "color.bg": { light: "#f7f7f8", dark: "#111214" },
  "color.surface": { light: "#ffffff", dark: "#1a1b1e" },
  "color.surface-raised": { light: "#f0f0f2", dark: "#24262a" },
  "color.text": { light: "#18181b", dark: "#e7e7ea" },
  "color.text-muted": { light: "#5f6068", dark: "#9a9ba3" },
  "color.border": { light: "#e2e2e6", dark: "#2e3035" },
  "color.accent": { light: "#2f5fd0", dark: "#7aa2ff" },
  "color.accent-text": { light: "#ffffff", dark: "#0b1020" },
  "color.ok": { light: "#1f7a3d", dark: "#5cc98a" },
  "color.warn": { light: "#9a5b00", dark: "#e6b450" },
  "color.danger": { light: "#b42318", dark: "#ff7b72" },
  "color.focus": { light: "#2f5fd0", dark: "#7aa2ff" },
  "color.meter-track": { light: "var(--px-color-surface-raised)" },
  "color.meter-fill": { light: "var(--px-color-accent)" },
  "color.chart-success": { light: "var(--px-color-ok)" },
  "color.chart-failure": { light: "var(--px-color-danger)" },
  "color.diff-add": { light: "#1f7a3d", dark: "#5cc98a" },
  "color.diff-remove": { light: "#b42318", dark: "#ff7b72" },
  "font.body": { light: 'system-ui, -apple-system, "Segoe UI", sans-serif' },
  "font.mono": { light: 'ui-monospace, "SF Mono", Menlo, Consolas, monospace' },
  "font.size": { light: "14px" },
  "font.size-small": { light: "12px" },
  "font.size-title": { light: "20px" },
  "font.line-height": { light: "1.45" },
  "font.weight-strong": { light: "600" },
  "space.unit": { light: "4px" },
  density: { light: "1" },
  "size.nav": { light: "200px" },
  "size.width-wide": { light: "1200px" },
  "size.width-narrow": { light: "760px" },
  "size.detail": { light: "360px" },
  "shape.radius": { light: "8px" },
  "shape.border": { light: "1px" },
  "shadow.raised": { light: "0 1px 2px rgb(0 0 0 / 0.06)" },
  "motion.duration": { light: "120ms" },
  "motion.easing": { light: "ease-out" },
} as const satisfies Record<string, TokenDef>;

export type TokenName = keyof typeof TOKENS;

export function isToken(name: string): name is TokenName {
  return Object.hasOwn(TOKENS, name);
}

/** The CSS custom property a token sets: `color.bg` is `--px-color-bg`. */
export function tokenProperty(name: string): string {
  return `--px-${name.replaceAll(".", "-")}`;
}

/** Every data-part hook a theme's CSS can rely on, in the order the README lists them. */
export const PARTS = [
  "shell",
  "nav",
  "nav-group",
  "nav-item",
  "main",
  "page-title",
  "list",
  "item",
  "detail",
  "card",
  "button",
  "badge",
  "field",
  "meter",
  "tabs",
  "tab",
  "tab-panel",
  "code",
  "diff",
  "diff-line",
  "dialog",
  "log",
  "log-line",
  "toolbar",
  "pager",
  "select",
  "batch",
  "account-meta",
  "upload-results",
  "key-notice",
  "key-value",
  "method",
  "flow",
  "device-code",
  "tile",
  "model-group",
  "stat",
  "chart",
  "account-tools",
  "account-check",
  "account-cooldowns",
  "account-cooldown-confirm",
  "account-tool-actions",
  "account-config-link",
  "account-test-hint",
  "account-models",
  "account-test-result",
  "account-test-reply",
  "account-sign-in",
  "account-settings",
  "account-edit",
  "account-edit-row",
  "account-edit-discard",
] as const;

export type Part = (typeof PARTS)[number];

/** Each layout option's values; the first is the default. */
export const LAYOUT = {
  nav: ["sidebar", "rail", "top"],
  list: ["table", "cards"],
  detail: ["side", "below"],
  width: ["wide", "full", "narrow"],
  tabs: ["top", "side"],
} as const;

export type LayoutOption = keyof typeof LAYOUT;
export type Layout = { [K in LayoutOption]: (typeof LAYOUT)[K][number] };

export const LAYOUT_OPTIONS = Object.keys(LAYOUT) as LayoutOption[];

export const DEFAULT_LAYOUT: Layout = {
  nav: "sidebar",
  list: "table",
  detail: "side",
  width: "wide",
  tabs: "top",
};

export function isLayoutValue<K extends LayoutOption>(option: K, value: unknown): value is Layout[K] {
  return (LAYOUT[option] as readonly unknown[]).includes(value);
}

/** Characters and sequences a token value may not hold: they could end the declaration or load something. */
const FORBIDDEN_IN_VALUE = /[;{}<>\\@]|\/\*|url\(/i;

/** Why a token value breaks the rules, or undefined when it is fine. */
export function tokenValueProblem(name: string, value: unknown): string | undefined {
  if (typeof value !== "string") return "is not a string";
  if (value.length < 1 || value.length > 200) return "must be 1 to 200 characters";
  if (FORBIDDEN_IN_VALUE.test(value)) return "holds a character or sequence tokens may not hold";
  if (name === "density") {
    const n = Number(value);
    if (!Number.isFinite(n) || n < 0.5 || n > 2) return "must be a number from 0.5 to 2";
  }
  return undefined;
}

/** A theme as the UI uses it, after validation. */
export const APPEARANCES = ["both", "light", "dark"] as const;
export type ThemeAppearance = (typeof APPEARANCES)[number];

export interface Theme {
  id: string;
  name: string;
  description?: string;
  appearance: ThemeAppearance;
  tokens: Partial<Record<TokenName, string>>;
  dark?: Partial<Record<TokenName, string>>;
  layout: Partial<Layout>;
  css?: string;
}

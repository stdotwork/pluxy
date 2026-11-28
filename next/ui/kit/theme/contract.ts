// The theme contract, version 0, as code. themes/README.md is its reference; tests/contract.test.ts
// checks that the two agree.

import type { T3Appearance, T3RoleMap } from "./t3";

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
  "color.bg": { light: "#fcfcfc", dark: "#111113" },
  "color.chrome": {
    light: "color-mix(in srgb, var(--px-color-bg) 96.5%, black)",
    dark: "color-mix(in srgb, var(--px-color-bg) 53%, black)",
  },
  "color.surface": { light: "#ffffff", dark: "#161618" },
  "color.surface-raised": { light: "#f6f6f7", dark: "#1c1c1f" },
  "color.text": { light: "#18181b", dark: "#f2f2f4" },
  "color.text-muted": { light: "#6b6b73", dark: "#9898a1" },
  "color.secondary-label": { light: "var(--px-color-text-muted)" },
  "color.border": { light: "#e6e6e9", dark: "#242428" },
  "color.accent": { light: "#2f5bea", dark: "#4f7bff" },
  "color.accent-text": { light: "#ffffff" },
  "color.message-action": {
    light: "var(--px-color-accent)",
    dark: "color-mix(in srgb, var(--px-color-accent) 85%, black)",
  },
  "color.message-action-foreground": { light: "var(--px-color-accent-text)" },
  "color.message-action-hover": { light: "color-mix(in srgb, var(--px-color-message-action) 88%, black)" },
  "color.ok": { light: "#15803d", dark: "#4ade80" },
  "color.warn": { light: "#b45309", dark: "#fbbf24" },
  "color.danger": { light: "#c42b31", dark: "#ff7a7e" },
  "color.success": { light: "#14923f", dark: "#2fbf6a" },
  "color.success-surface": {
    light: "color-mix(in srgb, var(--px-color-success) 8%, var(--px-color-bg))",
    dark: "color-mix(in srgb, var(--px-color-success) 16%, var(--px-color-bg))",
  },
  "color.warning": { light: "#c26a06", dark: "#f5b42a" },
  "color.error": { light: "#e5484d", dark: "#f2555a" },
  "color.info": { light: "#3b82f6", dark: "#60a5fa" },
  "color.info-foreground": { light: "#1d4ed8", dark: "#93c5fd" },
  "color.info-surface": {
    light: "color-mix(in srgb, var(--px-color-info) 8%, var(--px-color-bg))",
    dark: "color-mix(in srgb, var(--px-color-info) 16%, var(--px-color-bg))",
  },
  "color.update": { light: "var(--px-color-info)" },
  "color.update-foreground": { light: "var(--px-color-info-foreground)" },
  "color.update-surface": { light: "var(--px-color-info-surface)" },
  "color.focus": { light: "var(--px-color-accent)" },
  "color.meter-track": {
    light: "color-mix(in srgb, var(--px-color-text) 8%, var(--px-color-surface))",
  },
  "color.meter-fill": { light: "var(--px-color-success)" },
  "color.meter-mid": { light: "var(--px-color-warning)" },
  "color.meter-low": { light: "var(--px-color-error)" },
  "color.meter-unknown": {
    light: "color-mix(in srgb, var(--px-color-text) 22%, var(--px-color-surface))",
  },
  "color.chart-success": {
    light: "color-mix(in srgb, var(--px-color-text) 78%, var(--px-color-bg))",
  },
  "color.chart-failure": { light: "var(--px-color-error)" },
  "color.chart-1": { light: "#d97757", dark: "#e08a6d" },
  "color.chart-2": { light: "#5b6bf5", dark: "#7684f7" },
  "color.chart-3": { light: "#4f8bff", dark: "#6c9eff" },
  "color.chart-4": { light: "var(--px-color-text)" },
  "color.chart-5": { light: "#14b8a6", dark: "#2dd4bf" },
  "color.chart-6": { light: "#a855f7", dark: "#b875f8" },
  "color.plan-top": {
    light:
      "linear-gradient(135deg, color-mix(in srgb, var(--px-color-warning) 20%, var(--px-color-surface)), color-mix(in srgb, var(--px-color-warning) 40%, var(--px-color-surface)))",
    dark: "linear-gradient(135deg, color-mix(in srgb, var(--px-color-warning) 26%, var(--px-color-surface)), color-mix(in srgb, var(--px-color-warning) 14%, var(--px-color-surface)))",
  },
  "color.plan-top-text": {
    light: "color-mix(in srgb, var(--px-color-warning) 35%, var(--px-color-text))",
    dark: "color-mix(in srgb, var(--px-color-warning) 55%, var(--px-color-text))",
  },
  "color.plan-top-border": {
    light: "color-mix(in srgb, var(--px-color-warning) 55%, var(--px-color-surface))",
    dark: "color-mix(in srgb, var(--px-color-warning) 40%, var(--px-color-surface))",
  },
  "color.plan-high": {
    light:
      "linear-gradient(180deg, var(--px-color-surface), color-mix(in srgb, var(--px-color-text) 10%, var(--px-color-surface)))",
    dark: "linear-gradient(180deg, color-mix(in srgb, var(--px-color-text) 18%, var(--px-color-surface)), color-mix(in srgb, var(--px-color-text) 8%, var(--px-color-surface)))",
  },
  "color.plan-high-text": { light: "var(--px-color-text)" },
  "color.plan-high-border": {
    light: "color-mix(in srgb, var(--px-color-text) 24%, var(--px-color-surface))",
  },
  "color.plan-mid-text": { light: "var(--px-color-text)" },
  "color.plan-mid-border": { light: "var(--px-color-input-border)" },
  "color.plan-free-text": { light: "var(--px-color-secondary-label)" },
  "color.diff-add": { light: "var(--px-color-ok)" },
  "color.diff-remove": { light: "var(--px-color-danger)" },
  "color.nav": { light: "var(--px-color-chrome)" },
  "color.nav-text": { light: "var(--px-color-text)" },
  "color.nav-muted": { light: "var(--px-color-text-muted)" },
  "color.nav-hover": { light: "color-mix(in srgb, var(--px-color-text) 5%, var(--px-color-nav))" },
  "color.nav-active": { light: "color-mix(in srgb, var(--px-color-text) 9%, var(--px-color-nav))" },
  "color.nav-selected": { light: "var(--px-color-surface)", dark: "var(--px-color-surface-raised)" },
  "color.nav-border": { light: "var(--px-color-border)" },
  "color.overlay": { light: "var(--px-color-surface)" },
  "color.card": { light: "var(--px-color-surface)" },
  "color.card-border": { light: "var(--px-color-border)" },
  "color.input-border": { light: "#d4d4d8", dark: "#34343a" },
  "color.placeholder": { light: "var(--px-color-text-muted)" },
  "color.selected": { light: "var(--px-color-surface-raised)" },
  "color.selected-text": { light: "var(--px-color-text)" },
  "color.danger-surface": {
    light: "color-mix(in srgb, var(--px-color-error) 8%, var(--px-color-bg))",
    dark: "color-mix(in srgb, var(--px-color-error) 16%, var(--px-color-bg))",
  },
  "color.warn-surface": {
    light: "color-mix(in srgb, var(--px-color-warning) 8%, var(--px-color-bg))",
    dark: "color-mix(in srgb, var(--px-color-warning) 16%, var(--px-color-bg))",
  },
  "color.code-bg": { light: "var(--px-color-surface-raised)" },
  "color.code-text": { light: "var(--px-color-text)" },
  "color.search-match-background": { light: "#ffde8b", dark: "#8c6c1f" },
  "color.search-match-foreground": { light: "#21180a", dark: "#f5f5f5" },
  "font.body": {
    light: '"Inter Variable", -apple-system, BlinkMacSystemFont, "Segoe UI", system-ui, sans-serif',
  },
  "font.mono": { light: '"IBM Plex Mono", "SF Mono", ui-monospace, Menlo, Consolas, monospace' },
  "font.size": { light: "14px" },
  "font.size-small": { light: "12px" },
  "font.size-label": { light: "11px" },
  "font.size-title": { light: "14px" },
  "font.size-mono": { light: "13px" },
  "font.line-height": { light: "1.4" },
  "font.weight-medium": { light: "500" },
  "font.weight-strong": { light: "600" },
  "space.unit": { light: "4px" },
  density: { light: "1" },
  "size.nav": { light: "232px" },
  "size.rail": { light: "48px" },
  "size.header": { light: "48px" },
  "size.topics": { light: "180px" },
  "size.row": { light: "32px" },
  "size.row-tall": { light: "40px" },
  "size.control": { light: "28px" },
  "size.control-main": { light: "32px" },
  "size.width-wide": { light: "1240px" },
  "size.width-narrow": { light: "880px" },
  "size.detail": { light: "380px" },
  "size.card-padding": { light: "14px" },
  "size.frame-gap": { light: "8px" },
  "shape.radius": { light: "8px" },
  "shape.border": { light: "1px" },
  "shape.icon-stroke": { light: "1.75" },
  "shadow.raised": { light: "0 1px 2px rgb(0 0 0 / 0.04)", dark: "none" },
  "shadow.panel": {
    light: "0 0 0 1px rgb(0 0 0 / 0.04), 0 1px 2px rgb(0 0 0 / 0.04), 0 8px 24px -12px rgb(0 0 0 / 0.1)",
    dark: "0 0 0 1px rgb(255 255 255 / 0.04), 0 12px 32px -16px rgb(0 0 0 / 0.7)",
  },
  "shadow.overlay": {
    light: "0 16px 40px -18px rgb(0 0 0 / 0.55)",
    dark: "0 16px 40px -18px rgb(0 0 0 / 0.8)",
  },
  "shadow.dialog": { light: "0 24px 64px -24px rgb(0 0 0 / 0.65)" },
  "motion.duration": { light: "120ms" },
  "motion.duration-menu": { light: "160ms" },
  "motion.duration-slow": { light: "240ms" },
  "motion.easing": { light: "cubic-bezier(0.2, 0, 0, 1)" },
  "motion.easing-drawer": { light: "cubic-bezier(0.32, 0.72, 0, 1)" },
  "motion.press-scale": { light: "0.97" },
  "meter.mid-below": { light: "60" },
  "meter.low-below": { light: "20" },
} as const satisfies Record<string, TokenDef>;

/** Tokens that hold a length of time: reduced motion sets each to 0ms. */
export const DURATION_TOKENS = ["motion.duration", "motion.duration-menu", "motion.duration-slow"] as const;

/** Tokens that hold a share of a limit, in percent: a number from 0 to 100. */
const PERCENT_TOKENS: ReadonlySet<string> = new Set(["meter.mid-below", "meter.low-below"]);

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
  "nav-idle",
  "brand",
  "nav-item",
  "icon",
  "status",
  "status-menu",
  "header",
  "page-title",
  "header-tabs",
  "header-tab",
  "header-actions",
  "privacy-toggle",
  "topics",
  "nav-group",
  "topic",
  "topics-select",
  "main",
  "list",
  "list-frame",
  "item",
  "detail",
  "card",
  "settings-column",
  "settings-section",
  "setting-card",
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
  "select",
  "batch",
  "upload-results",
  "key-notice",
  "key-value",
  "method",
  "flow",
  "device-code",
  "tier",
  "order-row",
  "fallback",
  "fallback-editor",
  "tile",
  "model-group",
  "days",
  "account-tools",
  "account-test-hint",
  "account-models",
  "account-test-result",
  "account-test-reply",
  "account-sign-in",
  "account-settings",
  "account-edit",
  "account-edit-row",
  "account-edit-discard",
  "setup-step",
  "setup-import",
  "setup-report",
  "setup-pick",
  "setup-test",
  "account-page",
  "account-opening",
  "account-more",
  "auth-import",
  "round-notice",
  "method-group",
  "more-options",
  "login-details",
  "account-added",
  "account-row",
  "request-row",
  "setting-row",
  "tool-row",
  "state-pane",
  "plan-pill",
  "limits",
  "activity",
  "activity-filters",
  "usage-chart",
  "usage-chip",
  "usage-numbers",
  "usage-breakdown",
  "tool-steps",
  "tool-key",
] as const;

export type Part = (typeof PARTS)[number];

/**
 * T3 Code's data-slot names, on the parts both UIs have, so CSS written for a T3 Code theme
 * matches them. In the order the README lists them.
 */
export const SLOTS = ["button", "input", "switch", "select-trigger", "dialog-popup", "sidebar"] as const;

/** Each layout option's values; the first is the default. */
export const LAYOUT = {
  frame: ["inset", "flat"],
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
  frame: "inset",
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
  const forbidden = FORBIDDEN_IN_VALUE.exec(value);
  if (forbidden) return `contains ${JSON.stringify(forbidden[0])}, which a token value may not contain`;
  if (name === "density") {
    const n = Number(value);
    if (!Number.isFinite(n) || n < 0.5 || n > 2) return "must be a number from 0.5 to 2";
  }
  if (PERCENT_TOKENS.has(name)) {
    const n = Number(value);
    if (value.trim() === "" || !Number.isFinite(n) || n < 0 || n > 100)
      return "must be a number from 0 to 100";
  }
  return undefined;
}

/** A theme as the UI uses it, after validation. */
export const APPEARANCES = ["both", "light", "dark"] as const;
export type ThemeAppearance = (typeof APPEARANCES)[number];

export interface T3ThemeData {
  /** Which appearance holds `colors` and receives `canvas`/`accent` palette generation. */
  appearance: T3Appearance;
  generated?: T3RoleMap;
  colors?: T3RoleMap;
  variants?: Partial<Record<T3Appearance, T3RoleMap>>;
}

export interface Theme {
  id: string;
  name: string;
  description?: string;
  appearance: ThemeAppearance;
  tokens: Partial<Record<TokenName, string>>;
  dark?: Partial<Record<TokenName, string>>;
  layout: Partial<Layout>;
  css?: string;
  t3?: T3ThemeData;
}

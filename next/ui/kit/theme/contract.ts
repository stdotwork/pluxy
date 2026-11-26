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
  "color.bg": { light: "#fdfcf9", dark: "#171614" },
  "color.surface": { light: "#fdfcf9", dark: "#171614" },
  "color.surface-raised": { light: "#f2f0e9", dark: "#24231f" },
  "color.text": { light: "#1b1a17", dark: "#eceae3" },
  "color.text-muted": { light: "#55524a", dark: "#b5b2a7" },
  "color.border": { light: "#e3e0d6", dark: "#34322d" },
  "color.accent": { light: "#1b1a17", dark: "#eceae3" },
  "color.accent-text": { light: "#fdfcf9", dark: "#171614" },
  "color.ok": { light: "#1f7a43", dark: "#86d3a0" },
  "color.warn": { light: "#8f5600", dark: "#e5b866" },
  "color.danger": { light: "#b3261e", dark: "#ff8f80" },
  "color.focus": { light: "#1b1a17", dark: "#eceae3" },
  "color.meter-track": { light: "var(--px-color-surface-raised)" },
  "color.meter-fill": { light: "var(--px-color-accent)" },
  "color.chart-success": { light: "var(--px-color-text)" },
  "color.chart-failure": { light: "var(--px-color-danger)" },
  "color.diff-add": { light: "#1f7a43", dark: "#86d3a0" },
  "color.diff-remove": { light: "#b3261e", dark: "#ff8f80" },
  "color.nav": {
    light: "color-mix(in srgb, var(--px-color-surface) 97%, black)",
    dark: "color-mix(in srgb, var(--px-color-surface) 80%, black)",
  },
  "color.nav-text": { light: "var(--px-color-text)" },
  "color.nav-muted": { light: "var(--px-color-text-muted)" },
  "color.nav-hover": { light: "var(--px-color-surface-raised)" },
  "color.nav-selected": {
    light: "color-mix(in srgb, var(--px-color-accent) 7%, transparent)",
    dark: "color-mix(in srgb, var(--px-color-accent) 9%, transparent)",
  },
  "color.nav-border": { light: "var(--px-color-border)" },
  "color.overlay": { light: "var(--px-color-surface)" },
  "color.card": { light: "transparent" },
  "color.card-border": { light: "transparent" },
  "color.input-border": {
    light: "color-mix(in srgb, var(--px-color-text-muted) 70%, var(--px-color-surface))",
  },
  "color.placeholder": { light: "var(--px-color-text-muted)" },
  "color.selected": { light: "var(--px-color-surface-raised)" },
  "color.selected-text": { light: "var(--px-color-text)" },
  "color.danger-surface": { light: "transparent" },
  "color.warn-surface": { light: "transparent" },
  "color.code-bg": { light: "var(--px-color-surface-raised)" },
  "color.code-text": { light: "var(--px-color-text)" },
  "font.body": { light: '"IBM Plex Sans", system-ui, -apple-system, "Segoe UI", sans-serif' },
  "font.mono": { light: '"IBM Plex Mono", ui-monospace, "SF Mono", Menlo, Consolas, monospace' },
  "font.size": { light: "13px" },
  "font.size-small": { light: "12px" },
  "font.size-title": { light: "14px" },
  "font.size-mono": { light: "12px" },
  "font.line-height": { light: "1.4" },
  "font.weight-strong": { light: "600" },
  "space.unit": { light: "4px" },
  density: { light: "1" },
  "size.nav": { light: "200px" },
  "size.rail": { light: "48px" },
  "size.header": { light: "44px" },
  "size.topics": { light: "180px" },
  "size.row": { light: "36px" },
  "size.row-tall": { light: "44px" },
  "size.control": { light: "28px" },
  "size.control-main": { light: "32px" },
  "size.width-wide": { light: "1200px" },
  "size.width-narrow": { light: "760px" },
  "size.detail": { light: "360px" },
  "size.card-padding": { light: "0px" },
  "shape.radius": { light: "4px" },
  "shape.border": { light: "1px" },
  "shadow.raised": { light: "none" },
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
  "sign-out",
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
  "setup-found",
  "setup-pick",
  "setup-client",
  "setup-paste",
  "account-page",
  "account-opening",
  "account-more",
  "auth-import",
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
  const forbidden = FORBIDDEN_IN_VALUE.exec(value);
  if (forbidden) return `contains ${JSON.stringify(forbidden[0])}, which a token value may not contain`;
  if (name === "density") {
    const n = Number(value);
    if (!Number.isFinite(n) || n < 0.5 || n > 2) return "must be a number from 0.5 to 2";
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

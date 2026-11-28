// T3 Code theme dialect constants, role-to-token map, CSS variable map, and ID helper.
// Role names and variable names copied from T3 Code (commit 2a411317d9b7341547e5acb3e9bceb7e6195c5a2,
// packages/shared/src/themePalettes.ts and apps/web/src/themePalette.ts, MIT License).

import { type TokenName, tokenValueProblem } from "./contract";

export const T3_COLOR_ROLES = [
  "canvas",
  "chrome",
  "toolbar",
  "toolbarForeground",
  "toolbarBorder",
  "toolbarControl",
  "toolbarControlForeground",
  "toolbarControlHover",
  "surface",
  "surfaceRaised",
  "surfaceOverlay",
  "text",
  "textMuted",
  "border",
  "input",
  "focus",
  "accent",
  "accentForeground",
  "secondary",
  "secondaryForeground",
  "muted",
  "mutedForeground",
  "placeholder",
  "secondaryLabel",
  "iconMuted",
  "error",
  "errorForeground",
  "errorSurface",
  "warning",
  "warningForeground",
  "warningSurface",
  "update",
  "updateForeground",
  "updateSurface",
  "accentSurface",
  "accentSurfaceForeground",
  "messageSurface",
  "messageForeground",
  "messageAction",
  "messageActionForeground",
  "messageActionHover",
  "codeBackground",
  "codeForeground",
  "searchMatchBackground",
  "searchMatchForeground",
  "searchMatchActiveBackground",
  "searchMatchActiveForeground",
  "sidebar",
  "sidebarForeground",
  "sidebarMutedForeground",
  "sidebarControlSurface",
  "sidebarRowHover",
  "sidebarRowActive",
  "sidebarRowSelected",
  "sidebarBorder",
  "terminalBackground",
  "terminalForeground",
  "terminalCursor",
  "terminalSelection",
  "terminalScrollbar",
  "terminalScrollbarHover",
] as const;

export type T3ColorRole = (typeof T3_COLOR_ROLES)[number];
export type T3Appearance = "light" | "dark";
export type T3RoleMap = Partial<Record<T3ColorRole, string>>;

const T3_ROLE_SET: ReadonlySet<string> = new Set(T3_COLOR_ROLES);

export function isT3ColorRole(name: string): name is T3ColorRole {
  return T3_ROLE_SET.has(name);
}

/** T3 Code roles that map onto pluxy colour tokens. Unmapped roles keep pluxy defaults. */
export const T3_ROLE_TO_TOKEN: Readonly<Partial<Record<T3ColorRole, TokenName>>> = {
  canvas: "color.bg",
  chrome: "color.chrome",
  surface: "color.surface",
  surfaceRaised: "color.surface-raised",
  surfaceOverlay: "color.overlay",
  text: "color.text",
  mutedForeground: "color.text-muted",
  secondaryLabel: "color.secondary-label",
  border: "color.border",
  input: "color.input-border",
  placeholder: "color.placeholder",
  focus: "color.focus",
  accent: "color.accent",
  accentForeground: "color.accent-text",
  accentSurface: "color.selected",
  accentSurfaceForeground: "color.selected-text",
  messageAction: "color.message-action",
  messageActionForeground: "color.message-action-foreground",
  messageActionHover: "color.message-action-hover",
  error: "color.error",
  errorForeground: "color.danger",
  errorSurface: "color.danger-surface",
  warning: "color.warning",
  warningForeground: "color.warn",
  warningSurface: "color.warn-surface",
  update: "color.update",
  updateForeground: "color.update-foreground",
  updateSurface: "color.update-surface",
  codeBackground: "color.code-bg",
  codeForeground: "color.code-text",
  searchMatchBackground: "color.search-match-background",
  searchMatchForeground: "color.search-match-foreground",
  sidebar: "color.nav",
  sidebarForeground: "color.nav-text",
  sidebarMutedForeground: "color.nav-muted",
  sidebarRowHover: "color.nav-hover",
  sidebarRowActive: "color.nav-active",
  sidebarRowSelected: "color.nav-selected",
  sidebarBorder: "color.nav-border",
};

/** CSS custom properties set on :root when a theme carries T3 Code roles. */
export const APP_THEME_VARIABLES: Readonly<Record<T3ColorRole, string>> = {
  canvas: "--app-theme-canvas",
  chrome: "--app-theme-chrome",
  toolbar: "--app-theme-toolbar",
  toolbarForeground: "--app-theme-toolbar-foreground",
  toolbarBorder: "--app-theme-toolbar-border",
  toolbarControl: "--app-theme-toolbar-control",
  toolbarControlForeground: "--app-theme-toolbar-control-foreground",
  toolbarControlHover: "--app-theme-toolbar-control-hover",
  surface: "--app-theme-surface",
  surfaceRaised: "--app-theme-surface-raised",
  surfaceOverlay: "--app-theme-surface-overlay",
  text: "--app-theme-text",
  textMuted: "--app-theme-text-muted",
  border: "--app-theme-border",
  input: "--app-theme-input",
  focus: "--app-theme-focus",
  accent: "--app-theme-accent",
  accentForeground: "--app-theme-accent-foreground",
  secondary: "--app-theme-secondary",
  secondaryForeground: "--app-theme-secondary-foreground",
  muted: "--app-theme-muted",
  mutedForeground: "--app-theme-muted-foreground",
  placeholder: "--app-theme-placeholder",
  secondaryLabel: "--app-theme-secondary-label",
  iconMuted: "--app-theme-icon-muted",
  error: "--app-theme-error",
  errorForeground: "--app-theme-error-foreground",
  errorSurface: "--app-theme-error-surface",
  warning: "--app-theme-warning",
  warningForeground: "--app-theme-warning-foreground",
  warningSurface: "--app-theme-warning-surface",
  update: "--app-theme-update",
  updateForeground: "--app-theme-update-foreground",
  updateSurface: "--app-theme-update-surface",
  accentSurface: "--app-theme-accent-surface",
  accentSurfaceForeground: "--app-theme-accent-surface-foreground",
  messageSurface: "--app-theme-message-surface",
  messageForeground: "--app-theme-message-foreground",
  messageAction: "--app-theme-message-action",
  messageActionForeground: "--app-theme-message-action-foreground",
  messageActionHover: "--app-theme-message-action-hover",
  codeBackground: "--app-theme-code-background",
  codeForeground: "--app-theme-code-foreground",
  searchMatchBackground: "--app-theme-search-match-background",
  searchMatchForeground: "--app-theme-search-match-foreground",
  searchMatchActiveBackground: "--app-theme-search-match-active-background",
  searchMatchActiveForeground: "--app-theme-search-match-active-foreground",
  sidebar: "--app-theme-sidebar",
  sidebarForeground: "--app-theme-sidebar-foreground",
  sidebarMutedForeground: "--app-theme-sidebar-muted-foreground",
  sidebarControlSurface: "--app-theme-sidebar-control-surface",
  sidebarRowHover: "--app-theme-sidebar-row-hover",
  sidebarRowActive: "--app-theme-sidebar-row-active",
  sidebarRowSelected: "--app-theme-sidebar-row-selected",
  sidebarBorder: "--app-theme-sidebar-border",
  terminalBackground: "--app-theme-terminal-background",
  terminalForeground: "--app-theme-terminal-foreground",
  terminalCursor: "--app-theme-terminal-cursor",
  terminalSelection: "--app-theme-terminal-selection-background",
  terminalScrollbar: "--app-theme-terminal-scrollbar",
  terminalScrollbarHover: "--app-theme-terminal-scrollbar-hover",
};

export const T3_ID_PATTERN = /^[a-z0-9][a-z0-9-]{0,47}$/;

/** Ported from themeIdFromName in T3 Code's apps/web/src/themePalette.ts. */
export function themeIdFromName(name: string): string {
  const label = name.trim().toLowerCase();
  const normalized = label
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 48);
  if (normalized || !label) return normalized || "custom-theme";

  let hash = 2_166_136_261;
  for (let index = 0; index < label.length; index += 1) {
    hash ^= label.charCodeAt(index);
    hash = Math.imul(hash, 16_777_619);
  }
  return `theme-${(hash >>> 0).toString(36)}`;
}

function isObject(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

export function readT3Roles(field: string, raw: unknown, warnings: string[]): T3RoleMap {
  const out: T3RoleMap = {};
  if (raw === undefined) return out;
  if (!isObject(raw)) {
    warnings.push(`${field} is not an object; ignored`);
    return out;
  }
  for (const [role, value] of Object.entries(raw)) {
    if (!isT3ColorRole(role)) {
      warnings.push(`${field}: unknown role ${role}; ignored`);
      continue;
    }
    const problem = tokenValueProblem(role, value);
    if (problem) {
      warnings.push(`${field}: ${role} ${problem}; ignored`);
      continue;
    }
    out[role] = value as string;
  }
  return out;
}

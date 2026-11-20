// Ported from T3 Code (https://github.com/pingdotgg/t3code),
// apps/web/src/themePalette.ts at commit 2a411317d9b7341547e5acb3e9bceb7e6195c5a2
// (MIT License, Copyright (c) 2026 T3 Tools Inc.).
// Replaces culori with a small parser for #rgb, #rgba, #rrggbb, #rrggbbaa, rgb()/rgba(),
// and oklch(), and omits getDefaultThemeColors so missing roles keep pluxy defaults.

import type { T3Appearance, T3ColorRole } from "./t3";

type ThemeRgbColor = {
  r: number;
  g: number;
  b: number;
};

type ThemeOklch = { L: number; C: number; h: number };
type ParsedThemeColor = { color: ThemeOklch; alpha: number };

const THEME_LIGHT_FOREGROUND: ThemeRgbColor = { r: 255, g: 250, b: 255 };
const THEME_DARK_FOREGROUND: ThemeRgbColor = { r: 36, g: 21, b: 35 };
const THEME_WHITE_FOREGROUND: ThemeRgbColor = { r: 255, g: 255, b: 255 };
const THEME_BLACK_FOREGROUND: ThemeRgbColor = { r: 0, g: 0, b: 0 };

const NUMBER_RE = /^[+-]?(?:\d+\.?\d*|\.\d+)(?:[eE][+-]?\d+)?$/;

function parseCssNumber(token: string): number | null {
  if (!NUMBER_RE.test(token)) return null;
  const n = Number(token);
  return Number.isFinite(n) ? n : null;
}

function parseAlphaToken(token: string, allowNone: boolean): number | null {
  if (allowNone && token.toLowerCase() === "none") return 0;
  if (token.endsWith("%")) {
    const n = parseCssNumber(token.slice(0, -1));
    return n === null ? null : Math.min(1, Math.max(0, n / 100));
  }
  const n = parseCssNumber(token);
  return n === null ? null : Math.min(1, Math.max(0, n));
}

function parseRgbChannelToken(
  token: string,
  allowNone: boolean,
): { value: number; kind: "none" | "percent" | "number" } | null {
  if (allowNone && token.toLowerCase() === "none") return { value: 0, kind: "none" };
  if (token.endsWith("%")) {
    const n = parseCssNumber(token.slice(0, -1));
    return n === null ? null : { value: n * 2.55, kind: "percent" };
  }
  const n = parseCssNumber(token);
  return n === null ? null : { value: n, kind: "number" };
}

function parseRgbFunction(body: string): ParsedThemeColor | null {
  let channelTokens: string[];
  let alphaToken: string | undefined;
  const allowNone = !body.includes(",");

  if (!allowNone) {
    if (body.includes("/")) return null;
    const parts = body.split(",").map((s) => s.trim());
    if (parts.length !== 3 && parts.length !== 4) return null;
    channelTokens = parts.slice(0, 3);
    alphaToken = parts[3];
  } else {
    const slashParts = body.split("/");
    if (slashParts.length > 2) return null;
    channelTokens = (slashParts[0] ?? "").trim().split(/\s+/);
    if (channelTokens.length !== 3) return null;
    if (slashParts.length === 2) {
      const right = (slashParts[1] ?? "").trim();
      if (!right || /\s/.test(right)) return null;
      alphaToken = right;
    }
  }

  const parsedChannels = channelTokens.map((tok) => parseRgbChannelToken(tok, allowNone));
  const [r, g, b] = parsedChannels;
  if (!r || !g || !b) return null;
  const nonNoneKinds = new Set([r, g, b].map((c) => c.kind).filter((k) => k !== "none"));
  if (nonNoneKinds.size > 1) return null;

  const alpha = alphaToken !== undefined ? parseAlphaToken(alphaToken, allowNone) : 1;
  if (alpha === null) return null;

  return {
    color: themeRgbToOklch({ r: r.value, g: g.value, b: b.value }),
    alpha,
  };
}

function parseOklchAngle(token: string): number | null {
  const lower = token.toLowerCase();
  if (lower === "none") return 0;
  for (const [unit, scale] of [
    ["deg", 1],
    ["rad", 180 / Math.PI],
    ["grad", 0.9],
    ["turn", 360],
  ] as const) {
    if (lower.endsWith(unit)) {
      const n = parseCssNumber(token.slice(0, -unit.length));
      return n === null ? null : n * scale;
    }
  }
  return parseCssNumber(token);
}

function parseOklchFunction(body: string): ParsedThemeColor | null {
  if (body.includes(",")) return null;
  const slashParts = body.split("/");
  if (slashParts.length > 2) return null;
  const chParts = (slashParts[0] ?? "").trim().split(/\s+/);
  if (chParts.length !== 3) return null;

  const [lTok, cTok, hTok] = chParts as [string, string, string];

  let L: number | null;
  if (lTok.toLowerCase() === "none") L = 0;
  else if (lTok.endsWith("%")) {
    const n = parseCssNumber(lTok.slice(0, -1));
    L = n === null ? null : n / 100;
  } else {
    L = parseCssNumber(lTok);
  }

  let C: number | null;
  if (cTok.toLowerCase() === "none") C = 0;
  else if (cTok.endsWith("%")) {
    const n = parseCssNumber(cTok.slice(0, -1));
    C = n === null ? null : n * 0.004;
  } else {
    C = parseCssNumber(cTok);
  }

  const h = parseOklchAngle(hTok);

  let alpha: number | null = 1;
  if (slashParts.length === 2) {
    const right = (slashParts[1] ?? "").trim();
    if (!right || /\s/.test(right)) return null;
    alpha = parseAlphaToken(right, true);
  }

  if (L === null || C === null || h === null || alpha === null) return null;
  if (![L, C, h, alpha].every(Number.isFinite)) return null;

  return {
    color: {
      L: Math.min(1, Math.max(0, L)),
      C: Math.max(0, C),
      h,
    },
    alpha: Math.min(1, Math.max(0, alpha)),
  };
}

/**
 * Parses #rgb, #rgba, #rrggbb, #rrggbbaa, rgb()/rgba(), and oklch() into OKLCH.
 * Returns null for unsupported CSS colour forms or invalid syntax.
 */
export function parseThemeColor(value: unknown): ParsedThemeColor | null {
  if (typeof value !== "string") return null;
  const input = value.trim();
  if (!input) return null;

  const hex = /^#([0-9a-f]{3,4}|[0-9a-f]{6}|[0-9a-f]{8})$/i.exec(input);
  if (hex) {
    const digits = hex[1] ?? "";
    if (digits.length === 3 || digits.length === 4) {
      const r = Number.parseInt((digits[0] ?? "") + (digits[0] ?? ""), 16);
      const g = Number.parseInt((digits[1] ?? "") + (digits[1] ?? ""), 16);
      const b = Number.parseInt((digits[2] ?? "") + (digits[2] ?? ""), 16);
      const alpha =
        digits.length === 4 ? Number.parseInt((digits[3] ?? "") + (digits[3] ?? ""), 16) / 255 : 1;
      return { color: themeRgbToOklch({ r, g, b }), alpha };
    }
    const r = Number.parseInt(digits.slice(0, 2), 16);
    const g = Number.parseInt(digits.slice(2, 4), 16);
    const b = Number.parseInt(digits.slice(4, 6), 16);
    const alpha = digits.length === 8 ? Number.parseInt(digits.slice(6, 8), 16) / 255 : 1;
    return { color: themeRgbToOklch({ r, g, b }), alpha };
  }

  const rgbMatch = /^rgba?\((.+)\)$/is.exec(input);
  if (rgbMatch) return parseRgbFunction((rgbMatch[1] ?? "").trim());

  const oklchMatch = /^oklch\((.+)\)$/is.exec(input);
  if (oklchMatch) return parseOklchFunction((oklchMatch[1] ?? "").trim());

  return null;
}

function formatThemeColorNumber(value: number, precision: number): string {
  const rounded = Math.abs(value) < 10 ** -precision / 2 ? 0 : value;
  return rounded.toFixed(precision).replace(/(?:\.0+|(?:(\.[0-9]*?)0+))$/, "$1");
}

function formatOklchThemeColor(color: ThemeOklch, alpha = 1): string {
  const normalizedHue = color.C < 0.0000005 ? 0 : ((color.h % 360) + 360) % 360;
  const body = `${formatThemeColorNumber(color.L, 6)} ${formatThemeColorNumber(color.C, 6)} ${formatThemeColorNumber(normalizedHue, 3)}`;
  return alpha < 1 ? `oklch(${body} / ${formatThemeColorNumber(alpha, 4)})` : `oklch(${body})`;
}

export function toCanonicalThemeColor(value: unknown): string | null {
  const parsed = parseThemeColor(value);
  return parsed ? formatOklchThemeColor(parsed.color, parsed.alpha) : null;
}

function parseThemeRgbColor(value: string, fallback: ThemeRgbColor): ThemeRgbColor {
  const parsed = parseThemeColor(value);
  return parsed ? themeOklchToRgb(parsed.color) : fallback;
}

function themeRgbToThemeColor(color: ThemeRgbColor): string {
  return formatOklchThemeColor(themeRgbToOklch(color));
}

function mixThemeRgbColors(base: ThemeRgbColor, overlay: ThemeRgbColor, amount: number): ThemeRgbColor {
  return {
    r: base.r + (overlay.r - base.r) * amount,
    g: base.g + (overlay.g - base.g) * amount,
    b: base.b + (overlay.b - base.b) * amount,
  };
}

export function themeRelativeLuminance(color: ThemeRgbColor): number {
  const linearize = (channel: number) => {
    const normalized = channel / 255;
    return normalized <= 0.03928 ? normalized / 12.92 : ((normalized + 0.055) / 1.055) ** 2.4;
  };

  return 0.2126 * linearize(color.r) + 0.7152 * linearize(color.g) + 0.0722 * linearize(color.b);
}

function srgbChannelToLinear(channel: number): number {
  const c = channel / 255;
  return c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
}

function linearChannelToSrgb(channel: number, round = true): number {
  const c = channel <= 0.0031308 ? channel * 12.92 : 1.055 * channel ** (1 / 2.4) - 0.055;
  const srgb = Math.min(1, Math.max(0, c)) * 255;
  return round ? Math.round(srgb) : srgb;
}

function themeRgbToOklch(color: ThemeRgbColor): ThemeOklch {
  const r = srgbChannelToLinear(color.r);
  const g = srgbChannelToLinear(color.g);
  const b = srgbChannelToLinear(color.b);
  const l = Math.cbrt(0.4122214708 * r + 0.5363325363 * g + 0.0514459929 * b);
  const m = Math.cbrt(0.2119034982 * r + 0.6806995451 * g + 0.1073969566 * b);
  const s = Math.cbrt(0.0883024619 * r + 0.2817188376 * g + 0.6299787005 * b);
  const L = 0.2104542553 * l + 0.793617785 * m - 0.0040720468 * s;
  const a = 1.9779984951 * l - 2.428592205 * m + 0.4505937099 * s;
  const bb = 0.0259040371 * l + 0.7827717662 * m - 0.808675766 * s;
  return { L, C: Math.hypot(a, bb), h: (Math.atan2(bb, a) * 180) / Math.PI };
}

function oklchToRgbUnclamped({ L, C, h }: ThemeOklch): { r: number; g: number; b: number } {
  const hr = (h * Math.PI) / 180;
  const a = C * Math.cos(hr);
  const bb = C * Math.sin(hr);
  const l = (L + 0.3963377774 * a + 0.2158037573 * bb) ** 3;
  const m = (L - 0.1055613458 * a - 0.0638541728 * bb) ** 3;
  const s = (L - 0.0894841775 * a - 1.291485548 * bb) ** 3;
  return {
    r: 4.0767416621 * l - 3.3077115913 * m + 0.2309699292 * s,
    g: -1.2684380046 * l + 2.6097574011 * m - 0.3413193965 * s,
    b: -0.0041960863 * l - 0.7034186147 * m + 1.707614701 * s,
  };
}

function mapThemeOklchToSrgbGamut(color: ThemeOklch): ThemeOklch {
  const isInGamut = (C: number) => {
    const linear = oklchToRgbUnclamped({ ...color, C });
    return [linear.r, linear.g, linear.b].every((channel) => channel >= -0.0001 && channel <= 1.0001);
  };
  if (isInGamut(color.C)) return color;

  let low = 0;
  let high = color.C;
  const chromaResolution = 0.000001;
  const steps = Math.max(
    1,
    Math.ceil(Math.log2(Math.max(color.C, chromaResolution)) - Math.log2(chromaResolution)),
  );
  for (let step = 0; step < steps; step += 1) {
    const mid = (low + high) / 2;
    if (isInGamut(mid)) low = mid;
    else high = mid;
  }
  return { ...color, C: low };
}

export function themeOklchToRgb(color: ThemeOklch, round = true): ThemeRgbColor {
  const linear = oklchToRgbUnclamped(mapThemeOklchToSrgbGamut(color));
  return {
    r: linearChannelToSrgb(linear.r, round),
    g: linearChannelToSrgb(linear.g, round),
    b: linearChannelToSrgb(linear.b, round),
  };
}

function themeOklchToThemeColor(color: ThemeOklch): string {
  return formatOklchThemeColor(mapThemeOklchToSrgbGamut(color));
}

function solveOklchLightness(
  base: ThemeOklch,
  against: ReadonlyArray<ThemeRgbColor>,
  minContrast: number,
  direction: "lighter" | "darker",
): ThemeOklch {
  const contrastOf = (color: ThemeOklch) =>
    Math.min(...against.map((surface) => themeContrastRatio(themeOklchToRgb(color), surface)));
  let low = direction === "lighter" ? base.L : 0;
  let high = direction === "lighter" ? 1 : base.L;
  let candidate = { ...base };
  if (contrastOf(candidate) >= minContrast) return candidate;
  for (let step = 0; step < 18; step += 1) {
    const mid = (low + high) / 2;
    candidate = { ...base, L: mid };
    if (contrastOf(candidate) >= minContrast) {
      if (direction === "lighter") high = mid;
      else low = mid;
    } else {
      if (direction === "lighter") low = mid;
      else high = mid;
    }
  }
  return { ...base, L: direction === "lighter" ? high : low };
}

const STANDARD_STATUS_COLORS = {
  light: {
    error: "#fb2c36",
    errorForeground: "#c10007",
    warning: "#fe9a00",
    warningForeground: "#bb4d00",
  },
  dark: {
    error: "#fb414a",
    errorForeground: "#ff6467",
    warning: "#fe9a00",
    warningForeground: "#ffb900",
  },
} as const;

function standardStatusColors(canvas: ThemeRgbColor): {
  error: string;
  errorForeground: string;
  errorSurface: string;
  warning: string;
  warningForeground: string;
  warningSurface: string;
} {
  const appearance: T3Appearance = themeRelativeLuminance(canvas) < 0.179 ? "dark" : "light";
  const standard = STANDARD_STATUS_COLORS[appearance];
  const surfaceMix = appearance === "dark" ? 0.16 : 0.08;
  const surfaceOf = (value: string) =>
    mixThemeRgbColors(canvas, parseThemeRgbColor(value, canvas), surfaceMix);
  const readableOn = (foreground: string, surface: ThemeRgbColor) => {
    const oklch = themeRgbToOklch(parseThemeRgbColor(foreground, canvas));
    const solved = solveOklchLightness(oklch, [surface], 4.6, appearance === "dark" ? "lighter" : "darker");
    return themeOklchToThemeColor(solved);
  };
  const errorSurface = surfaceOf(standard.error);
  const warningSurface = surfaceOf(standard.warning);
  return {
    error: toCanonicalThemeColor(standard.error) ?? standard.error,
    errorForeground: readableOn(standard.errorForeground, errorSurface),
    errorSurface: themeRgbToThemeColor(errorSurface),
    warning: toCanonicalThemeColor(standard.warning) ?? standard.warning,
    warningForeground: readableOn(standard.warningForeground, warningSurface),
    warningSurface: themeRgbToThemeColor(warningSurface),
  };
}

export function createVividThemeColors(
  appearance: T3Appearance,
  backgroundValue: string,
  accentValue: string,
): Record<T3ColorRole, string> {
  const canvasRgb = parseThemeRgbColor(
    backgroundValue,
    appearance === "dark" ? { r: 24, g: 15, b: 27 } : { r: 250, g: 245, b: 250 },
  );
  const accentRgb = parseThemeRgbColor(accentValue, { r: 168, g: 67, b: 112 });
  const canvas = themeRgbToOklch(canvasRgb);
  const accent = themeRgbToOklch(accentRgb);
  // At luminance 0.179, black and white text have equal contrast.
  const dark = themeRelativeLuminance(canvasRgb) < 0.179;
  const hue = accent.C < 0.02 ? canvas.h : accent.h;
  const tintC = Math.min(0.045, Math.max(0.008, accent.C * 0.22));
  const step = dark ? 1 : -1;

  const surfaceAt = (deltaL: number, chroma = tintC): ThemeOklch => ({
    L: Math.min(0.98, Math.max(0.05, canvas.L + step * deltaL)),
    C: chroma,
    h: hue,
  });
  const themeColor = (color: ThemeOklch) => themeOklchToThemeColor(color);

  const textBase: ThemeOklch = {
    L: dark ? 0.95 : 0.2,
    C: Math.min(0.035, accent.C * 0.25),
    h: hue,
  };
  const text = solveOklchLightness(textBase, [canvasRgb], 7, dark ? "lighter" : "darker");
  const textRgb = themeOklchToRgb(text);
  const textMutedRgb = standardMutedThemeText(canvasRgb, textRgb);

  const action: ThemeOklch = {
    L: Math.min(0.85, Math.max(0.35, accent.L + (dark ? 0.06 : -0.02))),
    C: Math.max(accent.C * 0.9, 0.06),
    h: (hue + 50) % 360,
  };
  const actionRgb = themeOklchToRgb(action);
  const actionForeground = readableThemeForeground(actionRgb);
  const accentForeground = readableThemeForeground(accentRgb);

  const sidebar = surfaceAt(0.045, tintC * 1.4);
  const sidebarRgb = themeOklchToRgb(sidebar);
  const surface = surfaceAt(0.015);
  const surfaceRaised = surfaceAt(0.05);
  const surfaceRaisedRgb = themeOklchToRgb(surfaceRaised);
  const surfaceOverlay = surfaceAt(0.075);
  const border = surfaceAt(dark ? 0.16 : 0.12, Math.min(0.07, accent.C * 0.35));
  const input = surfaceAt(dark ? 0.21 : 0.16, Math.min(0.08, accent.C * 0.4));
  const secondary = surfaceAt(dark ? 0.1 : 0.06, Math.min(0.09, accent.C * 0.5));
  const secondaryRgb = themeOklchToRgb(secondary);
  const muted = surfaceAt(dark ? 0.06 : 0.04, Math.min(0.06, accent.C * 0.35));
  const mutedRgb = themeOklchToRgb(muted);
  const accentSurface = surfaceAt(dark ? 0.13 : 0.08, Math.min(0.11, accent.C * 0.55));
  const accentSurfaceRgb = themeOklchToRgb(accentSurface);
  const messageSurface = surfaceAt(dark ? 0.16 : 0.1, Math.min(0.13, accent.C * 0.6));
  const messageSurfaceRgb = themeOklchToRgb(messageSurface);
  const codeBackground = surfaceAt(0.035, tintC * 0.8);
  const updateSurface = surfaceAt(dark ? 0.14 : 0.09, Math.min(0.12, accent.C * 0.55));

  const foregroundOn = (surfaceRgb: ThemeRgbColor): string =>
    themeOklchToThemeColor(solveOklchLightness(textBase, [surfaceRgb], 4.6, dark ? "lighter" : "darker"));
  const mutedForeground = themeRgbToThemeColor(readableThemeText(mutedRgb, textRgb, 1, 4.6));
  const placeholder = themeRgbToThemeColor(readableThemeText(surfaceRaisedRgb, textRgb, 1, 4.6));

  const searchMatch = themeOklchToRgb(surfaceAt(0.22, Math.min(0.12, accent.C)));
  const searchMatchActive = themeOklchToRgb(surfaceAt(0.4, Math.min(0.18, accent.C)));

  const actionHover: ThemeOklch = { ...action, L: action.L + (dark ? 0.06 : -0.06) };

  return {
    ...standardStatusColors(canvasRgb),
    canvas: themeRgbToThemeColor(canvasRgb),
    chrome: themeRgbToThemeColor(canvasRgb),
    toolbar: themeRgbToThemeColor(canvasRgb),
    toolbarForeground: themeRgbToThemeColor(textRgb),
    toolbarBorder: themeColor(surfaceAt(dark ? 0.14 : 0.1, Math.min(0.08, accent.C * 0.4))),
    toolbarControl: themeColor(surfaceAt(dark ? 0.09 : 0.05, tintC * 1.3)),
    toolbarControlForeground: themeRgbToThemeColor(textRgb),
    toolbarControlHover: themeColor(surfaceAt(dark ? 0.14 : 0.09, tintC * 1.6)),
    surface: themeColor(surface),
    surfaceRaised: themeColor(surfaceRaised),
    surfaceOverlay: themeColor(surfaceOverlay),
    text: themeRgbToThemeColor(textRgb),
    textMuted: themeRgbToThemeColor(textMutedRgb),
    border: themeColor(border),
    input: themeColor(input),
    focus: themeRgbToThemeColor(accentRgb),
    accent: themeRgbToThemeColor(accentRgb),
    accentForeground: themeRgbToThemeColor(accentForeground),
    secondary: themeColor(secondary),
    secondaryForeground: foregroundOn(secondaryRgb),
    muted: themeColor(muted),
    mutedForeground,
    placeholder,
    secondaryLabel: themeRgbToThemeColor(textMutedRgb),
    iconMuted: themeRgbToThemeColor(textMutedRgb),
    update: themeRgbToThemeColor(accentRgb),
    updateForeground: foregroundOn(themeOklchToRgb(updateSurface)),
    updateSurface: themeColor(updateSurface),
    accentSurface: themeColor(accentSurface),
    accentSurfaceForeground: foregroundOn(accentSurfaceRgb),
    messageSurface: themeColor(messageSurface),
    messageForeground: foregroundOn(messageSurfaceRgb),
    messageAction: themeRgbToThemeColor(actionRgb),
    messageActionForeground: themeRgbToThemeColor(actionForeground),
    messageActionHover: themeColor(actionHover),
    codeBackground: themeColor(codeBackground),
    codeForeground: themeRgbToThemeColor(textRgb),
    searchMatchBackground: themeRgbToThemeColor(searchMatch),
    searchMatchForeground: themeRgbToThemeColor(readableThemeForeground(searchMatch)),
    searchMatchActiveBackground: themeRgbToThemeColor(searchMatchActive),
    searchMatchActiveForeground: themeRgbToThemeColor(readableThemeForeground(searchMatchActive)),
    sidebar: themeColor(sidebar),
    sidebarForeground: foregroundOn(sidebarRgb),
    sidebarMutedForeground: themeRgbToThemeColor(standardMutedThemeText(sidebarRgb, textRgb)),
    sidebarControlSurface: themeColor(surfaceAt(dark ? 0.1 : 0.07, tintC * 1.5)),
    sidebarRowHover: themeColor(surfaceAt(dark ? 0.08 : 0.06, Math.min(0.08, accent.C * 0.45))),
    sidebarRowActive: themeColor(surfaceAt(dark ? 0.12 : 0.09, Math.min(0.1, accent.C * 0.55))),
    sidebarRowSelected: themeColor(surfaceAt(dark ? 0.14 : 0.1, Math.min(0.11, accent.C * 0.6))),
    sidebarBorder: themeColor(surfaceAt(dark ? 0.17 : 0.12, Math.min(0.08, accent.C * 0.4))),
    terminalBackground: themeRgbToThemeColor(canvasRgb),
    terminalForeground: themeRgbToThemeColor(textRgb),
    terminalCursor: themeRgbToThemeColor(accentRgb),
    terminalSelection: themeColor(surfaceAt(dark ? 0.18 : 0.12, Math.min(0.12, accent.C * 0.55))),
    terminalScrollbar: themeColor(surfaceAt(dark ? 0.22 : 0.16, tintC)),
    terminalScrollbarHover: themeColor(surfaceAt(dark ? 0.3 : 0.22, tintC)),
  };
}

function themeContrastRatio(first: ThemeRgbColor, second: ThemeRgbColor): number {
  const firstLuminance = themeRelativeLuminance(first);
  const secondLuminance = themeRelativeLuminance(second);
  const lighter = Math.max(firstLuminance, secondLuminance);
  const darker = Math.min(firstLuminance, secondLuminance);
  return (lighter + 0.05) / (darker + 0.05);
}

function readableThemeForeground(background: ThemeRgbColor): ThemeRgbColor {
  const lightContrast = themeContrastRatio(background, THEME_LIGHT_FOREGROUND);
  const darkContrast = themeContrastRatio(background, THEME_DARK_FOREGROUND);
  if (Math.max(lightContrast, darkContrast) >= 4.5) {
    return lightContrast >= darkContrast ? THEME_LIGHT_FOREGROUND : THEME_DARK_FOREGROUND;
  }

  return themeContrastRatio(background, THEME_WHITE_FOREGROUND) >=
    themeContrastRatio(background, THEME_BLACK_FOREGROUND)
    ? THEME_WHITE_FOREGROUND
    : THEME_BLACK_FOREGROUND;
}

function readableThemeText(
  background: ThemeRgbColor,
  foreground: ThemeRgbColor,
  amount: number,
  minimumRatio: number,
): ThemeRgbColor {
  const softened = mixThemeRgbColors(foreground, background, amount);
  if (themeContrastRatio(softened, background) >= minimumRatio) return softened;

  let readable = foreground;
  let lowerAmount = 0;
  let upperAmount = amount;
  for (let index = 0; index < 12; index += 1) {
    const candidateAmount = (lowerAmount + upperAmount) / 2;
    const candidate = mixThemeRgbColors(foreground, background, candidateAmount);
    if (themeContrastRatio(candidate, background) >= minimumRatio) {
      readable = candidate;
      lowerAmount = candidateAmount;
    } else {
      upperAmount = candidateAmount;
    }
  }
  return readable;
}

const STANDARD_LIGHT_MUTED_CONTRAST = 4.705;
const STANDARD_DARK_MUTED_CONTRAST = 5.082;

function standardMutedThemeText(background: ThemeRgbColor, foreground: ThemeRgbColor): ThemeRgbColor {
  const target =
    themeRelativeLuminance(background) < 0.179 ? STANDARD_DARK_MUTED_CONTRAST : STANDARD_LIGHT_MUTED_CONTRAST;
  return readableThemeText(background, foreground, 1, target);
}

// Reads a theme file by the contract in themes/README.md: refuses a file the UI cannot use at all,
// and keeps what it can of the rest, with a warning for each part it ignores.

import {
  APPEARANCES,
  isLayoutValue,
  isToken,
  LAYOUT,
  LAYOUT_OPTIONS,
  type Layout,
  MAX_CSS_BYTES,
  MAX_THEME_BYTES,
  type T3ThemeData,
  THEME_VERSION,
  type Theme,
  type ThemeAppearance,
  type TokenName,
  tokenValueProblem,
} from "./contract";
import { readT3Roles, T3_ID_PATTERN, type T3Appearance, type T3RoleMap, themeIdFromName } from "./t3";
import {
  createVividThemeColors,
  parseThemeColor,
  themeOklchToRgb,
  themeRelativeLuminance,
} from "./t3palette";

export type ThemeResult = { ok: true; theme: Theme; warnings: string[] } | { ok: false; error: string };

const ID_PART = /^[a-z0-9-]{1,64}$/;
const PLUXY_KNOWN_FIELDS = new Set([
  "theme_version",
  "id",
  "name",
  "description",
  "appearance",
  "tokens",
  "dark",
  "layout",
  "css",
  "canvas",
  "accent",
  "colors",
  "variants",
]);

const T3_KNOWN_FIELDS = new Set([
  "version",
  "id",
  "name",
  "description",
  "appearance",
  "canvas",
  "accent",
  "colors",
  "variants",
  "collection",
  "managed",
  "tokens",
  "dark",
  "layout",
  "css",
]);

const bytes = (s: string) => new TextEncoder().encode(s).length;

function isObject(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

export function validId(id: unknown): id is string {
  if (typeof id !== "string") return false;
  const parts = id.split("/");
  return parts.length === 2 && parts.every((p) => ID_PART.test(p));
}

/** Returns why custom CSS is dropped, or undefined when it can be used. */
export function cssProblem(css: string): string | undefined {
  if (bytes(css) > MAX_CSS_BYTES) return `is over ${MAX_CSS_BYTES / 1024} KiB`;
  if (/@import/i.test(css)) return "uses @import, which themes may not";
  return undefined;
}

function readTokens(field: string, raw: unknown, warnings: string[]): Partial<Record<TokenName, string>> {
  const out: Partial<Record<TokenName, string>> = {};
  if (raw === undefined) return out;
  if (!isObject(raw)) {
    warnings.push(`${field} is not an object; ignored`);
    return out;
  }
  for (const [name, value] of Object.entries(raw)) {
    if (!isToken(name)) {
      warnings.push(`${field}: unknown token ${name}; ignored`);
      continue;
    }
    const problem = tokenValueProblem(name, value);
    if (problem) {
      warnings.push(`${field}: ${name} ${problem}; the default is kept`);
      continue;
    }
    out[name] = value as string;
  }
  return out;
}

function readLayout(raw: unknown, warnings: string[]): Partial<Layout> {
  const out: Partial<Layout> = {};
  if (raw === undefined) return out;
  if (!isObject(raw)) {
    warnings.push("layout is not an object; ignored");
    return out;
  }
  for (const [option, value] of Object.entries(raw)) {
    if (!(LAYOUT_OPTIONS as string[]).includes(option)) {
      warnings.push(`layout: unknown option ${option}; ignored`);
      continue;
    }
    const o = option as keyof Layout;
    if (!isLayoutValue(o, value)) {
      warnings.push(`layout: ${option} has no value ${JSON.stringify(value)}; using ${LAYOUT[o][0]}`);
      continue;
    }
    (out as Record<string, string>)[o] = value;
  }
  return out;
}

function inferPluxyBaseAppearance(raw: Record<string, unknown>): T3Appearance {
  if (isObject(raw.variants)) {
    const hasLight = isObject(raw.variants.light);
    const hasDark = isObject(raw.variants.dark);
    if (hasDark && !hasLight) return "light";
    if (hasLight && !hasDark) return "dark";
  }
  const parsed = parseThemeColor(raw.canvas);
  if (parsed) {
    return themeRelativeLuminance(themeOklchToRgb(parsed.color)) < 0.179 ? "dark" : "light";
  }
  return "light";
}

function readT3Data(
  raw: Record<string, unknown>,
  baseAppearance: T3Appearance,
  warnings: string[],
  allowedAppearance: ThemeAppearance,
): T3ThemeData | undefined {
  const hasAnyT3Field =
    raw.canvas !== undefined ||
    raw.accent !== undefined ||
    raw.colors !== undefined ||
    raw.variants !== undefined;
  if (!hasAnyT3Field) return undefined;

  let generated: T3RoleMap | undefined;
  if (raw.canvas !== undefined && raw.accent !== undefined) {
    const canvasParsed = parseThemeColor(raw.canvas);
    const accentParsed = parseThemeColor(raw.accent);
    if (canvasParsed && accentParsed) {
      generated = createVividThemeColors(baseAppearance, raw.canvas as string, raw.accent as string);
    } else {
      warnings.push("canvas and accent must use hex, rgb() or oklch(); generated palette skipped");
    }
  } else if (raw.canvas !== undefined || raw.accent !== undefined) {
    warnings.push(
      raw.canvas !== undefined
        ? "canvas requires accent to generate a palette; ignored"
        : "accent requires canvas to generate a palette; ignored",
    );
  }

  let colors: T3RoleMap | undefined;
  if (raw.colors !== undefined) {
    colors = readT3Roles("colors", raw.colors, warnings);
  }

  const variants: Partial<Record<T3Appearance, T3RoleMap>> = {};
  if (raw.variants !== undefined) {
    if (!isObject(raw.variants)) {
      warnings.push("variants is not an object; ignored");
    } else {
      for (const [key, value] of Object.entries(raw.variants)) {
        if (key !== "light" && key !== "dark") {
          warnings.push(`variants: unknown appearance ${key}; ignored`);
          continue;
        }
        if (key === baseAppearance) {
          warnings.push(`variants: ${key} repeats the file's appearance; ignored`);
          continue;
        }
        if (allowedAppearance !== "both" && key !== allowedAppearance) {
          warnings.push(`variants.${key} is ignored in a ${allowedAppearance} theme`);
          continue;
        }
        if (!isObject(value)) {
          warnings.push(`variants.${key} is not an object; ignored`);
          continue;
        }
        variants[key] = readT3Roles(`variants.${key}`, value, warnings);
      }
    }
  }

  const data: T3ThemeData = { appearance: baseAppearance };
  if (generated) data.generated = generated;
  if (colors) data.colors = colors;
  if (Object.keys(variants).length > 0) data.variants = variants;
  return data;
}

function readSharedOptionalFields(raw: Record<string, unknown>, theme: Theme, warnings: string[]) {
  if (raw.description !== undefined) {
    if (typeof raw.description === "string" && raw.description.length <= 200)
      theme.description = raw.description;
    else warnings.push("description must be up to 200 characters; ignored");
  }
  if (raw.dark !== undefined) {
    if (theme.appearance === "light") warnings.push("dark is ignored in a light theme");
    else theme.dark = readTokens("dark", raw.dark, warnings);
  }
  if (raw.css !== undefined) {
    if (typeof raw.css !== "string") warnings.push("css is not a string; ignored");
    else {
      const problem = cssProblem(raw.css);
      if (problem) warnings.push(`css ${problem}; custom CSS dropped`);
      else theme.css = raw.css;
    }
  }
}

/** Reads a theme from its file's text. */
export function parseThemeText(text: string): ThemeResult {
  if (bytes(text) > MAX_THEME_BYTES)
    return { ok: false, error: `the file is over ${MAX_THEME_BYTES / 1024} KiB` };
  let raw: unknown;
  try {
    raw = JSON.parse(text);
  } catch {
    return { ok: false, error: "the file is not JSON" };
  }
  return parseTheme(raw);
}

/** Reads a theme from its parsed JSON. */
export function parseTheme(raw: unknown): ThemeResult {
  if (!isObject(raw)) return { ok: false, error: "the file is not a JSON object" };
  const version = raw.theme_version;
  if (version === undefined) {
    return parseT3Theme(raw);
  }
  if (typeof version !== "number" || !Number.isInteger(version) || version < 0) {
    return { ok: false, error: "theme_version is missing or not a whole number" };
  }
  if (version > THEME_VERSION) {
    return { ok: false, error: `theme_version ${version} is newer than this UI supports (${THEME_VERSION})` };
  }
  if (!validId(raw.id)) {
    return { ok: false, error: "id must be publisher/name, each part 1 to 64 of a-z, 0-9 and -" };
  }
  if (typeof raw.name !== "string" || raw.name.length < 1 || raw.name.length > 64) {
    return { ok: false, error: "name must be 1 to 64 characters" };
  }

  const warnings: string[] = [];
  for (const field of Object.keys(raw)) {
    if (!PLUXY_KNOWN_FIELDS.has(field)) warnings.push(`unknown field ${field}; ignored`);
  }
  let appearance: ThemeAppearance = "both";
  if (raw.appearance !== undefined) {
    if ((APPEARANCES as readonly unknown[]).includes(raw.appearance))
      appearance = raw.appearance as ThemeAppearance;
    else warnings.push(`appearance has no value ${JSON.stringify(raw.appearance)}; using both`);
  }
  const theme: Theme = {
    id: raw.id,
    name: raw.name,
    appearance,
    tokens: readTokens("tokens", raw.tokens, warnings),
    layout: readLayout(raw.layout, warnings),
  };
  readSharedOptionalFields(raw, theme, warnings);
  const baseT3Appearance: T3Appearance = appearance === "both" ? inferPluxyBaseAppearance(raw) : appearance;
  const t3 = readT3Data(raw, baseT3Appearance, warnings, appearance);
  if (t3) theme.t3 = t3;
  return { ok: true, theme, warnings };
}

function parseT3Theme(raw: Record<string, unknown>): ThemeResult {
  const name = raw.name;
  const appearanceRaw = raw.appearance;
  const hasColorsOrSeeds = isObject(raw.colors) || (raw.canvas !== undefined && raw.accent !== undefined);
  const named = typeof name === "string" && name.trim().length > 0;

  // A T3 Code file with no colours: say what it lacks, not that theme_version is missing.
  if (named && (appearanceRaw === "light" || appearanceRaw === "dark") && !hasColorsOrSeeds) {
    return { ok: false, error: "a T3 Code theme file needs a colors object, or both canvas and accent" };
  }
  if (
    typeof name !== "string" ||
    name.trim().length === 0 ||
    (appearanceRaw !== "light" && appearanceRaw !== "dark") ||
    !hasColorsOrSeeds
  ) {
    return { ok: false, error: "theme_version is missing or not a whole number" };
  }
  if (name.length > 64) {
    return { ok: false, error: "name must be 1 to 64 characters" };
  }
  if (raw.version !== undefined && raw.version !== 1) {
    return { ok: false, error: `unsupported T3 Code theme version ${JSON.stringify(raw.version)}` };
  }

  const warnings: string[] = [];
  for (const field of Object.keys(raw)) {
    if (!T3_KNOWN_FIELDS.has(field)) warnings.push(`unknown field ${field}; ignored`);
  }

  let slug: string;
  if (typeof raw.id === "string" && T3_ID_PATTERN.test(raw.id)) {
    slug = raw.id;
  } else {
    if (raw.id !== undefined) warnings.push("id is not a valid T3 Code theme id; derived from name");
    slug = themeIdFromName(name);
  }

  const baseAppearance: T3Appearance = appearanceRaw;
  const t3 = readT3Data(raw, baseAppearance, warnings, "both");
  const otherAppearance: T3Appearance = baseAppearance === "light" ? "dark" : "light";
  const appearance: ThemeAppearance = t3?.variants?.[otherAppearance] ? "both" : baseAppearance;

  const theme: Theme = {
    id: `t3/${slug}`,
    name,
    appearance,
    tokens: readTokens("tokens", raw.tokens, warnings),
    layout: readLayout(raw.layout, warnings),
  };
  if (t3) theme.t3 = t3;
  readSharedOptionalFields(raw, theme, warnings);
  return { ok: true, theme, warnings };
}

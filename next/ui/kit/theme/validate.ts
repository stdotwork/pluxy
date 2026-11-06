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
  THEME_VERSION,
  type Theme,
  type ThemeAppearance,
  type TokenName,
  tokenValueProblem,
} from "./contract";

export type ThemeResult = { ok: true; theme: Theme; warnings: string[] } | { ok: false; error: string };

const ID_PART = /^[a-z0-9-]{1,64}$/;
const KNOWN_FIELDS = new Set([
  "theme_version",
  "id",
  "name",
  "description",
  "appearance",
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
    if (!KNOWN_FIELDS.has(field)) warnings.push(`unknown field ${field}; ignored`);
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
  if (raw.description !== undefined) {
    if (typeof raw.description === "string" && raw.description.length <= 200)
      theme.description = raw.description;
    else warnings.push("description must be up to 200 characters; ignored");
  }
  if (raw.dark !== undefined) {
    if (appearance === "light") warnings.push("dark is ignored in a light theme");
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
  return { ok: true, theme, warnings };
}

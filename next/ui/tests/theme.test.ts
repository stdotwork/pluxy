import { describe, expect, test } from "bun:test";
import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { LAYOUT, MAX_THEME_BYTES, PARTS, THEME_VERSION, TOKENS, tokenProperty } from "../kit/theme/contract";
import { type Choices, DEFAULT_CHOICES, resolve, tokensCss } from "../kit/theme/engine";
import { parseTheme, parseThemeText } from "../kit/theme/validate";

const themesDir = join(import.meta.dir, "..", "themes");
const readme = readFileSync(join(themesDir, "README.md"), "utf8");
const env = { systemDark: false, reducedMotion: false };
const base = { theme_version: 0, id: "test/t", name: "T" };

function ok(raw: unknown) {
  const r = parseTheme(raw);
  if (!r.ok) throw new Error(r.error);
  return r;
}

/** Rows of the README tables whose first cell is a code span. */
function rows(section: string): string[][] {
  const start = readme.indexOf(section);
  const end = readme.indexOf("\n## ", start + 1);
  return readme
    .slice(start, end)
    .split("\n")
    .filter((l) => /^\| `/.test(l))
    .map((l) =>
      l
        .split("|")
        .slice(1, -1)
        .map((c) => c.trim()),
    );
}

describe("the README and the code agree", () => {
  test("every token, with its defaults", () => {
    const documented = rows("## Tokens").map(([name, , defaults]) => {
      const [light, dark] = (defaults ?? "").split("` / `").map((d) => d.replace(/^`|`$/g, ""));
      return { name: name?.replace(/`/g, ""), light, dark };
    });
    const coded = Object.entries(TOKENS).map(([name, d]) => ({
      name,
      light: d.light,
      dark: "dark" in d ? d.dark : undefined,
    }));
    expect(documented).toEqual(coded);
  });

  test("every layout option and its values, default first", () => {
    const documented = Object.fromEntries(
      rows("## Layout options").map(([option, values]) => [
        option?.replace(/`/g, ""),
        [...(values ?? "").matchAll(/`([^`]+)`/g)].map((m) => m[1]),
      ]),
    );
    expect(documented).toEqual(LAYOUT as unknown as Record<string, string[]>);
  });

  test("every data-part hook", () => {
    expect(PARTS).toContain("log");
    expect(PARTS).toContain("log-line");
    expect(readme).toContain("`data-level` is `info`, `warn`");
    const documented = rows("### Hooks you can rely on")
      .map(([hook]) => hook?.match(/^`data-part="([^"]+)"`/)?.[1])
      .filter((h) => h !== undefined);
    expect(documented).toEqual([...PARTS]);
  });

  test("account sign-in hooks are part of the documented contract", () => {
    for (const part of ["method", "flow", "device-code"] as const) {
      expect(PARTS).toContain(part);
      expect(readme).toContain(`\`data-part="${part}"\``);
    }
    expect(readme).toContain("`add-account`");
  });

  test("the version the README describes", () => {
    expect(readme).toContain(`# Theme contract, version ${THEME_VERSION}`);
  });
});

describe("built-in themes", () => {
  for (const file of readdirSync(themesDir).filter((f) => f.endsWith(".json"))) {
    test(`${file} is valid with no warnings`, () => {
      const r = parseThemeText(readFileSync(join(themesDir, file), "utf8"));
      expect(r.ok && r.warnings).toEqual([]);
    });
  }
});

describe("reading a theme", () => {
  test("accepts tabs side and diff color tokens", () => {
    const r = ok({
      ...base,
      tokens: { "color.diff-add": "#1f7a3d", "color.diff-remove": "#b42318" },
      dark: { "color.diff-add": "#5cc98a", "color.diff-remove": "#ff7b72" },
      layout: { tabs: "side" },
    });
    expect(r.theme.tokens).toEqual({ "color.diff-add": "#1f7a3d", "color.diff-remove": "#b42318" });
    expect(r.theme.dark).toEqual({ "color.diff-add": "#5cc98a", "color.diff-remove": "#ff7b72" });
    expect(r.theme.layout).toEqual({ tabs: "side" });
    expect(r.warnings).toEqual([]);
  });

  test("refuses what the UI cannot use at all", () => {
    for (const [raw, why] of [
      [[], "not a JSON object"],
      [{ ...base, theme_version: THEME_VERSION + 1 }, "newer than this UI supports"],
      [{ ...base, theme_version: "0" }, "theme_version"],
      [{ ...base, id: "Upper/case" }, "id must be"],
      [{ ...base, id: "no-slash" }, "id must be"],
      [{ ...base, name: "" }, "name must be"],
    ] as const) {
      const r = parseTheme(raw);
      expect(r.ok).toBe(false);
      if (!r.ok) expect(r.error).toContain(why);
    }
    expect(parseThemeText("{").ok).toBe(false);
    const huge = JSON.stringify({ ...base, description: "x".repeat(MAX_THEME_BYTES) });
    expect(parseThemeText(huge).ok).toBe(false);
  });

  test("keeps what it can, with a warning for each part it ignores", () => {
    const r = ok({
      ...base,
      future: true,
      appearance: "sepia",
      tokens: {
        "color.bg": "#000",
        "color.text": "red; } body { display: none",
        "color.accent": "url(https://example.com/x)",
        "font.body": "x /* y",
        "color.unknown": "#fff",
        density: "3",
      },
      layout: { nav: "floating", list: "cards", sideways: true },
      css: '@import "https://example.com/x.css";',
    });
    expect(r.theme.tokens).toEqual({ "color.bg": "#000" });
    expect(r.theme.layout).toEqual({ list: "cards" });
    expect(r.theme.appearance).toBe("both");
    expect(r.theme.css).toBeUndefined();
    expect(r.warnings).toHaveLength(10);
  });

  test("a light theme ignores dark", () => {
    const r = ok({ ...base, appearance: "light", dark: { "color.bg": "#000" } });
    expect(r.theme.dark).toBeUndefined();
    expect(r.warnings).toEqual(["dark is ignored in a light theme"]);
  });
});

describe("resolving", () => {
  test("quota meter tokens follow a theme's colours and accept overrides", () => {
    expect(PARTS).toContain("meter");
    const custom = ok({
      ...base,
      tokens: { "color.meter-track": "#123456", "color.meter-fill": "#abcdef" },
    }).theme;
    const resolved = resolve(custom, DEFAULT_CHOICES, env);
    expect(resolved.tokens["color.meter-track"]).toBe("#123456");
    expect(resolved.tokens["color.meter-fill"]).toBe("#abcdef");
    expect(resolve(ok(base).theme, DEFAULT_CHOICES, env).tokens["color.meter-fill"]).toBe(
      "var(--px-color-accent)",
    );
  });
  const theme = ok({
    ...base,
    tokens: { "color.bg": "#111111", density: "0.75" },
    dark: { "color.bg": "#000000" },
    layout: { nav: "rail", list: "cards" },
    css: "a{}",
  }).theme;
  const choose = (c: Partial<Choices>): Choices => ({ ...DEFAULT_CHOICES, ...c });

  test("tokens: defaults, then tokens, then dark", () => {
    expect(resolve(theme, choose({}), env).tokens["color.bg"]).toBe("#111111");
    const dark = resolve(theme, choose({ appearance: "dark" }), env);
    expect(dark.appearance).toBe("dark");
    expect(dark.tokens["color.bg"]).toBe("#000000");
    expect(dark.tokens["color.text"]).toBe(TOKENS["color.text"].dark);
    expect(resolve(theme, choose({}), { ...env, systemDark: true }).appearance).toBe("dark");
  });

  test("a single-appearance theme ignores the person's appearance", () => {
    const night = ok({ ...base, appearance: "dark" }).theme;
    expect(resolve(night, choose({ appearance: "light" }), env).appearance).toBe("dark");
  });

  test("the person's layout and density override the theme's", () => {
    const r = resolve(theme, choose({ layout: { nav: "top" }, density: "1.25" }), env);
    expect(r.layout).toEqual({ nav: "top", list: "cards", detail: "side", width: "wide", tabs: "top" });
    expect(r.tokens.density).toBe("1.25");
    expect(resolve(theme, choose({ density: "9" }), env).warnings).toHaveLength(1);
  });

  test("custom CSS: theme's then the person's; none in safe mode", () => {
    expect(resolve(theme, choose({ css: "b{}" }), env).css).toEqual(["a{}", "b{}"]);
    expect(resolve(theme, choose({ css: "b{}", safe: true }), env).css).toEqual([]);
    const bad = resolve(theme, choose({ css: "@import 'x';" }), env);
    expect(bad.css).toEqual(["a{}"]);
    expect(bad.warnings).toHaveLength(1);
  });

  test("reduced motion stops transitions", () => {
    expect(resolve(theme, choose({}), { ...env, reducedMotion: true }).tokens["motion.duration"]).toBe("0ms");
  });

  test("tokens become custom properties", () => {
    expect(tokenProperty("color.surface-raised")).toBe("--px-color-surface-raised");
    expect(tokensCss({ "color.bg": "#fff", density: "1" })).toBe(":root{--px-color-bg:#fff;--px-density:1;}");
  });
});

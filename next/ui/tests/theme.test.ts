import { describe, expect, test } from "bun:test";
import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import {
  LAYOUT,
  MAX_THEME_BYTES,
  PARTS,
  SLOTS,
  THEME_VERSION,
  TOKENS,
  tokenProperty,
  tokenValueProblem,
} from "../kit/theme/contract";
import { type Choices, DEFAULT_CHOICES, resolve, ThemeApplier, tokensCss } from "../kit/theme/engine";
import {
  APP_THEME_VARIABLES,
  T3_COLOR_ROLES,
  T3_ROLE_TO_TOKEN,
  type T3Appearance,
  type T3ColorRole,
} from "../kit/theme/t3";
import {
  createVividThemeColors,
  parseThemeColor,
  themeOklchToRgb,
  themeRelativeLuminance,
} from "../kit/theme/t3palette";
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

  test("every T3 Code data-slot name, and the kit sets each one", () => {
    const documented = rows("### Hooks you can rely on")
      .map(([hook]) => hook?.match(/^`data-slot="([^"]+)"`/)?.[1])
      .filter((h) => h !== undefined);
    expect(documented).toEqual([...SLOTS]);
    expect(readme).toContain('| `class="dark"` | Root element, while the appearance is dark |');
    const kit = join(import.meta.dir, "..", "kit");
    const source = (readdirSync(kit, { recursive: true }) as string[])
      .filter((f) => f.endsWith(".tsx"))
      .map((f) => readFileSync(join(kit, f), "utf8"))
      .join("\n");
    for (const slot of SLOTS) expect(source).toContain(`data-slot="${slot}"`);
  });

  test("a token's default is a value a theme could set, and follows only tokens that exist", () => {
    for (const [name, def] of Object.entries(TOKENS)) {
      for (const value of [def.light, "dark" in def ? def.dark : def.light]) {
        expect(tokenValueProblem(name, value)).toBeUndefined();
        for (const [, property] of value.matchAll(/var\((--px-[a-z-]+)\)/g)) {
          expect(Object.keys(TOKENS).map(tokenProperty)).toContain(property as string);
        }
      }
    }
  });

  test("every colour token is used by the kit's CSS", () => {
    const kit = join(import.meta.dir, "..", "kit");
    const css = (readdirSync(kit, { recursive: true }) as string[])
      .filter((f) => f.endsWith(".css"))
      .map((f) => readFileSync(join(kit, f), "utf8"))
      .join("\n");
    for (const name of Object.keys(TOKENS).filter((n) => n.startsWith("color."))) {
      expect(css).toContain(`var(${tokenProperty(name)})`);
    }
  });

  test("account list controls expose theme hooks", () => {
    for (const part of ["toolbar", "tabs", "tab", "pager", "select", "batch", "account-meta"] as const) {
      expect(PARTS).toContain(part);
    }
  });

  test("routing tiers have a documented theme hook", () => {
    expect(PARTS).toContain("tier");
    expect(readme).toContain('data-part="tier"');
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

  test("the T3 Code role-to-token table and variable-only roles agree with the code", () => {
    const documented = Object.fromEntries(
      rows("## T3 Code themes").map(([role, token]) => [role?.replace(/`/g, ""), token?.replace(/`/g, "")]),
    );
    expect(documented).toEqual(T3_ROLE_TO_TOKEN as Record<string, string>);
    for (const role of T3_COLOR_ROLES) {
      expect(readme).toContain(`\`${role}\``);
    }
    expect(APP_THEME_VARIABLES.terminalSelection).toBe("--app-theme-terminal-selection-background");
    expect(readme).toContain(APP_THEME_VARIABLES.terminalSelection);
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

  test("a T3 Code file with no colours says what it needs; other files without theme_version do not", () => {
    const why = (raw: unknown) => {
      const r = parseTheme(raw);
      return r.ok ? "accepted" : r.error;
    };
    const needs = "a T3 Code theme file needs a colors object, or both canvas and accent";
    expect(why({ name: "No colours", appearance: "dark" })).toBe(needs);
    expect(why({ name: "No colours", appearance: "light", canvas: "#ffffff" })).toBe(needs);
    // Anything else is not a theme file: the reason names what either kind of file has, and does
    // not blame a field the person never heard of.
    for (const raw of [
      { foo: 1 },
      { name: "No colours" },
      { name: "No colours", appearance: "sepia" },
      { name: " ", appearance: "dark" },
      { appearance: "dark", colors: {} },
    ]) {
      const reason = why(raw);
      expect(reason).toStartWith("the file is not a theme file.");
      expect(reason).not.toContain("missing");
      for (const field of ['"theme_version"', '"name"', '"appearance"', '"colors"', '"canvas"', '"accent"'])
        expect(reason).toContain(field);
    }
  });

  test("a token value the rules refuse is named with the part that breaks them", () => {
    const r = ok({ ...base, tokens: { "color.bg": "url(https://example.com/x)", "color.text": "red; x" } });
    expect(r.theme.tokens).toEqual({});
    expect(r.warnings).toEqual([
      'tokens: color.bg contains "url(", which a token value may not contain; the default is kept',
      'tokens: color.text contains ";", which a token value may not contain; the default is kept',
    ]);
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

  test("the newer colour tokens follow the older ones until a theme sets them", () => {
    const plain = resolve(ok(base).theme, DEFAULT_CHOICES, env).tokens;
    expect(plain["color.nav"]).toBe("var(--px-color-surface)");
    expect(plain["color.code-bg"]).toBe("var(--px-color-surface-raised)");
    expect(plain["color.danger-surface"]).toBe("color-mix(in srgb, var(--px-color-danger) 8%, transparent)");
    const set = resolve(ok({ ...base, tokens: { "color.nav": "#102030" } }).theme, DEFAULT_CHOICES, env);
    expect(set.tokens["color.nav"]).toBe("#102030");
    expect(tokensCss(set.tokens)).toContain("--px-color-nav:#102030;");
  });

  test("the root has class dark only while the appearance is dark", () => {
    const g = globalThis as { CSSStyleSheet?: unknown };
    const had = g.CSSStyleSheet;
    g.CSSStyleSheet = class {
      replaceSync() {}
    };
    try {
      const classes = new Set<string>();
      const doc = {
        documentElement: {
          dataset: {} as Record<string, string>,
          style: {} as Record<string, string>,
          classList: {
            toggle: (name: string, on: boolean) => (on ? classes.add(name) : classes.delete(name)),
          },
        },
        adoptedStyleSheets: [] as unknown[],
      };
      const applier = new ThemeApplier(doc as unknown as Document);
      applier.apply(resolve(theme, choose({ appearance: "dark" }), env));
      expect(doc.documentElement.dataset.appearance).toBe("dark");
      expect([...classes]).toEqual(["dark"]);
      applier.apply(resolve(theme, choose({ appearance: "light" }), env));
      expect([...classes]).toEqual([]);
    } finally {
      g.CSSStyleSheet = had;
    }
  });

  test("tokens become custom properties", () => {
    expect(tokenProperty("color.surface-raised")).toBe("--px-color-surface-raised");
    expect(tokensCss({ "color.bg": "#fff", density: "1" })).toBe(":root{--px-color-bg:#fff;--px-density:1;}");
  });
});

const fixturePath = (name: string) => join(import.meta.dir, "fixtures", name);
const readFixture = <T = unknown>(name: string): T =>
  JSON.parse(readFileSync(fixturePath(name), "utf8")) as T;

// tests/fixtures/t3-vivid-oracle.json was recorded by running T3 Code's createVividThemeColors
// and helpers unchanged from pingdotgg/t3code apps/web/src/themePalette.ts at commit
// 2a411317d9b7341547e5acb3e9bceb7e6195c5a2 with culori (getDefaultThemeColors stubbed to {}).
interface OracleEntry {
  appearance: T3Appearance;
  canvas: string;
  accent: string;
  colors: Record<T3ColorRole, string>;
}

describe("T3 Code palette generator", () => {
  test("matches T3 Code's own output across all reference inputs", () => {
    const oracle = readFixture<OracleEntry[]>("t3-vivid-oracle.json");
    expect(oracle).toHaveLength(6);
    for (const entry of oracle) {
      const actual = createVividThemeColors(entry.appearance, entry.canvas, entry.accent);
      expect(actual).toEqual(entry.colors);
    }
  });

  test("parses hex, rgb()/rgba(), and oklch() forms and rejects unsupported forms", () => {
    for (const seed of [
      "#123",
      "#1238",
      "#1a1b26",
      "#1a1b26cc",
      "rgb(26, 27, 38)",
      "rgba(26, 27, 38, 0.5)",
      "rgb(26 27 38 / 50%)",
      "rgb(10% 20% 30%)",
      "oklch(0.62 0.2 280)",
      "oklch(62% 50% 0.5turn / 50%)",
      "oklch(0.62 0.2 280deg / none)",
    ]) {
      expect(parseThemeColor(seed)).not.toBeNull();
    }
    for (const unsupported of [
      "hsl(220 20% 10%)",
      "rebeccapurple",
      "transparent",
      "color-mix(in srgb, #fff 50%, #000)",
      "oklch(0.5 0.1 180); body{}",
      "",
      123,
    ]) {
      expect(parseThemeColor(unsupported)).toBeNull();
    }
  });

  test("enforces WCAG contrast floors and status hue bounds (ported from T3 Code)", () => {
    const rgbOf = (color: string) => {
      const parsed = parseThemeColor(color);
      if (!parsed) throw new Error(`unparsed color: ${color}`);
      return themeOklchToRgb(parsed.color);
    };
    const contrast = (a: string, b: string) => {
      const la = themeRelativeLuminance(rgbOf(a));
      const lb = themeRelativeLuminance(rgbOf(b));
      return (Math.max(la, lb) + 0.05) / (Math.min(la, lb) + 0.05);
    };
    const seeds: ReadonlyArray<[T3Appearance, string, string]> = [
      ["dark", "#101a2c", "#4f8fe8"],
      ["dark", "#211a23", "#df5398"],
      ["light", "#fdf6ec", "#c2571b"],
      ["light", "#111827", "#8ab4f8"],
      ["dark", "#f5ecf5", "#a84370"],
    ];
    for (const [appearance, canvas, accent] of seeds) {
      const colors = createVividThemeColors(appearance, canvas, accent);
      expect(colors.canvas).toMatch(/^oklch\(/);
      expect(colors.accent).toMatch(/^oklch\(/);
      expect(contrast(colors.text, colors.canvas)).toBeGreaterThanOrEqual(7);
      expect(contrast(colors.textMuted, colors.canvas)).toBeGreaterThanOrEqual(4.5);
      expect(contrast(colors.textMuted, colors.canvas)).toBeLessThan(5.5);
      expect(contrast(colors.mutedForeground, colors.muted)).toBeGreaterThanOrEqual(4.5);
      expect(contrast(colors.mutedForeground, colors.muted)).toBeLessThan(
        contrast(colors.text, colors.muted),
      );
      expect(contrast(colors.placeholder, colors.surfaceRaised)).toBeGreaterThanOrEqual(4.5);
      expect(contrast(colors.placeholder, colors.surfaceRaised)).toBeLessThan(
        contrast(colors.text, colors.surfaceRaised),
      );
      expect(colors.secondaryLabel).toBe(colors.textMuted);
      expect(contrast(colors.accentForeground, colors.accent)).toBeGreaterThanOrEqual(4.5);
      expect(contrast(colors.searchMatchForeground, colors.searchMatchBackground)).toBeGreaterThanOrEqual(
        4.5,
      );
      expect(
        contrast(colors.searchMatchActiveForeground, colors.searchMatchActiveBackground),
      ).toBeGreaterThanOrEqual(4.5);
      expect(contrast(colors.messageActionForeground, colors.messageAction)).toBeGreaterThanOrEqual(4.5);
      expect(contrast(colors.messageForeground, colors.messageSurface)).toBeGreaterThanOrEqual(4.5);
      expect(contrast(colors.secondaryForeground, colors.secondary)).toBeGreaterThanOrEqual(4.5);
      expect(contrast(colors.sidebarForeground, colors.sidebar)).toBeGreaterThanOrEqual(4.5);
      expect(colors.messageAction).not.toBe(colors.accent);
      const err = rgbOf(colors.error);
      expect(err.r).toBeGreaterThan(err.g * 2);
      expect(err.r).toBeGreaterThan(err.b * 2);
      expect(contrast(colors.error, "#ffffff")).toBeGreaterThanOrEqual(2.5);
      expect(contrast(colors.errorForeground, colors.errorSurface)).toBeGreaterThanOrEqual(4.5);
      const warn = rgbOf(colors.warning);
      expect(warn.r).toBeGreaterThan(warn.b);
      expect(warn.g).toBeGreaterThan(warn.b);
    }
  });
});

describe("T3 Code themes", () => {
  const choose = (c: Partial<Choices>): Choices => ({ ...DEFAULT_CHOICES, ...c });

  test("loads the short file from T3 Code docs unchanged", () => {
    const text = readFileSync(fixturePath("t3-docs-nightfall.json"), "utf8");
    const r = parseThemeText(text);
    expect(r.ok).toBe(true);
    if (!r.ok) return;
    expect(r.warnings).toEqual([]);
    expect(r.theme.id).toBe("t3/nightfall");
    expect(r.theme.appearance).toBe("dark");

    const resolved = resolve(r.theme, choose({ appearance: "light" }), env);
    expect(resolved.appearance).toBe("dark");
    const expected = createVividThemeColors("dark", "#1a1b26", "#7aa2f7");
    expect(resolved.tokens["color.bg"]).toBe(expected.canvas);
    expect(resolved.tokens["color.accent"]).toBe(expected.accent);
    expect(resolved.tokens["color.nav"]).toBe(expected.sidebar);
    expect(resolved.tokens["color.danger"]).toBe(expected.errorForeground);
    // Unmapped roles keep pluxy defaults.
    expect(resolved.tokens["color.ok"]).toBe(TOKENS["color.ok"].dark);
    expect(resolved.tokens["color.diff-add"]).toBe(TOKENS["color.diff-add"].dark);
    // Explicit overrides win in --app-theme-* and generated roles keep generated values.
    expect(resolved.variables["--app-theme-terminal-selection-background"]).toBe("#292e42");
    expect(resolved.variables["--app-theme-error"]).toBe("#f7768e");
    expect(resolved.variables["--app-theme-error-foreground"]).toBe(expected.errorForeground);
    expect(Object.keys(resolved.variables)).toHaveLength(T3_COLOR_ROLES.length);
  });

  test("loads an exported GROVE_THEME file with all 61 roles and variants.dark", () => {
    const raw = readFixture<{
      colors: Record<T3ColorRole, string>;
      variants: { dark: Record<T3ColorRole, string> };
    }>("t3-grove-exported.json");
    const r = parseTheme(raw);
    expect(r.ok).toBe(true);
    if (!r.ok) return;
    expect(r.warnings).toEqual([]);
    expect(r.theme.id).toBe("t3/grove");
    expect(r.theme.appearance).toBe("both");

    const light = resolve(r.theme, choose({ appearance: "light" }), env);
    expect(light.appearance).toBe("light");
    expect(light.tokens["color.bg"]).toBe(raw.colors.canvas);
    expect(light.tokens["color.surface"]).toBe(raw.colors.surface);
    expect(light.tokens["color.nav-selected"]).toBe(raw.colors.sidebarRowSelected);
    expect(light.variables["--app-theme-chrome"]).toBe(raw.colors.chrome);
    expect(light.variables["--app-theme-terminal-selection-background"]).toBe(raw.colors.terminalSelection);
    expect(Object.keys(light.variables)).toHaveLength(T3_COLOR_ROLES.length);

    const dark = resolve(r.theme, choose({ appearance: "dark" }), env);
    expect(dark.appearance).toBe("dark");
    expect(dark.tokens["color.bg"]).toBe(raw.variants.dark.canvas);
    expect(dark.tokens["color.accent"]).toBe(raw.variants.dark.accent);
    expect(dark.tokens["color.nav"]).toBe(raw.variants.dark.sidebar);
    expect(dark.variables["--app-theme-terminal-cursor"]).toBe(raw.variants.dark.terminalCursor);
    expect(Object.keys(dark.variables)).toHaveLength(T3_COLOR_ROLES.length);
  });

  test("loads a short file with variants and css, and swaps --app-theme-* cleanly", () => {
    const raw = readFixture<{ css: string }>("t3-short-variants-css.json");
    const r = parseTheme(raw);
    expect(r.ok).toBe(true);
    if (!r.ok) return;
    expect(r.warnings).toEqual([]);
    expect(r.theme.id).toBe("t3/dawn-and-dusk");
    expect(r.theme.appearance).toBe("both");

    const light = resolve(r.theme, choose({ appearance: "light" }), env);
    const lightGen = createVividThemeColors("light", "#f6f1e7", "#9a3412");
    expect(light.tokens["color.bg"]).toBe(lightGen.canvas);
    expect(light.tokens["color.nav"]).toBe("#efe7d8");
    expect(light.variables["--app-theme-sidebar"]).toBe("#efe7d8");
    expect(light.variables["--app-theme-surface"]).toBe(lightGen.surface);
    expect(light.css).toEqual([raw.css]);

    // The dark variant sets only what variants.dark declares; other roles keep pluxy dark defaults.
    const dark = resolve(r.theme, choose({ appearance: "dark" }), env);
    expect(dark.appearance).toBe("dark");
    expect(dark.tokens["color.bg"]).toBe("#1c1814");
    expect(dark.tokens["color.surface"]).toBe("#241f1a");
    expect(dark.tokens["color.text"]).toBe("#efe6d8");
    expect(dark.tokens["color.accent"]).toBe("#f0a36b");
    expect(dark.tokens["color.nav"]).toBe(TOKENS["color.nav"].light);
    expect(dark.tokens["color.border"]).toBe(TOKENS["color.border"].dark);
    expect(dark.variables).toEqual({
      "--app-theme-canvas": "#1c1814",
      "--app-theme-surface": "#241f1a",
      "--app-theme-text": "#efe6d8",
      "--app-theme-accent": "#f0a36b",
    });

    const cssOut = tokensCss(dark.tokens, dark.variables);
    expect(cssOut).toContain("--px-color-bg:#1c1814;");
    expect(cssOut).toContain("--app-theme-canvas:#1c1814;");

    // Switching back to a non-T3 theme removes all --app-theme-* declarations.
    const plain = resolve(ok(base).theme, choose({}), env);
    expect(plain.variables).toEqual({});
    expect(tokensCss(plain.tokens, plain.variables)).not.toContain("--app-theme-");

    const g = globalThis as { CSSStyleSheet?: unknown };
    const had = g.CSSStyleSheet;
    g.CSSStyleSheet = class {
      text = "";
      replaceSync(text: string) {
        this.text = text;
      }
    };
    try {
      const doc = {
        documentElement: {
          dataset: {} as Record<string, string>,
          style: {} as Record<string, string>,
          classList: { toggle: () => {} },
        },
        adoptedStyleSheets: [] as Array<{ text: string }>,
      };
      const applier = new ThemeApplier(doc as unknown as Document);
      applier.apply(dark);
      expect(doc.adoptedStyleSheets[0]?.text).toContain("--app-theme-accent:#f0a36b;");
      expect(doc.adoptedStyleSheets[1]?.text).toBe(raw.css);
      applier.apply(plain);
      expect(doc.adoptedStyleSheets[0]?.text).not.toContain("--app-theme-");
      expect(doc.adoptedStyleSheets).toHaveLength(1);
    } finally {
      g.CSSStyleSheet = had;
    }
  });

  test("mixes both dialects in either direction and lets tokens win over roles", () => {
    const t3WithTokens = ok({
      name: "Hybrid T3",
      appearance: "light",
      canvas: "#faf5fa",
      accent: "#a84370",
      colors: { text: "#201020" },
      variants: { dark: { canvas: "#121212" } },
      tokens: { "color.bg": "#fdfdfd", "font.mono": "JetBrains Mono, monospace" },
      dark: { "color.bg": "#050505" },
      layout: { nav: "rail" },
      collection: { id: "c", label: "Custom" },
      managed: true,
    });
    expect(t3WithTokens.warnings).toEqual([]);
    expect(t3WithTokens.theme.appearance).toBe("both");
    const lightT3 = resolve(t3WithTokens.theme, choose({ appearance: "light" }), env);
    expect(lightT3.tokens["color.bg"]).toBe("#fdfdfd");
    expect(lightT3.tokens["color.text"]).toBe("#201020");
    expect(lightT3.tokens["font.mono"]).toBe("JetBrains Mono, monospace");
    expect(lightT3.layout.nav).toBe("rail");
    const darkT3 = resolve(t3WithTokens.theme, choose({ appearance: "dark" }), env);
    expect(darkT3.tokens["color.bg"]).toBe("#050505");

    const pluxyWithT3 = ok({
      ...base,
      canvas: "#1a1b26",
      accent: "#7aa2f7",
      colors: { surface: "#222436" },
      tokens: { "color.surface": "#33354a" },
    });
    expect(pluxyWithT3.warnings).toEqual([]);
    const darkPluxy = resolve(pluxyWithT3.theme, choose({ appearance: "dark" }), env);
    expect(darkPluxy.tokens["color.surface"]).toBe("#33354a");
    expect(darkPluxy.tokens["color.accent"]).toBe(
      createVividThemeColors("dark", "#1a1b26", "#7aa2f7").accent,
    );
    expect(darkPluxy.variables["--app-theme-surface"]).toBe("#222436");
  });

  test("existing built-in themes resolve identically to the pre-change baseline", () => {
    const baseline = readFixture<
      Record<string, { light: Record<string, string>; dark: Record<string, string> }>
    >("builtin-resolved-baseline.json");
    for (const file of readdirSync(themesDir).filter((f) => f.endsWith(".json"))) {
      const parsed = ok(JSON.parse(readFileSync(join(themesDir, file), "utf8")));
      const id = parsed.theme.id;
      expect(baseline[id]).toBeDefined();
      for (const appearance of ["light", "dark"] as const) {
        const resolved = resolve(parsed.theme, choose({ appearance }), env);
        expect(resolved.tokens).toEqual(baseline[id]?.[appearance] as Record<string, string>);
        expect(resolved.variables).toEqual({});
      }
    }
  });

  test("refuses unusable T3-like files and derives IDs safely", () => {
    expect(parseTheme({ appearance: "dark", canvas: "#1a1b26", accent: "#7aa2f7" }).ok).toBe(false);
    expect(
      parseTheme({
        name: "Both without version",
        appearance: "both",
        canvas: "#1a1b26",
        accent: "#7aa2f7",
      }).ok,
    ).toBe(false);
    expect(
      parseTheme({
        version: 2,
        name: "Future",
        appearance: "dark",
        canvas: "#1a1b26",
        accent: "#7aa2f7",
      }).ok,
    ).toBe(false);

    const nonAscii = ok({
      name: "日本語",
      appearance: "dark",
      colors: { canvas: "#111" },
    });
    expect(nonAscii.warnings).toEqual([]);
    expect(nonAscii.theme.id).toMatch(/^t3\/theme-[a-z0-9]+$/);

    const badId = ok({
      id: "Not_Valid/Id",
      name: "Bad Id",
      appearance: "light",
      colors: { canvas: "#fff" },
    });
    expect(badId.theme.id).toBe("t3/bad-id");
    expect(badId.warnings).toHaveLength(1);
  });

  test("keeps valid T3 fields and warns on bad roles, values, seeds, and variants", () => {
    const r = ok({
      name: "Noisy",
      appearance: "dark",
      canvas: "hsl(220 20% 10%)",
      accent: "#7aa2f7",
      colors: {
        canvas: "#101010",
        unknownRole: "#ffffff",
        text: "red; } body { display: none",
      },
      variants: {
        dark: { surface: "#000000" },
        sepia: { surface: "#f0e6d2" },
        light: { surface: "#ffffff" },
      },
    });
    expect(r.theme.appearance).toBe("both");
    // Unsupported seed -> 1 warning, no generated palette; bad role & unknown role & repeating/unknown variant -> 1 each.
    expect(r.warnings).toHaveLength(5);

    const lightResolved = resolve(r.theme, choose({ appearance: "light" }), env);
    expect(lightResolved.tokens["color.surface"]).toBe("#ffffff");

    const darkResolved = resolve(r.theme, choose({ appearance: "dark" }), env);
    expect(darkResolved.tokens["color.bg"]).toBe("#101010");
    // No generated palette + rejected text role -> keeps pluxy dark defaults.
    expect(darkResolved.tokens["color.text"]).toBe(TOKENS["color.text"].dark);
    expect(darkResolved.tokens["color.accent"]).toBe(TOKENS["color.accent"].dark);
  });

  test("a both-appearance file with only colors paints them in the appearance its canvas belongs to", () => {
    const night = ok({ ...base, colors: { canvas: "#101010", text: "#eeeeee" } });
    expect(night.theme.appearance).toBe("both");
    expect(night.warnings).toEqual([]);
    const nightDark = resolve(night.theme, choose({ appearance: "dark" }), env);
    expect(nightDark.tokens["color.bg"]).toBe("#101010");
    expect(nightDark.tokens["color.text"]).toBe("#eeeeee");
    expect(nightDark.variables["--app-theme-canvas"]).toBe("#101010");
    // Light keeps pluxy's own light colours instead of a dark canvas.
    const nightLight = resolve(night.theme, choose({ appearance: "light" }), env);
    expect(nightLight.tokens["color.bg"]).toBe(TOKENS["color.bg"].light);
    expect(nightLight.tokens["color.text"]).toBe(TOKENS["color.text"].light);
    expect(nightLight.variables).toEqual({});

    const day = ok({ ...base, colors: { canvas: "#fafafa", text: "#111111" } });
    expect(resolve(day.theme, choose({ appearance: "light" }), env).tokens["color.bg"]).toBe("#fafafa");
    expect(resolve(day.theme, choose({ appearance: "dark" }), env).tokens["color.bg"]).toBe(
      TOKENS["color.bg"].dark as string,
    );

    // The canvas seed still decides when the file has one.
    const seeded = ok({ ...base, canvas: "#fafafa", accent: "#3355ff", colors: { canvas: "#101010" } });
    expect(seeded.theme.t3?.appearance).toBe("light");
  });

  test("warns when colors holds no roles", () => {
    const warning = "colors has no roles; ignored";
    const empty = ok({ name: "X", appearance: "dark", colors: {} });
    expect(empty.warnings).toEqual([warning]);
    expect(empty.theme.id).toBe("t3/x");
    expect(ok({ ...base, colors: {} }).warnings).toEqual([warning]);
    // A file that also has seeds still gets its generated palette.
    const seeded = ok({ name: "X", appearance: "dark", canvas: "#1a1b26", accent: "#7aa2f7", colors: {} });
    expect(seeded.warnings).toEqual([warning]);
    expect(resolve(seeded.theme, choose({}), env).variables["--app-theme-canvas"]).toBeDefined();
    // A role is enough, and a bad role already has its own warning.
    expect(ok({ name: "X", appearance: "dark", colors: { canvas: "#101010" } }).warnings).toEqual([]);
    expect(ok({ name: "X", appearance: "dark", colors: { nope: "#101010" } }).warnings).toEqual([
      "colors: unknown role nope; ignored",
    ]);
  });
});

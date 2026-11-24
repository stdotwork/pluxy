// Colours come from theme tokens (kit/theme/contract.ts, TOKENS; each becomes a --px-* custom
// property). This test reads the UI source and fails on a raw colour value that is not on the
// baseline below, so a new one cannot land by copying nearby code.

import { expect, test } from "bun:test";
import { readdirSync, readFileSync } from "node:fs";
import { join, relative } from "node:path";

const next = join(import.meta.dir, "..", "..");

/** Folders scanned, relative to next/. Theme token and palette code and the theme files are not here. */
const SCANNED = ["ui/kit", "ui/panel", "ui/extension-pages", "app/src", "app/page"];
const SCANNED_EXTENSIONS = [".css", ".ts", ".tsx"];
/** The token and palette code: it defines the colours, so it is the one place that holds them. */
const EXCLUDED = ["ui/kit/theme/"];

/** Raw colours that exist today, per file: the literal (hex lowercased) and how many times it occurs.
 * This list may only shrink: replace the colour with a theme token and delete its entry. */
const BASELINE: { file: string; literal: string; count: number }[] = [
  // The release-notes window's fallbacks when the host passes no theme values.
  { file: "app/page/notes.css", literal: "#18181b", count: 1 },
  { file: "app/page/notes.css", literal: "#ffffff", count: 1 },
  { file: "app/page/notes.css", literal: "#2f5fd0", count: 1 },
  { file: "app/page/notes.css", literal: "#e2e2e6", count: 4 },
  { file: "app/page/notes.css", literal: "#5f6068", count: 1 },
];

/** Colour-like text that is not a colour the UI paints. Each entry names the file, the literal and why. */
const NOT_COLOURS: { file: string; literal: string; why: string }[] = [
  // The placeholder of the "paste a theme" box: sample JSON shown to the person, never applied.
  { file: "ui/kit/pages/themes.tsx", literal: "#1a1b26", why: "sample theme JSON in a placeholder" },
  { file: "ui/kit/pages/themes.tsx", literal: "#7aa2f7", why: "sample theme JSON in a placeholder" },
];

const FIX =
  "Use a theme token (next/ui/kit/theme/contract.ts): read it as var(--px-color-...) in CSS, or in a new " +
  "token if none fits (see next/ui/themes/README.md, 'Changes to this contract'). The baseline may only shrink.";

const HEX = /(?<![\w&/.$-])#(?:[0-9a-fA-F]{8}|[0-9a-fA-F]{6}|[0-9a-fA-F]{3,4})(?![\w-])/g;
const COLOUR_FUNCTION = /\b(?:rgba?|hsla?|hwb|lab|lch|oklab|oklch|color)\(([^()]*)\)/gi;

function sourceFiles(dir: string): string[] {
  const out: string[] = [];
  for (const entry of readdirSync(join(next, dir), { withFileTypes: true, recursive: true })) {
    if (!entry.isFile() || !SCANNED_EXTENSIONS.some((e) => entry.name.endsWith(e))) continue;
    const file = relative(next, join(entry.parentPath, entry.name));
    if (file.split("/").includes("node_modules") || EXCLUDED.some((p) => file.startsWith(p))) continue;
    out.push(file);
  }
  return out;
}

const blank = (m: string) => m.replace(/[^\n]/g, " ");

/** The text colours can sit in: without comments, and for CSS without selectors (so `#add` as an
 * id selector is not a hex colour). Line breaks stay, so a position still gives its line number. */
function paintable(file: string, text: string): string {
  const noBlock = text.replace(/\/\*[\s\S]*?\*\//g, blank);
  if (file.endsWith(".css")) {
    // A chunk that ends at `{` is a selector or an at-rule prelude; the rest are declarations.
    return noBlock.replace(/[^{};]*\{/g, blank);
  }
  return noBlock.replace(/(^|[\s;,)])\/\/[^\n]*/g, (m, head: string) => head + blank(m.slice(head.length)));
}

/** Raw colour literals in one file, with line numbers; hex is lowercased and spaces collapsed. */
export function colourLiterals(file: string, text: string): { literal: string; line: number }[] {
  const body = paintable(file, text);
  const found: { literal: string; line: number; at: number }[] = [];
  const lineAt = (i: number) => body.slice(0, i).split("\n").length;
  for (const m of body.matchAll(HEX)) {
    found.push({ literal: m[0].toLowerCase(), line: lineAt(m.index), at: m.index });
  }
  for (const m of body.matchAll(COLOUR_FUNCTION)) {
    // rgb(var(--x)) and relative colours derive from a token; rgb(0 0 0 / 50%) is a literal.
    // The regex stops at a nested `(`, so `rgb(var(--x))` does not match: only plain arguments do.
    if (!/\d|#/.test(m[1] ?? "") || /^\s*from\b/.test(m[1] ?? "")) continue;
    found.push({ literal: m[0].toLowerCase().replace(/\s+/g, " "), line: lineAt(m.index), at: m.index });
  }
  // color-mix(): a literal only when one of its colours is; var() and currentColor are tokens.
  for (const m of body.matchAll(/color-mix\(/gi)) {
    let depth = 1;
    let end = m.index + m[0].length;
    for (; end < body.length && depth > 0; end++) {
      if (body[end] === "(") depth++;
      else if (body[end] === ")") depth--;
    }
    const inner = body.slice(m.index, end);
    // Hex and colour functions inside are already counted above; only a bare named colour is new.
    const named = inner.match(/[\s,](?:white|black|red|green|blue|gray|grey|orange|yellow|purple|pink)\b/i);
    if (named) found.push({ literal: inner.replace(/\s+/g, " "), line: lineAt(m.index), at: m.index });
  }
  return found.sort((a, b) => a.at - b.at).map(({ literal, line }) => ({ literal, line }));
}

test("the colour finder sees colours and skips what is not one", () => {
  const css = [
    "#add, .a:hover { color: var(--px-color-text); }", // an id selector
    "a { color: var(--x, #ABC); border: 1px solid rgb(0 0 0 / 50%); }",
    "b { background: color-mix(in srgb, currentColor 8%, transparent); }",
    "c { background: color-mix(in srgb, var(--px-color-bg) 70%, white); }",
    "/* #123456 in a comment */ d { color: rgb(var(--x)); }",
  ].join("\n");
  expect(colourLiterals("x.css", css)).toEqual([
    { literal: "#abc", line: 2 },
    { literal: "rgb(0 0 0 / 50%)", line: 2 },
    { literal: "color-mix(in srgb, var(--px-color-bg) 70%, white)", line: 4 },
  ]);
  const ts = [
    'const a = "#5f6068"; // #111111 in a comment',
    'const link = "page.html#abcdef"; const href = "/#fade";',
    'const url = "https://example.com/#cafe";',
  ].join("\n");
  expect(colourLiterals("x.ts", ts)).toEqual([{ literal: "#5f6068", line: 1 }]);
});

test("raw colours outside the theme tokens are only the ones on the baseline", () => {
  const seen = new Map<string, { count: number; lines: number[] }>();
  const files = SCANNED.flatMap(sourceFiles);
  expect(files.length).toBeGreaterThan(50); // the scan reads the UI, not an empty folder
  for (const file of files) {
    for (const { literal, line } of colourLiterals(file, readFileSync(join(next, file), "utf8"))) {
      if (NOT_COLOURS.some((n) => n.file === file && n.literal === literal)) continue;
      const key = `${file} ${literal}`;
      const entry = seen.get(key) ?? { count: 0, lines: [] };
      entry.count++;
      entry.lines.push(line);
      seen.set(key, entry);
    }
  }
  const allowed = new Map(BASELINE.map((b) => [`${b.file} ${b.literal}`, b.count]));
  const added: string[] = [];
  for (const [key, { count, lines }] of seen) {
    const [file, ...literal] = key.split(" ");
    if (count > (allowed.get(key) ?? 0)) {
      added.push(`  ${file}:${lines.join(",")}  ${literal.join(" ")}`);
    }
  }
  const gone: string[] = [];
  for (const [key, count] of allowed) {
    const now = seen.get(key)?.count ?? 0;
    if (now < count) gone.push(`  ${key}: baseline says ${count}, source has ${now}`);
  }
  const message = [
    added.length ? `New raw colour values:\n${added.join("\n")}\n${FIX}` : "",
    gone.length
      ? `Colours on the baseline in ui/tests/colours.test.ts that are gone; lower the count or delete the entry:\n${gone.join("\n")}`
      : "",
  ]
    .filter(Boolean)
    .join("\n\n");
  expect(message).toBe("");
});

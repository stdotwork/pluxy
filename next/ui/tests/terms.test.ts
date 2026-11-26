// docs/terms.md is the glossary for UI wording. Its English tables have a "Do not use" column; this
// test reads it and fails when a UI text (a value in a `strings(...)` table in next/ui/kit or
// next/app/src) contains a banned term as a whole word or phrase, ignoring case.

import { expect, test } from "bun:test";
import { readdirSync, readFileSync } from "node:fs";
import { join, relative } from "node:path";

const next = join(import.meta.dir, "..", "..");
const SCANNED = ["ui/kit", "app/src"];

/** Banned terms that texts already use, per file and key: the strings were written before the
 * glossary was checked. This list may only shrink: reword the text with the term to use, then delete
 * its entry. The key is `namespace.key`, plus `.one` or `.other` for a count-dependent text. */
const BASELINE: { file: string; key: string; banned: string }[] = [
  { file: "ui/kit/extensions/settings.tsx", key: "settingsForm.couldNotLoad", banned: "Load" },
  {
    file: "ui/kit/pages/accountSettings.tsx",
    key: "accountSettings.disableCoolingHint",
    banned: "Credentials",
  },
  {
    file: "ui/kit/pages/accountSettings.tsx",
    key: "accountSettings.proxyUrlHint",
    banned: "Credentials",
  },
  { file: "ui/kit/pages/accounts.tsx", key: "accounts.tip", banned: "Credentials" },
  { file: "ui/kit/pages/accounts.tsx", key: "accounts.search", banned: "Credentials" },
  { file: "ui/kit/pages/accounts.tsx", key: "accounts.credentials.other", banned: "Credentials" },
  { file: "ui/kit/pages/accounts.tsx", key: "accounts.meta", banned: "Credentials" },
  { file: "ui/kit/pages/configStrings.tsx", key: "config.maxCredentials", banned: "Credentials" },
  { file: "ui/kit/pages/routing.tsx", key: "routing.credentials", banned: "Credentials" },
  { file: "ui/kit/pages/routing.tsx", key: "routing.credentialsHint", banned: "Credentials" },
  { file: "ui/kit/pages/themes.tsx", key: "themes.notRead", banned: "Load" },
];

/** Banned terms that are wrong only as a standalone label: a text holds one when the whole text, trimmed
 * and compared without regard to case, is the term. `Active` is banned as the condition of an account
 * (docs/terms.md, "Credentials and status": `Serving` and the other condition words instead), but
 * "Active revision", "Least active" and "No active cooldowns" are correct English and are not that status. */
const WHOLE_TEXT_ONLY = ["Active"];

export interface Rule {
  /** The banned text as written in the glossary, without its backticks. */
  banned: string;
  match: RegExp;
  /** The term to use instead: the row's "Term" cell. */
  use: string;
  concept: string;
  confirm: boolean;
}

/** A term to use, kept to blank it before its own banned words are looked for. */
export interface Preferred {
  text: string;
  re: RegExp;
}

export interface UiText {
  file: string;
  line: number;
  key: string;
  text: string;
}

const spans = (cell: string) => [...cell.matchAll(/`([^`]+)`/g)].map((m) => m[1] as string);

/** A glossary term as a pattern: `{n}` and other placeholders stand for a number or a `{name}`. */
function termPattern(term: string): string {
  return term
    .split(/\{\w+\}/)
    .map((piece) => piece.replace(/[.*+?^${}()|[\]\\]/g, "\\$&").replace(/\s+/g, "\\s+"))
    .join("(?:\\{\\w+\\}|[\\w.%$]+)");
}

/** The English tables' rows whose "Do not use" cell lists terms, and the terms they list. */
export function parseTerms(markdown: string): { rules: Rule[]; preferred: Preferred[]; skipped: string[] } {
  const start = markdown.indexOf("\n## English");
  const end = markdown.indexOf("\n## ", start + 1);
  const rules: Rule[] = [];
  const preferred: Preferred[] = [];
  const skipped: string[] = [];
  let columns: string[] = [];
  for (const line of markdown.slice(start, end).split("\n")) {
    if (!line.startsWith("|")) continue;
    const cells = line
      .split("|")
      .slice(1, -1)
      .map((c) => c.trim());
    if (cells.every((c) => /^-+$/.test(c))) continue;
    if (cells.includes("Do not use")) {
      columns = cells;
      continue;
    }
    const [concept = "", term = "", banned = ""] = cells;
    if (columns.length !== 3 || cells.length !== 3) continue;
    for (const s of spans(term)) preferred.push({ text: s, re: new RegExp(termPattern(s), "gi") });
    if (!banned) continue;
    // A cell that is prose around a code span ("a bare `{n}%` on a full card") is not a list of terms.
    if (banned.replace(/`[^`]+`/g, "").replace(/[,\s]/g, "") !== "") {
      skipped.push(`${concept}: ${banned}`);
      continue;
    }
    // "not yet applied": the glossary itself says the UI does not follow the row yet.
    if (/not yet applied/i.test(term)) {
      skipped.push(`${concept}: ${term}`);
      continue;
    }
    for (const b of spans(banned)) {
      const whole = WHOLE_TEXT_ONLY.includes(b)
        ? `^\\s*${termPattern(b)}\\s*$`
        : `(?<![\\w])${termPattern(b)}(?![\\w])`;
      rules.push({
        banned: b,
        match: new RegExp(whole, "i"),
        use: term.replace(/`/g, ""),
        concept,
        confirm: /owner to confirm/i.test(term),
      });
    }
  }
  return { rules, preferred, skipped };
}

// A small reader for the object literal of a strings("namespace", { ... }) call: keys, nested
// objects (plural texts), and the string literals of each value. Everything else is skipped.
class Reader {
  pos: number;
  constructor(
    readonly src: string,
    start: number,
  ) {
    this.pos = start;
  }
  line(at = this.pos) {
    return this.src.slice(0, at).split("\n").length;
  }
  skipSpace() {
    for (;;) {
      const rest = this.src.slice(this.pos, this.pos + 2);
      if (/\s/.test(this.src[this.pos] ?? "x")) this.pos++;
      else if (rest === "//") this.pos = this.src.indexOf("\n", this.pos) + 1 || this.src.length;
      else if (rest === "/*") {
        const close = this.src.indexOf("*/", this.pos + 2);
        this.pos = close < 0 ? this.src.length : close + 2;
      } else return;
    }
  }
  /** A quoted string or template at the position, as its text; undefined when there is none. */
  string(): string | undefined {
    const q = this.src[this.pos];
    if (q !== '"' && q !== "'" && q !== "`") return undefined;
    let out = "";
    let i = this.pos + 1;
    while (i < this.src.length && this.src[i] !== q) {
      const c = this.src[i] as string;
      if (c === "\\") {
        const n = this.src[i + 1] as string;
        out += n === "n" ? "\n" : n === "t" ? "\t" : n;
        i += 2;
      } else if (q === "`" && c === "$" && this.src[i + 1] === "{") {
        let depth = 0;
        i++;
        do {
          if (this.src[i] === "{") depth++;
          else if (this.src[i] === "}") depth--;
          i++;
        } while (i < this.src.length && depth > 0);
        out += "{}";
      } else {
        out += c;
        i++;
      }
    }
    this.pos = i + 1;
    return out;
  }
  /** An unquoted key at the position. */
  word(): string | undefined {
    const w = /^[\w$]+/.exec(this.src.slice(this.pos))?.[0];
    if (w) this.pos += w.length;
    return w;
  }
  /** Reads the object at `{` and returns [key path, text, line] for each text in it. */
  object(path: string[]): [string, string, number][] {
    const out: [string, string, number][] = [];
    this.pos++; // {
    for (;;) {
      this.skipSpace();
      if (this.pos >= this.src.length) return out;
      if (this.src[this.pos] === "}") {
        this.pos++;
        return out;
      }
      if (this.src[this.pos] === ",") {
        this.pos++;
        continue;
      }
      const key = this.string() ?? this.word();
      if (key === undefined) return out; // not a plain table: stop rather than guess
      this.skipSpace();
      if (this.src[this.pos] !== ":") return out;
      this.pos++;
      this.skipSpace();
      if (this.src[this.pos] === "{") {
        out.push(...this.object([...path, key]));
        continue;
      }
      out.push(...this.value([...path, key]));
    }
  }
  /** One value: the string literals in it, joined where `+` joins them, up to the next `,` or `}`. */
  value(path: string[]): [string, string, number][] {
    const out: [string, string, number][] = [];
    let depth = 0;
    let joined = false;
    for (;;) {
      this.skipSpace();
      const c = this.src[this.pos];
      if (c === undefined) return out;
      if (depth === 0 && (c === "," || c === "}")) return out;
      const at = this.pos;
      const s = this.string();
      if (s !== undefined) {
        const last = out[out.length - 1];
        if (joined && last) last[1] += s;
        else out.push([path.join("."), s, this.line(at)]);
        joined = false;
        continue;
      }
      if (c === "+") joined = out.length > 0;
      else if (!/\s/.test(c)) joined = false;
      if ("([{".includes(c)) depth++;
      else if (")]}".includes(c)) depth--;
      this.pos++;
    }
  }
}

/** The texts in every `strings("namespace", { ... })` table of one file. */
export function uiTexts(file: string, source: string): UiText[] {
  const out: UiText[] = [];
  for (const m of source.matchAll(/\bstrings\(\s*(["'])([\w.-]+)\1\s*,\s*\{/g)) {
    // The usage example in a comment is not a table.
    const lineStart = source.lastIndexOf("\n", m.index) + 1;
    if (/\/\/|^\s*\*/.test(source.slice(lineStart, m.index))) continue;
    const reader = new Reader(source, m.index + m[0].length - 1);
    for (const [key, text, line] of reader.object([m[2] as string])) out.push({ file, line, key, text });
  }
  return out;
}

export interface Violation extends UiText {
  banned: string;
  use: string;
  confirm: boolean;
}

/** The banned terms a text contains. A term to use that holds the banned word is blanked first:
 * `cooling down` is the term to use even though `cooling` alone is banned. */
export function violations(texts: UiText[], rules: Rule[], preferred: Preferred[]): Violation[] {
  const out: Violation[] = [];
  for (const t of texts) {
    for (const r of rules) {
      let text = t.text;
      for (const p of preferred) {
        if (r.match.test(p.text)) text = text.replace(p.re, (m) => " ".repeat(m.length));
      }
      if (!r.match.test(text)) continue;
      // Two rows can ban the same word (`cooling`, as a noun and as a state): say both.
      const same = out.find((v) => v.key === t.key && v.file === t.file && v.banned === r.banned);
      if (same) same.use = `${same.use}, or ${r.use}`;
      else out.push({ ...t, banned: r.banned, use: r.use, confirm: r.confirm });
    }
  }
  return out;
}

function sourceFiles(dir: string): string[] {
  const out: string[] = [];
  for (const entry of readdirSync(join(next, dir), { withFileTypes: true, recursive: true })) {
    if (!entry.isFile() || !/\.tsx?$/.test(entry.name)) continue;
    const file = relative(next, join(entry.parentPath, entry.name));
    if (!file.split("/").includes("node_modules")) out.push(file);
  }
  return out;
}

const glossary = parseTerms(readFileSync(join(next, "..", "docs", "terms.md"), "utf8"));

test("the glossary's English tables give banned terms to match", () => {
  expect(glossary.rules.length).toBeGreaterThan(30);
  const one = (banned: string, text: string) =>
    violations([{ file: "x.ts", line: 1, key: "k", text }], glossary.rules, glossary.preferred).filter(
      (v) => v.banned === banned,
    ).length;
  expect(one("5h limit", "Your 5h limit is used")).toBe(1);
  expect(one("5h limit", "Your 5-hour limit is used")).toBe(0);
  expect(one("{n}h limit", "Resets after 12h limit")).toBe(1);
  expect(one("cooling", "The account is cooling")).toBe(1);
  expect(one("cooling", "The account is cooling down")).toBe(0); // `cooling down` is the term to use
  expect(one("Credentials", "Stored credentials")).toBe(1);
  expect(one("Credentials", "Stored credential")).toBe(0); // whole words only
  expect(one("Active", "Active")).toBe(1);
  expect(one("Active", " active ")).toBe(1); // the whole text, trimmed, any case
  expect(one("Active", "Active revision")).toBe(0); // a phrase, not the status label
  expect(one("Active", "Least active")).toBe(0);
  expect(one("Active", "No active cooldowns.")).toBe(0);
});

test("the table reader finds keys, plurals and joined strings", () => {
  const src = [
    '// const t = strings("example", { title: "Log in" });',
    'const t = strings("ns", {',
    '  title: "Auth Files", // a comment: Login',
    "  \"quoted-key\": 'It\\'s',",
    "  count: { one: `{n} file`, other: `{n} $" + "{x} files` },",
    '  joined: "one " + "two",',
    "  cond: cond ? \"yes\" : 'no',",
    "});",
  ].join("\n");
  expect(uiTexts("x.ts", src).map((t) => [t.key, t.text, t.line])).toEqual([
    ["ns.title", "Auth Files", 3],
    ["ns.quoted-key", "It's", 4],
    ["ns.count.one", "{n} file", 5],
    ["ns.count.other", "{n} {} files", 5],
    ["ns.joined", "one two", 6],
    ["ns.cond", "yes", 7],
    ["ns.cond", "no", 7],
  ]);
});

test("UI texts use the glossary's terms, apart from the ones on the baseline", () => {
  const files = SCANNED.flatMap(sourceFiles);
  const texts = files.flatMap((f) => uiTexts(f, readFileSync(join(next, f), "utf8")));
  expect(texts.length).toBeGreaterThan(500); // the scan reads the tables, not nothing
  const found = violations(texts, glossary.rules, glossary.preferred);
  const known = (v: Violation) =>
    BASELINE.some((b) => b.file === v.file && b.key === v.key && b.banned === v.banned);
  const added = found
    .filter((v) => !known(v))
    .map(
      (v) =>
        `  ${v.file}:${v.line} ${v.key}: "${v.text}"\n    contains the banned term "${v.banned}"; use ${v.use}` +
        (v.confirm ? " (the glossary marks this term 'owner to confirm')" : ""),
    );
  const gone = BASELINE.filter(
    (b) => !found.some((v) => v.file === b.file && v.key === b.key && v.banned === b.banned),
  ).map((b) => `  ${b.file} ${b.key}: "${b.banned}" is no longer used`);
  const message = [
    added.length
      ? `UI text uses terms docs/terms.md says not to use. Reword it with the term to use, or, if the glossary is wrong, change docs/terms.md first:\n${added.join("\n")}`
      : "",
    gone.length
      ? `Delete these entries from the baseline in ui/tests/terms.test.ts:\n${gone.join("\n")}`
      : "",
  ]
    .filter(Boolean)
    .join("\n\n");
  expect(message).toBe("");
});

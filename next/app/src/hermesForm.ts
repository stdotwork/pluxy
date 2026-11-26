// The Hermes Agent page's form logic (hermes.tsx), apart from the page so its unit tests need no
// webview: what the tool's preview reads back, and whether a model ID can be applied.

import { hasSpaceOrControl, length } from "./textRules";

export const PACKAGE = "pluxy/hermes-setup";
/** The client label the tool's recipe gives its launches, which the launcher keys recent folders by. */
export const CLIENT = "Hermes Agent";

/** What the tool's preview reads back of the config file; `refused` (with `reason`) for a file
 * Hermes Agent would not take, which apply and clear refuse and Restore Backup still reaches. */
export interface ReadBack {
  state: "configured" | "not-configured" | "needs-update" | "refused";
  reason?: string;
  base_url?: string | null;
  key_set?: boolean;
  provider?: string | null;
  model?: string | null;
  legacy?: boolean;
  [k: string]: unknown;
}

/** The model the form starts from: pluxy's default model in the file, else none. */
export const modelOf = (r: ReadBack): string => (r.state === "refused" ? "" : (r.model ?? ""));

/** A model ID as plain text: 1 to 240 characters, no spaces or control characters, no `${` (Hermes
 * expands it). */
export const plainModel = (m: string): boolean =>
  m.length > 0 && length(m) <= 240 && !hasSpaceOrControl(m) && !m.includes("${");

/** A model ID the tool can write (hermes-setup's `settings`): plain text that YAML 1.1 also reads as
 * text, not as a number, a date or yes/no such as `1.5`, `2026-11-14` or `yes`. */
export const validModel = (m: string): boolean => plainModel(m) && !yaml11NotText(m);

// ---- YAML 1.1: hermes-setup's yaml11_not_text (src/hermes.rs), rule for rule ----------------------

/** The words YAML 1.1 reads as a boolean, a null or the value key. */
const WORDS = new Set([
  "y",
  "Y",
  "yes",
  "Yes",
  "YES",
  "n",
  "N",
  "no",
  "No",
  "NO",
  "true",
  "True",
  "TRUE",
  "false",
  "False",
  "FALSE",
  "on",
  "On",
  "ON",
  "off",
  "Off",
  "OFF",
  "~",
  "null",
  "Null",
  "NULL",
  "=",
  ".nan",
  ".NaN",
  ".NAN",
  ".inf",
  ".Inf",
  ".INF",
]);

const isDigit = (c: string | undefined) => c !== undefined && c >= "0" && c <= "9";

/** `[0-9][0-9_]*` at the start of s: its length (0 when s does not start with a digit). */
function digits(s: string): number {
  if (!isDigit(s[0])) return 0;
  let i = 1;
  while (i < s.length && (isDigit(s[i]) || s[i] === "_")) i++;
  return i;
}

/** `(:[0-5]?[0-9])+` at the start of s: its length (0 when there is none). */
function sexagesimal(s: string): number {
  let i = 0;
  for (;;) {
    if (s[i] !== ":") return i;
    let n: number;
    if (isDigit(s[i + 1]) && isDigit(s[i + 2]) && (s[i + 1] as string) <= "5") n = 2;
    else if (isDigit(s[i + 1])) n = 1;
    else return i;
    i += 1 + n;
  }
}

/** YAML 1.1's integers, without their sign. */
function int11(s: string): boolean {
  const all = (r: string, f: (c: string) => boolean) => r !== "" && [...r].every((c) => f(c) || c === "_");
  if (s.startsWith("0b")) return all(s.slice(2), (c) => c === "0" || c === "1");
  if (s.startsWith("0x")) return all(s.slice(2), (c) => /^[0-9a-fA-F]$/.test(c));
  if (s.startsWith("0")) {
    const r = s.slice(1);
    const octal = (x: string) => [...x].every((c) => (c >= "0" && c <= "7") || c === "_");
    return r.startsWith("o") ? r.length > 1 && octal(r.slice(1)) : octal(r);
  }
  if (!(s[0] !== undefined && s[0] >= "1" && s[0] <= "9")) return false;
  const n = digits(s);
  return n === s.length || sexagesimal(s.slice(n)) === s.length - n;
}

/** `[eE][-+]?[0-9]+` is all of s (with a required sign when signed). */
function exponent(s: string, signed: boolean): boolean {
  if (s[0] !== "e" && s[0] !== "E") return false;
  let r = s.slice(1);
  if (r[0] === "-" || r[0] === "+") r = r.slice(1);
  else if (signed) return false;
  return r !== "" && [...r].every(isDigit);
}

/** YAML 1.1's floats (ruamel's spellings): s as written, and without its sign. */
function float11(s: string, unsigned: string): boolean {
  // `.5`, `.5e+3`: no sign.
  if (s.startsWith(".")) {
    const r = s.slice(1);
    const n = digits(r);
    return n > 0 && (n === r.length || exponent(r.slice(n), true));
  }
  const n = digits(unsigned);
  if (n === 0) return false;
  const r = unsigned.slice(n);
  if (r.startsWith(".")) {
    const f = r.slice(1);
    let m = 0;
    while (m < f.length && (isDigit(f[m]) || f[m] === "_")) m++;
    return m === f.length || exponent(f.slice(m), false);
  }
  if (exponent(r, false)) return true;
  // Sexagesimal with a fraction: `1:30.5`.
  const m = sexagesimal(r);
  return m > 0 && r.slice(m).startsWith(".") && [...r.slice(m + 1)].every((c) => isDigit(c) || c === "_");
}

/** YAML 1.1's timestamps: `2001-12-14`, or with a time (`T` or `t`), seconds' fraction and zone. */
function timestamp11(s: string): boolean {
  const d = (i: number) => isDigit(s[i]);
  // Two digits (or one, when oneOrTwo) from i: the index after them.
  const num = (i: number, oneOrTwo: boolean): number | undefined => {
    if (!d(i)) return undefined;
    if (d(i + 1)) return i + 2;
    return oneOrTwo ? i + 1 : undefined;
  };
  if (!(d(0) && d(1) && d(2) && d(3)) || s[4] !== "-") return false;
  // The date alone takes exactly two-digit month and day.
  if (s.length === 10 && d(5) && d(6) && s[7] === "-" && d(8) && d(9)) return true;
  let i = num(5, true);
  if (i === undefined || s[i] !== "-") return false;
  i = num(i + 1, true);
  if (i === undefined || (s[i] !== "T" && s[i] !== "t")) return false;
  i = num(i + 1, true);
  if (i === undefined || s[i] !== ":") return false;
  i = num(i + 1, false);
  if (i === undefined || s[i] !== ":") return false;
  i = num(i + 1, false);
  if (i === undefined) return false;
  if (s[i] === ".") {
    i++;
    while (d(i)) i++;
  }
  if (i === s.length) return true;
  if (s[i] === "Z") return i + 1 === s.length;
  if (s[i] === "-" || s[i] === "+") {
    let j = num(i + 1, true);
    if (j !== undefined && s[j] === ":") j = num(j + 1, false);
    return j === s.length;
  }
  return false;
}

/** Whether a YAML 1.1 reader (Hermes reads config.yaml with ruamel's YAML 1.1 rules) takes the plain
 * text s for something other than text: a boolean, a null, the value key `=`, an integer (decimal,
 * `0b`, `0x`, octal, sexagesimal), a float (also `.inf` and `.nan`) or a timestamp. */
export function yaml11NotText(s: string): boolean {
  if (s === "" || WORDS.has(s)) return true;
  const unsigned = s.startsWith("-") || s.startsWith("+") ? s.slice(1) : s;
  return (
    unsigned === ".inf" ||
    unsigned === ".Inf" ||
    unsigned === ".INF" ||
    int11(unsigned) ||
    float11(s, unsigned) ||
    timestamp11(s)
  );
}

// The Edit form's draft of an account's settings (accountSettings.tsx), and the PATCH it makes.

import type { Account, AccountChange, AccountModelAlias, AccountModelEntry } from "../api";

export interface SettingsDraft {
  prefix: string;
  /** The account's proxy_url as core shows it, with "redacted" for a user name and password. */
  proxyUrl: string;
  priority: string;
  weight: string;
  disableCooling: boolean;
  maxConcurrency: string;
  retry: "inherit" | "custom";
  retryCount: string;
  models: { name: string; alias: string }[];
  /** One excluded-model rule per line. */
  excluded: string;
  /** The account's headers as JSON text, secret values as core's placeholders; empty for none. */
  headers: string;
  aliases: { name: string; alias: string; display_name: string; fork: boolean }[];
  /** The extension's settings as JSON text, secrets as core's placeholders; empty for none. */
  settings: string;
}

/** A field the draft holds no valid value for. */
export type InvalidField =
  | "priority"
  | "weight"
  | "maxConcurrency"
  | "retryCount"
  | "models"
  | "aliases"
  | "headersJson"
  | "headersObject"
  | "headersValue"
  | "settings"
  | "settingsNumber";

/** A model entry as a row: core writes one without an alias as its name alone. */
export function modelRow(m: string | AccountModelEntry): { name: string; alias: string } {
  return typeof m === "string" ? { name: m, alias: "" } : { name: m.name, alias: m.alias ?? "" };
}

export function draftOf(a: Account): SettingsDraft {
  return {
    prefix: a.prefix ?? "",
    proxyUrl: a.proxy_url ?? "",
    priority: a.priority ? String(a.priority) : "",
    weight: a.weight === undefined ? "" : String(a.weight),
    disableCooling: a.disable_cooling ?? false,
    maxConcurrency: a.max_concurrency ? String(a.max_concurrency) : "",
    retry: a.request_retry === undefined ? "inherit" : "custom",
    retryCount: a.request_retry === undefined ? "" : String(a.request_retry),
    models: (a.models ?? []).map(modelRow),
    excluded: (a.excluded_models ?? []).join("\n"),
    headers: a.headers && Object.keys(a.headers).length ? JSON.stringify(a.headers, null, 2) : "",
    aliases: (a.model_alias ?? []).map((r) => ({
      name: r.name,
      alias: r.alias,
      display_name: r.display_name ?? "",
      fork: r.fork ?? false,
    })),
    settings: a.settings ? JSON.stringify(a.settings, null, 2) : "",
  };
}

/** Whether the draft differs from the account as it was read. */
export function draftChanged(a: Account, d: SettingsDraft): boolean {
  return JSON.stringify(d) !== JSON.stringify(draftOf(a));
}

/** An integer from a field: null when empty, undefined when it is not a (non-negative) integer. */
function integer(text: string, min?: number): number | null | undefined {
  const s = text.trim();
  if (s === "") return null;
  if (!/^-?[0-9]+$/.test(s)) return undefined;
  const n = Number(s);
  return Number.isSafeInteger(n) && (min === undefined || n >= min) ? n : undefined;
}

function modelEntries(rows: { name: string; alias?: string }[]): AccountModelEntry[] | null | undefined {
  const out: AccountModelEntry[] = [];
  for (const r of rows) {
    const name = r.name.trim();
    const alias = (r.alias ?? "").trim();
    if (!name && !alias) continue;
    if (!name) return undefined;
    out.push(alias ? { name, alias } : { name });
  }
  return out.length ? out : null;
}

function aliasRules(
  rows: { name: string; alias: string; display_name?: string; fork?: boolean }[],
): AccountModelAlias[] | null | undefined {
  const out: AccountModelAlias[] = [];
  for (const r of rows) {
    const name = r.name.trim();
    const alias = r.alias.trim();
    const display = (r.display_name ?? "").trim();
    if (!name && !alias && !display) continue;
    if (!name || !alias) return undefined;
    out.push({
      name,
      alias,
      ...(r.fork ? { fork: true } : {}),
      ...(display ? { display_name: display } : {}),
    });
  }
  return out.length ? out : null;
}

/** The settings JSON text as an object: null when empty, undefined when it is not an object. */
export function settingsObject(text: string): Record<string, unknown> | null | undefined {
  if (text.trim() === "") return null;
  try {
    const v: unknown = JSON.parse(text);
    return v !== null && typeof v === "object" && !Array.isArray(v)
      ? (v as Record<string, unknown>)
      : undefined;
  } catch {
    return undefined;
  }
}

/**
 * The headers text as a map: null when empty or `{}`, a field name when it is not a JSON object
 * whose values are all text.
 */
function headerMap(text: string): Record<string, string> | null | Extract<InvalidField, `headers${string}`> {
  if (text.trim() === "") return null;
  let v: unknown;
  try {
    v = JSON.parse(text);
  } catch {
    return "headersJson";
  }
  if (v === null || typeof v !== "object" || Array.isArray(v)) return "headersObject";
  const out = v as Record<string, unknown>;
  if (!Object.values(out).every((x) => typeof x === "string")) return "headersValue";
  return Object.keys(out).length ? (out as Record<string, string>) : null;
}

/** Headers as a list of name and value in name order, so two spellings of one map compare equal. */
const headerList = (h: Record<string, string> | null | undefined) =>
  Object.entries(h ?? {}).sort(([x], [y]) => (x < y ? -1 : x > y ? 1 : 0));

/** A decimal number's value as digits and an exponent, so two spellings can be compared. */
function decimal(text: string): string | undefined {
  const m = /^-?([0-9]+)(?:\.([0-9]+))?(?:[eE]([+-]?[0-9]+))?$/.exec(text);
  if (!m) return undefined;
  const frac = m[2] ?? "";
  let digits = (m[1] + frac).replace(/^0+/, "");
  let exp = Number(m[3] ?? 0) - frac.length;
  if (digits === "") return "0";
  while (digits.endsWith("0")) {
    digits = digits.slice(0, -1);
    exp++;
  }
  return `${text.startsWith("-") ? "-" : ""}${digits}e${exp}`;
}

/** Whether every number in a JSON text keeps its exact value when the browser reads it. */
export function exactNumbers(text: string): boolean {
  const number = /-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?/y;
  for (let i = 0; i < text.length; i++) {
    const c = text.charAt(i);
    if (c === '"') {
      for (i++; i < text.length && text[i] !== '"'; i++) if (text[i] === "\\") i++;
    } else if (c === "-" || (c >= "0" && c <= "9")) {
      number.lastIndex = i;
      const tok = number.exec(text)?.[0];
      if (!tok) return false;
      const read = Number(tok);
      if (!Number.isFinite(read) || decimal(tok) !== decimal(String(read).replace("e+", "e"))) return false;
      i += tok.length - 1;
    }
  }
  return true;
}

/**
 * The PATCH that turns account a into the draft: only the fields that changed, null for one set
 * back to its default. Settings are sent only for an account added by login.
 */
export function accountChange(
  a: Account,
  d: SettingsDraft,
): { change: AccountChange; invalid?: undefined } | { invalid: InvalidField } {
  const change: AccountChange = {};
  const differs = (x: unknown, y: unknown) => JSON.stringify(x ?? null) !== JSON.stringify(y ?? null);

  const prefix = d.prefix.trim() || null;
  if (differs(prefix, a.prefix)) change.prefix = prefix;

  // Core shows the URL without its user name and password. It is sent only when the person
  // changed it, so the stored one, password included, stays unless they type another.
  const proxy = d.proxyUrl.trim() || null;
  if (differs(proxy, a.proxy_url)) change.proxy_url = proxy;

  const priority = integer(d.priority);
  if (priority === undefined) return { invalid: "priority" };
  if (differs(priority || null, a.priority || null)) change.priority = priority;

  const weight = integer(d.weight);
  if (weight === undefined) return { invalid: "weight" };
  if (differs(weight, a.weight)) change.weight = weight;

  if (d.disableCooling !== (a.disable_cooling ?? false)) change.disable_cooling = d.disableCooling;

  const concurrency = integer(d.maxConcurrency, 0);
  if (concurrency === undefined) return { invalid: "maxConcurrency" };
  if (differs(concurrency || null, a.max_concurrency || null)) change.max_concurrency = concurrency || null;

  let retry: number | null = null;
  if (d.retry === "custom") {
    const n = integer(d.retryCount, 0);
    if (n === undefined || n === null) return { invalid: "retryCount" };
    retry = n;
  }
  if (differs(retry, a.request_retry)) change.request_retry = retry;

  const models = modelEntries(d.models);
  if (models === undefined) return { invalid: "models" };
  if (differs(models, modelEntries((a.models ?? []).map(modelRow)))) change.models = models;

  const excluded = d.excluded
    .split("\n")
    .map((l) => l.trim())
    .filter((l) => l !== "");
  const excludedNext = excluded.length ? excluded : null;
  if (differs(excludedNext, a.excluded_models?.length ? a.excluded_models : null))
    change.excluded_models = excludedNext;

  // Headers go only when their text changed, and then all of them: core keeps the value a
  // placeholder stands for, so a secret the person did not touch goes back as it was shown.
  if (d.headers !== draftOf(a).headers) {
    const headers = headerMap(d.headers);
    if (typeof headers === "string") return { invalid: headers };
    if (differs(headerList(headers), headerList(a.headers))) change.headers = headers;
  }

  const aliases = aliasRules(d.aliases);
  if (aliases === undefined) return { invalid: "aliases" };
  if (differs(aliases, aliasRules(a.model_alias ?? []))) change.model_alias = aliases;

  // Settings go only when their text changed, and never with a number the browser does not
  // keep exactly: in the text, or in the stored settings as core marks them.
  if (a.source === "login" && d.settings !== draftOf(a).settings) {
    const settings = settingsObject(d.settings);
    if (settings === undefined) return { invalid: "settings" };
    if (!exactNumbers(d.settings) || a.settings_inexact) return { invalid: "settingsNumber" };
    if (differs(settings, a.settings)) change.settings = settings;
  }
  return { change };
}

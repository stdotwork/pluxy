// The kit's string layer. Each page or part declares its English texts beside it, under its own
// namespace; a locale is a data file of "namespace.key" texts that the host loads and passes to
// setLocale. A key the locale lacks shows the English. There is no central catalogue.
//
//   const t = strings("accounts", {
//     title: "Accounts",
//     selected: { one: "{count} account selected", other: "{count} accounts selected" },
//   });
//   t("title");                    // "Accounts", or the locale's "accounts.title"
//   t("selected", { count: 3 });   // "3 accounts selected"

import type { ComponentChildren } from "preact";

/** A text that depends on a count: one/other by the language's plural rules. */
export interface Plural {
  one: string;
  other: string;
}

export type Text = string | Plural;

/** Values for a text's {name} placeholders. */
export type Vars = Record<string, string | number>;

/** A locale as the host loads it: its language tag and its texts by "namespace.key". */
export interface Locale {
  lang: string;
  texts: Record<string, Text>;
}

let locale: Locale | undefined;
let rules = new Intl.PluralRules("en");

/**
 * Sets the locale every text is shown in from now on; undefined is English. Call it before
 * rendering (a page shows the locale it finds when it renders).
 */
export function setLocale(l: Locale | undefined) {
  locale = l;
  rules = new Intl.PluralRules(l?.lang ?? "en");
}

/** Replaces each {name} in text with its value; a placeholder with no value stays as written. */
export function format(text: string, vars?: Vars): string {
  if (!vars) return text;
  return text.replace(/\{(\w+)\}/g, (m, name: string) =>
    Object.hasOwn(vars, name) ? String(vars[name]) : m,
  );
}

/**
 * Puts markup into the placeholders a text still holds, for JSX:
 * rich(t("help"), { path: <span class="mono">README.md</span> }).
 */
export function rich(text: string, parts: Record<string, ComponentChildren>): ComponentChildren[] {
  return text
    .split(/\{(\w+)\}/)
    .map((piece, i) => (i % 2 === 0 ? piece : Object.hasOwn(parts, piece) ? parts[piece] : `{${piece}}`));
}

const declared = new Set<string>();

/** A plural key takes its count; other keys take placeholder values when their text has any. */
type Args<V> = V extends Plural ? [vars: Vars & { count: number }] : [vars?: Vars];

/**
 * Declares a namespace's English texts and returns its lookup function. Each namespace is
 * declared once, by the module whose texts it holds.
 */
export function strings<const T extends Record<string, Text>>(namespace: string, english: T) {
  if (declared.has(namespace)) throw new Error(`strings: namespace ${namespace} is declared twice`);
  declared.add(namespace);
  return <K extends keyof T & string>(key: K, ...[vars]: Args<T[K]>): string => {
    const own = locale?.texts[`${namespace}.${key}`];
    const fallback = english[key] as Text;
    // A locale's text must be the same kind as the English one, or the English shows.
    const text = own !== undefined && typeof own === typeof fallback ? own : fallback;
    if (typeof text === "string") return format(text, vars);
    const count = Number(vars?.count ?? 0);
    // shortcut: only one/other; a language's few and many categories show the other form. Add
    // the categories to Plural when a locale needs them.
    return format(rules.select(count) === "one" ? text.one : text.other, vars);
  };
}

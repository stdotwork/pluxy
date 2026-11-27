/** The Config page's tabs, in order. The first is the one `#/config` opens. */
export const configTabIds = ["settings", "source"] as const;
export type ConfigTab = (typeof configTabIds)[number];

/** The Settings tab's sections, by the id each has on the page. */
export type ConfigSection = "network" | "logging" | "keys";

export interface ConfigAddress {
  tab: ConfigTab;
  /** The section of the Settings tab the address opens at. */
  section?: ConfigSection;
  /** The account the Settings file tab opens at. */
  account?: string;
}

/**
 * The old tabs' addresses, which still open where their settings now are: `#/config/common` and
 * `#/config/network` at Network, `#/config/logging` at Logging, `#/config/connectivity` and
 * `#/config/advanced` at Keys.
 */
const OLD_TABS: Record<string, ConfigSection> = {
  common: "network",
  network: "network",
  logging: "logging",
  connectivity: "keys",
  advanced: "keys",
};

/**
 * Reads a page address of the Config page: `#/config`, `#/config/<tab>` and
 * `#/config/source?account=<id>`. An old tab's address opens the Settings tab at the section that
 * holds its settings; any other name opens the Settings tab. Undefined for the address of another
 * page.
 */
export function parseConfigAddress(hash: string): ConfigAddress | undefined {
  const m = /^#\/config(?:\/([^?]*))?(?:\?(.*))?$/.exec(hash);
  if (!m) return undefined;
  const named = (m[1] ?? "").split("/")[0] ?? "";
  const tab: ConfigTab = named === "source" ? "source" : "settings";
  const section = tab === "settings" ? OLD_TABS[named] : undefined;
  let account: string | undefined;
  for (const part of (m[2] ?? "").split("&")) {
    if (!part.startsWith("account=")) continue;
    account = part.slice("account=".length);
    try {
      account = decodeURIComponent(account);
    } catch {
      /* Keep malformed input as text. */
    }
    break;
  }
  return { tab, ...(section ? { section } : {}), ...(account ? { account } : {}) };
}

/** The address of a tab of the Config page, opening the Settings file at an account if one is given. */
export function configAddress(tab: ConfigTab, account?: string): string {
  const path = tab === "source" ? "#/config/source" : "#/config";
  return account ? `${path}?account=${encodeURIComponent(account)}` : path;
}

/** Whether the text before `at` on its line has opened a YAML comment (a # after a space). */
function inComment(content: string, at: number): boolean {
  const from = content.lastIndexOf("\n", at - 1) + 1;
  let quote = "";
  for (let i = from; i < at; i++) {
    const c = content[i];
    if (quote) {
      if (c === quote) quote = "";
      else if (c === "\\" && quote === '"') i++;
    } else if (c === '"' || c === "'") quote = c;
    else if (c === "#" && (i === from || /\s/.test(content[i - 1] ?? ""))) return true;
  }
  return false;
}

/**
 * Finds where an account's ID is written in the config text, as the range of the ID itself, for
 * an account listed under `extension` or `extensions` (`accounts[].id`), in a JSON or a YAML
 * file. Undefined when the file does not hold that ID in exactly one place that is not a comment:
 * an account of a provider key section (`claude-api-key`, `api-keys.codex`, ...) has no `id`, its
 * ID is made from the entry's name and place (see docs/config.md, "Provider key sections"), and the
 * file does not say it.
 */
export function findAccount(content: string, id: string): { start: number; end: number } | undefined {
  const escaped = id.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const key = new RegExp(
    `(^|[\\s{,])["']?id["']?\\s*:\\s*(["']?)${escaped}\\2(?=\\s*(?:[,}\\]]|$)|\\s+#)`,
    "gm",
  );
  const found: { start: number; end: number }[] = [];
  for (const m of content.matchAll(key)) {
    if (inComment(content, m.index + (m[1] ?? "").length)) continue;
    const end = m.index + m[0].length - (m[2] ?? "").length;
    found.push({ start: end - id.length, end });
  }
  return found.length === 1 ? found[0] : undefined;
}

/** What the review shows in place of a secret. */
export const HIDDEN = "••••••••";

// Which values are secrets, after core's rules (internal/configdoc/redact.go): a key named like a
// secret, compared in lower case with its words written together and a trailing number or word
// saying how it is written left out.
const SECRET_NAMES = new Set(["apikeys", "managementkey", "clientkeys"]);
const SECRET_ENDINGS = [
  "token",
  "secret",
  "secrets",
  "password",
  "passwords",
  "apikey",
  "secretkey",
  "privatekey",
  "secretaccesskey",
  "passphrase",
];
const SPELLING_WORDS = [
  "value",
  "pem",
  "der",
  "data",
  "raw",
  "b64",
  "base64",
  "hex",
  "str",
  "string",
  "text",
  "bytes",
];
// The provider sections at the top of a config.yaml, whose entries are read by the rules above
// rather than hidden whole: the older lists and the api-keys mapping that holds the newer ones.
const PROVIDER_SECTIONS = new Set([
  "api-keys",
  "codex-api-key",
  "claude-api-key",
  "gemini-api-key",
  "vertex-api-key",
  "xai-api-key",
  "openai-compatibility",
  "meta-api-key",
  "interactions-api-key",
]);
const PLACEHOLDER = /^pluxy-redacted-[0-9a-f]{24}$/;

/** Whether a key of the settings file is named like a secret (api-key, management_key, clientKeys, …). */
export function secretKeyName(key: string): boolean {
  const words = key
    .replace(/([a-z0-9])([A-Z])/g, "$1 $2")
    .toLowerCase()
    .split(/[\s_.-]+/)
    .filter(Boolean);
  while (words.length > 1) {
    const last = words[words.length - 1] ?? "";
    if (!SPELLING_WORDS.includes(last) && last.replace(/[0-9]/g, "") !== "") break;
    words.pop();
  }
  if (words.length) words[words.length - 1] = (words[words.length - 1] ?? "").replace(/[0-9]+$/, "");
  const name = words.join("");
  return SECRET_NAMES.has(name) || SECRET_ENDINGS.some((end) => name.endsWith(end));
}

const QUOTED = /"(?:[^"\\]|\\.)*"|'(?:[^']|'')*'/g;

/** A quoted value hidden, unless it is core's placeholder, which stands for a secret and holds none. */
const hideQuoted = (s: string) => (PLACEHOLDER.test(s.slice(1, -1)) ? s : `${s[0]}${HIDDEN}${s[0]}`);

/** A value written after a secret key on its line, hidden: every string in it, or the bare value itself. */
function hideValue(value: string): string {
  if (/["']/.test(value)) return value.replace(QUOTED, hideQuoted);
  const m = /^(\s*)(.*?)(\s+#.*)?$/.exec(value) ?? [];
  const [, lead = "", bare = "", comment = ""] = m;
  if (
    bare === "" ||
    /^[|>][+-]?$|^[[{]$/.test(bare) ||
    /^(true|false|null|~)$/i.test(bare) ||
    PLACEHOLDER.test(bare)
  )
    return value;
  // A flow list or mapping: each item, or each mapping value.
  if (/^\[.*\]$/.test(bare))
    return `${lead}[${bare
      .slice(1, -1)
      .split(",")
      .map((item) =>
        item.trim() && !PLACEHOLDER.test(item.trim()) ? item.replace(item.trim(), HIDDEN) : item,
      )
      .join(",")}]${comment}`;
  return `${lead}${HIDDEN}${comment}`;
}

/** The end of the JSON value that starts at from (a string, a number or word, or a list or object). */
function jsonValueEnd(line: string, from: number): number {
  let depth = 0;
  for (let i = from; i < line.length; i++) {
    const c = line[i];
    if (c === '"') {
      for (i++; i < line.length && line[i] !== '"'; i++) if (line[i] === "\\") i++;
      if (depth === 0) return i + 1;
    } else if (c === "[" || c === "{") depth++;
    else if (c === "]" || c === "}") {
      if (depth === 0) return i;
      if (--depth === 0) return i + 1;
    } else if (c === "," && depth === 0) return i;
  }
  return line.length;
}

/** Each secret key's value in a line of JSON (such as core's file, written on one line), hidden. */
function hideJsonPairs(line: string): string {
  let out = "";
  let at = 0;
  const pair = /"((?:[^"\\]|\\.)*)"\s*:\s*/g;
  for (let m = pair.exec(line); m; m = pair.exec(line)) {
    if (!secretKeyName(m[1] ?? "")) continue;
    const start = m.index + m[0].length;
    const end = jsonValueEnd(line, start);
    // Each string in the value, but not the keys of an object in it.
    const value = line
      .slice(start, end)
      .replace(/("(?:[^"\\]|\\.)*")(\s*:)?/g, (s, quoted: string, key?: string) =>
        key ? s : hideQuoted(quoted),
      );
    out += line.slice(at, start) + value;
    at = end;
    pair.lastIndex = end;
  }
  return out + line.slice(at);
}

/** A URL's user name, password and query, hidden, wherever one is written. */
const hideUrls = (line: string) =>
  line
    .replace(/([a-z][a-z0-9+.-]*:\/\/)[^\s/@"'<>]+@/gi, `$1${HIDDEN}@`)
    .replace(/([a-z][a-z0-9+.-]*:\/\/[^\s"'?#]*)\?[^\s"'#,]+/gi, `$1?${HIDDEN}`);

/**
 * The lines of a settings file (as the review splits them, each with its line ending) with the
 * secrets written in them hidden: the value of a key named like a secret and everything below it,
 * and a URL's user name, password and query. Core's placeholders stay as they are. It follows
 * core's rules closely enough to hide what a person types into the file, not to decide what core
 * keeps secret.
 */
export function hideSecrets(text: string, format: "json" | "yaml"): string[] {
  const lines = text.match(/[^\n]*\n|[^\n]+$/g) ?? [];
  // The indent of the secret key whose block the lines are in, if any.
  let below: number | undefined;
  return lines.map((line) => {
    const body = line.replace(/\n$/, "");
    const content = body.trim();
    const indent = body.length - body.trimStart().length;
    let out = body;
    const listItem = /^-(\s|$)/.test(content);
    if (content === "" || content.startsWith("#")) return line;
    if (below !== undefined && (indent > below || (format === "yaml" && indent === below && listItem))) {
      // A line below a secret key: keys stay, every value goes.
      const keyed = /^(-\s+)?((?:"(?:[^"\\]|\\.)*"|'[^']*'|[^"'#:\s][^:#]*?)\s*:)(\s.*|$)/.exec(content);
      const item = /^(-\s+)(.*)$/.exec(content);
      const hidden = keyed
        ? `${keyed[1] ?? ""}${keyed[2]}${hideValue(keyed[3] ?? "")}`
        : item
          ? `${item[1]}${hideValue(item[2] ?? "")}`
          : hideValue(content);
      return `${body.slice(0, indent)}${hidden}${line.endsWith("\n") ? "\n" : ""}`;
    }
    below = undefined;
    const keyed =
      format === "json"
        ? /^()"((?:[^"\\]|\\.)*)"\s*:(.*)$/.exec(content)
        : /^(-\s+)?(?:"((?:[^"\\]|\\.)*)"|'([^']*)'|([^"'#:\s][^:#]*?))\s*:(\s.*|$)/.exec(content);
    if (keyed) {
      const dash = keyed[1] ?? "";
      const key = (format === "json" ? keyed[2] : (keyed[2] ?? keyed[3] ?? keyed[4])) ?? "";
      const value = (format === "json" ? keyed[3] : keyed[5]) ?? "";
      const section = format === "yaml" && indent === 0 && PROVIDER_SECTIONS.has(key);
      if (secretKeyName(key) && !section) {
        const v = value.trim();
        // The value starts on the next lines: a block, a list or an object left open.
        if (v === "" || /^[|>][+-]?(\s+#.*)?$/.test(v) || /^[[{],?$/.test(v)) below = indent + dash.length;
        out = body.slice(0, body.length - value.length) + hideValue(value);
      }
    }
    if (format === "json") out = hideJsonPairs(out);
    return `${hideUrls(out)}${line.endsWith("\n") ? "\n" : ""}`;
  });
}

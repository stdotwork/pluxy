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
 * ID is made from the entry's name and place (see the README's provider key sections), and the
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

/** The Config Panel's tabs, in order. The first is the one `#/config` opens. */
export const configTabIds = ["common", "connectivity", "network", "logging", "advanced", "source"] as const;

export interface ConfigAddress {
  tab: string;
  /** The account the Source tab opens at. */
  account?: string;
}

/**
 * Reads a page address of the Config Panel: `#/config`, `#/config/<tab>` and
 * `#/config/source?account=<id>`. A tab the page does not have opens the first one. Undefined for
 * the address of another page.
 */
export function parseConfigAddress(hash: string): ConfigAddress | undefined {
  const m = /^#\/config(?:\/([^?]*))?(?:\?(.*))?$/.exec(hash);
  if (!m) return undefined;
  const named = (m[1] ?? "").split("/")[0] ?? "";
  const tab = configTabIds.find((id) => id === named) ?? configTabIds[0];
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
  return account ? { tab, account } : { tab };
}

/** The address of a tab of the Config Panel, opening Source at an account if one is given. */
export function configAddress(tab: string, account?: string): string {
  const path = tab === configTabIds[0] ? "#/config" : `#/config/${tab}`;
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

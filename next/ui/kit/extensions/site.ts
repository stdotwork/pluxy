// Where the panel may show an extension page from: only the pages site core names, and only when
// that is another site than the panel's own (extension-pages/README.md).

function hostname(h: string): string {
  return h
    .toLowerCase()
    .replace(/^\[|\]$/g, "")
    .replace(/\.$/, "");
}

function isAddress(h: string): boolean {
  return h.includes(":") || /^\d+(\.\d+){3}$/.test(h);
}

/**
 * Whether two hosts may share cookies or a site: the same address, or names that end in the
 * same two labels (pages.example.com and panel.example.com share example.com's cookies). With no
 * list of public suffixes, this errs toward refusing: a.example.co.uk and b.other.co.uk count as
 * one site too.
 */
export function related(a: string, b: string): boolean {
  const x = hostname(a);
  const y = hostname(b);
  if (x === y) return true;
  if (isAddress(x) || isAddress(y)) return false;
  const tail = (h: string) => h.split(".").slice(-2).join(".");
  return tail(x) === tail(y);
}

/**
 * Why the page at pageUrl may not be shown in a panel on panelHost, or undefined when it may:
 * it must be on pagesOrigin, over http or https, and pagesOrigin must not be the panel's site.
 * A host that shows each page in a webview of its own, sharing no cookies or storage with the
 * host's document (the desktop app), passes no panelHost: only the address is checked.
 */
export function pageProblem(
  pageUrl: string,
  pagesOrigin: string | undefined,
  panelHost?: string,
): string | undefined {
  if (!pagesOrigin) return "core has no address for extension pages";
  let page: URL;
  let origin: URL;
  try {
    page = new URL(pageUrl);
    origin = new URL(pagesOrigin);
  } catch {
    return "core gave an address that is not a URL";
  }
  if (origin.protocol !== "http:" && origin.protocol !== "https:")
    return "extension pages must be served over http or https";
  if (page.origin !== origin.origin) return "the page is not on the extension pages site";
  if (panelHost !== undefined && related(origin.hostname, panelHost))
    return `extension pages are on ${origin.host}, the panel's own site: set extension_pages.url to another host name`;
  return undefined;
}

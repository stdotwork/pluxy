// Where the panel may show an extension page from: only the pages site core names, and only when
// that is another site than the panel's own (extension-pages/README.md).

function hostname(h: string): string {
  return h
    .toLowerCase()
    .replace(/^\[|\]$/g, "")
    .replace(/\.$/, "");
}

/** Whether two host names may share cookies or a site: the same, or one under the other. */
export function related(a: string, b: string): boolean {
  const x = hostname(a);
  const y = hostname(b);
  return x === y || x.endsWith(`.${y}`) || y.endsWith(`.${x}`);
}

/**
 * Why the page at pageUrl may not be shown in a panel on panelHost, or undefined when it may:
 * it must be on pagesOrigin, over http or https, and pagesOrigin must not be the panel's site.
 */
export function pageProblem(
  pageUrl: string,
  pagesOrigin: string | undefined,
  panelHost: string,
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
  if (related(origin.hostname, panelHost))
    return `extension pages are on ${origin.host}, the panel's own site: set extension_pages.url to another host name`;
  return undefined;
}

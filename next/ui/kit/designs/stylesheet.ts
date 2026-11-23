// Links a direction's stylesheet files. The panel's CSP takes styles from core only and no inline
// styles, and the theme engine owns the document's constructed stylesheets, so a direction's CSS
// arrives as <link> elements.

/**
 * Adds a stylesheet link for each address not linked yet, in order, and resolves when all have
 * loaded or failed: a direction renders once its styles are there, never unstyled first.
 */
export function linkStylesheets(hrefs: readonly string[], doc: Document = document): Promise<void> {
  const loads = hrefs.map((href) => {
    for (const link of doc.querySelectorAll<HTMLLinkElement>("link[data-design-style]")) {
      if (link.getAttribute("href") === href) return Promise.resolve();
    }
    return new Promise<void>((done) => {
      const link = doc.createElement("link");
      link.rel = "stylesheet";
      link.href = href;
      link.dataset.designStyle = "";
      link.addEventListener("load", () => done());
      link.addEventListener("error", () => done());
      doc.head.append(link);
    });
  });
  return Promise.all(loads).then(() => {});
}

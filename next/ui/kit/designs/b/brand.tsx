// Direction B's brand: the word "pluxy" set in Literata and closed with a full stop, the way the
// panel's verdict is a sentence. The full stop is the one coloured part: it takes the colour of
// the state pluxy is in, so the tab's icon says whether anything needs the person before the page
// is read. The mark is the word's first letter and the same full stop.

import { P_PATH, WORD_PATH } from "./brandPaths";

/** What the full stop says: nothing known yet, all serving, something waiting, something needs the person. */
export type BrandState = "unknown" | "ok" | "waiting" | "needs";

/** The wordmark, height px tall (the letters' own height, ascender to descender). */
export function Wordmark(props: { height?: number; state?: BrandState }) {
  const height = props.height ?? 26;
  return (
    <svg
      class="db-wordmark"
      viewBox="0 -80 306 106"
      height={height}
      width={(height * 306) / 106}
      role="img"
      aria-label="pluxy"
    >
      <path d={WORD_PATH} fill="currentColor" />
      <circle class="db-stop" data-state={props.state ?? "unknown"} cx="296.5" cy="-7.5" r="8" />
    </svg>
  );
}

/** The mark: "p" and the full stop, in a square. */
export function Mark(props: { size?: number; state?: BrandState }) {
  const size = props.size ?? 24;
  return (
    <svg class="db-mark" viewBox="-3 -62 92 92" width={size} height={size} role="img" aria-label="pluxy">
      <path d={P_PATH} fill="currentColor" />
      <circle class="db-stop" data-state={props.state ?? "unknown"} cx="76" cy="-9" r="9.5" />
    </svg>
  );
}

// The tab's icon cannot read the page's CSS variables: these are the light palette's values.
const TILE = "#ffffff";
const INK = "#1b1a17";
const STOP: Record<BrandState, string> = {
  unknown: "#1b1a17",
  ok: "#1f7a43",
  waiting: "#b06a00",
  needs: "#c22a1f",
};

/** The favicon as a data: address, which the panel's CSP allows for images: the mark on a white tile. */
export function faviconHref(state: BrandState): string {
  const svg =
    '<svg xmlns="http://www.w3.org/2000/svg" viewBox="-9 -68 104 104">' +
    `<rect x="-9" y="-68" width="104" height="104" rx="22" fill="${TILE}"/>` +
    `<path d="${P_PATH}" fill="${INK}"/>` +
    `<circle cx="76" cy="-9" r="10.5" fill="${STOP[state]}"/>` +
    "</svg>";
  return `data:image/svg+xml,${encodeURIComponent(svg)}`;
}

/** Shows the favicon for state, replacing the one this direction showed before. */
export function showFavicon(state: BrandState, doc: Document = document): void {
  let link = doc.head.querySelector<HTMLLinkElement>("link[data-db-favicon]");
  if (!link) {
    link = doc.createElement("link");
    link.rel = "icon";
    link.type = "image/svg+xml";
    link.dataset.dbFavicon = "";
    doc.head.append(link);
  }
  link.href = faviconHref(state);
}

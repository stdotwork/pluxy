// Direction A's brand: the mark, the wordmark and the favicon. The mark is the thing pluxy does
// that a plain proxy does not: one request comes in, the first account is tried and left (the
// hollow node), the next one serves (the filled node). It is drawn on a 16-unit grid with square
// 2-unit strokes, so it holds at 16 px. The wordmark is drawn with the same stroke.

import type { VNode } from "preact";

/** The mark's shapes, shared by the component and the favicon. served is the colour of the node that answers. */
const markShapes = (line: string, served: string) =>
  `<path d="M1 8h5M6 4v8M6 4h4M6 12h4" fill="none" stroke="${line}" stroke-width="2" stroke-linecap="square"/>` +
  `<rect x="11" y="2" width="4" height="4" fill="none" stroke="${line}" stroke-width="1.5"/>` +
  `<rect x="10.25" y="9.25" width="5.5" height="5.5" fill="${served}"/>`;

/** The mark at size px. It takes the text colour, with the serving node in the accent. */
export function Mark(props: { size?: number; title?: string }): VNode {
  const size = props.size ?? 20;
  return (
    <svg
      class="da-mark"
      width={size}
      height={size}
      viewBox="0 0 16 16"
      role={props.title ? "img" : undefined}
      aria-label={props.title}
      aria-hidden={props.title ? undefined : "true"}
    >
      <path
        d="M1 8h5M6 4v8M6 4h4M6 12h4"
        fill="none"
        stroke="currentColor"
        stroke-width="2"
        stroke-linecap="square"
      />
      <rect x="11" y="2" width="4" height="4" fill="none" stroke="currentColor" stroke-width="1.5" />
      <rect class="da-mark-served" x="10.25" y="9.25" width="5.5" height="5.5" />
    </svg>
  );
}

/**
 * "pluxy" as five monoline letters, 17 units high (the p and y descend to 17, the l rises to 1).
 * The x, where two lines cross, is in the accent.
 */
export function Wordmark(props: { height?: number }): VNode {
  const height = props.height ?? 14;
  return (
    <svg
      class="da-wordmark"
      height={height}
      width={(height * 55) / 18}
      viewBox="0 0 55 18"
      role="img"
      aria-label="pluxy"
      fill="none"
      stroke="currentColor"
      stroke-width="2"
      stroke-linecap="square"
      stroke-linejoin="miter"
    >
      {/* p */}
      <path d="M2 17V6M2 6h3.5a3.5 3.5 0 0 1 0 7H2" />
      {/* l */}
      <path d="M14 1v9.5a2.5 2.5 0 0 0 2.5 2.5H17" />
      {/* u */}
      <path d="M23 6v3.5a3.5 3.5 0 0 0 7 0M30 6v7" />
      {/* x */}
      <path class="da-wordmark-x" d="M35 6l7 7M42 6l-7 7" />
      {/* y */}
      <path d="M46 6l3.5 7M53 6l-4.8 11" />
    </svg>
  );
}

/** The favicon: the mark on a dark tile, as a data: address the panel's CSP allows for images. */
export function faviconHref(): string {
  const svg =
    '<svg xmlns="http://www.w3.org/2000/svg" viewBox="-2 -2 20 20">' +
    '<rect x="-2" y="-2" width="20" height="20" rx="4" fill="#11110f"/>' +
    markShapes("#eceae3", "#4cc9e0") +
    "</svg>";
  return `data:image/svg+xml,${encodeURIComponent(svg)}`;
}

/** Shows the favicon while the direction is on; the function it returns takes it away again. */
export function showFavicon(doc: Document = document): () => void {
  const link = doc.createElement("link");
  link.rel = "icon";
  link.type = "image/svg+xml";
  link.href = faviconHref();
  doc.head.append(link);
  return () => link.remove();
}

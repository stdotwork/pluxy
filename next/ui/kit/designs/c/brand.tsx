// Direction C's brand: the mark is the x of pluxy drawn as two routes changing tracks, which is
// what the proxy does when one account cannot take a request and another does. The wordmark is
// the name in the direction's own face. The favicon is the mark alone on its tile, with strokes
// heavy enough to read at 16 pixels.

import favicon from "./favicon.svg" with { type: "file" };

export function Mark(props: { size?: number }) {
  const size = props.size ?? 26;
  return (
    <svg class="dc-mark" width={size} height={size} viewBox="0 0 24 24" aria-hidden="true" focusable="false">
      <rect class="dc-mark-tile" width="24" height="24" rx="7" />
      <path class="dc-mark-line" d="M5.5 8C11.5 8 12.5 16 18.5 16" />
      <path class="dc-mark-line" d="M5.5 16C11.5 16 12.5 8 18.5 8" />
    </svg>
  );
}

export function Wordmark() {
  return <span class="dc-brand-word">pluxy</span>;
}

/** Shows the direction's favicon in place of the page's own (the panel has none of its own). */
export function showFavicon(doc: Document = document): () => void {
  const link = doc.createElement("link");
  link.rel = "icon";
  link.type = "image/svg+xml";
  link.href = favicon;
  doc.head.append(link);
  return () => link.remove();
}

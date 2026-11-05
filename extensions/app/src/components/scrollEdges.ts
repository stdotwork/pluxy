import './scrollEdges.css';

/**
 * Marks a horizontally scrolling element with `data-scroll-start` / `data-scroll-end` while content is hidden past that edge, so
 * scrollEdges.css can fade the edge: a cue that more columns exist when the scrollbar is hidden (overlay scrollbars, or tables that
 * hide their own). Also sets `--scroll-edge-bar` to the vertical scrollbar's width so the fade stops short of it.
 * Use as a React 19 callback ref: `ref={scrollEdges}`; it returns its own cleanup.
 */
export function scrollEdges(element: HTMLElement | null): (() => void) | undefined {
  if (!element) return undefined;
  element.classList.add('scroll-edges');
  const update = () => {
    const style = getComputedStyle(element);
    const bar = Math.max(0, element.offsetWidth - element.clientWidth - parseFloat(style.borderLeftWidth || '0') - parseFloat(style.borderRightWidth || '0'));
    // A reserved scrollbar gutter without a vertical scrollbar makes Chrome stop scrolling the gutter's width short of the end.
    const slack = element.scrollHeight > element.clientHeight ? 0 : bar;
    const end = element.scrollWidth - element.clientWidth - element.scrollLeft - slack;
    element.toggleAttribute('data-scroll-start', element.scrollLeft > 1);
    element.toggleAttribute('data-scroll-end', end > 1);
    element.style.setProperty('--scroll-edge-bar', `${bar}px`);
  };
  update();
  element.addEventListener('scroll', update, { passive: true });
  const observer = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(update);
  observer?.observe(element);
  // Content width changes (columns shown, rows loaded) do not resize the scroller itself.
  if (observer) for (const child of element.children) observer.observe(child);
  return () => {
    element.removeEventListener('scroll', update);
    observer?.disconnect();
  };
}

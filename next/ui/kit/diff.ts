export interface DiffLine {
  change: "same" | "add" | "remove";
  /** Includes its line ending, so a changed final newline is visible in the diff. */
  text: string;
}

const lines = (text: string): string[] => text.match(/[^\n]*\n|[^\n]+$/g) ?? [];

/** Preserve common ends, then find a shortest edit path for the remaining middle. */
export function lineDiff(before: string, after: string): DiffLine[] {
  const old = lines(before);
  const next = lines(after);
  let start = 0;
  while (start < old.length && start < next.length && old[start] === next[start]) start++;
  let end = 0;
  while (
    end < old.length - start &&
    end < next.length - start &&
    old[old.length - end - 1] === next[next.length - end - 1]
  )
    end++;
  const context = (items: string[]): DiffLine[] => items.map((text) => ({ change: "same", text }));
  return [
    ...context(old.slice(0, start)),
    ...middleDiff(old.slice(start, old.length - end), next.slice(start, next.length - end)),
    ...context(old.slice(old.length - end)),
  ];
}

function middleDiff(a: string[], b: string[]): DiffLine[] {
  const replacement = (): DiffLine[] => [
    ...a.map((text) => ({ change: "remove" as const, text })),
    ...b.map((text) => ({ change: "add" as const, text })),
  ];
  if (!a.length || !b.length) return replacement();
  // Bound the trace to 128 edits. Larger replacements remain exact, but need not be minimal.
  // This caps quadratic search storage independently of a pasted config's line count.
  const maxEdits = 128;
  if (Math.abs(a.length - b.length) > maxEdits) return replacement();
  let frontier = new Map<number, number>([[1, 0]]);
  const trace: Map<number, number>[] = [];
  for (let d = 0; d <= Math.min(a.length + b.length, maxEdits); d++) {
    trace.push(frontier);
    const next = new Map<number, number>();
    for (let k = -d; k <= d; k += 2) {
      const down = k === -d || (k !== d && (frontier.get(k - 1) ?? -1) < (frontier.get(k + 1) ?? -1));
      let x = down ? (frontier.get(k + 1) ?? 0) : (frontier.get(k - 1) ?? 0) + 1;
      let y = x - k;
      while (x < a.length && y < b.length && a[x] === b[y]) {
        x++;
        y++;
      }
      next.set(k, x);
      if (x < a.length || y < b.length) continue;
      const result: DiffLine[] = [];
      for (let step = d; step >= 0; step--) {
        const v = trace[step];
        if (!v) break;
        const diagonal = x - y;
        const prevK =
          diagonal === -step ||
          (diagonal !== step && (v.get(diagonal - 1) ?? -1) < (v.get(diagonal + 1) ?? -1))
            ? diagonal + 1
            : diagonal - 1;
        const prevX = v.get(prevK) ?? 0;
        const prevY = prevX - prevK;
        while (x > prevX && y > prevY) {
          result.push({ change: "same", text: a[--x] ?? "" });
          y--;
        }
        if (step === 0) break;
        if (x === prevX) result.push({ change: "add", text: b[--y] ?? "" });
        else result.push({ change: "remove", text: a[--x] ?? "" });
      }
      return result.reverse();
    }
    frontier = next;
  }
  return replacement();
}

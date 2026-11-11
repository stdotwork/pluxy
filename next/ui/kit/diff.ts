export interface DiffLine {
  change: "same" | "add" | "remove";
  /** Includes its line ending, so a changed final newline is visible in the diff. */
  text: string;
}

const lines = (text: string): string[] => text.match(/[^\n]*\n|[^\n]+$/g) ?? [];

/** A line diff using Myers' shortest edit path; unchanged runs take no matrix of config size. */
export function lineDiff(before: string, after: string): DiffLine[] {
  const a = lines(before);
  const b = lines(after);
  let frontier = new Map<number, number>([[1, 0]]);
  const trace: Map<number, number>[] = [];
  for (let d = 0; d <= a.length + b.length; d++) {
    trace.push(new Map(frontier));
    for (let k = -d; k <= d; k += 2) {
      const down = k === -d || (k !== d && (frontier.get(k - 1) ?? -1) < (frontier.get(k + 1) ?? -1));
      let x = down ? (frontier.get(k + 1) ?? 0) : (frontier.get(k - 1) ?? 0) + 1;
      let y = x - k;
      while (x < a.length && y < b.length && a[x] === b[y]) {
        x++;
        y++;
      }
      frontier.set(k, x);
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
    frontier = new Map(frontier);
  }
  return [];
}

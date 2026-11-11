import { expect, test } from "bun:test";
import { lineDiff } from "../kit/diff";

test("line diff preserves context and orders a replacement before its addition", () => {
  expect(
    lineDiff("logs:\n  error-logs-max-files: 10\nend\n", "logs:\n  error-logs-max-files: 12\nend\n"),
  ).toEqual([
    { change: "same", text: "logs:\n" },
    { change: "remove", text: "  error-logs-max-files: 10\n" },
    { change: "add", text: "  error-logs-max-files: 12\n" },
    { change: "same", text: "end\n" },
  ]);
  expect(lineDiff("", "a")).toEqual([{ change: "add", text: "a" }]);
  expect(lineDiff("a\n", "a")).toEqual([
    { change: "remove", text: "a\n" },
    { change: "add", text: "a" },
  ]);
  expect(lineDiff("a\na\nb", "a\nb")).toEqual([
    { change: "same", text: "a\n" },
    { change: "remove", text: "a\n" },
    { change: "same", text: "b" },
  ]);
  expect(lineDiff("", "")).toEqual([]);
});

test("large replacements use remove-all/add-all and retain only common ends", () => {
  const old = Array.from({ length: 4000 }, (_, i) => `old-${i}\n`);
  const next = Array.from({ length: 4000 }, (_, i) => `new-${i}\n`);
  const start = performance.now();
  const diff = lineDiff(old.join(""), next.join(""));
  expect(performance.now() - start).toBeLessThan(1000);
  expect(diff).toEqual([
    ...old.map((text) => ({ change: "remove" as const, text })),
    ...next.map((text) => ({ change: "add" as const, text })),
  ]);
  const a = [...old.slice(0, 150), "shared\n", ...old.slice(150, 300)];
  const b = [...next.slice(0, 150), "shared\n", ...next.slice(150, 300)];
  expect(lineDiff(`prefix\n${a.join("")}suffix\n`, `prefix\n${b.join("")}suffix\n`)).toEqual([
    { change: "same", text: "prefix\n" },
    ...a.map((text) => ({ change: "remove" as const, text })),
    ...b.map((text) => ({ change: "add" as const, text })),
    { change: "same", text: "suffix\n" },
  ]);
});

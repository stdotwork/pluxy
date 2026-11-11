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

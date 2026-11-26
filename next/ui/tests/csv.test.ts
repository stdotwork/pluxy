import { expect, test } from "bun:test";
import { csvField } from "../kit/csv";

test("CSV fields quote delimiters and neutralize spreadsheet formulas", () => {
  expect(["plain", "a,b", 'a"b', "a\nb", "=1+1", "+2", "-3", "@x"].map(csvField)).toEqual([
    "plain",
    '"a,b"',
    '"a""b"',
    '"a\nb"',
    "'=1+1",
    "'+2",
    "'-3",
    "'@x",
  ]);
});

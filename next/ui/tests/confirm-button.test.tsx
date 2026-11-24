import { expect, test } from "bun:test";
import { confirmActionLabel } from "../kit/ui";

test("confirmActionLabel lower-cases normal words while keeping all-capital words", () => {
  expect(confirmActionLabel("Restart CLI service")).toBe("restart CLI service");
  expect(confirmActionLabel("Restore backup")).toBe("restore backup");
  expect(confirmActionLabel("Delete")).toBe("delete");
  expect(confirmActionLabel("Clear API URL")).toBe("clear API URL");
});

// How the end of Requests' list names the oldest request pluxy keeps.
import { expect, test } from "bun:test";
import { endWhen } from "../kit/pages/requests";

// Local times, so the test does not depend on the machine's zone. Friday 27 November 2026, 15:00.
const now = new Date(2026, 10, 27, 15, 0).getTime();
const at = (day: number, hour: number, minute: number) => new Date(2026, 10, day, hour, minute).toISOString();

test("a request from today is called today, not by today's weekday", () => {
  expect(endWhen(at(27, 14, 3), now)).toBe("14:03 today");
});

test("a request from yesterday says yesterday", () => {
  expect(endWhen(at(26, 9, 5), now)).toBe("yesterday 09:05");
});

test("a request from the last week says its weekday, and an older one its date", () => {
  expect(endWhen(at(23, 14, 5), now)).toBe("Monday 14:05");
  expect(endWhen(at(21, 14, 5), now)).toBe("Saturday 14:05");
  // A week back is the same weekday as today: it must not read as today.
  expect(endWhen(at(20, 14, 5), now)).toBe("Nov 20, 14:05");
});

test("a time that cannot be read is shown as it came", () => {
  expect(endWhen("not a time", now)).toBe("not a time");
});

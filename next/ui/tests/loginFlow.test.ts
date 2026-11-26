import { expect, test } from "bun:test";
import { optionalInput, signInRunning } from "../kit/loginFlow";

test("a sign-in again may leave a stored input empty, a new account may not", () => {
  const baseURL = { name: "base_url", label: "Base URL" };
  expect(optionalInput(baseURL, [])).toBe(false);
  expect(optionalInput({ ...baseURL, optional: true }, [])).toBe(true);
  expect(optionalInput(baseURL, ["base_url"])).toBe(true);
  // A secret is never kept in settings as its value, so a sign-in again still asks for it.
  expect(optionalInput({ name: "api_key", label: "API key", secret: true }, ["api_key"])).toBe(false);
});

test("only a conflict about a running sign-in is told as one", () => {
  const conflict = (message: string) => ({ status: 409, error: { code: "conflict", message } });
  expect(signInRunning(conflict('conflict: a login for account "a" is already running (login-3)'))).toBe(
    true,
  );
  expect(signInRunning(conflict('account "a" belongs to extension x'))).toBe(false);
  expect(signInRunning({ status: 502, error: { code: "conflict", message: "already running" } })).toBe(false);
  expect(signInRunning({ status: 201 })).toBe(false);
});

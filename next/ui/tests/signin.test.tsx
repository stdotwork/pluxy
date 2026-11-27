// The sign-in screen: it says where the management key is and how to get it.
import { afterEach, expect, test } from "bun:test";
import type { Host } from "../kit/host";
import { SignIn } from "../kit/shell";
import { mountIn, restoreDom } from "./fakeDom";

afterEach(restoreDom);

const host = { signIn: async () => undefined } as unknown as Host;

test("the sign-in screen says where the management key is and the command that prints it", async () => {
  const m = await mountIn(<SignIn host={host} onDone={() => {}} />);
  const text = m.text();
  expect(text).toContain("Sign in with pluxy's management key.");
  expect(text).toContain("on the computer pluxy runs on");
  expect(text).toContain("pluxy key management --show-key");
  expect(text).toContain("pluxy help");
  expect(text).not.toContain("core");
});

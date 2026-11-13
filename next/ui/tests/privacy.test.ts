// The privacy masks. Expected values are the old app's (extensions/app tests/privacyMask.test.ts),
// with its two bugs fixed: `$&` in an identity and an identity starting with `@`.
import { expect, test } from "bun:test";
import { join } from "node:path";
import { maskEmail, maskIdentifier, maskSensitiveText } from "../kit/privacy";

test("masks emails while keeping enough shape to tell accounts apart", () => {
  expect(maskEmail("alice.smith@example.com")).toBe("al***@e***.com");
  expect(maskEmail("bob@corp.example.co")).toBe("b***@c***.co");
});

test("masks opaque identifiers without revealing their middle", () => {
  expect(maskIdentifier("my-gcp-project-123456")).toBe("my***56");
  expect(maskIdentifier("proj-12")).toBe("p***2");
  expect(maskIdentifier("abcd")).toBe("***");
  expect(maskIdentifier("svc@my-project.iam.gserviceaccount.com")).toBe("s***@m***.com");
  expect(maskIdentifier("")).toBe("");
});

test("masks every email in free text", () => {
  expect(maskSensitiveText("token for alice@example.com and bob@test.io expired")).toBe(
    "token for al***@e***.com and b***@t***.io expired",
  );
  expect(maskSensitiveText("Codex · 3f9a2c")).toBe("Codex · 3f9a2c");
  expect(maskSensitiveText("")).toBe("");
});

test("masks proxy URL credentials", () => {
  expect(maskSensitiveText("socks5://example:example@127.0.0.1:1080")).toBe("socks5://***@127.0.0.1:1080");
  expect(maskSensitiveText("http://proxy.local:8080")).toBe("http://proxy.local:8080");
});

test("masks known identities inside names, including file-name forms", () => {
  const identities = ["alice@example.com", "my-gcp-project-123456"];
  // The address is masked as an address first (the old app masked the identity first).
  expect(maskSensitiveText("codex-alice@example.com-plus", identities)).toBe("co***@e***.com-plus");
  expect(maskSensitiveText("antigravity-alice_example_com", identities)).toBe("antigravity-al***@e***.com");
  expect(maskSensitiveText("alice@example.com-my-gcp-project-123456", identities)).toBe(
    "al***@e***.com-my***56",
  );
  // Without the identity, the e-mail pattern still hides the address.
  expect(maskSensitiveText("codex-alice@example.com")).not.toContain("alice");
});

test("a value that starts with @ is masked and the call returns", async () => {
  // Without the fix these calls never return, and a test timeout cannot stop a synchronous loop,
  // so they run in a child process that is killed after a few seconds.
  const modulePath = join(import.meta.dir, "..", "kit", "privacy");
  const script = `import { maskEmail, maskIdentifier, maskSensitiveText } from ${JSON.stringify(modulePath)};
console.log(JSON.stringify([maskIdentifier("@alice"), maskEmail("@alice"), maskIdentifier("@ab"), maskIdentifier("@x"), maskSensitiveText("@x said hi", ["@x"])]));`;
  const child = Bun.spawn([process.execPath, "-e", script], {
    stdout: "pipe",
    stderr: "pipe",
    timeout: 5000,
  });
  const output = await new Response(child.stdout).text();
  await child.exited;
  expect(child.signalCode).toBeNull();
  expect(JSON.parse(output)).toEqual(["@***e", "@***e", "***", "***", "@x said hi"]);
}, 15000);

test("replacement patterns in an identity do not leak it", () => {
  // `$&` in a string replacement would paste the whole matched identity back in.
  const identity = "$&abcdef@example.com";
  expect(maskSensitiveText(`account ${identity} failed`, [identity])).toBe("account $&ab***@e***.com failed");
  const project = "$&abcdefghijk-project-77";
  expect(maskSensitiveText(`project ${project} failed`, [project])).toBe("project $&***77 failed");
  // `$'` would paste in the text after the match.
  const other = "$'abcdefghijk-project-88";
  expect(maskSensitiveText(`project ${other} failed`, [other])).toBe("project $'***88 failed");
});

test("long runs of address characters are masked in linear time", async () => {
  // A request body in an error log can hold such a run; a regular expression that retries it
  // from every position takes seconds and freezes the page. A child process, as a test timeout
  // cannot stop a synchronous loop.
  const modulePath = join(import.meta.dir, "..", "kit", "privacy");
  const script = `import { maskSensitiveText } from ${JSON.stringify(modulePath)};
const run = "a.b-c_d+e%".repeat(20000);
const start = performance.now();
const out = [maskSensitiveText(run), maskSensitiveText("http://" + run), maskSensitiveText(run + " bob@test.io")];
console.log(JSON.stringify({ ms: performance.now() - start, same: out[0] === run && out[1] === "http://" + run, tail: out[2].slice(-13) }));`;
  const child = Bun.spawn([process.execPath, "-e", script], {
    stdout: "pipe",
    stderr: "pipe",
    timeout: 20000,
  });
  const output = await new Response(child.stdout).text();
  await child.exited;
  expect(child.signalCode).toBeNull();
  const r = JSON.parse(output);
  expect(r.same).toBe(true);
  expect(r.tail).toBe(" b***@t***.io");
  // 200,000 characters three times: linear work takes milliseconds; the quadratic form took
  // seconds for 80,000.
  expect(r.ms).toBeLessThan(2000);
}, 30000);

test("an identity inside an address does not break the address's mask", () => {
  // Masking the identity first left the rest of the address unmatched, and so shown.
  expect(maskSensitiveText("mail alice.smith@gmail.com", ["gmail"])).toBe("mail al***@g***.com");
  expect(maskSensitiveText("bob.jones@devcorp.io", ["dev"])).toBe("bo***@d***.io");
  expect(maskSensitiveText("alice.smith@example.com", ["smith"])).toBe("al***@e***.com");
  expect(maskSensitiveText("socks5://smith:pw@proxy.local", ["smith"])).toBe("socks5://***@proxy.local");
});

test("account IDs core makes from imported file names are masked", () => {
  // Core's legacy AccountID: each character an ID cannot hold becomes "-"
  // (claude-alice.smith@example.com.json is account claude-alice.smith-example.com).
  expect(maskSensitiveText("claude-alice.smith-example.com", ["alice.smith@example.com"])).toBe(
    "claude-al***@e***.com",
  );
  expect(maskSensitiveText("kimi-upload-example.com", ["upload@example.com"])).toBe("kimi-up***@e***.com");
  expect(maskSensitiveText("codex-bob.jones-work-example.com", ["bob.jones+work@example.com"])).toBe(
    "codex-bo***@e***.com",
  );
});

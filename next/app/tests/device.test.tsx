import { afterEach, expect, test } from "bun:test";
import { type DevicePackage, type DeviceRegistry, newest } from "../src/device";
import { DevicePage } from "../src/devicePage";
import { restoreDom } from "./fakeDom";
import { mount } from "./pageMount";

afterEach(() => {
  restoreDom();
  Reflect.deleteProperty(globalThis, "location");
});

test("a catalog load that answers after a newer one is dropped", async () => {
  const applied: string[] = [];
  const load = newest<string>((v) => applied.push(v));
  let first: (v: string) => void = () => {};
  let second: (v: string) => void = () => {};
  load(new Promise((r) => (first = r)));
  load(new Promise((r) => (second = r)));
  second("newer");
  await Bun.sleep(0);
  first("older");
  await Bun.sleep(0);
  expect(applied).toEqual(["newer"]);
  // A load after both still applies.
  load(Promise.resolve("latest"));
  await Bun.sleep(0);
  expect(applied).toEqual(["newer", "latest"]);
});

// ---- the page ----------------------------------------------------------------------------------------------

const hex = (c: string) => c.repeat(64);
const release = (over: Record<string, unknown> = {}) => ({
  version: "1.0.0",
  sha256: hex("c"),
  size: 1000,
  installed_at: 1_780_000_000,
  manifest_version: 1,
  app_version: 1,
  host_versions: [1],
  ui_version: 1,
  pages: [],
  resources: [],
  declared: [],
  signer: null,
  settings: false,
  tier: "web",
  operations: [],
  imports: [],
  exports: null,
  ...over,
});
const pkg = (over: Record<string, unknown> = {}): DevicePackage =>
  ({
    id: "example/notes",
    enabled: true,
    trust: "unverified",
    tier: "web",
    authorized: false,
    imports_allowed: false,
    source: { kind: "local", name: "notes.zip" },
    active: release(),
    ...over,
  }) as unknown as DevicePackage;
const signer = (approved: boolean) => ({
  publisher: "example",
  key_id: "k1",
  public_key: hex("a"),
  source: "https://example.com/index.json",
  approved,
  access: true,
});
const files = (approved: boolean) =>
  pkg({
    active: release({
      resources: ["notes-file"],
      declared: [{ id: "notes-file", kind: "file", operations: ["read"] }],
      signer: signer(approved),
    }),
  });
const tool = pkg({
  id: "pluxy/claude-code",
  enabled: false,
  trust: "curated",
  tier: "native",
  active: release({ tier: "native" }),
});
const catalog = (packages: DevicePackage[], over: Record<string, unknown> = {}) => ({
  safe_mode: false,
  packages,
  ...over,
});
const offered = (over: Partial<DeviceRegistry> = {}): DeviceRegistry => ({
  name: "pluxy",
  url: "https://example.com/index.json",
  packages: [{ id: "pluxy/codex-setup", versions: ["1.0.0"] }],
  ...over,
});
const page = (handlers: Record<string, unknown> = {}) =>
  mount(<DevicePage onChanged={() => {}} />, {
    device_list: catalog([]),
    device_registries: [],
    access_list: { signers: [], grants: [] },
    ...handlers,
  });
/** Opens an installed add-on's detail: the name in the list is a button. */
const open = async (m: Awaited<ReturnType<typeof page>>, id: string) => {
  await m.click(id);
};

test("there is one Refresh, and it reads the add-ons and the sources again", async () => {
  const m = await page();
  expect(m.labels().filter((l) => l === "Refresh").length).toBe(1);
  expect(m.commands("device_list").length).toBe(1);
  expect(m.commands("device_registries").length).toBe(1);
  await m.click("Refresh");
  expect(m.commands("device_list").length).toBe(2);
  expect(m.commands("device_registries").length).toBe(2);
});

test("until the first reply, the page says it is reading and shows no empty list", async () => {
  const m = await page({ device_list: () => new Promise(() => {}) });
  expect(m.text()).toContain("Reading add-ons…");
  expect(m.text()).not.toContain("No add-ons installed on this computer.");
  expect(m.text()).not.toContain("Available to install");
});

test("a list of add-ons says how to open one", async () => {
  const m = await page({ device_list: catalog([pkg()]) });
  expect(m.text()).toContain("Choose an add-on to see its details and what you can do with it.");
  const empty = await page();
  expect(empty.text()).not.toContain("Choose an add-on");
});

test("with no sources set up, the page says so without naming a file to edit", async () => {
  const m = await page();
  const text = m.text();
  expect(text).toContain("No add-on sources are set up in this copy of the app.");
  expect(text).toContain("Install from file…");
  expect(text).not.toMatch(/json|registries|config folder/i);
  expect(text).toContain("No add-ons installed on this computer.");
});

test("safe mode is said in plain words, with no command-line flag, and the page still works", async () => {
  const m = await page({ device_list: catalog([tool], { safe_mode: true }) });
  expect(m.text()).toContain("Safe mode is on: add-on pages and coding-tool add-ons are paused.");
  expect(m.text()).not.toContain("--safe-mode");
  await open(m, "pluxy/claude-code");
  expect(m.has("Turn on")).toBe(true);
});

test("a reply to an older read does not replace a newer one", async () => {
  const answers: ((v: unknown) => void)[] = [];
  const m = await page({
    device_list: () => new Promise((resolve) => answers.push(resolve)),
  });
  await m.click("Refresh");
  expect(answers.length).toBe(2);
  const newer = answers[1] as (v: unknown) => void;
  const older = answers[0] as (v: unknown) => void;
  newer(catalog([pkg({ id: "example/new" })]));
  await Bun.sleep(0);
  older(catalog([pkg({ id: "example/old" })]));
  await Bun.sleep(0);
  await m.show(<DevicePage onChanged={() => {}} />);
  expect(m.text()).toContain("example/new");
  expect(m.text()).not.toContain("example/old");
});

test("a publisher the person did not approve hides Choose a file…, and one they did shows it", async () => {
  const before = await page({ device_list: catalog([files(false)]) });
  await open(before, "example/notes");
  expect(before.text()).toContain("Publisher not approved");
  expect(before.has("Approve publisher…")).toBe(true);
  expect(before.has("Choose a file…")).toBe(false);
  restoreDom();
  const after = await page({ device_list: catalog([files(true)]) });
  await open(after, "example/notes");
  expect(after.text()).toContain("Publisher approved");
  expect(after.has("Choose a file…")).toBe(true);
  expect(after.has("Approve publisher…")).toBe(false);
  expect(after.text()).toContain("An add-on's pages can send what they read anywhere they can reach.");
});

test("Everything these sources offer is installed. is said only when no source failed", async () => {
  const done = pkg({ id: "pluxy/codex-setup", source: { kind: "registry", name: "pluxy" } });
  const clean = await page({
    device_list: catalog([done]),
    device_registries: [offered({ packages: [{ id: "pluxy/codex-setup", versions: ["1.0.0"] }] })],
  });
  expect(clean.text()).toContain("Everything these sources offer is installed.");
  restoreDom();
  const broken = await page({
    device_list: catalog([done]),
    device_registries: [
      offered({ packages: [{ id: "pluxy/codex-setup", versions: ["1.0.0"] }] }),
      offered({ name: "mirror", error: "connection refused", packages: [] }),
    ],
  });
  expect(broken.text()).not.toContain("Everything these sources offer is installed.");
  expect(broken.text()).toContain("Could not read mirror: connection refused");
});

test("an offer says Install, Update to {v}, or Blocked with its reason in view", async () => {
  const m = await page({
    device_list: catalog([
      pkg({
        id: "pluxy/older",
        source: { kind: "registry", name: "pluxy" },
        active: release({ version: "1.0.0" }),
      }),
    ]),
    device_registries: [
      offered({
        packages: [
          { id: "pluxy/codex-setup", versions: ["1.0.0"] },
          { id: "pluxy/older", versions: ["1.1.0"] },
          { id: "pluxy/reserved", versions: ["2.0.0"], refused: "Its publisher is reserved." },
        ],
      }),
    ],
  });
  expect(m.has("Install")).toBe(true);
  expect(m.has("Update to 1.1.0")).toBe(true);
  expect(m.text()).toContain("Blocked");
  expect(m.text()).toContain("Its publisher is reserved.");
});

test("Turn on for a coding-tool add-on shows nothing when the person cancels in the confirmation window", async () => {
  const m = await page({
    device_list: catalog([tool]),
    device_enable: () => Promise.reject("native enable cancelled; pluxy/claude-code stays disabled"),
  });
  await open(m, "pluxy/claude-code");
  expect(m.text()).toContain("Coding-tool add-on");
  expect(m.text()).toContain("Runs as you, with your permissions.");
  await m.click("Turn on");
  expect(m.commands("device_enable")).toEqual([{ id: "pluxy/claude-code", enabled: true }]);
  expect(m.text()).not.toContain("cancelled");
  // A refusal that is not a cancel is shown.
  restoreDom();
  const refused = await page({
    device_list: catalog([tool]),
    device_enable: () =>
      Promise.reject("pluxy/claude-code changed while it was being confirmed; enable it again"),
  });
  await open(refused, "pluxy/claude-code");
  await refused.click("Turn on");
  expect(refused.text()).toContain("changed while it was being confirmed");
});

test("Remove asks first, in the page, and removes only after Yes, remove", async () => {
  const m = await page({ device_list: catalog([pkg()]), device_remove: catalog([]) });
  await open(m, "example/notes");
  await m.click("Remove");
  expect(m.commands("device_remove").length).toBe(0);
  expect(m.has("Yes, remove")).toBe(true);
  await m.click("Yes, remove");
  expect(m.commands("device_remove")).toEqual([{ id: "example/notes" }]);
});

test("Turn off and Go back run on one click", async () => {
  const m = await page({
    device_list: catalog([pkg({ last_good: release({ version: "0.9.0" }) })]),
    device_enable: catalog([]),
    device_rollback: catalog([]),
  });
  await open(m, "example/notes");
  await m.click("Turn off");
  expect(m.commands("device_enable")).toEqual([{ id: "example/notes", enabled: false }]);
  await m.click("Go back to 0.9.0");
  expect(m.commands("device_rollback")).toEqual([{ id: "example/notes" }]);
});

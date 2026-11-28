// What an open request that no account served says: the accounts that serve models, the two ways to
// fix it, and where the alias link leads; against a fake pluxy.
import { afterEach, expect, test } from "bun:test";
import { api } from "../kit/api";
import type { Host, Method, Reply } from "../kit/host";
import { aliasHref, modelsByAccount, NoServeHelp } from "../kit/pages/requestNoServe";
import { type Kit, KitContext } from "../kit/ui";
import { all, type FakeNode, mountIn, restoreDom, settle } from "./fakeDom";

afterEach(restoreDom);

const model = (id: string, accounts: string[]) => ({ id, upstream: id, extension: "x/y", accounts });

async function mount(models: Reply<unknown>, editable = (_: string) => true) {
  const host: Host = {
    call: async <T,>(_m: Method, path: string) => (path === "/models" ? models : { status: 404 }) as Reply<T>,
    signedIn: () => true,
    signIn: async () => undefined,
    signOut: () => {},
    prefs: { get: () => null, set: () => {} },
    features: new Set(),
    address: "http://127.0.0.1:8317",
  };
  const kit = { host, api: api(host), unauthorized: () => {}, extensionsChanged: () => {} } as Kit;
  const m = await mountIn(
    <KitContext.Provider value={kit}>
      <NoServeHelp
        model="claude-opus-5-5"
        name={(a) => `name of ${a}`}
        idOf={(a) => `id-${a}`}
        editable={editable}
      />
    </KitContext.Provider>,
  );
  await settle();
  return m;
}

const href = (node: FakeNode) => node.attributes.find((a) => a.name === "href")?.value;
const links = (root: FakeNode) => all(root, "a").map((a) => [a.textContent, href(a)]);

test("the models are grouped by the accounts that serve them, in the order accounts first appear", () => {
  expect(modelsByAccount([model("m1", ["a", "b"]), model("m2", ["b"]), model("m3", ["c", "a"])])).toEqual([
    { account: "a", models: ["m1", "m3"] },
    { account: "b", models: ["m1", "m2"] },
    { account: "c", models: ["m3"] },
  ]);
});

test("the alias link names the account's page and the model the tool asked for, encoded", () => {
  expect(aliasHref("acc one", "claude-opus-5-5")).toBe("#/accounts/acc%20one?alias=claude-opus-5-5");
  expect(aliasHref("a", "gpt 6/sol")).toBe("#/accounts/a?alias=gpt%206%2Fsol");
});

test("it lists each account's models and says both fixes, with a link per account to its alias form", async () => {
  const m = await mount({
    status: 200,
    body: { models: [model("fake-gpt-1", ["a", "b"]), model("fake-gpt-mini", ["a"])] },
  });
  expect(m.text()).toContain("These accounts serve:");
  expect(m.text()).toContain("name of a: fake-gpt-1, fake-gpt-mini");
  expect(m.text()).toContain("name of b: fake-gpt-1");
  expect(m.text()).toContain("set the tool's model to one of these (Tools has the text for each tool)");
  expect(m.text()).toContain("an alias makes claude-opus-5-5 go to one of that account's models.");
  expect(links(m.root)).toEqual([
    ["Add an alias on name of a", "#/accounts/id-a?alias=claude-opus-5-5"],
    ["Add an alias on name of b", "#/accounts/id-b?alias=claude-opus-5-5"],
    ["Tools", "#/tools"],
  ]);
});

test("an account set in the settings file links to its page, which says where to change it", async () => {
  const m = await mount({ status: 200, body: { models: [model("fake-gpt-1", ["a"])] } }, () => false);
  expect(links(m.root)).toEqual([
    ["Open name of a", "#/accounts/id-a"],
    ["Tools", "#/tools"],
  ]);
});

test("with no model served it says to add an account, and with the models unread it still says the fixes", async () => {
  const none = await mount({ status: 200, body: { models: [] } });
  expect(none.text()).toContain("pluxy serves no models yet.");
  expect(links(none.root)).toEqual([["Add an account", "#/add-account"]]);
  restoreDom();
  const failed = await mount({ status: 502, error: { code: "bad_gateway", message: "down" } });
  expect(failed.text()).not.toContain("These accounts serve:");
  expect(failed.text()).toContain("add a model alias to an account");
});

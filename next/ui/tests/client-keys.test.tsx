import { expect, test } from "bun:test";
import render from "preact-render-to-string";
import { api } from "../kit/api";
import type { Feature, Host } from "../kit/host";
import { ConfigConnectivity, useConfigKeys } from "../kit/pages/configKeys";
import { type Kit, KitContext } from "../kit/ui";

test("Connectivity offers Rotate only when its host supports management-key rotation", () => {
  const host: Host = {
    call: () => new Promise(() => {}),
    signedIn: () => true,
    signIn: async () => undefined,
    signOut: () => {},
    prefs: { get: () => null, set: () => {} },
    features: new Set<Feature>(),
  };
  const kit = { host, api: api(host), unauthorized: () => {} } as Kit;
  function Connectivity() {
    const state = useConfigKeys("connectivity", () => {});
    return <ConfigConnectivity state={state} tab="connectivity" search="" onChanged={() => {}} />;
  }
  const html = () =>
    render(
      <KitContext.Provider value={kit}>
        <Connectivity />
      </KitContext.Provider>,
    );
  expect(html()).toContain("Management Key");
  expect(html()).not.toContain(">Rotate</button>");
  host.features = new Set<Feature>(["rotateManagementKey"]);
  expect(html()).toContain(">Rotate</button>");
});

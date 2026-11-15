// What the Launcher page's new-launch form offers, apart from drawing it (launcherPage.tsx).

export type Kind = "terminal" | "direct";
export type Lifetime = "detached" | "managed";

/**
 * The lifetime a new launch is sent with. Only a direct launch can be managed: the process the
 * app holds for a terminal launch is the terminal's own launcher, which many terminals end at once
 * after handing the work to a server process, so the app could not stop the agent. The host
 * refuses a managed terminal launch too (`spawn::parse`).
 */
export const lifetimeFor = (kind: Kind, chosen: Lifetime): Lifetime =>
  kind === "direct" ? chosen : "detached";

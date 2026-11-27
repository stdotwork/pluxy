// What the app gives the Tools page and the guided setup's step 2 through the kit's hooks
// (CorePageOptions.tools and .setup): the two get the same tools, links and refresh, and the same
// button for a row; only the Tools page also gets the names of the keys.

import type { SetupClient } from "../../ui/kit/api";
import type { SetupOptions } from "../../ui/kit/pages/setup";
import type { ToolsOptions } from "../../ui/kit/pages/tools";
import { type AddonView, type agentPages, toolAction, toolNotes } from "./agentPages";
import type { Connection } from "./host";
import { readKeyOwners } from "./keyOwners";

export interface ToolHooksInput {
  /** The connection the window shows pages for. */
  conn: Pick<Connection, "mode" | "target">;
  /** The tool pages of this window. */
  agents: ReturnType<typeof agentPages>;
  /** This device's add-ons, and what the registries offer. */
  view: AddonView;
  /** The tools core does not scan, as the app reads them; none until it has. */
  extraTools?: SetupClient[];
  /** Reads the device's add-ons again, once an add-on's step is over. */
  loadDevice: () => void;
  /** Runs on Refresh, in the Tools page and in the setup. */
  onRefresh: () => void;
}

export function toolHooks(input: ToolHooksInput): { tools: ToolsOptions; setup: SetupOptions } {
  const { agents, view } = input;
  // A pluxy elsewhere: its tools are on that computer, so no add-on is offered here and its list
  // has no rows of ours; the tool pages of this computer are linked under the list.
  const remote = input.conn.mode === "remote";
  const common = {
    extraTools: remote ? undefined : (input.extraTools ?? []),
    isRemote: remote,
    localPages: remote ? agents.map((a) => ({ name: a.title, page: a.id })) : undefined,
    onRefresh: input.onRefresh,
    notes: remote ? undefined : toolNotes(agents, view),
  };
  return {
    tools: {
      ...common,
      toolAction: remote ? undefined : toolAction(agents, view, input.loadDevice),
      keyOwners: () => readKeyOwners(input.conn.target),
    },
    setup: {
      ...common,
      toolAction: remote ? undefined : toolAction(agents, view, input.loadDevice, true),
    },
  };
}

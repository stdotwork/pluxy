// ToolRow: one coding tool on one line, the same in the panel and the app. Its name and version,
// its state in one of six words, where its settings file is, and the one action the page gives it;
// what the page opens under it goes in its slot.

import type { ComponentChildren } from "preact";
import type { SetupClient } from "../api/setup";
import { stateText, stateTone } from "../states";
import { strings } from "../strings";

const t = strings("parts.toolRow", {
  unchecked: "pluxy cannot see how {tool} is set up.",
});

/**
 * A tool's state, as stateText(…, "tool") reads it: unchecked for a tool the host marks so (only
 * the app does, for a tool pluxy cannot read), not_installed for one core did not find, else core's
 * own state.
 */
export function toolState(client: Pick<SetupClient, "installed" | "state">, unchecked?: boolean): string {
  if (unchecked) return "unchecked";
  if (!client.installed) return "not_installed";
  return client.state;
}

/**
 * One tool. client is the tool as GET /setup found it; unchecked marks a row whose setup pluxy
 * cannot read (the app's, for a tool core does not scan); reason is why pluxy will not change its
 * settings, for a refused one. action is the page's one action; children, when given, show under
 * the row (the page's own content, such as what to paste).
 */
export function ToolRow(props: {
  client: SetupClient;
  unchecked?: boolean;
  reason?: string;
  action?: ComponentChildren;
  children?: ComponentChildren;
}) {
  const c = props.client;
  const state = toolState(c, props.unchecked);
  const title =
    state === "unchecked" ? t("unchecked", { tool: c.name }) : state === "refused" ? props.reason : undefined;
  return (
    <div class="tool-row" data-part="tool-row" data-state={state} data-open={props.children ? "" : undefined}>
      <div class="tool-line">
        <span class="tool-who">
          <span class="tool-name">{c.name}</span>
          {c.version && <span class="tool-version">{c.version}</span>}
        </span>
        <span class="tool-state">
          <span
            class="badge"
            data-part="badge"
            data-tone={stateTone(state, "tool")}
            data-state={state}
            title={title}
          >
            {stateText(state, "tool")}
          </span>
        </span>
        <span class="tool-path" title={c.config}>
          {c.config}
        </span>
        <span class="tool-action">{props.action}</span>
      </div>
      {props.children && <div class="tool-more">{props.children}</div>}
    </div>
  );
}

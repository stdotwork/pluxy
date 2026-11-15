// The Codex page's form logic: the keys it offers from the vault and the form it fills from the
// tool's read-back of config.toml and the model catalog.

export const PACKAGE = "pluxy/codex-setup";

/** A vault secret as `vault_secrets` lists it: never its value. */
export interface Secret {
  id: string;
  label: string;
  /** Set for a captured client key: the package it may be given to. */
  client_key?: { client: string };
}

/** The keys the page offers from `vault_secrets`'s answer (`{secrets: [...]}`): plain secrets, and
 * client keys bound to this package; a client key bound to another package would be refused. */
export function keysOf(body: { secrets?: Secret[] } | undefined): Secret[] {
  return (body?.secrets ?? []).filter((s) => !s.client_key || s.client_key.client === PACKAGE);
}

export interface Model {
  slug: string;
  upstream?: string;
  non_codex?: boolean;
  display_name: string;
  description: string;
  context_window: number | null;
  max_context_window: number | null;
  effective_context_window_percent: number | null;
  auto_compact_token_limit: number | null;
  reasoning: string[];
  default_reasoning: string;
  input_modalities: string[];
  visible: boolean;
  tool_search: boolean;
  system_prompt_chars?: number;
  system_prompt?: string;
}

export interface Agents {
  enabled: boolean;
  max_threads: number | null;
  model: string;
  effort: string;
  interrupt: boolean;
}

/** What the tool's preview reads back of the two files; `refused` (with `reason`) when Codex
 * would not take one of them, which apply and clear refuse and Restore Backup still reaches. */
export type ReadBack =
  | { state: "refused"; reason: string }
  | {
      state: "configured" | "not-configured" | "needs-update";
      key_set: boolean;
      provider: string | null;
      model: string | null;
      profile: string | null;
      remote_compaction: boolean;
      agents: Agents;
      models: Model[];
      approval_model: string;
    };

export interface Form {
  model: string;
  approval: string;
  compaction: boolean;
  agents: Agents;
  models: Model[];
}

export const fresh = (): Form => ({
  model: "",
  approval: "",
  compaction: false,
  agents: { enabled: true, max_threads: null, model: "", effort: "", interrupt: true },
  models: [],
});

/** The form for the files read back; the fresh form for files Codex would not take. */
export function fromFile(r: ReadBack): Form {
  if (r.state === "refused") return fresh();
  return {
    model: r.model ?? "",
    approval: r.approval_model,
    compaction: r.remote_compaction,
    agents: r.agents,
    models: r.models,
  };
}

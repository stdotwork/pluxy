// The OpenCode page's form logic (opencode.tsx), apart from the page so its unit tests need no
// webview: the models' Attachments default, and whether the form can be applied.

export const PACKAGE = "pluxy/opencode";
/** The client label the tool's CLI recipe gives its launches, which the launcher keys recent
 * folders by. */
export const CLIENT = "OpenCode";

export interface Model {
  id: string;
  name: string;
  attachment: boolean;
}

/** What the tool's preview reads back of the config file; `refused` (with `reason`) for a file
 * OpenCode would not take, which apply and clear refuse and Restore Backup still reaches. */
export interface ReadBack {
  state: "configured" | "not-configured" | "needs-update" | "refused";
  reason?: string;
  base_url?: string | null;
  key_set?: boolean;
  model?: string | null;
  default_model?: string | null;
  models?: Model[];
  old_provider?: boolean;
  /** OpenCode 2's `providers.pluxy` is in the file: OpenCode would use it, so Apply refuses. */
  v2_provider?: boolean;
}

/** The Attachments switch's first setting for a model new to the list: the old app's rule of
 * model names that take images. A default only; the person sets the switch. */
const ATTACHMENT_NAMES = [
  "claude",
  "gemini",
  "gpt-4o",
  "gpt-4.1",
  "gpt-5",
  "o3",
  "o4",
  "qwen-vl",
  "qwen2.5-vl",
  "qwen3-vl",
  "glm-4v",
  "glm-4.5v",
  "pixtral",
  "llama-4",
  "kimi-vl",
  "doubao-vision",
  "step-1v",
  "internvl",
  "phi-4-multimodal",
];
export const attachmentDefault = (id: string) => ATTACHMENT_NAMES.some((n) => id.toLowerCase().includes(n));

/** The models listed so far with the new ones whose IDs are not listed yet. */
export const withModels = (have: Model[], found: Model[]) => [
  ...have,
  ...found.filter((f, i) => !have.some((h) => h.id === f.id) && found.findIndex((g) => g.id === f.id) === i),
];

/** The longest model ID or display name the tool takes. */
const TEXT_MAX = 240;

/** Whether the form can be applied: 1 to 256 models with IDs the tool takes (no control characters
 * or #, at most 240 characters) and names of at most 240, and the default one of them. */
export function ready(models: Model[], model: string): boolean {
  // biome-ignore lint/suspicious/noControlCharactersInRegex: control characters are what it refuses
  const bad = /[\u0000-\u001f\u007f#]/;
  return (
    models.length >= 1 &&
    models.length <= 256 &&
    models.every(
      (m) =>
        m.id.trim() !== "" &&
        m.id.trim().length <= TEXT_MAX &&
        !bad.test(m.id) &&
        m.name.trim().length <= TEXT_MAX,
    ) &&
    models.some((m) => m.id === model)
  );
}

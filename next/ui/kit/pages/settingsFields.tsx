import type { CoreSettings, SettingsChange } from "../api/settings";
import { Switch } from "../ui";
import { configText as t } from "./configStrings";

export type SettingsTab = "common" | "network" | "logging" | "advanced";
export type Value = string | number | boolean | null;
export interface Field {
  key: string;
  label: string;
  kind: "switch" | "number" | "text" | "strategy" | "quota";
  hint?: string;
  min?: number;
  step?: "any";
}
export const fields = (): Record<SettingsTab, Field[]> => {
  const log: Field = { key: "observability.logs.logging-to-file", label: t("logToFile"), kind: "switch" };
  return {
    common: [log],
    network: [
      { key: "routing.strategy", label: t("strategy"), kind: "strategy" },
      { key: "routing.retry.request-retry", label: t("retry"), kind: "number", min: 0 },
      { key: "routing.retry.max-retry-credentials", label: t("maxCredentials"), kind: "number", min: 0 },
      {
        key: "routing.retry.max-retry-interval",
        label: t("maxInterval"),
        kind: "number",
        min: 0,
        step: "any",
      },
      { key: "routing.session-affinity", label: t("affinity"), kind: "switch" },
      { key: "routing.session-affinity-ttl", label: t("ttl"), kind: "text", hint: t("ttlHint") },
      { key: "routing.force-model-prefix", label: t("prefix"), kind: "switch" },
      { key: "routing.cooldown.disable-cooling", label: t("disableCooling"), kind: "switch" },
      {
        key: "routing.cooldown.transient-error-cooldown-seconds",
        label: t("transient"),
        step: "any",
        kind: "number",
        min: -1,
      },
      { key: "routing.cooldown.quota-response-mode", label: t("quota"), kind: "quota" },
    ],
    logging: [
      log,
      { key: "observability.logs.logs-max-total-size-mb", label: t("size"), kind: "number", min: 0 },
      { key: "observability.logs.error-logs-max-files", label: t("retained"), kind: "number", min: 0 },
      { key: "observability.usage.usage-statistics-enabled", label: t("usage"), kind: "switch" },
      {
        key: "observability.logs.request-log",
        label: t("requestLog"),
        kind: "switch",
        hint: t("requestWarning"),
      },
    ],
    advanced: [{ key: "token_count_estimate", label: t("estimate"), kind: "switch" }],
  };
};
export const get = (settings: CoreSettings, key: string): Value =>
  key.split(".").reduce<unknown>((v, k) => (v as Record<string, unknown>)[k], settings) as Value;
export function changeSetting(settings: CoreSettings, key: string, value: Value): CoreSettings {
  const copy = structuredClone(settings);
  const path = key.split(".");
  const last = path.pop();
  const obj = path.reduce<unknown>((v, k) => (v as Record<string, unknown>)[k], copy) as Record<
    string,
    unknown
  >;
  if (last) obj[last] = value;
  return copy;
}
/** Send only edited fields, so another page's changes to other settings survive. */
export function settingsChange(base: CoreSettings, draft: CoreSettings): SettingsChange {
  const change: Record<string, unknown> = {};
  for (const field of Object.values(fields()).flat()) {
    const value = get(draft, field.key);
    if (value === get(base, field.key)) continue;
    if (field.kind === "number" && (typeof value !== "number" || !Number.isFinite(value))) {
      throw new Error(t("numberRequired", { label: field.label }));
    }
    const path = field.key.split(".");
    const last = path.pop();
    let obj = change;
    for (const k of path) {
      obj[k] ??= {};
      obj = obj[k] as Record<string, unknown>;
    }
    if (last) obj[last] = value;
  }
  return change as SettingsChange;
}
export function SettingControl({
  field,
  settings,
  set,
  immediate = false,
  strategies,
  invalid,
}: {
  field: Field;
  settings: CoreSettings;
  set: (value: Value) => void;
  immediate?: boolean;
  strategies?: Record<CoreSettings["routing"]["strategy"], string>;
  /** With `immediate`: called with the reason when an entered value is refused; the saved value comes back. */
  invalid?: (reason: string) => void;
}) {
  const value = get(settings, field.key);
  const refuse = (input: HTMLInputElement, saved: string) => {
    if (input.validity.valid) return false;
    const reason = input.validationMessage;
    input.value = saved;
    invalid?.(reason);
    return true;
  };
  return (
    <>
      {field.kind === "switch" || field.kind === "quota" ? (
        <Switch
          label={field.label}
          hint={field.hint}
          checked={field.kind === "quota" ? value === "usage-limit" : value === true}
          onChange={(v) => set(field.kind === "quota" ? (v ? "usage-limit" : "retryable") : v)}
        />
      ) : (
        <label class="field" data-part="field" for={field.key}>
          <span class="label">{field.label}</span>
          {field.kind === "strategy" ? (
            <select id={field.key} value={String(value)} onChange={(e) => set(e.currentTarget.value)}>
              <option value="round-robin">{strategies?.["round-robin"] ?? t("roundRobin")}</option>
              <option value="weighted-round-robin">
                {strategies?.["weighted-round-robin"] ?? t("weighted")}
              </option>
              <option value="fill-first">{strategies?.["fill-first"] ?? t("fillFirst")}</option>
              <option value="least-active">{strategies?.["least-active"] ?? t("leastActive")}</option>
            </select>
          ) : field.kind === "number" ? (
            <input
              id={field.key}
              type="number"
              min={field.min}
              step={field.step ?? 1}
              required={field.key !== "routing.retry.request-retry" || value !== null}
              value={value === null ? "" : String(value)}
              onInput={(e) => {
                if (!immediate) set(e.currentTarget.value === "" ? "" : Number(e.currentTarget.value));
              }}
              onChange={(e) => {
                if (!immediate) return;
                const input = e.currentTarget;
                if (refuse(input, value === null ? "" : String(value))) return;
                set(input.value === "" ? "" : Number(input.value));
              }}
            />
          ) : (
            <input
              id={field.key}
              type="text"
              required
              disabled={field.key === "routing.session-affinity-ttl" && !settings.routing["session-affinity"]}
              value={String(value)}
              onInput={(e) => {
                if (!immediate) set(e.currentTarget.value);
              }}
              onChange={(e) => {
                if (!immediate) return;
                const input = e.currentTarget;
                if (refuse(input, String(value))) return;
                set(input.value);
              }}
            />
          )}
          {field.hint && <small class="hint">{field.hint}</small>}
        </label>
      )}
    </>
  );
}

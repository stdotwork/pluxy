// SettingRow: one setting in two columns, its name and what it does beside its control. A row that
// saves on change says Saving… while it does, and a save that fails puts the control back to the
// saved value with the reason under the row.

import type { ComponentChildren } from "preact";
import { useEffect, useId, useRef, useState } from "preact/hooks";
import { usePrivacy } from "../privacy";
import { strings } from "../strings";
import { Notice } from "../ui";
import { type Outcome, useOutcome } from "./hooks";

const t = strings("parts.settingRow", {
  saving: "Saving…",
});

/**
 * A setting's value for a row that saves on change. saved is the value core holds; set shows the
 * new value at once and saves it with save, which gives back the reply, an error to show, or
 * nothing. A save that fails puts the value back to saved and keeps the error until the next set.
 * Only the last set's answer counts.
 */
export function useSetting<T>(saved: T, save: (value: T) => Promise<Outcome> | Outcome) {
  const outcome = useOutcome();
  const [value, setValue] = useState(saved);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string>();
  const turn = useRef(0);
  const live = useRef(true);
  useEffect(() => {
    live.current = true;
    return () => {
      live.current = false;
    };
  }, []);
  // A newer value from core shows, unless a save is on its way.
  useEffect(() => {
    if (!saving) setValue(saved);
  }, [saved]);
  const set = async (next: T) => {
    const mine = ++turn.current;
    setValue(next);
    setSaving(true);
    setError(undefined);
    let why: string | undefined;
    try {
      why = outcome(await save(next));
    } catch (err) {
      why = err instanceof Error ? err.message : String(err);
    }
    if (!live.current || mine !== turn.current) return;
    setSaving(false);
    if (why !== undefined) {
      setValue(saved);
      setError(why);
    }
  };
  return { value, set, saving, error };
}

/**
 * One setting. name is the setting's name, description one sentence of what it does, configKey
 * (on Config only) its key in the settings file. children is the control: a Switch, Choices, a
 * select, an input or a Button. saving shows Saving… beside it; disabled, a sentence, dims the
 * control and says why under it; error, a save that failed, shows under the row.
 */
export function SettingRow(props: {
  name: string;
  description?: string;
  configKey?: string;
  saving?: boolean;
  disabled?: string;
  error?: string;
  children: ComponentChildren;
}) {
  const p = usePrivacy();
  const id = useId();
  return (
    <div class="setting-row" data-part="setting-row" data-disabled={props.disabled ? "" : undefined}>
      <div class="setting-grid">
        <div class="setting-about">
          <span class="setting-name" id={`${id}-name`}>
            {props.name}
          </span>
          {props.description && <span class="setting-description">{props.description}</span>}
          {props.configKey && <span class="setting-key">{props.configKey}</span>}
        </div>
        <div class="setting-control">
          <fieldset
            disabled={!!props.disabled}
            aria-labelledby={`${id}-name`}
            aria-describedby={props.disabled ? `${id}-why` : undefined}
          >
            {props.children}
          </fieldset>
          <span class="setting-saving" aria-live="polite">
            {props.saving ? t("saving") : ""}
          </span>
          {props.disabled && (
            <span class="setting-why" id={`${id}-why`}>
              {props.disabled}
            </span>
          )}
        </div>
      </div>
      {props.error && (
        <div class="setting-error">
          <Notice tone="danger">{p.text(props.error)}</Notice>
        </div>
      )}
    </div>
  );
}

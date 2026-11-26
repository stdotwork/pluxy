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
 * nothing. One save at a time, so core keeps the newest value: a value set while a save is on its
 * way is sent after it ends, and of several set meanwhile only the newest. The last save's answer
 * counts: when it fails, the value goes back to saved and the error stays until the next set.
 */
export function useSetting<T>(saved: T, save: (value: T) => Promise<Outcome> | Outcome) {
  const outcome = useOutcome();
  const [value, setValue] = useState(saved);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string>();
  // The newest value not sent yet, and the saves on their way, one after another.
  const waiting = useRef<{ value: T } | undefined>(undefined);
  const sending = useRef<Promise<void> | undefined>(undefined);
  // The newest render's saved value and save, for a save sent after a later render.
  const savedNow = useRef(saved);
  savedNow.current = saved;
  const saveNow = useRef(save);
  saveNow.current = save;
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
  const send = async () => {
    let why: string | undefined;
    for (let next = waiting.current; next; next = waiting.current) {
      waiting.current = undefined;
      try {
        why = outcome(await saveNow.current(next.value));
      } catch (err) {
        why = err instanceof Error ? err.message : String(err);
      }
    }
    sending.current = undefined;
    if (!live.current) return;
    setSaving(false);
    if (why !== undefined) {
      setValue(savedNow.current);
      setError(why);
    }
  };
  const set = (next: T): Promise<void> => {
    setValue(next);
    setSaving(true);
    setError(undefined);
    waiting.current = { value: next };
    sending.current ??= send();
    return sending.current;
  };
  return { value, set, saving, error };
}

/**
 * One setting. name is the setting's name, description one sentence of what it does, configKey
 * (on Config only) its key in the settings file. children is the control: a Switch, Choices, a
 * select, an input or a Button. saving shows Saving… beside it; disabled, a sentence, dims the
 * control and says why under it; error, a save that failed, shows under the row. The reason and
 * the error are masked in privacy mode.
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
              {p.text(props.disabled)}
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

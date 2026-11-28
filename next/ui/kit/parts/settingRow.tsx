// SettingRow: one setting in two columns, its name and what it does beside its control. A row that
// saves on change says Saving… while it does, and a save that fails puts the control back to the
// saved value with the reason under the row.

import { type ComponentChildren, createContext } from "preact";
import { useContext, useEffect, useId, useRef, useState } from "preact/hooks";
import { Icon } from "../icons";
import { usePrivacy } from "../privacy";
import { strings } from "../strings";
import { Notice } from "../ui";
import { type Outcome, useOutcome } from "./hooks";

const t = strings("parts.settingRow", {
  saving: "Saving…",
  saved: "Saved",
});

export const SettingsSearchContext = createContext("");

export function matchesSearch(query: string, ...texts: (string | undefined)[]): boolean {
  const hay = texts.filter(Boolean).join(" ").toLowerCase();
  return query
    .toLowerCase()
    .split(/\s+/)
    .filter(Boolean)
    .every((word) => hay.includes(word));
}

const SAVED_SHOWS_MS = 1600;

export function useSavedTick() {
  const [saved, setSaved] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  useEffect(() => () => clearTimeout(timer.current), []);
  return {
    saved,
    flash() {
      clearTimeout(timer.current);
      setSaved(true);
      timer.current = setTimeout(() => setSaved(false), SAVED_SHOWS_MS);
    },
    clear() {
      clearTimeout(timer.current);
      setSaved(false);
    },
  };
}

function savedOk(result: Outcome): boolean {
  if (result === undefined) return true;
  if (typeof result === "string") return false;
  return result.status >= 200 && result.status < 300;
}

export function useSetting<T>(saved: T, save: (value: T) => Promise<Outcome> | Outcome) {
  const outcome = useOutcome();
  const tick = useSavedTick();
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
    let ok = false;
    for (let next = waiting.current; next; next = waiting.current) {
      waiting.current = undefined;
      try {
        const result = await saveNow.current(next.value);
        why = outcome(result);
        ok = why === undefined && savedOk(result);
      } catch (err) {
        why = err instanceof Error ? err.message : String(err);
        ok = false;
      }
    }
    sending.current = undefined;
    if (!live.current) return;
    setSaving(false);
    if (ok) {
      tick.flash();
    } else {
      setValue(savedNow.current);
      if (why !== undefined) setError(why);
    }
  };
  const set = (next: T): Promise<void> => {
    setValue(next);
    setSaving(true);
    setError(undefined);
    tick.clear();
    waiting.current = { value: next };
    sending.current ??= send();
    return sending.current;
  };
  return { value, set, saving, saved: tick.saved, error };
}

export function SettingRow(props: {
  name: string;
  description?: string;
  note?: string;
  configKey?: string;
  saving?: boolean;
  saved?: boolean;
  disabled?: string;
  error?: string;
  children: ComponentChildren;
}) {
  const p = usePrivacy();
  const id = useId();
  const search = useContext(SettingsSearchContext);
  if (search && !matchesSearch(search, props.name, props.description, props.note, props.configKey))
    return null;
  return (
    <div class="setting-row" data-part="setting-row" data-disabled={props.disabled ? "" : undefined}>
      <div class="setting-grid">
        <div class="setting-about">
          <span class="setting-name" id={`${id}-name`}>
            {props.name}
          </span>
          {props.description && <span class="setting-description">{props.description}</span>}
          {props.note && <span class="setting-note">{props.note}</span>}
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
          <span
            class="setting-saving"
            aria-live="polite"
            data-saved={!props.saving && props.saved ? "" : undefined}
          >
            {props.saving ? (
              t("saving")
            ) : props.saved ? (
              <>
                <Icon name="check" size={12} />
                {t("saved")}
              </>
            ) : (
              ""
            )}
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

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

/**
 * What a Settings page is searched for. A SettingRow whose name, sentence, note or settings-file
 * key do not hold every word of it is not drawn. Empty: every row is drawn.
 */
export const SettingsSearchContext = createContext("");

/** Whether text holds every word of query, in any case. */
export function matchesSearch(query: string, ...texts: (string | undefined)[]): boolean {
  const hay = texts.filter(Boolean).join(" ").toLowerCase();
  return query
    .toLowerCase()
    .split(/\s+/)
    .filter(Boolean)
    .every((word) => hay.includes(word));
}

/** How long "Saved" stays beside a setting that was saved. */
const SAVED_SHOWS_MS = 1600;

/**
 * "Saved" for a setting that takes effect at once with nothing to wait for (a choice kept on this
 * device): flash() shows it for a moment, and a new flash starts the moment again.
 */
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

/** Whether a save's outcome means the change was saved (nothing, or a 2xx reply). */
function savedOk(result: Outcome): boolean {
  if (result === undefined) return true;
  if (typeof result === "string") return false;
  return result.status >= 200 && result.status < 300;
}

/**
 * A setting's value for a row that saves on change. saved is the value core holds; set shows the
 * new value at once and saves it with save, which gives back the reply, an error to show, or
 * nothing. One save at a time, so core keeps the newest value: a value set while a save is on its
 * way is sent after it ends, and of several set meanwhile only the newest. The last save's answer
 * counts: when it fails, the value goes back to saved and the error stays until the next set; when
 * it works, saved is true for a moment (the row's "Saved").
 */
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

/**
 * One setting. name is the setting's name, description one sentence of what it does, note a line
 * after it on what else it changes, configKey (on Config only) its key in the settings file. children is the control: a Switch, Choices, a
 * select, an input or a Button. saving shows Saving… beside it, and saved (for a moment after a save
 * that worked) a tick and Saved; disabled, a sentence, dims the
 * control and says why under it; error, a save that failed, shows under the row. The reason and
 * the error are masked in privacy mode.
 */
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

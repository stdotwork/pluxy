import { useEffect, useRef, useState } from "preact/hooks";
import type { CoreSettings } from "../api/settings";
import { Icon } from "../icons";
import { SettingRow, useSetting } from "../parts";
import { strings } from "../strings";
import { Switch, useKit } from "../ui";
import {
  nest,
  numberText,
  parseNumber,
  RULES,
  type RuleDef,
  ruleWords,
  settingAt,
  strategies,
  switchOn,
  switchValue,
  ttlOffWhy,
} from "./routing.rules";

const t = strings("routing.rulesPane", {
  more: "More rules",
  saved: "Saved",
});

const SAVED_MS = 1500;

function useSave(reload: () => void) {
  const { api } = useKit();
  const [saved, setSaved] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const live = useRef(true);
  useEffect(() => {
    live.current = true;
    return () => {
      live.current = false;
      clearTimeout(timer.current);
    };
  }, []);
  const save = async (key: string, value: unknown) => {
    const reply = await api.changeCoreSettings(nest(key, value));
    if (reply.status >= 200 && reply.status < 300) {
      reload();
      if (live.current) {
        clearTimeout(timer.current);
        setSaved(true);
        timer.current = setTimeout(() => {
          if (live.current) setSaved(false);
        }, SAVED_MS);
      }
    }
    return reply;
  };
  return { save, saved };
}

function SavedTick(props: { shown: boolean }) {
  if (!props.shown) return null;
  return (
    <span class="routing-saved" aria-live="polite">
      <Icon name="check" size={12} />
      {t("saved")}
    </span>
  );
}

function SwitchRule(props: { def: RuleDef; settings: CoreSettings; reload: () => void }) {
  const { def } = props;
  const words = ruleWords(def.key);
  const { save, saved } = useSave(props.reload);
  const s = useSetting(switchOn(def.key, settingAt(props.settings, def.key)), (on: boolean) =>
    save(def.key, switchValue(def.key, on)),
  );
  return (
    <div class="routing-rule-item" data-kind="switch">
      <SettingRow name={words.name} description={words.about} saving={s.saving} error={s.error}>
        <span class="routing-control-wrap">
          <SavedTick shown={saved && !s.saving && !s.error} />
          <Switch label={words.name} checked={s.value} onChange={s.set} />
        </span>
      </SettingRow>
    </div>
  );
}

function StrategyRule(props: { def: RuleDef; settings: CoreSettings; reload: () => void }) {
  const { def } = props;
  const words = ruleWords(def.key);
  const { save, saved } = useSave(props.reload);
  const s = useSetting<string>(String(settingAt(props.settings, def.key)), (value) => save(def.key, value));
  const options = strategies();
  return (
    <div class="routing-rule-item" data-kind="strategy">
      <SettingRow
        name={words.name}
        description={options.find((o) => o.value === s.value)?.about}
        saving={s.saving}
        error={s.error}
      >
        <span class="routing-control-wrap">
          <select
            data-slot="select-trigger"
            aria-label={words.name}
            value={s.value}
            onChange={(e) => s.set(e.currentTarget.value)}
          >
            {options.map((o) => (
              <option key={o.value} value={o.value}>
                {o.label}
              </option>
            ))}
          </select>
          <SavedTick shown={saved && !s.saving && !s.error} />
        </span>
      </SettingRow>
    </div>
  );
}

/** A number or a duration: typed freely, checked and saved when Enter is pressed or the field is left. */
function FieldRule(props: { def: RuleDef; settings: CoreSettings; reload: () => void }) {
  const { def } = props;
  const words = ruleWords(def.key);
  const { save, saved } = useSave(props.reload);
  const value = settingAt(props.settings, def.key);
  const savedText = def.kind === "number" ? numberText(value) : String(value ?? "");
  const s = useSetting<string>(savedText, (text) => {
    if (def.kind === "text") return save(def.key, text.trim());
    const n = parseNumber(def, text);
    return "error" in n ? n.error : save(def.key, n.value);
  });
  const [draft, setDraft] = useState(s.value);
  useEffect(() => setDraft(s.value), [s.value]);
  const commit = () => {
    if (draft !== s.value) void s.set(draft);
  };
  const onKey = (e: KeyboardEvent) => {
    if (e.key === "Enter") {
      e.preventDefault();
      commit();
    }
  };
  const off =
    def.key === "routing.session-affinity-ttl" &&
    settingAt(props.settings, "routing.session-affinity") !== true;
  return (
    <div class="routing-rule-item" data-kind={def.kind}>
      <SettingRow
        name={words.name}
        description={words.about}
        saving={s.saving}
        error={s.error}
        disabled={off ? ttlOffWhy() : undefined}
      >
        <span class="routing-control-wrap">
          {def.kind === "number" ? (
            <input
              type="number"
              min={def.min}
              step={def.whole ? undefined : "any"}
              data-slot="input"
              aria-label={words.name}
              placeholder={words.placeholder}
              value={draft}
              onInput={(e) => setDraft(e.currentTarget.value)}
              onBlur={commit}
              onKeyDown={onKey}
            />
          ) : (
            <input
              type="text"
              data-slot="input"
              aria-label={words.name}
              placeholder={words.placeholder}
              value={draft}
              onInput={(e) => setDraft(e.currentTarget.value)}
              onBlur={commit}
              onKeyDown={onKey}
            />
          )}
          <SavedTick shown={saved && !s.saving && !s.error} />
        </span>
      </SettingRow>
    </div>
  );
}

function Rule(props: { def: RuleDef; settings: CoreSettings; reload: () => void }) {
  const Row =
    props.def.kind === "strategy" ? StrategyRule : props.def.kind === "switch" ? SwitchRule : FieldRule;
  return <Row {...props} />;
}

export function Rules(props: { settings: CoreSettings; reload: () => void }) {
  return (
    <div class="routing-rules card">
      {RULES.filter((d) => !d.more).map((def) => (
        <Rule key={def.key} def={def} settings={props.settings} reload={props.reload} />
      ))}
      <details class="routing-more">
        <summary>
          <Icon name="chevron" size={14} />
          {t("more")}
        </summary>
        <div class="routing-more-list">
          {RULES.filter((d) => d.more).map((def) => (
            <Rule key={def.key} def={def} settings={props.settings} reload={props.reload} />
          ))}
        </div>
      </details>
    </div>
  );
}

// The Rules column of Order & fallback: how a group shares requests and whether a conversation stays
// on one account, and under More rules the rest. A switch or a choice saves at once; a number or a
// duration saves on Enter or on leaving the field.

import { useEffect, useState } from "preact/hooks";
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
});

/** Saves one setting and reads the settings again, so every row shows what core holds. */
function useSave(reload: () => void) {
  const { api } = useKit();
  return async (key: string, value: unknown) => {
    const reply = await api.changeCoreSettings(nest(key, value));
    if (reply.status >= 200 && reply.status < 300) reload();
    return reply;
  };
}

function SwitchRule(props: { def: RuleDef; settings: CoreSettings; reload: () => void }) {
  const { def } = props;
  const words = ruleWords(def.key);
  const save = useSave(props.reload);
  const s = useSetting(switchOn(def.key, settingAt(props.settings, def.key)), (on: boolean) =>
    save(def.key, switchValue(def.key, on)),
  );
  return (
    <SettingRow name={words.name} description={words.about} saving={s.saving} error={s.error}>
      <Switch label={words.name} checked={s.value} onChange={s.set} />
    </SettingRow>
  );
}

function StrategyRule(props: { def: RuleDef; settings: CoreSettings; reload: () => void }) {
  const { def } = props;
  const words = ruleWords(def.key);
  const save = useSave(props.reload);
  const s = useSetting<string>(String(settingAt(props.settings, def.key)), (value) => save(def.key, value));
  const options = strategies();
  return (
    <SettingRow
      name={words.name}
      description={options.find((o) => o.value === s.value)?.about}
      saving={s.saving}
      error={s.error}
    >
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
    </SettingRow>
  );
}

/** A number or a duration: typed freely, checked and saved when Enter is pressed or the field is left. */
function FieldRule(props: { def: RuleDef; settings: CoreSettings; reload: () => void }) {
  const { def } = props;
  const words = ruleWords(def.key);
  const save = useSave(props.reload);
  const value = settingAt(props.settings, def.key);
  const saved = def.kind === "number" ? numberText(value) : String(value ?? "");
  const s = useSetting<string>(saved, (text) => {
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
    <SettingRow
      name={words.name}
      description={words.about}
      saving={s.saving}
      error={s.error}
      disabled={off ? ttlOffWhy() : undefined}
    >
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
    </SettingRow>
  );
}

function Rule(props: { def: RuleDef; settings: CoreSettings; reload: () => void }) {
  const Row =
    props.def.kind === "strategy" ? StrategyRule : props.def.kind === "switch" ? SwitchRule : FieldRule;
  return <Row {...props} />;
}

export function Rules(props: { settings: CoreSettings; reload: () => void }) {
  return (
    <div class="routing-rules">
      {RULES.filter((d) => !d.more).map((def) => (
        <Rule key={def.key} def={def} settings={props.settings} reload={props.reload} />
      ))}
      <details class="routing-more">
        <summary>
          <Icon name="chevron" />
          {t("more")}
        </summary>
        {RULES.filter((d) => d.more).map((def) => (
          <Rule key={def.key} def={def} settings={props.settings} reload={props.reload} />
        ))}
      </details>
    </div>
  );
}

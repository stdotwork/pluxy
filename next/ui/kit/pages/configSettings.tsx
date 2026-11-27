// The Config page's Settings tab: Network, Logging and Keys, each row saving the moment it changes,
// as Order & fallback's rules do. A switch saves at once; a number or text saves on Enter or on
// leaving the field. Each save sends only its own setting, so a change made elsewhere survives.

import type { ComponentChildren } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";
import type { ConfigDocument } from "../api/config";
import type { CoreSettings } from "../api/settings";
import { SettingRow, useSetting } from "../parts";
import { Card, Switch, useKit } from "../ui";
import { ManagementKey, type ReplaceKey } from "./configKeys";
import { configText as t } from "./configStrings";
import { nest, parseNumber, settingAt } from "./routing.rules";
import { type Field, fields, managementKeyFile } from "./settingsFields";

interface RowProps {
  field: Field;
  settings: CoreSettings;
  format?: ConfigDocument["format"];
  disabled?: string;
  saved: (settings: CoreSettings) => void;
}

/** Saves one setting; the reply, all of core's settings, goes to saved. */
function useSave(saved: (settings: CoreSettings) => void) {
  const { api } = useKit();
  return async (key: string, value: unknown) => {
    const reply = await api.changeCoreSettings(nest(key, value));
    if (reply.status >= 200 && reply.status < 300 && reply.body) saved(reply.body);
    return reply;
  };
}

function Row(props: RowProps & { saving: boolean; error?: string; children: ComponentChildren }) {
  const f = props.field;
  return (
    <SettingRow
      name={f.name}
      description={f.about}
      note={f.note}
      configKey={props.format && f.file[props.format]}
      saving={props.saving}
      error={props.error}
      disabled={props.disabled}
    >
      {props.children}
    </SettingRow>
  );
}

function SwitchRow(props: RowProps) {
  const f = props.field;
  const save = useSave(props.saved);
  const s = useSetting(settingAt(props.settings, f.key) === true, (on: boolean) => save(f.key, on));
  return (
    <Row {...props} saving={s.saving} error={s.error}>
      <Switch label={f.name} checked={s.value} onChange={s.set} />
    </Row>
  );
}

/** A number of 0 or more, or a text: typed freely, saved when Enter is pressed or the field is left. */
function FieldRow(props: RowProps) {
  const f = props.field;
  const save = useSave(props.saved);
  // What core holds, as the field shows it. A proxy address comes back with its password hidden.
  const saved = String(settingAt(props.settings, f.key) ?? "");
  const s = useSetting<string>(saved, (text) => {
    if (f.kind === "text") return save(f.key, text.trim());
    const n = parseNumber({ key: f.key, kind: "number", more: false, min: 0, whole: true }, text);
    return "error" in n ? n.error : save(f.key, n.value);
  });
  const [draft, setDraft] = useState(saved);
  // The field shows what core holds when that changes and after each save: core's own form of a
  // value that was taken, and the value it had for one that was refused.
  // Not when the row first shows: an effect runs after the first paint, and would undo what was
  // typed in that moment.
  const [round, setRound] = useState(0);
  const shown = useRef({ saved, round });
  useEffect(() => {
    if (shown.current.saved === saved && shown.current.round === round) return;
    shown.current = { saved, round };
    setDraft(saved);
  }, [saved, round]);
  const commit = () => {
    if (draft !== saved) void s.set(draft).then(() => setRound((n) => n + 1));
  };
  const input = {
    "aria-label": f.name,
    placeholder: f.placeholder,
    value: draft,
    onInput: (e: Event) => setDraft((e.currentTarget as HTMLInputElement).value),
    onBlur: commit,
    onKeyDown: (e: KeyboardEvent) => {
      if (e.key === "Enter") {
        e.preventDefault();
        commit();
      }
    },
  };
  return (
    <Row {...props} saving={s.saving} error={s.error}>
      {f.kind === "number" ? (
        <input type="number" min={0} data-slot="input" class="config-number" {...input} />
      ) : (
        <input type="text" data-slot="input" class="config-text" spellcheck={false} {...input} />
      )}
    </Row>
  );
}

function FieldRows(props: Omit<RowProps, "field"> & { fields: Field[] }) {
  return (
    <>
      {props.fields.map((field) =>
        field.kind === "switch" ? (
          <SwitchRow key={field.key} {...props} field={field} />
        ) : (
          <FieldRow key={field.key} {...props} field={field} />
        ),
      )}
    </>
  );
}

/** A row that leads to the page where its settings are. */
function LinkRow(props: { name: string; about: string; label: string; href: string }) {
  return (
    <SettingRow name={props.name} description={props.about}>
      <a class="btn" data-part="button" data-variant="quiet" href={props.href}>
        {props.label}
      </a>
    </SettingRow>
  );
}

/** A section of the tab, with the id an address scrolls to. */
function Section(props: { id: string; title: string; children: ComponentChildren }) {
  return (
    <div id={props.id} class="config-section">
      <Card title={props.title}>
        <div class="config-rows">{props.children}</div>
      </Card>
    </div>
  );
}

/**
 * The Settings tab. disabled, a sentence, turns every setting off with that reason (while the
 * settings file does not parse, core can change none). saved gets core's settings after a row saves.
 */
export function ConfigSettings(props: {
  settings: CoreSettings;
  format?: ConfigDocument["format"];
  disabled?: string;
  saved: (settings: CoreSettings) => void;
  keys: ReplaceKey;
}) {
  const f = fields();
  const rows = {
    settings: props.settings,
    format: props.format,
    disabled: props.disabled,
    saved: props.saved,
  };
  return (
    <div class="stack config-settings">
      <Section id="network" title={t("network")}>
        <FieldRows {...rows} fields={f.network} />
        <LinkRow name={t("routing")} about={t("routingAbout")} label={t("openRouting")} href="#/routing" />
      </Section>
      <Section id="logging" title={t("logging")}>
        <FieldRows {...rows} fields={f.logging} />
      </Section>
      <Section id="keys" title={t("keys")}>
        <LinkRow name={t("clientKeys")} about={t("clientKeysAbout")} label={t("openTools")} href="#/tools" />
        <ManagementKey
          state={props.keys}
          configKey={props.format && managementKeyFile[props.format]}
          disabled={props.disabled}
        />
      </Section>
    </div>
  );
}

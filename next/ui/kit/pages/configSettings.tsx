// The Config page's Settings tab: Network, Logging and Keys, each row saving the moment it changes,
// as Order & fallback's rules do. A switch saves at once; a number or text saves on Enter or on
// leaving the field. Each save sends only its own setting, so a change made elsewhere survives.

import type { ComponentChildren } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";
import type { ConfigDocument } from "../api/config";
import type { CoreSettings } from "../api/settings";
import type { Reply } from "../host";
import { SettingRow, useSetting } from "../parts";
import { Card, Switch } from "../ui";
import { ManagementKey, type ReplaceKey } from "./configKeys";
import { configText as t } from "./configStrings";
import { parseNumber, settingAt } from "./routing.rules";
import { type Field, fields, managementKeyFile } from "./settingsFields";

/** Saves one setting (only that field) and gives back core's reply; the page keeps what it saved. */
export type ChangeSetting = (key: string, value: unknown) => Promise<Reply<CoreSettings>>;

interface RowProps {
  field: Field;
  settings: CoreSettings;
  format?: ConfigDocument["format"];
  disabled?: string;
  change: ChangeSetting;
}

const PROXY = "proxy-url";

/**
 * A proxy address as the row shows it. Core shows a saved user name and password as "redacted"
 * (socks5://redacted@host:1080), and keeps them only when that exact text comes back: the row
 * shows the address without it, and says apart that a password is saved.
 */
function proxyShown(saved: string): { address: string; login: boolean } {
  const m = /^([a-z][a-z0-9+.-]*:\/\/)redacted(?::redacted)?@(.*)$/i.exec(saved);
  return m ? { address: `${m[1]}${m[2]}`, login: true } : { address: saved, login: false };
}

/** Whether a typed proxy address has a user name or password of its own (not core's "redacted"). */
function ownLogin(text: string) {
  const m = /^[a-z][a-z0-9+.-]*:\/\/([^/@\s]*)@/i.exec(text);
  return !!m && !/^redacted(?::redacted)?$/i.test(m[1] ?? "");
}

function Row(
  props: RowProps & { note?: string; saving: boolean; error?: string; children: ComponentChildren },
) {
  const f = props.field;
  return (
    <SettingRow
      name={f.name}
      description={f.about}
      note={props.note ?? f.note}
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
  const s = useSetting(settingAt(props.settings, f.key) === true, (on: boolean) => props.change(f.key, on));
  return (
    <Row {...props} saving={s.saving} error={s.error}>
      <Switch label={f.name} checked={s.value} onChange={s.set} />
    </Row>
  );
}

/**
 * A number of 0 or more, or a text: typed freely, saved when Enter is pressed or the field is left.
 * What was typed is never lost to a reply: the field shows core's value only while it still holds
 * the text last shown or sent, once every save of the row has its reply. A save with no answer
 * keeps the text, and says it may or may not be saved.
 */
function FieldRow(props: RowProps) {
  const f = props.field;
  // What core holds, as the field shows it. A proxy address comes back with its password hidden,
  // and shows without it.
  const saved = String(settingAt(props.settings, f.key) ?? "");
  const proxy = f.key === PROXY ? proxyShown(saved) : undefined;
  const shownValue = proxy ? proxy.address : saved;
  // Whether the newest reply of the row was no answer at all.
  const lostReply = useRef(false);
  const s = useSetting<string>(shownValue, async (text) => {
    let reply: Reply<CoreSettings> | string;
    if (f.kind === "text") reply = await props.change(f.key, text.trim());
    else {
      const n = parseNumber({ key: f.key, kind: "number", more: false, min: 0, whole: true }, text);
      reply = "error" in n ? n.error : await props.change(f.key, n.value);
    }
    lostReply.current = typeof reply !== "string" && reply.status === 0;
    return reply;
  });
  const [draft, setDraft] = useState(shownValue);
  const draftNow = useRef(draft);
  draftNow.current = draft;
  // The text the field last took from core or sent: while the draft is still that, nothing was
  // typed since, and core's value may replace it. null after a save with no answer: the draft stays
  // until the person sends it again.
  const base = useRef<string | null>(shownValue);
  // Saves sent and not answered yet: their replies wait until the last one is in.
  const pending = useRef(0);
  const [lost, setLost] = useState(false);
  const [refused, setRefused] = useState<string>();
  // The field shows what core holds when that changes and after each save: core's own form of a
  // value that was taken, and the value it had for one that was refused.
  // Not when the row first shows: an effect runs after the first paint, and would undo what was
  // typed in that moment.
  const [round, setRound] = useState(0);
  const shown = useRef({ value: shownValue, round });
  useEffect(() => {
    if (shown.current.value === shownValue && shown.current.round === round) return;
    shown.current = { value: shownValue, round };
    if (pending.current > 0 || draftNow.current !== base.current) return;
    base.current = shownValue;
    setDraft(shownValue);
  }, [shownValue, round]);
  const commit = () => {
    const text = draft;
    if (text === shownValue && base.current !== null) return;
    setRefused(undefined);
    // A new address with no user name and password of its own would lose the saved ones: core
    // keeps them only for the address they were saved with.
    const typed = text.trim();
    if (proxy?.login && typed !== "" && typed !== saved && !ownLogin(typed)) {
      setRefused(t("proxyNeedsLogin"));
      return;
    }
    base.current = text;
    pending.current++;
    setLost(false);
    void s.set(text).then(() => {
      pending.current--;
      if (pending.current === 0 && lostReply.current) {
        base.current = null;
        setLost(true);
      }
      setRound((n) => n + 1);
    });
  };
  const input = {
    "aria-label": f.name,
    placeholder: f.placeholder,
    value: draft,
    onInput: (e: Event) => {
      setDraft((e.currentTarget as HTMLInputElement).value);
      setRefused(undefined);
    },
    onBlur: commit,
    onKeyDown: (e: KeyboardEvent) => {
      if (e.key === "Enter") {
        e.preventDefault();
        commit();
      }
    },
  };
  return (
    <Row
      {...props}
      note={proxy?.login ? t("proxySaved") : undefined}
      saving={s.saving}
      error={refused ?? (lost ? t("noAnswerRow") : s.error)}
    >
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

/**
 * A row that leads to the page where its settings are. It stays usable while the settings file
 * does not parse: the page it opens says for itself what it can and cannot do then.
 */
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
 * settings file does not parse, core can change none); the two rows that open another page stay
 * usable. change saves one setting.
 */
export function ConfigSettings(props: {
  settings: CoreSettings;
  format?: ConfigDocument["format"];
  disabled?: string;
  change: ChangeSetting;
  keys: ReplaceKey;
}) {
  const f = fields();
  const rows = {
    settings: props.settings,
    format: props.format,
    disabled: props.disabled,
    change: props.change,
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

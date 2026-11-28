import { useEffect, useState } from "preact/hooks";
import { problem as replyProblem, type SettingField, type Settings } from "../api";
import { SettingRow } from "../parts";
import { strings } from "../strings";
import { Button, Notice, Switch, useKit } from "../ui";

const t = strings("settingsForm", {
  none: "(none)",
  unchanged: "unchanged",
  valueSet: "A value is set.",
  clear: "Clear {label}",
  couldNotLoad: "Could not load settings. Refresh and try again.",
  settings: "Settings",
  settingsOf: "Settings of {id}",
  saved: "Saved.",
  notSaved: "Could not save settings. Check your values and try again.",
  saveSettings: "Save",
});

type Draft = Record<string, string | boolean>;

/** The form's starting values: what core shows, or empty. Secrets start empty. */
function draftOf(s: Settings): Draft {
  const d: Draft = {};
  for (const [name, f] of Object.entries(s.schema.properties)) {
    const v = f.writeOnly ? undefined : s.values[name];
    d[name] = f.type === "boolean" ? v === true : v === undefined || v === null ? "" : String(v);
  }
  return d;
}

/** The change to send: only fields that differ from what core shows. An emptied field is null. */
export function changeOf(s: Settings, draft: Draft, clear: Set<string>): Record<string, unknown> {
  const start = draftOf(s);
  const change: Record<string, unknown> = {};
  for (const [name, f] of Object.entries(s.schema.properties)) {
    const v = draft[name];
    if (f.writeOnly) {
      if (clear.has(name)) change[name] = null;
      else if (typeof v === "string" && v !== "") change[name] = v;
      continue;
    }
    if (v === start[name]) continue;
    if (f.type === "boolean") change[name] = v === true;
    else if (v === "") change[name] = null;
    else if (f.type === "string") change[name] = v;
    else {
      const n = Number(v);
      change[name] = Number.isFinite(n) ? n : v; // not a number: core says so
    }
  }
  return change;
}

function FieldInput(props: {
  name: string;
  field: SettingField;
  value: string | boolean;
  isSet: boolean;
  clear: boolean;
  onChange: (v: string | boolean) => void;
  onClear: (c: boolean) => void;
}) {
  const { field: f } = props;
  const label = f.title ?? props.name;
  const hint = f.description;
  const value = typeof props.value === "string" ? props.value : "";
  return (
    <SettingRow name={label} description={hint}>
      {f.type === "boolean" ? (
        <Switch label={label} checked={props.value === true} onChange={props.onChange} />
      ) : (
        <div class="ext-control">
          {f.enum ? (
            <select
              data-slot="select-trigger"
              aria-label={label}
              value={value}
              onChange={(e) => props.onChange(e.currentTarget.value)}
            >
              {!f.enum.includes(value) && <option value="">{t("none")}</option>}
              {f.enum.map((o) => (
                <option key={o} value={o}>
                  {o}
                </option>
              ))}
            </select>
          ) : (
            <input
              data-slot="input"
              aria-label={label}
              {...(f.writeOnly
                ? {
                    type: "password" as const,
                    autocomplete: "off",
                    placeholder: props.isSet ? t("unchanged") : undefined,
                  }
                : f.type === "string"
                  ? { type: "text" as const, maxLength: f.maxLength }
                  : {
                      type: "number" as const,
                      min: f.minimum,
                      max: f.maximum,
                      step: f.type === "integer" ? 1 : "any",
                    })}
              value={value}
              onInput={(e) => props.onChange(e.currentTarget.value)}
            />
          )}
          {f.writeOnly && props.isSet && (
            <span class="ext-secret">
              <small class="hint">{t("valueSet")}</small>
              <label class="check">
                <input
                  type="checkbox"
                  checked={props.clear}
                  onChange={(e) => props.onClear(e.currentTarget.checked)}
                />
                <span>{t("clear", { label })}</span>
              </label>
            </span>
          )}
        </div>
      )}
    </SettingRow>
  );
}

/** The settings of extension id, as a form; nothing when it has none. */
export function SettingsForm(props: { id: string }) {
  const { api, unauthorized } = useKit();
  const [settings, setSettings] = useState<Settings>();
  const [missing, setMissing] = useState(false);
  const [draft, setDraft] = useState<Draft>({});
  const [clear, setClear] = useState<Set<string>>(new Set());
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState<{ tone: "ok" | "danger"; text: string }>();

  useEffect(() => {
    let live = true;
    api.settings(props.id).then((r) => {
      if (!live) return;
      if (r.status === 401) return unauthorized();
      if (r.status === 404) return setMissing(true);
      if (r.body) {
        setSettings(r.body);
        setDraft(draftOf(r.body));
      } else setMessage({ tone: "danger", text: replyProblem(r) ?? t("couldNotLoad") });
    });
    return () => {
      live = false;
    };
  }, [api, props.id, unauthorized]);

  if (missing) return null;
  return (
    <section class="ext-settings">
      <h3 class="ext-subheading">{t("settings")}</h3>
      {settings && (
        <form
          aria-label={t("settingsOf", { id: props.id })}
          noValidate
          onSubmit={async (e) => {
            e.preventDefault();
            setBusy(true);
            setMessage(undefined);
            const r = await api.changeSettings(props.id, changeOf(settings, draft, clear));
            setBusy(false);
            if (r.status === 401) return unauthorized();
            if (r.status === 200 && r.body) {
              setSettings(r.body);
              setDraft(draftOf(r.body));
              setClear(new Set());
              setMessage({ tone: "ok", text: t("saved") });
            } else setMessage({ tone: "danger", text: replyProblem(r) ?? t("notSaved") });
          }}
        >
          {Object.entries(settings.schema.properties).map(([name, f]) => (
            <FieldInput
              key={name}
              name={name}
              field={f}
              value={draft[name] ?? ""}
              isSet={settings.set.includes(name)}
              clear={clear.has(name)}
              onChange={(v) => setDraft({ ...draft, [name]: v })}
              onClear={(c) => {
                const next = new Set(clear);
                if (c) next.add(name);
                else next.delete(name);
                setClear(next);
              }}
            />
          ))}
          {message && <Notice tone={message.tone}>{message.text}</Notice>}
          <div class="actions">
            <Button type="submit" variant="primary" disabled={busy}>
              {t("saveSettings")}
            </Button>
          </div>
        </form>
      )}
      {!settings && message && <Notice tone="danger">{message.text}</Notice>}
    </section>
  );
}

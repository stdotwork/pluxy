// The settings form the panel draws from an extension's schema (extension-pages/README.md).
// Core checks every change against the schema, so the form leaves checking to it and shows
// what core says.

import { useEffect, useId, useState } from "preact/hooks";
import { problem as replyProblem, type SettingField, type Settings } from "../api";
import { Button, Card, Notice, useKit } from "../ui";

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
  const id = useId();
  const { field: f } = props;
  const label = f.title ?? props.name;
  const hint = f.description;
  if (f.type === "boolean") {
    return (
      <label class="check" data-part="field">
        <input
          type="checkbox"
          checked={props.value === true}
          onChange={(e) => props.onChange(e.currentTarget.checked)}
        />
        <span>
          <span class="label">{label}</span>
          {hint && <small class="hint">{hint}</small>}
        </span>
      </label>
    );
  }
  const value = typeof props.value === "string" ? props.value : "";
  return (
    <div class="field" data-part="field">
      <label class="label" for={id}>
        {label}
      </label>
      {f.enum ? (
        <select id={id} value={value} onChange={(e) => props.onChange(e.currentTarget.value)}>
          {!f.enum.includes(value) && <option value="">(none)</option>}
          {f.enum.map((o) => (
            <option key={o} value={o}>
              {o}
            </option>
          ))}
        </select>
      ) : (
        <input
          id={id}
          {...(f.writeOnly
            ? {
                type: "password" as const,
                autocomplete: "off",
                placeholder: props.isSet ? "unchanged" : undefined,
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
      {hint && <small class="hint">{hint}</small>}
      {f.writeOnly && props.isSet && (
        <span class="check">
          <small class="hint">A value is set.</small>
          <label class="check">
            <input
              type="checkbox"
              checked={props.clear}
              onChange={(e) => props.onClear(e.currentTarget.checked)}
            />
            <span>Clear {label}</span>
          </label>
        </span>
      )}
    </div>
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
      } else setMessage({ tone: "danger", text: replyProblem(r) ?? "could not load the settings" });
    });
    return () => {
      live = false;
    };
  }, [api, props.id, unauthorized]);

  if (missing) return null;
  return (
    <Card title="Settings">
      {settings && (
        <form
          class="stack"
          aria-label={`Settings of ${props.id}`}
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
              setMessage({ tone: "ok", text: "Saved." });
            } else setMessage({ tone: "danger", text: replyProblem(r) ?? "not saved" });
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
              Save settings
            </Button>
          </div>
        </form>
      )}
      {!settings && message && <Notice tone="danger">{message.text}</Notice>}
    </Card>
  );
}

// An account's routing fields and settings in its details (accountDetail.tsx): shown read-only,
// and for an account added by login changed in the Edit form. Core shows a secret in the
// settings only as a placeholder, and keeps the secret when the placeholder is sent back.

import { useEffect, useRef, useState } from "preact/hooks";
import type { Account } from "../api";
import { strings } from "../strings";
import { Button, Choices, Facts, Notice, Switch, useAction, useKit } from "../ui";
import {
  accountChange,
  draftChanged,
  draftOf,
  type InvalidField,
  modelRow,
  type SettingsDraft,
  settingsObject,
} from "./accountSettings.helpers";
import { ConfigDialog } from "./configDialog";

const t = strings("accountSettings", {
  heading: "Account settings",
  edit: "Edit",
  editTitle: "Edit {name}",
  prefix: "Prefix (prefix)",
  priority: "Priority (priority)",
  priorityPlaceholder: "e.g. 10 or -1",
  priorityHint: "Integers only. Larger value means higher priority.",
  weight: "Scheduling weight (weight)",
  weightHint:
    "Defaults to 1. Values at or below 0 exclude this credential from weighted scheduling. It takes effect only with weighted round robin.",
  disableCooling: "Disable exponential cooldown (disable_cooling)",
  disableCoolingHint:
    "Skip failure cooldown and exponential backoff only for this credential; other credentials are unaffected.",
  websockets: "WebSockets (websockets)",
  websocketsHint: "Enable Responses API websocket transport for this credential.",
  retry: "Credential request retries",
  inherit: "Inherit",
  custom: "Custom",
  retryCount: "Retry count",
  retryHint:
    "Inherit uses the global request-retry setting. Custom 0 explicitly disables request retries for this credential.",
  maxConcurrency: "Max concurrency (max_concurrency)",
  maxConcurrencyHint: "Requests the account runs at once. Empty means no limit.",
  noLimit: "No limit",
  models: "Models (models)",
  modelsHint: "The models the account serves, by upstream name. None: every model its extension lists.",
  modelName: "Model name",
  modelAlias: "Alias (optional)",
  addModel: "Add model",
  removeModel: "Remove model",
  excluded: "Excluded models (excluded_models)",
  excludedHint: "One rule per line. * matches any text, as in gpt-* or *-preview; case does not count.",
  aliases: "Account model aliases",
  aliasName: "Alias",
  aliasDisplay: "Display name",
  aliasFork: "Keep the original model in listings",
  addAlias: "Add alias",
  removeAlias: "Remove alias",
  settings: "Settings (settings)",
  settingsHint:
    "The extension's settings for this account, as JSON. A secret shows as a pluxy-redacted- placeholder that keeps it; type a new value to replace it.",
  on: "On",
  off: "Off",
  cancel: "Cancel",
  close: "Close",
  save: "Save",
  discardTitle: "Discard unsaved changes?",
  discardMessage: "You have unsaved edits in this form. Closing will discard them.",
  discard: "Discard changes",
  keepEditing: "Keep editing",
  invalid_priority: "Priority must be an integer.",
  invalid_weight: "Weight must be an integer.",
  invalid_maxConcurrency: "Max concurrency must be a non-negative integer.",
  invalid_retryCount: "Enter a non-negative safe integer, or select Inherit.",
  invalid_models: "Enter a model name for each model with an alias.",
  invalid_aliases: "Enter a non-empty model name and alias for each new or edited row.",
  invalid_settings: "Settings must be a JSON object.",
});

const pairs = (rows: { name: string; alias?: string }[] | undefined) =>
  rows?.length ? rows.map((r) => (r.alias ? `${r.name} → ${r.alias}` : r.name)).join(", ") : undefined;

/** A model entry as text: its name, or its name and the alias clients use. */
const modelText = (entries: Account["models"]) => pairs(entries?.map(modelRow));

/** The account's routing fields and settings, read-only. */
export function AccountSettings(props: { account: Account }) {
  const a = props.account;
  const flag = (v?: boolean) => (v === undefined ? undefined : v ? t("on") : t("off"));
  return (
    <section class="stack" data-part="account-settings" aria-label={t("heading")}>
      <h3>{t("heading")}</h3>
      <Facts
        rows={[
          [t("prefix"), a.prefix],
          [t("priority"), a.priority ?? 0],
          [t("weight"), a.weight],
          [t("disableCooling"), flag(a.disable_cooling)],
          [t("retry"), a.request_retry ?? t("inherit")],
          [t("maxConcurrency"), a.max_concurrency || t("noLimit")],
          [t("models"), modelText(a.models)],
          [t("excluded"), a.excluded_models?.join(", ")],
          [t("aliases"), pairs(a.model_alias)],
          [
            t("settings"),
            a.settings && <pre class="mono account-settings-json">{JSON.stringify(a.settings, null, 2)}</pre>,
          ],
        ]}
      />
    </section>
  );
}

/**
 * Asks before the page is left while dirty.current is set: the browser's own question on unload,
 * and ask(go) on a hash change (go makes the change after all). The modal form blocks every other
 * way out.
 */
function useLeaveGuard(dirty: { current: boolean }, ask: (go: () => void) => void) {
  const state = useRef({ dirty, ask });
  state.current = { dirty, ask };
  useEffect(() => {
    const beforeUnload = (e: BeforeUnloadEvent) => {
      if (!state.current.dirty.current) return;
      e.preventDefault();
      e.returnValue = "";
    };
    const hash = (e: HashChangeEvent) => {
      if (!state.current.dirty.current) return;
      e.stopImmediatePropagation();
      history.replaceState(null, "", e.oldURL);
      state.current.ask(() => location.assign(e.newURL));
    };
    window.addEventListener("beforeunload", beforeUnload);
    window.addEventListener("hashchange", hash, true);
    return () => {
      window.removeEventListener("beforeunload", beforeUnload);
      window.removeEventListener("hashchange", hash, true);
    };
  }, []);
}

/** The Edit button and its form, for an account added by login. */
export function AccountEdit(props: { account: Account; onChanged: () => void; disabled?: boolean }) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <Button disabled={props.disabled} onClick={() => setOpen(true)}>
        {t("edit")}
      </Button>
      {open && (
        <EditForm
          account={props.account}
          close={() => setOpen(false)}
          saved={() => {
            setOpen(false);
            props.onChanged();
          }}
        />
      )}
    </>
  );
}

function EditForm(props: { account: Account; close: () => void; saved: () => void }) {
  const { api } = useKit();
  const a = props.account;
  const [draft, setDraft] = useState<SettingsDraft>(() => draftOf(a));
  const [invalid, setInvalid] = useState<InvalidField>();
  const [leave, setLeave] = useState<{ go: () => void }>();
  const save = useAction(props.saved);
  const dirty = draftChanged(a, draft);
  const guard = useRef(false);
  guard.current = dirty && !save.busy;
  const ask = (go: () => void) => (dirty ? setLeave({ go }) : go());
  useLeaveGuard(guard, (go) => setLeave({ go }));
  const set = <K extends keyof SettingsDraft>(key: K, value: SettingsDraft[K]) => {
    setInvalid(undefined);
    setDraft((d) => ({ ...d, [key]: value }));
  };
  const submit = () => {
    const r = accountChange(a, draft);
    if (r.invalid) return setInvalid(r.invalid);
    if (Object.keys(r.change).length === 0) return props.close();
    void save.run(() => api.setAccount(a.id, r.change));
  };
  const settings = settingsObject(draft.settings);
  const rows = <R,>(list: R[], next: (l: R[]) => void) => ({
    edit: (i: number, row: Partial<R>) => next(list.map((r, j) => (j === i ? { ...r, ...row } : r))),
    remove: (i: number) => next(list.filter((_, j) => j !== i)),
  });
  const models = rows(draft.models, (l) => set("models", l));
  const aliases = rows(draft.aliases, (l) => set("aliases", l));
  return (
    <ConfigDialog title={t("editTitle", { name: a.id })} close={() => !save.busy && ask(props.close)}>
      <form
        class="stack account-edit"
        data-part="account-edit"
        onSubmit={(e) => {
          e.preventDefault();
          submit();
        }}
      >
        <fieldset class="stack" disabled={save.busy || !!leave}>
          <label class="field" data-part="field">
            <span class="label">{t("prefix")}</span>
            <input type="text" value={draft.prefix} onInput={(e) => set("prefix", e.currentTarget.value)} />
          </label>
          <label class="field" data-part="field">
            <span class="label">{t("priority")}</span>
            <input
              type="text"
              inputMode="numeric"
              placeholder={t("priorityPlaceholder")}
              value={draft.priority}
              onInput={(e) => set("priority", e.currentTarget.value)}
            />
            <small class="hint">{t("priorityHint")}</small>
          </label>
          <label class="field" data-part="field">
            <span class="label">{t("weight")}</span>
            <input
              type="text"
              inputMode="numeric"
              value={draft.weight}
              onInput={(e) => set("weight", e.currentTarget.value)}
            />
            <small class="hint">{t("weightHint")}</small>
          </label>
          <Switch
            label={t("disableCooling")}
            hint={t("disableCoolingHint")}
            checked={draft.disableCooling}
            onChange={(v) => set("disableCooling", v)}
          />
          {a.extension === "pluxy/codex" && (
            <Switch
              label={t("websockets")}
              hint={t("websocketsHint")}
              checked={settings?.websockets === true}
              onChange={(v) => {
                if (settings === undefined) return setInvalid("settings");
                set("settings", JSON.stringify({ ...settings, websockets: v }, null, 2));
              }}
            />
          )}
          <Choices
            label={t("retry")}
            value={draft.retry}
            choices={[
              { value: "inherit", label: t("inherit") },
              { value: "custom", label: t("custom") },
            ]}
            onChange={(v) => set("retry", v)}
          />
          {draft.retry === "custom" && (
            <label class="field" data-part="field">
              <span class="label">{t("retryCount")}</span>
              <input
                type="text"
                inputMode="numeric"
                value={draft.retryCount}
                onInput={(e) => set("retryCount", e.currentTarget.value)}
              />
            </label>
          )}
          <small class="hint">{t("retryHint")}</small>
          <label class="field" data-part="field">
            <span class="label">{t("maxConcurrency")}</span>
            <input
              type="text"
              inputMode="numeric"
              value={draft.maxConcurrency}
              onInput={(e) => set("maxConcurrency", e.currentTarget.value)}
            />
            <small class="hint">{t("maxConcurrencyHint")}</small>
          </label>
          <fieldset class="field account-edit-rows" data-part="field">
            <legend class="label">{t("models")}</legend>
            {draft.models.map((m, i) => (
              <div class="account-edit-row" data-part="account-edit-row" key={i}>
                <input
                  type="text"
                  aria-label={t("modelName")}
                  placeholder={t("modelName")}
                  value={m.name}
                  onInput={(e) => models.edit(i, { name: e.currentTarget.value })}
                />
                <input
                  type="text"
                  aria-label={t("modelAlias")}
                  placeholder={t("modelAlias")}
                  value={m.alias}
                  onInput={(e) => models.edit(i, { alias: e.currentTarget.value })}
                />
                <Button label={t("removeModel")} onClick={() => models.remove(i)}>
                  ✕
                </Button>
              </div>
            ))}
            <div class="actions">
              <Button onClick={() => set("models", [...draft.models, { name: "", alias: "" }])}>
                {t("addModel")}
              </Button>
            </div>
            <small class="hint">{t("modelsHint")}</small>
          </fieldset>
          <label class="field" data-part="field">
            <span class="label">{t("excluded")}</span>
            <textarea
              class="mono"
              rows={4}
              value={draft.excluded}
              onInput={(e) => set("excluded", e.currentTarget.value)}
            />
            <small class="hint">{t("excludedHint")}</small>
          </label>
          <fieldset class="field account-edit-rows" data-part="field">
            <legend class="label">{t("aliases")}</legend>
            {draft.aliases.map((r, i) => (
              <div class="account-edit-row" data-part="account-edit-row" key={i}>
                <input
                  type="text"
                  aria-label={t("modelName")}
                  placeholder={t("modelName")}
                  value={r.name}
                  onInput={(e) => aliases.edit(i, { name: e.currentTarget.value })}
                />
                <input
                  type="text"
                  aria-label={t("aliasName")}
                  placeholder={t("aliasName")}
                  value={r.alias}
                  onInput={(e) => aliases.edit(i, { alias: e.currentTarget.value })}
                />
                <input
                  type="text"
                  aria-label={t("aliasDisplay")}
                  placeholder={t("aliasDisplay")}
                  value={r.display_name}
                  onInput={(e) => aliases.edit(i, { display_name: e.currentTarget.value })}
                />
                <label class="check">
                  <input
                    type="checkbox"
                    checked={r.fork}
                    onChange={(e) => aliases.edit(i, { fork: e.currentTarget.checked })}
                  />
                  <span>{t("aliasFork")}</span>
                </label>
                <Button label={t("removeAlias")} onClick={() => aliases.remove(i)}>
                  ✕
                </Button>
              </div>
            ))}
            <div class="actions">
              <Button
                onClick={() =>
                  set("aliases", [...draft.aliases, { name: "", alias: "", display_name: "", fork: false }])
                }
              >
                {t("addAlias")}
              </Button>
            </div>
          </fieldset>
          <label class="field" data-part="field">
            <span class="label">{t("settings")}</span>
            <textarea
              class="mono"
              rows={8}
              spellcheck={false}
              value={draft.settings}
              onInput={(e) => set("settings", e.currentTarget.value)}
            />
            <small class="hint">{t("settingsHint")}</small>
          </label>
        </fieldset>
        {(invalid || save.error) && (
          <Notice tone="danger">{invalid ? t(`invalid_${invalid}`) : save.error}</Notice>
        )}
        {leave ? (
          <div
            class="stack"
            data-part="account-edit-discard"
            role="alertdialog"
            aria-label={t("discardTitle")}
          >
            <strong>{t("discardTitle")}</strong>
            <p>{t("discardMessage")}</p>
            <div class="actions">
              <Button
                variant="danger"
                onClick={() => {
                  const go = leave.go;
                  guard.current = false;
                  setLeave(undefined);
                  setDraft(draftOf(a));
                  go();
                }}
              >
                {t("discard")}
              </Button>
              <Button onClick={() => setLeave(undefined)}>{t("keepEditing")}</Button>
            </div>
          </div>
        ) : (
          <div class="actions">
            <Button disabled={save.busy} onClick={() => ask(props.close)}>
              {dirty ? t("cancel") : t("close")}
            </Button>
            <Button type="submit" variant="primary" disabled={save.busy || !dirty}>
              {t("save")}
            </Button>
          </div>
        )}
      </form>
    </ConfigDialog>
  );
}

// Fallbacks: what pluxy tries for a model when no account can serve it. Each is a line with its steps
// in order; one at a time can be open in the editor under the list.

import { Fragment } from "preact";
import { useId, useState } from "preact/hooks";
import { type Account, type Chain, problem, type ServedModel } from "../api";
import { Phases, providerName, useNaming } from "../parts";
import { accountIdentities, usePrivacy } from "../privacy";
import { strings } from "../strings";
import { Button, ConfirmButton, type Loaded, Notice, useKit } from "../ui";
import {
  accountsOf,
  addStep,
  canAddStep,
  changeStep,
  draftOf,
  draftProblem,
  type FallbackDraft,
  MAX_STEPS,
  moveStep,
  newDraft,
  providerOfModel,
  removeStep,
  skipsItself,
  stepsOf,
  stepWords,
  withName,
} from "./routing.helpers";

const t = strings("routing.fallbacks", {
  heading: "Fallbacks",
  intro: "When no account can serve a model, pluxy can try another model instead.",
  add: "+ Add a fallback",
  none: "No fallbacks yet. When every account for a model is out, its requests wait or fail until one is back.",
  loading: "Loading fallbacks…",
  failed: "Could not read the fallbacks.",
  yaml: "pluxy's settings file is CLIProxyAPI's config.yaml, which cannot hold fallbacks. Move it to pluxy's own format with pluxy import cpa, then add fallbacks here.",
  edit: "Edit",
  editFor: "Edit the fallback for {model}",
  remove: "Remove…",
  removeFor: "Remove the fallback for {model}…",
  removeNow: "Remove",
  removeQuestion: "Remove the fallback for {model}? Requests for {model} go back to its own accounts only.",
  asks: "When a tool asks for",
  steps: "Try, in order:",
  model: "Model",
  modelFor: "Model, step {n}",
  provider: "Provider",
  providerFor: "Provider, step {n}",
  anyProvider: "Any provider",
  only: "Only some accounts",
  onlyFor: "Only some accounts, step {n}",
  up: "↑",
  down: "↓",
  upFor: "Move step {n} up",
  downFor: "Move step {n} down",
  removeStep: "Remove step",
  removeStepFor: "Remove step {n}",
  addStep: "+ Add a step",
  tooMany: "A fallback has at most {max} steps.",
  skips: "Requests for {model} will never reach {model} itself. Add it as step 1 to try it first.",
  save: "Save",
  cancel: "Cancel",
  refused: "pluxy could not save this fallback: {message}",
  removeFailed: "pluxy could not remove this fallback: {message}",
});

function Editor(props: {
  draft: FallbackDraft;
  onChange: (d: FallbackDraft) => void;
  accounts: Account[];
  models?: ServedModel[];
  providers: string[];
  problem?: string;
  saving: boolean;
  onSave: () => void;
  onCancel: () => void;
}) {
  const d = props.draft;
  const list = useId();
  const naming = useNaming(props.accounts);
  const p = usePrivacy();
  const listed = props.models ? list : undefined;
  return (
    <form
      class="fallback-editor"
      data-part="fallback-editor"
      onSubmit={(e) => {
        e.preventDefault();
        props.onSave();
      }}
    >
      {props.models && (
        <datalist id={list}>
          {[...new Set(props.models.map((m) => m.id))].map((id) => (
            <option key={id} value={id} />
          ))}
        </datalist>
      )}
      <label class="field" data-part="field">
        <span class="label">{t("asks")}</span>
        <input
          data-slot="input"
          class="mono"
          list={listed}
          value={d.name}
          readOnly={d.existing}
          autocomplete="off"
          onInput={(e) => props.onChange(withName(d, e.currentTarget.value))}
        />
      </label>
      <p class="fallback-steps-label">{t("steps")}</p>
      <ol class="fallback-steps">
        {d.steps.map((s, i) => {
          const n = i + 1;
          const accounts = s.provider ? accountsOf(props.accounts, s.provider) : [];
          const providers =
            props.providers.includes(s.provider) || !s.provider
              ? props.providers
              : [...props.providers, s.provider];
          return (
            <li key={s.id} class="fallback-step">
              <span class="fallback-n">{n}</span>
              <div class="fallback-fields">
                <label class="field" data-part="field">
                  <span class="label">{t("model")}</span>
                  <input
                    data-slot="input"
                    class="mono"
                    aria-label={t("modelFor", { n })}
                    list={listed}
                    value={s.model}
                    autocomplete="off"
                    onInput={(e) => {
                      const model = e.currentTarget.value;
                      const provider = providerOfModel(model, props.models);
                      props.onChange(
                        changeStep(
                          d,
                          s.id,
                          provider !== undefined && provider !== s.provider
                            ? { model, provider, accounts: [] }
                            : { model },
                        ),
                      );
                    }}
                  />
                </label>
                <label class="field" data-part="field">
                  <span class="label">{t("provider")}</span>
                  <select
                    data-slot="select-trigger"
                    aria-label={t("providerFor", { n })}
                    value={s.provider}
                    onChange={(e) =>
                      props.onChange(changeStep(d, s.id, { provider: e.currentTarget.value, accounts: [] }))
                    }
                  >
                    <option value="">{t("anyProvider")}</option>
                    {providers.map((id) => (
                      <option key={id} value={id}>
                        {providerName(id)}
                      </option>
                    ))}
                  </select>
                </label>
              </div>
              {s.provider && accounts.length > 0 && (
                <fieldset class="fallback-accounts" aria-label={t("onlyFor", { n })}>
                  <legend>{t("only")}</legend>
                  {accounts.map((a) => (
                    <label key={a.id} class="fallback-account">
                      <input
                        type="checkbox"
                        checked={s.accounts.includes(a.id)}
                        onChange={(e) =>
                          props.onChange(
                            changeStep(d, s.id, {
                              accounts: e.currentTarget.checked
                                ? [...s.accounts, a.id]
                                : s.accounts.filter((id) => id !== a.id),
                            }),
                          )
                        }
                      />
                      <span title={naming.of(a)}>{naming.of(a)}</span>
                    </label>
                  ))}
                </fieldset>
              )}
              <span class="fallback-step-actions">
                <Button
                  disabled={i === 0}
                  label={t("upFor", { n })}
                  onClick={() => props.onChange(moveStep(d, s.id, "up"))}
                >
                  {t("up")}
                </Button>
                <Button
                  disabled={i === d.steps.length - 1}
                  label={t("downFor", { n })}
                  onClick={() => props.onChange(moveStep(d, s.id, "down"))}
                >
                  {t("down")}
                </Button>
                <Button label={t("removeStepFor", { n })} onClick={() => props.onChange(removeStep(d, s.id))}>
                  {t("removeStep")}
                </Button>
              </span>
            </li>
          );
        })}
      </ol>
      <div class="fallback-add">
        <Button disabled={!canAddStep(d)} onClick={() => props.onChange(addStep(d))}>
          {t("addStep")}
        </Button>
        {!canAddStep(d) && <span class="muted">{t("tooMany", { max: MAX_STEPS })}</span>}
      </div>
      {skipsItself(d) && <p class="muted">{t("skips", { model: d.name.trim() })}</p>}
      {props.problem && (
        <Notice tone="danger">{p.text(props.problem, props.accounts.flatMap(accountIdentities))}</Notice>
      )}
      <div class="actions">
        <Button type="submit" variant="primary" busy={props.saving}>
          {t("save")}
        </Button>
        <Button onClick={props.onCancel}>{t("cancel")}</Button>
      </div>
    </form>
  );
}

export function Fallbacks(props: {
  chains: Loaded<{ chains: Chain[] }>;
  accounts: Account[];
  models?: ServedModel[];
  running?: ReadonlySet<string>;
  /** The settings file is a config.yaml. */
  yaml: boolean;
  /** Core refused a fallback because of it. */
  onYaml: () => void;
}) {
  const { api, unauthorized } = useKit();
  const p = usePrivacy();
  const naming = useNaming(props.accounts);
  const [draft, setDraft] = useState<FallbackDraft>();
  const [why, setWhy] = useState<string>();
  const [saving, setSaving] = useState(false);
  const [removeError, setRemoveError] = useState<string>();
  const names = props.chains.data?.chains.map((c) => c.name) ?? [];
  const identities = props.accounts.flatMap(accountIdentities);
  // The providers a step can name: those with an account, and running when that is known.
  const providers = [...new Set(props.accounts.map((a) => a.extension).filter((e): e is string => !!e))]
    .filter((id) => !props.running || props.running.has(id))
    .sort((a, b) => providerName(a).localeCompare(providerName(b)));

  const open = (next: FallbackDraft) => {
    setWhy(undefined);
    setDraft(next);
  };
  const save = async () => {
    if (!draft) return;
    const bad = draftProblem(draft, names);
    if (bad) return setWhy(bad);
    setSaving(true);
    try {
      const reply = await api.saveChain(draft.name.trim(), stepsOf(draft));
      if (reply.status === 401) return unauthorized();
      if (reply.status === 409) {
        setDraft(undefined);
        return props.onYaml();
      }
      const error = problem(reply);
      if (error !== undefined) return setWhy(t("refused", { message: error }));
      setDraft(undefined);
      props.chains.reload();
    } finally {
      setSaving(false);
    }
  };
  const remove = async (name: string) => {
    setRemoveError(undefined);
    const reply = await api.deleteChain(name);
    if (reply.status === 401) return unauthorized();
    // 404: it is gone already; the list read again shows that.
    const error = reply.status === 404 ? undefined : problem(reply);
    if (error !== undefined) setRemoveError(t("removeFailed", { message: error }));
    props.chains.reload();
  };

  return (
    <section class="routing-fallbacks" aria-labelledby="routing-fallbacks-h">
      <div class="routing-section-head">
        <h2 id="routing-fallbacks-h">{t("heading")}</h2>
        {!props.yaml && (
          <Button disabled={props.chains.data === undefined || !!draft} onClick={() => open(newDraft())}>
            {t("add")}
          </Button>
        )}
      </div>
      <p class="routing-section-intro">{t("intro")}</p>
      {props.yaml && <Notice tone="warn">{t("yaml")}</Notice>}
      {removeError && <Notice tone="danger">{p.text(removeError, identities)}</Notice>}
      <Phases
        load={props.chains}
        loading={t("loading")}
        failed={{ title: t("failed") }}
        isEmpty={(d) => d.chains.length === 0}
        empty={<p class="muted">{t("none")}</p>}
      >
        {(data) => (
          <div class="fallback-list">
            {data.chains.map((chain) => (
              <div
                key={chain.name}
                class="fallback"
                data-part="fallback"
                data-open={draft?.existing && draft.name === chain.name ? "" : undefined}
              >
                <span class="fallback-ask mono" title={chain.name}>
                  {chain.name}
                </span>
                <span class="fallback-line mono">
                  {chain.steps.map((s, i) => (
                    <Fragment key={`${i}:${s.model}`}>
                      {i > 0 && " "}
                      <span class="fallback-step-text">
                        {i > 0 && "→ "}
                        {stepWords(s, naming.byId)}
                      </span>
                    </Fragment>
                  ))}
                </span>
                <span class="fallback-actions">
                  <Button
                    label={t("editFor", { model: chain.name })}
                    onClick={() => open(draftOf(chain.name, chain.steps))}
                  >
                    {t("edit")}
                  </Button>
                  <ConfirmButton
                    label={t("removeFor", { model: chain.name })}
                    confirmLabel={t("removeNow")}
                    question={t("removeQuestion", { model: chain.name })}
                    onConfirm={() => remove(chain.name)}
                  >
                    {t("remove")}
                  </ConfirmButton>
                </span>
              </div>
            ))}
          </div>
        )}
      </Phases>
      {draft && (
        <Editor
          draft={draft}
          onChange={setDraft}
          accounts={props.accounts}
          models={props.models}
          providers={providers}
          problem={why}
          saving={saving}
          onSave={save}
          onCancel={() => setDraft(undefined)}
        />
      )}
    </section>
  );
}

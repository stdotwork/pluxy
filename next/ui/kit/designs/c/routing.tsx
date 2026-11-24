// Routing: the middle of the line. It answers "where would a request for this model go right
// now" from core's own plan (nothing is sent), step by step for a fallback chain, and shows the
// chains as lists of steps that can be changed here. Tiers, weights and the retry policy are
// today's page, one stop further down.

import { useState } from "preact/hooks";
import { type ChainStep, problem, type RoutePlan } from "../../api";
import { ConfirmButton, useKit } from "../../ui";
import { type Fleet, useAsk, useFleet, useNow, useResource } from "./data";
import { Icon } from "./icons";
import { type Address, duration, hashFor, millis } from "./lib";
import { leftOutWords } from "./overview";
import { Btn, Note, PageHead, ProviderTile } from "./parts";

export function Routing(props: { address: Address }) {
  const { api } = useKit();
  const ask = useAsk();
  const fleet = useFleet();
  const now = useNow();
  const model = props.address.params.get("model") ?? "";
  const [typed, setTyped] = useState(model);
  const [seen, setSeen] = useState(model);
  if (seen !== model) {
    setSeen(model);
    setTyped(model);
  }
  const served = useResource("served-models", () => ask(api.servedModels()));
  const names = [
    ...new Set([...(fleet.chains ?? []).map((c) => c.name), ...(served.data?.models ?? []).map((m) => m.id)]),
  ].sort();
  const plan = useResource(
    `plan:${model}`,
    async () => (model ? ask(api.routePlan(model)) : undefined),
    30_000,
  );

  return (
    <div class="dc-page" data-view="routing">
      <PageHead
        title="Routing"
        sub="A request for a model goes down its fallback chain if it has one, then to the accounts that serve it, in order."
      />

      <section class="dc-card" aria-label="Where a model would go now">
        <div class="dc-card-head">
          <h2>Where would it go right now?</h2>
        </div>
        <div class="dc-card-pad dc-stack">
          <form
            class="dc-ask"
            onSubmit={(e) => {
              e.preventDefault();
              location.hash = hashFor("routing", { model: typed.trim() || undefined });
            }}
          >
            <label class="dc-field">
              <Icon name="model" size={16} />
              <span class="dc-sr">Model or chain</span>
              <input
                list="dc-models"
                placeholder="A model or chain, as a client asks for it"
                value={typed}
                onInput={(e) => setTyped(e.currentTarget.value)}
              />
            </label>
            <datalist id="dc-models">
              {names.map((n) => (
                <option key={n} value={n} />
              ))}
            </datalist>
            <button type="submit" class="dc-btn" data-kind="primary" disabled={typed.trim() === ""}>
              Ask core
            </button>
          </form>
          {!model ? (
            <div class="dc-suggest">
              <span class="dc-muted">Try</span>
              {names.slice(0, 8).map((n) => (
                <a key={n} class="dc-tag dc-ellipsis" href={hashFor("routing", { model: n })}>
                  {n}
                </a>
              ))}
              {served.loading && names.length === 0 && <span class="dc-muted">Reading the models…</span>}
            </div>
          ) : plan.error && !plan.data ? (
            <Note tone="warn">
              Core could not plan a request for <strong>{model}</strong>: {plan.error}.
            </Note>
          ) : !plan.data ? (
            <p class="dc-muted" role="status">
              Asking core…
            </p>
          ) : (
            <Plan plan={plan.data} fleet={fleet} now={now} />
          )}
        </div>
      </section>

      <Chains fleet={fleet} names={names} />

      <section class="dc-card dc-card-pad dc-next" aria-label="Tiers and policy">
        <div>
          <h2 class="dc-h2">Order among accounts</h2>
          <p class="dc-muted">
            Which accounts are preferred, their weights, and what happens after an error.
          </p>
        </div>
        <Btn href="#/routing/tiers" icon="go">
          Tiers and policy
        </Btn>
      </section>
    </div>
  );
}

/** Core's plan for one model, drawn as the steps it would walk. */
function Plan(props: { plan: RoutePlan; fleet: Fleet; now: number }) {
  const { plan, fleet } = props;
  const steps: ChainStep[] = plan.chain ?? [{ model: plan.model }];
  const first = plan.order[0];
  const liveStep = first ? (first.step ?? 1) : undefined;
  return (
    <div class="dc-plan-view">
      <p class="dc-verdict" data-none={first ? undefined : ""}>
        {first ? (
          <>
            <Icon name="go" size={18} />
            <span>
              It would go to <strong>{fleet.mask(first.id)}</strong> (
              {fleet.providers.of(first.provider).name})
              {plan.chain ? `, at step ${first.step ?? 1} of ${steps.length}` : ""}
              {first.model !== plan.model ? `, as ${first.model}` : ""}.
            </span>
          </>
        ) : (
          <>
            <Icon name="failed" size={18} />
            <span>No account can take it right now: the request would fail.</span>
          </>
        )}
      </p>
      <ol class="dc-steps">
        {steps.map((step, i) => {
          const n = i + 1;
          const order = plan.order.filter((o) => (o.step ?? 1) === n);
          const out = plan.left_out.filter((o) => (o.step ?? 1) === n);
          const live = liveStep === n;
          return (
            <li
              key={i}
              class="dc-step"
              data-live={live ? "" : undefined}
              data-dead={order.length === 0 ? "" : undefined}
            >
              <span class="dc-step-n dc-num" aria-hidden="true">
                {n}
              </span>
              <div class="dc-step-body">
                <div class="dc-step-head">
                  <strong class="dc-ellipsis" title={step.model}>
                    {step.model}
                  </strong>
                  <span class="dc-muted">
                    {step.provider ? `at ${fleet.providers.of(step.provider).name}` : "at any provider"}
                    {step.accounts?.length
                      ? `, only on ${step.accounts.map((a) => fleet.mask(a)).join(", ")}`
                      : ""}
                  </span>
                  <span class="dc-step-say">
                    {live
                      ? "takes it"
                      : order.length === 0
                        ? "skipped: no account free"
                        : liveStep && n > liveStep
                          ? "next if that fails"
                          : "would take it"}
                  </span>
                </div>
                {order.length > 0 && (
                  <ol class="dc-step-order" aria-label="Tried in this order">
                    {order.map((o, k) => (
                      <li key={o.id} data-slot-p={fleet.providers.of(o.provider).slot}>
                        <span class="dc-plan-n dc-num">{k + 1}</span>
                        <a
                          class="dc-ellipsis"
                          href={`#/accounts/${encodeURIComponent(o.id)}`}
                          title={fleet.mask(o.id)}
                        >
                          {fleet.mask(o.id)}
                        </a>
                      </li>
                    ))}
                  </ol>
                )}
                {out.length > 0 && (
                  <ul class="dc-step-out" aria-label="Left out">
                    {out.map((o) => {
                      const until = millis(o.until);
                      return (
                        <li key={o.id}>
                          <Icon name="close" size={12} />
                          <a
                            class="dc-ellipsis"
                            href={`#/accounts/${encodeURIComponent(o.id)}`}
                            title={fleet.mask(o.id)}
                          >
                            {fleet.mask(o.id)}
                          </a>
                          <span class="dc-muted">
                            {leftOutWords(o.state)}
                            {until && until > props.now ? ` · ${duration((until - props.now) / 1000)}` : ""}
                          </span>
                        </li>
                      );
                    })}
                  </ul>
                )}
              </div>
            </li>
          );
        })}
      </ol>
    </div>
  );
}

interface Draft {
  /** The chain being changed, or "" for a new one. */
  name: string;
  newName: string;
  steps: { model: string; provider: string; accounts: string }[];
}

const draftOf = (name: string, steps: ChainStep[]): Draft => ({
  name,
  newName: name,
  steps: steps.map((s) => ({
    model: s.model,
    provider: s.provider ?? "",
    accounts: (s.accounts ?? []).join(", "),
  })),
});

/** The fallback chains, each a list of steps tried in order, changed in place. */
function Chains(props: { fleet: Fleet; names: string[] }) {
  const { host, unauthorized } = useKit();
  const { fleet } = props;
  const [draft, setDraft] = useState<Draft>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const chains = fleet.chains;

  const send = async (method: "PUT" | "DELETE", name: string, steps?: ChainStep[]) => {
    setBusy(true);
    setError(undefined);
    const r = await host.call(method, `/chains/${encodeURIComponent(name)}`, steps ? { steps } : undefined);
    setBusy(false);
    if (r.status === 401) return unauthorized();
    const why = problem(r);
    if (why) return setError(why);
    setDraft(undefined);
    fleet.reload();
  };
  const save = (d: Draft) =>
    send(
      "PUT",
      d.newName.trim(),
      d.steps.map((s) => ({
        model: s.model.trim(),
        ...(s.provider ? { provider: s.provider } : {}),
        ...(s.accounts.trim()
          ? {
              accounts: s.accounts
                .split(",")
                .map((a) => a.trim())
                .filter(Boolean),
            }
          : {}),
      })),
    );
  const change = (i: number, field: "model" | "provider" | "accounts", value: string) =>
    draft &&
    setDraft({ ...draft, steps: draft.steps.map((s, k) => (k === i ? { ...s, [field]: value } : s)) });
  const move = (i: number, by: number) => {
    if (!draft) return;
    const steps = [...draft.steps];
    const [s] = steps.splice(i, 1);
    if (s) steps.splice(i + by, 0, s);
    setDraft({ ...draft, steps });
  };

  const editor = (d: Draft) => (
    <form
      class="dc-chain-edit"
      onSubmit={(e) => {
        e.preventDefault();
        save(d);
      }}
    >
      {d.name === "" && (
        <label class="dc-chain-name">
          <span class="dc-h3">The name clients ask for</span>
          <span class="dc-field">
            <input
              required
              value={d.newName}
              placeholder="best"
              onInput={(e) => setDraft({ ...d, newName: e.currentTarget.value })}
            />
          </span>
        </label>
      )}
      <ol class="dc-steps">
        {d.steps.map((s, i) => (
          <li key={i} class="dc-step">
            <span class="dc-step-n dc-num" aria-hidden="true">
              {i + 1}
            </span>
            <div class="dc-step-fields">
              <label>
                <span class="dc-sr">Step {i + 1}: </span>
                <span class="dc-h3">Model</span>
                <span class="dc-field">
                  <input
                    required
                    list="dc-models"
                    value={s.model}
                    onInput={(e) => change(i, "model", e.currentTarget.value)}
                  />
                </span>
              </label>
              <label>
                <span class="dc-h3">Provider</span>
                <select
                  class="dc-select"
                  value={s.provider}
                  onChange={(e) => change(i, "provider", e.currentTarget.value)}
                >
                  <option value="">Any provider</option>
                  {fleet.providers.list.map((p) => (
                    <option key={p.id} value={p.id}>
                      {p.name}
                    </option>
                  ))}
                </select>
              </label>
              <label>
                <span class="dc-h3">Only these accounts</span>
                <span class="dc-field">
                  <input
                    value={s.accounts}
                    placeholder="Any that serve it"
                    onInput={(e) => change(i, "accounts", e.currentTarget.value)}
                  />
                </span>
              </label>
              <span class="dc-step-tools">
                <Btn
                  kind="plain"
                  size="small"
                  icon="down"
                  label={`Move step ${i + 1} down`}
                  disabled={i === d.steps.length - 1}
                  onClick={() => move(i, 1)}
                />
                <Btn
                  kind="plain"
                  size="small"
                  icon="close"
                  label={`Remove step ${i + 1}`}
                  disabled={d.steps.length === 1}
                  onClick={() => setDraft({ ...d, steps: d.steps.filter((_, k) => k !== i) })}
                />
              </span>
            </div>
          </li>
        ))}
      </ol>
      {error && <Note tone="danger">Not saved: {fleet.mask(error)}.</Note>}
      <div class="dc-chain-actions">
        <Btn
          size="small"
          icon="plus"
          onClick={() => setDraft({ ...d, steps: [...d.steps, { model: "", provider: "", accounts: "" }] })}
        >
          Add a step
        </Btn>
        <span class="dc-chain-actions-end">
          <Btn
            size="small"
            kind="plain"
            onClick={() => {
              setDraft(undefined);
              setError(undefined);
            }}
          >
            Cancel
          </Btn>
          <button type="submit" class="dc-btn" data-kind="primary" data-size="small" disabled={busy}>
            {busy ? "Saving…" : "Save chain"}
          </button>
        </span>
      </div>
    </form>
  );

  return (
    <section class="dc-card" aria-label="Fallback chains">
      <div class="dc-card-head">
        <h2>Fallback chains</h2>
        <span class="dc-muted">A name clients ask for, and the models tried for it in order.</span>
        <div class="dc-head-tools">
          <Btn
            size="small"
            icon="plus"
            disabled={!!draft}
            onClick={() =>
              setDraft({ name: "", newName: "", steps: [{ model: "", provider: "", accounts: "" }] })
            }
          >
            New chain
          </Btn>
        </div>
      </div>
      <div class="dc-card-pad dc-stack">
        {draft?.name === "" && editor(draft)}
        {!chains ? (
          <p class="dc-muted" role="status">
            {fleet.loading ? "Reading the chains…" : "The chains could not be read."}
          </p>
        ) : chains.length === 0 && !draft ? (
          <p class="dc-muted">No chains. Without one, a model goes to the accounts of one provider only.</p>
        ) : (
          chains.map((c) =>
            draft?.name === c.name ? (
              <div key={c.name} class="dc-chain">
                <h3 class="dc-chain-title">
                  <Icon name="chain" size={16} />
                  {c.name}
                </h3>
                {editor(draft)}
              </div>
            ) : (
              <div key={c.name} class="dc-chain">
                <div class="dc-chain-head">
                  <h3 class="dc-chain-title">
                    <Icon name="chain" size={16} />
                    {c.name}
                  </h3>
                  <span class="dc-chain-tools">
                    <a href={hashFor("routing", { model: c.name })}>Where it goes now</a>
                    <Btn
                      size="small"
                      disabled={!!draft}
                      onClick={() => {
                        setError(undefined);
                        setDraft(draftOf(c.name, c.steps));
                      }}
                    >
                      Edit steps
                    </Btn>
                    <ConfirmButton disabled={busy || !!draft} onConfirm={() => send("DELETE", c.name)}>
                      Remove
                    </ConfirmButton>
                  </span>
                </div>
                <ol class="dc-chain-steps">
                  {c.steps.map((s, i) => {
                    const p = s.provider ? fleet.providers.of(s.provider) : undefined;
                    return (
                      <li key={i} data-slot-p={p?.slot}>
                        <span class="dc-step-n dc-num" aria-hidden="true">
                          {i + 1}
                        </span>
                        <strong class="dc-ellipsis" title={s.model}>
                          {s.model}
                        </strong>
                        {p && <ProviderTile p={p} />}
                        <span class="dc-muted dc-ellipsis">
                          {p ? p.name : "any provider"}
                          {s.accounts?.length
                            ? ` · only ${s.accounts.map((a) => fleet.mask(a)).join(", ")}`
                            : ""}
                        </span>
                      </li>
                    );
                  })}
                </ol>
                {error && !draft && <Note tone="danger">Not removed: {fleet.mask(error)}.</Note>}
              </div>
            ),
          )
        )}
      </div>
    </section>
  );
}

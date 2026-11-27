// Setup's step 3, Check it works: one test request through an account, and its result, before
// setup ends. Core tests the account directly, so routing is never changed.

import type { ComponentChildren } from "preact";
import { useEffect, useState } from "preact/hooks";
import type { Account, AccountModel } from "../api";
import { Icon } from "../icons";
import { accountHref, accountName, providerName, timeTaken } from "../parts";
import { accountIdentities, usePrivacy } from "../privacy";
import { failureWord } from "../states";
import { rich, strings } from "../strings";
import { Button, Notice, useKit } from "../ui";
import { canTest, defaultTestAccount } from "./setup.helpers";
import { Toggle } from "./setupAccounts";

const t = strings("setupCheck", {
  noAccount: "There is no account to test yet.",
  addAccount: "Add an account",
  finishWithout: "Finish without testing",
  explain: "This sends one short test message through {account}. It counts against that account's use.",
  account: "Account",
  accountOption: "{account} ({provider})",
  chooseModel: "Choose a model",
  model: "Model",
  firstModel: "First model the account offers",
  modelsLoading: "Loading the models…",
  send: "Send a test request",
  sending: "Sending…",
  succeeded: "Succeeded",
  passed: "It works: {account} replied in {time} using {model}.",
  finish: "Finish setup",
  testAgain: "Test again",
  signInAgain: "Sign in again",
  seeWhatToDo: "See what to do",
  finishAnyway: "Finish anyway",
  noModels: "This account lists no models, so there is nothing to test it with.",
  summaryPassed: "Passed: {model} in {time}.",
  summaryFailed: "Failed: {word}.",
  summaryNone: "Not tested yet.",
  back: "Back",
});

/** What the last test on this page came to; kept by the page, so it outlives the step folding. */
export type TestOutcome =
  | { kind: "passed"; account: string; model: string; ms: number }
  | { kind: "failed"; account: string; code?: string; word: string; message: string }
  | { kind: "no-models"; account: string };

/** The step's folded summary. */
export function checkSummary(outcome?: TestOutcome): string {
  if (outcome?.kind === "passed")
    return t("summaryPassed", { model: outcome.model, time: timeTaken(outcome.ms) });
  if (outcome?.kind === "failed") return t("summaryFailed", { word: outcome.word });
  return t("summaryNone");
}

/** The models an account offers, read when the person opens Choose a model. */
function useModels(id: string | undefined, open: boolean) {
  const { api, unauthorized } = useKit();
  const [models, setModels] = useState<{ id: string; list?: AccountModel[]; error?: string }>();
  useEffect(() => {
    if (!open || !id || models?.id === id) return;
    let live = true;
    setModels({ id });
    api.accountModels(id).then((r) => {
      if (!live) return;
      if (r.status === 401) return unauthorized();
      setModels({
        id,
        list: r.body?.models.filter((m) => !m.excluded),
        error: r.body ? undefined : r.error?.message,
      });
    });
    return () => {
      live = false;
    };
  }, [id, open]);
  return models?.id === id ? models : undefined;
}

/** Step 3's open content: the test and what it came to, with the ways to finish. */
export function CheckStep(props: {
  accounts: Account[];
  running?: ReadonlySet<string>;
  /** The account added in step 1 on this visit. */
  added?: string;
  outcome?: TestOutcome;
  onOutcome: (outcome: TestOutcome | undefined) => void;
  /** Opens step 1. */
  onAddAccount: () => void;
  /** Reads the accounts again (the tested one was gone). */
  onAccountsChanged: () => void;
  finishing: boolean;
  onFinish: () => void;
  /** On the first run: the way to step 2, at the start of the card's foot. */
  back?: () => void;
}) {
  const { api, unauthorized } = useKit();
  const p = usePrivacy();
  const usable = props.accounts.filter((a) => canTest(a, props.running));
  const [chosen, setChosen] = useState<string>();
  const [model, setModel] = useState("");
  const [choosing, setChoosing] = useState(false);
  const [sending, setSending] = useState(false);
  const account =
    usable.find((a) => a.id === chosen) ?? defaultTestAccount(props.accounts, props.running, props.added);
  const models = useModels(account?.id, choosing);
  // On the first run the actions are the card's foot, with Back at its start and the main action
  // at its end; elsewhere the main action comes first.
  const foot = (...items: ComponentChildren[]) => (
    <div class={props.back ? "focus-foot setup-next" : "actions"}>
      {props.back && (
        <span class="focus-foot-start">
          <Button variant="ghost" onClick={props.back}>
            {t("back")}
          </Button>
        </span>
      )}
      {props.back ? [...items].reverse() : items}
    </div>
  );
  const finishButton = (label: string) => (
    <Button key="finish" variant="quiet" disabled={props.finishing} onClick={props.onFinish}>
      {label}
    </Button>
  );
  if (!account) {
    return (
      <>
        <p>{t("noAccount")}</p>
        {foot(
          <Button key="add" variant="primary" onClick={props.onAddAccount}>
            {t("addAccount")}
          </Button>,
          finishButton(t("finishWithout")),
        )}
      </>
    );
  }
  const name = p.id(accountName(account));
  // Provider messages can name the account: they are masked with its identities.
  const identities = accountIdentities(account);
  const outcome = props.outcome?.account === account.id ? props.outcome : undefined;
  const send = async () => {
    if (sending) return;
    setSending(true);
    props.onOutcome(undefined);
    const id = account.id;
    const r = await api.testAccount(id, model || undefined);
    setSending(false);
    if (r.status === 401) return unauthorized();
    if (r.status === 404) {
      // The account is gone: read them again, and test the default one next.
      setChosen(undefined);
      setModel("");
      return props.onAccountsChanged();
    }
    if (r.status === 422) return props.onOutcome({ kind: "no-models", account: id });
    const result = r.body?.result;
    if (result?.ok) {
      return props.onOutcome({
        kind: "passed",
        account: id,
        model: result.served_model || result.model,
        ms: result.latency_ms,
      });
    }
    const error = result?.error ?? r.error;
    const status = result ? (result.error?.status ?? undefined) : r.status;
    props.onOutcome({
      kind: "failed",
      account: id,
      code: error?.code,
      word: failureWord(error?.code, status),
      message: (error?.message ?? "").replace(/\.$/, ""),
    });
  };
  const signInFix =
    outcome?.kind === "failed" && (outcome.code === "auth" || outcome.code === "permission")
      ? account.source === "config"
        ? { label: t("seeWhatToDo"), href: accountHref(account.id) }
        : { label: t("signInAgain"), href: accountHref(account.id, "sign-in") }
      : undefined;
  return (
    <>
      {usable.length >= 2 && (
        <label class="setup-field">
          <span>{t("account")}</span>
          <select
            value={account.id}
            disabled={sending}
            onChange={(e) => {
              setChosen(e.currentTarget.value);
              setModel("");
            }}
          >
            {usable.map((a) => (
              <option key={a.id} value={a.id}>
                {t("accountOption", { account: p.id(accountName(a)), provider: providerName(a.extension) })}
              </option>
            ))}
          </select>
        </label>
      )}
      <p>{t("explain", { account: name })}</p>
      <div class="setup-model">
        <Toggle open={choosing} onToggle={() => setChoosing(!choosing)}>
          {t("chooseModel")}
        </Toggle>
        {choosing &&
          (models?.list ? (
            <label class="setup-field">
              <span>{t("model")}</span>
              <select value={model} disabled={sending} onChange={(e) => setModel(e.currentTarget.value)}>
                <option value="">{t("firstModel")}</option>
                {models.list.map((m) => (
                  <option key={m.id} value={m.id}>
                    {m.id}
                  </option>
                ))}
              </select>
            </label>
          ) : models?.error ? (
            <Notice tone="danger">{p.text(models.error, identities)}</Notice>
          ) : (
            <p class="muted">{t("modelsLoading")}</p>
          ))}
      </div>
      {outcome?.kind === "passed" && (
        <p class="setup-passed" data-part="setup-test" data-state="passed">
          <Icon name="check" label={t("succeeded")} />
          <span>
            {rich(t("passed", { account: name, time: timeTaken(outcome.ms) }), {
              model: <span class="setup-model-name">{outcome.model}</span>,
            })}
          </span>
        </p>
      )}
      {outcome?.kind === "failed" && (
        <Notice tone="danger">
          {outcome.message ? `${outcome.word}: ${p.text(outcome.message, identities)}` : outcome.word}
        </Notice>
      )}
      {outcome?.kind === "no-models" && <Notice tone="warn">{t("noModels")}</Notice>}
      {outcome?.kind === "passed"
        ? foot(
            <Button key="finish" variant="primary" disabled={props.finishing} onClick={props.onFinish}>
              {t("finish")}
            </Button>,
          )
        : outcome?.kind === "no-models"
          ? foot(finishButton(t("finishAnyway")))
          : foot(
              // A refused sign-in is fixed by signing in again, so that is the filled button then.
              signInFix && (
                <a key="fix" class="btn" data-part="button" data-variant="primary" href={signInFix.href}>
                  {signInFix.label}
                </a>
              ),
              <Button
                key="send"
                variant={signInFix ? "quiet" : "primary"}
                busy={sending}
                disabled={sending}
                onClick={send}
              >
                {sending ? t("sending") : outcome ? t("testAgain") : t("send")}
              </Button>,
              finishButton(outcome ? t("finishAnyway") : t("finishWithout")),
            )}
    </>
  );
}

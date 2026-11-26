// What an account's page offers under "Settings, models and a test request" besides its settings:
// the models it serves, a test request to one of them, and renewing its sign-in now. Signing in
// again is the page's opening, once.

import { useEffect, useRef, useState } from "preact/hooks";
import { type Account, type AccountTestResult, problem } from "../api";
import { Icon } from "../icons";
import { timeTaken } from "../parts";
import { accountIdentities, usePrivacy } from "../privacy";
import { failureWord } from "../states";
import { strings } from "../strings";
import { Button, Notice, useKit, useLoad } from "../ui";

const t = strings("accountTools", {
  models: "Models it serves",
  loading: "Loading models…",
  noModels: "It serves no models. Check its settings.",
  tryAgain: "Try again",
  testWith: "Test with",
  first: "The first model it serves",
  test: "Send a test request",
  costs: "Uses one request.",
  result: "Test request",
  succeeded: "Succeeded",
  missingResult: "pluxy sent back no result. Try again.",
  renew: "Renew sign-in now",
  renewed: "Signed in again.",
});

export function AccountTools(props: { account: Account; onChanged(): void }) {
  const { api, unauthorized } = useKit();
  const p = usePrivacy();
  const a = props.account;
  const ids = [...accountIdentities(a), a.id];
  const models = useLoad(() => api.accountModels(a.id));
  // A change to the account (its models, its excluded ones) changes what it serves: read again.
  const changed = `${a.version ?? ""}/${a.updated_at ?? ""}`;
  const seen = useRef(changed);
  useEffect(() => {
    if (seen.current === changed) return;
    seen.current = changed;
    models.reload();
  }, [changed, models.reload]);
  const served = (models.data?.models ?? []).filter((m) => !m.excluded);
  const [model, setModel] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const [notice, setNotice] = useState<string>();
  const [result, setResult] = useState<AccountTestResult>();
  const run = async (action: () => Promise<void>) => {
    if (busy) return;
    setBusy(true);
    setError(undefined);
    setNotice(undefined);
    try {
      await action();
    } finally {
      setBusy(false);
    }
  };
  const test = () =>
    run(async () => {
      setResult(undefined);
      const r = await api.testAccount(a.id, model || undefined);
      if (r.status === 401) return unauthorized();
      const why = problem(r);
      if (why) setError(why);
      else if (r.body?.result) setResult(r.body.result);
      else setError(t("missingResult"));
    });
  const renew = () =>
    run(async () => {
      const r = await api.refresh(a.id);
      if (r.status === 401) return unauthorized();
      const why = problem(r);
      setError(why);
      if (!why) {
        setNotice(t("renewed"));
        props.onChanged();
      }
    });
  return (
    <div class="account-tools stack" data-part="account-tools">
      <section class="stack" data-part="account-models" aria-labelledby="account-models">
        <h3 id="account-models">{t("models")}</h3>
        {models.phase === "loading" && <p class="muted">{t("loading")}</p>}
        {models.phase === "failed" && (
          <Notice tone="danger">
            {p.text(models.error, ids)} <Button onClick={models.reload}>{t("tryAgain")}</Button>
          </Notice>
        )}
        {models.data &&
          (served.length === 0 ? (
            <p class="muted">{t("noModels")}</p>
          ) : (
            <ul class="account-model-list">
              {served.map((m) => {
                // The names tools ask for it by, when an alias renames it.
                const names = (m.names ?? []).filter((n) => n !== m.id);
                return (
                  <li key={m.id} class="mono">
                    {names.length ? `${m.id} → ${names.join(", ")}` : m.id}
                  </li>
                );
              })}
            </ul>
          ))}
      </section>
      <section class="stack" aria-labelledby="account-test">
        <h3 id="account-test" class="sr-only">
          {t("result")}
        </h3>
        <div class="account-test-row">
          <label class="field" data-part="field">
            <span class="label">{t("testWith")}</span>
            <select
              data-slot="select-trigger"
              disabled={busy}
              value={model}
              onChange={(e) => setModel(e.currentTarget.value)}
            >
              <option value="">{t("first")}</option>
              {served.map((m) => (
                <option key={m.id} value={m.id}>
                  {m.id}
                </option>
              ))}
            </select>
          </label>
          <Button disabled={busy} onClick={test}>
            {t("test")}
          </Button>
        </div>
        <p class="hint" data-part="account-test-hint">
          {t("costs")}
        </p>
        {result && (
          <div class="account-test-result" data-part="account-test-result" aria-live="polite">
            {result.ok ? (
              <span class="request-result" data-result="ok">
                <Icon name="check" />
                <span>{t("succeeded")}</span>
              </span>
            ) : (
              <span class="request-result" data-result="failed">
                <Icon name="needsYou" />
                <span>{failureWord(result.error?.code, result.error?.status)}</span>
              </span>
            )}
            <span class="mono">{result.served_model || result.model}</span>
            <span>{timeTaken(result.latency_ms)}</span>
            {result.error?.message && <span class="muted">{p.text(result.error.message, ids)}</span>}
            {result.reply && (
              <pre class="mono" data-part="account-test-reply">
                {p.text(result.reply, ids)}
              </pre>
            )}
          </div>
        )}
      </section>
      {a.source === "login" && a.refreshable && (
        <div class="actions">
          <Button disabled={busy} onClick={renew}>
            {t("renew")}
          </Button>
        </div>
      )}
      {error && <Notice tone="danger">{p.text(error, ids)}</Notice>}
      {notice && <Notice tone="ok">{notice}</Notice>}
    </div>
  );
}

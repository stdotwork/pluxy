import { useRef, useState } from "preact/hooks";
import { type Account, type AccountTestResult, problem } from "../api";
import { LoginFlow } from "../loginFlow";
import { accountIdentities, usePrivacy } from "../privacy";
import { strings } from "../strings";
import { Badge, Button, ConfirmButton, Facts, List, Notice, useKit, useLoad, when } from "../ui";
import { credentialTools } from "./accountTools.helpers";
import { ConfigDialog } from "./configDialog";
import { configAddress } from "./configSource.helpers";

const t = strings("accountTools", {
  health: "Health",
  check: "Check",
  checkState: "Check result",
  checking: "Checking",
  ok: "Passed",
  failed: "Failed",
  not_checked: "Not checked",
  detail: "Detail",
  error: "Error",
  checkedAt: "Checked at",
  lastError: "Last error",
  lastErrorAt: "Last error at",
  active: "Requests now",
  cooling: "Cooling",
  noCooldown: "No active cooldowns.",
  scopeModel: "{scope} · {model}",
  model: "Model",
  kind: "Kind",
  until: "Until",
  credential: "Entire credential",
  endpoint: "Shared endpoint",
  modelScope: "One model",
  rate_limit: "Rate limit",
  quota: "Quota restriction",
  cooldown: "Cooldown",
  clear: "Clear cooldown",
  clearTitle: "Clear credential cooldown?",
  clearConfirm:
    "Clear the local routing cooldown for “{name}”? This may make the credential eligible for another request immediately. It does not restore upstream quota.",
  cleared: "Cooldown cleared for {name}.",
  cancel: "Cancel",
  models: "Models",
  supported: "Supported models",
  names: "Client names",
  status: "Status",
  excluded: "Excluded",
  included: "Included",
  noModels: "No supported models.",
  loading: "Loading…",
  retry: "Retry",
  test: "Test (spends one request)",
  testModel: "Test model",
  automatic: "First supported model",
  testHint: "Sends Hi with at most 16 reply tokens. This spends one request and ignores local cooldowns.",
  result: "Test result",
  reply: "Reply start",
  served: "Served model",
  latency: "Latency",
  milliseconds: "{count} ms",
  testPassed: "Request succeeded",
  testFailed: "Request failed",
  missingResult: "Core returned no test result.",
  refresh: "Refresh OAuth credential",
  refreshed: "OAuth credential refreshed.",
  signIn: "Sign in again",
  methods: "Sign-in methods",
  method: "Method",
  noMethods: "No running extension offers a sign-in method for this account.",
  edit: "Edit in Config Panel",
});

export function AccountTools(props: { account: Account; onChanged(): void }) {
  const { api, unauthorized } = useKit();
  const p = usePrivacy();
  const a = props.account;
  const ids = accountIdentities(a);
  const tools = credentialTools(a);
  const [modelsOpen, setModelsOpen] = useState(false);
  const [loginOpen, setLoginOpen] = useState(false);
  const [confirm, setConfirm] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const [notice, setNotice] = useState<string>();
  const [result, setResult] = useState<AccountTestResult>();
  const [model, setModel] = useState("");
  const lock = useRef(false);
  const run = async (action: () => Promise<void>) => {
    if (lock.current) return;
    lock.current = true;
    setBusy(true);
    setError(undefined);
    setNotice(undefined);
    try {
      await action();
    } finally {
      lock.current = false;
      setBusy(false);
    }
  };
  const check = a.check;
  const cooldowns = a.cooldowns ?? [];
  return (
    <div class="account-tools stack" data-part="account-tools">
      <section class="stack" data-part="account-check" aria-label={t("health")}>
        <h3>{t("health")}</h3>
        <Facts
          rows={[
            [
              t("checkState"),
              <Badge tone={check?.state === "ok" ? "ok" : check?.state === "failed" ? "danger" : "muted"}>
                {check && ["checking", "ok", "failed", "not_checked"].includes(check.state)
                  ? t(check.state as "checking" | "ok" | "failed" | "not_checked")
                  : (check?.state ?? t("not_checked"))}
              </Badge>,
            ],
            [t("detail"), p.text(check?.detail, ids)],
            [t("error"), check?.error && p.text(errorText(check.error), ids)],
            [t("checkedAt"), when(check?.at)],
            [t("lastError"), a.last_error && p.text(errorText(a.last_error), ids)],
            [t("lastErrorAt"), when(a.last_error_at)],
            [t("active"), a.active_requests ?? 0],
          ]}
        />
      </section>
      <section class="stack" data-part="account-cooldowns" aria-label={t("cooldown")}>
        {cooldowns.length > 0 ? (
          <>
            <h3>
              <Badge tone="warn">{t("cooling")}</Badge>
            </h3>
            <ul class="cards account-tool-list" data-part="list" aria-label={t("cooldown")}>
              {cooldowns.map((c) => {
                const scope =
                  c.scope === "credential"
                    ? t("credential")
                    : c.scope === "model"
                      ? t("modelScope")
                      : c.scope === "endpoint"
                        ? t("endpoint")
                        : c.scope;
                return (
                  <li key={[c.scope, c.model, c.kind].join("/")} class="item" data-part="item">
                    <strong>{c.model ? t("scopeModel", { scope, model: c.model }) : scope}</strong>
                    <Facts
                      rows={[
                        [
                          t("kind"),
                          ["rate_limit", "quota", "cooldown"].includes(c.kind)
                            ? t(c.kind as "rate_limit" | "quota" | "cooldown")
                            : c.kind,
                        ],
                        [t("until"), when(c.until)],
                        [t("error"), c.error && p.text(errorText(c.error), ids)],
                      ]}
                    />
                  </li>
                );
              })}
            </ul>
            <div>
              <Button disabled={busy} onClick={() => setConfirm(true)}>
                {t("clear")}
              </Button>
            </div>
          </>
        ) : (
          <p class="muted">{t("noCooldown")}</p>
        )}
      </section>
      {confirm && (
        <ConfigDialog title={t("clearTitle")} close={() => !busy && setConfirm(false)}>
          <p data-part="account-cooldown-confirm">
            {t("clearConfirm", { name: a.label ? p.id(a.label) : p.text(a.id, ids) })}
          </p>
          <div class="actions">
            <ConfirmButton
              disabled={busy}
              onConfirm={() =>
                void run(async () => {
                  const r = await api.resetCooldown(a.id);
                  if (r.status === 401) return unauthorized();
                  const why = problem(r);
                  setError(why);
                  setConfirm(false);
                  if (!why) {
                    setNotice(t("cleared", { name: a.id }));
                    props.onChanged();
                  }
                })
              }
            >
              {t("clear")}
            </ConfirmButton>
            <Button disabled={busy} onClick={() => setConfirm(false)}>
              {t("cancel")}
            </Button>
          </div>
        </ConfigDialog>
      )}
      {error && <Notice tone="danger">{p.text(error, ids)}</Notice>}
      {notice && <Notice tone="ok">{p.text(notice, ids)}</Notice>}
      <div class="actions" data-part="account-tool-actions">
        <Button onClick={() => setModelsOpen((open) => !open)}>{t("models")}</Button>
        <Button
          disabled={busy}
          onClick={() =>
            void run(async () => {
              setResult(undefined);
              const r = await api.testAccount(a.id, model || undefined);
              if (r.status === 401) return unauthorized();
              const why = problem(r);
              if (why) setError(why);
              else if (r.body?.result) setResult(r.body.result);
              else setError(t("missingResult"));
            })
          }
        >
          {t("test")}
        </Button>
        {tools.refresh && (
          <Button
            disabled={busy}
            onClick={() =>
              void run(async () => {
                const r = await api.refresh(a.id);
                if (r.status === 401) return unauthorized();
                const why = problem(r);
                setError(why);
                if (!why) {
                  setNotice(t("refreshed"));
                  props.onChanged();
                }
              })
            }
          >
            {t("refresh")}
          </Button>
        )}
        {tools.signIn && (
          <Button disabled={busy} onClick={() => setLoginOpen((open) => !open)}>
            {t("signIn")}
          </Button>
        )}
        {a.source === "config" && (
          <a data-part="account-config-link" href={configAddress("source", a.id)}>
            {t("edit")}
          </a>
        )}
      </div>
      <p class="hint" data-part="account-test-hint">
        {t("testHint")}
      </p>
      {modelsOpen && <AccountModels accountId={a.id} model={model} onModel={setModel} busy={busy} />}
      {result && (
        <section class="stack" data-part="account-test-result" aria-label={t("result")} aria-live="polite">
          <h3>{t("result")}</h3>
          <Badge tone={result.ok ? "ok" : "danger"}>{t(result.ok ? "testPassed" : "testFailed")}</Badge>
          <Facts
            rows={[
              [t("model"), result.model],
              [t("served"), result.served_model],
              [t("latency"), t("milliseconds", { count: result.latency_ms })],
              [t("error"), result.error && p.text(errorText(result.error), ids)],
            ]}
          />
          {result.reply && (
            <div data-part="account-test-reply">
              <h4>{t("reply")}</h4>
              <pre class="mono">{p.text(result.reply, ids)}</pre>
            </div>
          )}
        </section>
      )}
      {loginOpen && <AccountSignIn account={a} onChanged={props.onChanged} />}
    </div>
  );
}

function errorText(error: { code: string; message: string }): string {
  return [error.code, error.message].filter(Boolean).join(": ");
}

function AccountModels(props: {
  accountId: string;
  model: string;
  onModel(model: string): void;
  busy: boolean;
}) {
  const { api } = useKit();
  const models = useLoad(() => api.accountModels(props.accountId));
  return (
    <section class="stack" data-part="account-models" aria-label={t("supported")}>
      <h3>{t("supported")}</h3>
      {models.loading && <Notice tone="muted">{t("loading")}</Notice>}
      {models.error && (
        <>
          <Notice tone="danger">{models.error}</Notice>
          <div>
            <Button onClick={models.reload}>{t("retry")}</Button>
          </div>
        </>
      )}
      {models.data && (
        <>
          {models.data.models.length === 0 ? (
            <p>{t("noModels")}</p>
          ) : (
            <ul class="cards account-tool-list" data-part="list" aria-label={t("supported")}>
              {models.data.models.map((m) => (
                <li key={m.id} class="item" data-part="item">
                  <strong class="mono">{m.id}</strong>
                  <Facts
                    rows={[
                      [t("names"), (m.names ?? []).join(", ")],
                      [
                        t("status"),
                        <Badge tone={m.excluded ? "muted" : "ok"}>
                          {t(m.excluded ? "excluded" : "included")}
                        </Badge>,
                      ],
                    ]}
                  />
                </li>
              ))}
            </ul>
          )}
          <label class="field" data-part="field">
            <span class="label">{t("testModel")}</span>
            <select
              disabled={props.busy}
              value={props.model}
              onChange={(e) => props.onModel(e.currentTarget.value)}
            >
              <option value="">{t("automatic")}</option>
              {models.data.models.map((m) => (
                <option key={m.id} value={m.id}>
                  {m.id}
                </option>
              ))}
            </select>
          </label>
        </>
      )}
    </section>
  );
}

function AccountSignIn(props: { account: Account; onChanged(): void }) {
  const { api } = useKit();
  const methods = useLoad(api.loginMethods);
  const [selected, setSelected] = useState<string>();
  const [attempt, setAttempt] = useState(0);
  const available =
    methods.data?.extensions.find((e) => e.extension === props.account.extension)?.methods ?? [];
  return (
    <section class="stack" data-part="account-sign-in" aria-label={t("signIn")}>
      <h3>{t("signIn")}</h3>
      {methods.loading && <Notice tone="muted">{t("loading")}</Notice>}
      {methods.error && (
        <>
          <Notice tone="danger">{methods.error}</Notice>
          <div>
            <Button onClick={methods.reload}>{t("retry")}</Button>
          </div>
        </>
      )}
      {methods.data && available.length === 0 && <p>{t("noMethods")}</p>}
      <List
        label={t("methods")}
        items={available.filter((m) => !selected || m.method === selected)}
        id={(m) => m.method}
        columns={[
          {
            label: t("method"),
            value: (method) => (
              <div data-part="method" class="stack">
                <div>
                  <Button disabled={selected !== undefined} onClick={() => setSelected(method.method)}>
                    {method.label || method.method}
                  </Button>
                </div>
                {selected === method.method && (
                  <LoginFlow
                    key={`${method.method}/${attempt}`}
                    extension={props.account.extension ?? ""}
                    method={method}
                    existingAccountId={props.account.id}
                    keptInputs={Object.keys(props.account.settings ?? {})}
                    onSuccess={props.onChanged}
                    onReset={() => {
                      setSelected(undefined);
                      setAttempt((n) => n + 1);
                    }}
                  />
                )}
              </div>
            ),
          },
        ]}
      />
    </section>
  );
}

import type { TargetedInputEvent } from "preact";
import { useEffect, useId, useLayoutEffect, useRef, useState } from "preact/hooks";
import { type Login, type LoginInput, type LoginMethod, problem } from "./api";
import type { Reply } from "./host";
import { usePrivacy } from "./privacy";
import { strings } from "./strings";
import { Button, Notice, useCopy, useKit } from "./ui";

const t = strings("loginFlow", {
  accountId: "Account ID",
  optional: "{label} (optional)",
  chooseFile: "Choose File",
  readFailed: "The file could not be read. Choose it again.",
  add: "Add account",
  signInAgain: "Sign in again",
  generating: "Generating sign-in link…",
  waiting: "Waiting for authentication…",
  authorize: "Open the sign-in page and authorize",
  device: "Enter this code on the sign-in page",
  deviceCode: "Device code:",
  copyCode: "Copy device code",
  open: "Open Link",
  copy: "Copy Link",
  copied: "Copied",
  copyFailed: "Could not copy. Select and copy the text instead.",
  addressOrCode: "Address or code",
  submit: "Submit",
  remaining: "Time left: {time}",
  success: "Authentication successful!",
  viewAccounts: "View accounts",
  another: "Log in another account",
  failed: "Authentication failed: {reason}",
  alreadyRunning:
    "A sign-in for this Account ID is already running. Use another Account ID, or start over once it has ended.",
  noReason: "No reason was provided.",
  invalidReply: "Core returned no login.",
  pollStopped: "Could not check sign-in after three attempts. {reason}",
  invalidId:
    "Use 1–128 letters, digits, dots, underscores or hyphens, starting with a letter or digit. The ID must not end with a dot.",
  startOver: "Start over",
  cancel: "Cancel",
  cancelling: "Cancelling…",
  cancelled: "Sign-in cancelled.",
});

/** Values stay in this form's memory and go only into a management request body. */
function LoginField(props: {
  field: LoginInput;
  value: string;
  disabled: boolean;
  onChange(value: string): void;
  onReading(reading: boolean): void;
}) {
  const file = useRef<HTMLInputElement>(null);
  const controlId = useId();
  const [reading, setReading] = useState(false);
  const [error, setError] = useState<string>();
  const label = props.field.label || props.field.name;
  const control = {
    id: controlId,
    autoComplete: props.field.secret ? "new-password" : "off",
    spellcheck: false,
    required: !props.field.optional,
    disabled: props.disabled || reading,
    value: props.value,
    onInput: (e: TargetedInputEvent<HTMLInputElement>) => props.onChange(e.currentTarget.value),
  };
  return (
    <div class="stack" data-part="field">
      <label class="field" for={controlId}>
        <span class="label">{props.field.optional ? t("optional", { label }) : label}</span>
        {props.field.secret ? (
          <input data-slot="input" type="password" {...control} />
        ) : (
          <input data-slot="input" type="text" {...control} />
        )}
      </label>
      {props.field.secret && (
        <div>
          <input
            ref={file}
            type="file"
            hidden
            aria-label={t("chooseFile")}
            disabled={props.disabled || reading}
            onChange={async (e) => {
              const input = e.currentTarget;
              const selected = input.files?.[0];
              input.value = "";
              if (!selected) return;
              if (selected.size > 1024 * 1024) {
                setError(t("readFailed"));
                return;
              }
              setReading(true);
              props.onReading(true);
              setError(undefined);
              try {
                props.onChange(await selected.text());
              } catch {
                setError(t("readFailed"));
              } finally {
                setReading(false);
                props.onReading(false);
              }
            }}
          />
          <Button disabled={props.disabled || reading} onClick={() => file.current?.click()}>
            {t("chooseFile")}
          </Button>
        </div>
      )}
      {error && <Notice tone="danger">{error}</Notice>}
    </div>
  );
}

function LoginFields(props: {
  fields: LoginInput[];
  values: Record<string, string>;
  disabled: boolean;
  onChange(name: string, value: string): void;
  onReading(name: string, reading: boolean): void;
}) {
  return (
    <>
      {props.fields.map((field) => (
        <LoginField
          key={field.name}
          field={field}
          value={props.values[field.name] ?? ""}
          disabled={props.disabled}
          onChange={(value) => props.onChange(field.name, value)}
          onReading={(reading) => props.onReading(field.name, reading)}
        />
      ))}
    </>
  );
}

function safeLink(raw?: string): string | undefined {
  if (!raw) return;
  try {
    const url = new URL(raw);
    if (url.protocol === "https:" || url.protocol === "http:") return raw;
  } catch {
    // A provider's malformed URL cannot be opened as a browser action.
  }
}

function CopyButton(props: { value: string; children: string }) {
  const { state, copy } = useCopy(props.value);
  return (
    <>
      <Button onClick={copy}>{state === "copied" ? t("copied") : props.children}</Button>
      {state === "failed" && <Notice tone="danger">{t("copyFailed")}</Notice>}
    </>
  );
}

export interface LoginFlowProps {
  extension: string;
  method: LoginMethod;
  /** Replaces this account's credential instead of adding an account. */
  existingAccountId?: string;
  /** A new account ID already chosen by the caller; methods without inputs start at once. */
  newAccountId?: string;
  /** Names of the inputs the account keeps in its settings; a sign-in again may leave them empty. */
  keptInputs?: string[];
  onSuccess(accountId: string): void;
  /** Called to leave this flow (Cancel, Add another): the caller remounts it by key. */
  onReset(completed: boolean): void;
}

export function suggestedAccountId(): string {
  const bytes = crypto.getRandomValues(new Uint8Array(6));
  return `account-${Array.from(bytes, (byte) => byte.toString(16).padStart(2, "0")).join("")}`;
}

/** An input may be left empty when the method says so, or when a sign-in again keeps its value
 * in the account's settings (a secret is never kept there, so it is always asked for). */
export function optionalInput(field: LoginInput, kept: string[]): boolean {
  return field.optional === true || (!field.secret && kept.includes(field.name));
}

export function validAccountId(id: string): boolean {
  return /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/.test(id) && !id.endsWith(".");
}

/** Whether core refused a start because a sign-in for that account is already running. Core's
 * reply carries no field for it beyond the code `conflict`, so the sentence is matched too. */
export function signInRunning(r: Reply<unknown>): boolean {
  return r.status === 409 && r.error?.code === "conflict" && /already running/.test(r.error.message);
}

/** Shared by adding an account and signing in to an existing account again. A sign-in still
 * waiting when the flow unmounts or the page is hidden for good is cancelled, as Cancel does. */
export function LoginFlow(props: LoginFlowProps) {
  const { api, unauthorized } = useKit();
  const p = usePrivacy();
  const [accountId, setAccountId] = useState(suggestedAccountId);
  const [values, setValues] = useState<Record<string, string>>({});
  const [reading, setReading] = useState<Record<string, boolean>>({});
  const [answer, setAnswer] = useState<Record<string, string>>({});
  const [login, setLogin] = useState<Login>();
  const [busy, setBusy] = useState(false);
  const [starting, setStarting] = useState(false);
  const [cancelling, setCancelling] = useState(false);
  const [error, setError] = useState<string>();
  const [pollError, setPollError] = useState<string>();
  const [pollStopped, setPollStopped] = useState<string>();
  const [startError, setStartError] = useState<string>();
  const [deadline, setDeadline] = useState<number>();
  const [now, setNow] = useState(Date.now());
  const live = useRef(true);
  const operation = useRef(0);
  const started = useRef(false);
  const promptKey = useRef("");
  const succeeded = useRef<string | undefined>(undefined);
  const locked = useRef(false);
  const success = useRef(props.onSuccess);
  const waiting = useRef<string | undefined>(undefined);
  const cancelApi = useRef(api);
  cancelApi.current = api;
  const root = useRef<HTMLElement>(null);
  const idHint = useId();
  success.current = props.onSuccess;
  const id = props.existingAccountId ?? props.newAccountId ?? accountId;
  const kept = props.keptInputs ?? [];
  const inputs = (props.method.inputs ?? []).map((field) => ({
    ...field,
    optional: optionalInput(field, kept),
  }));
  const validId = validAccountId(id);
  const readingFile = Object.values(reading).some(Boolean);
  const fileReading = (name: string, active: boolean) => setReading((all) => ({ ...all, [name]: active }));

  useEffect(() => {
    live.current = true;
    // Best effort and not awaited: the page may be going away. A login that finished is not touched.
    const leave = () => {
      const running = waiting.current;
      if (!running) return;
      waiting.current = undefined;
      void Promise.resolve(cancelApi.current.cancelLogin(running)).catch(() => {});
    };
    addEventListener("pagehide", leave);
    return () => {
      removeEventListener("pagehide", leave);
      live.current = false;
      operation.current++;
      leave();
    };
  }, []);

  const accept = (next: Login) => {
    waiting.current = next.state === "pending" ? next.id : undefined;
    setLogin(next);
    const key = JSON.stringify(next.prompt) ?? "";
    if (key !== promptKey.current) {
      promptKey.current = key;
      setAnswer({});
      setDeadline(next.prompt?.expires_in ? Date.now() + next.prompt.expires_in * 1000 : undefined);
      setNow(Date.now());
    }
    if (next.state === "done" && succeeded.current !== next.id) {
      succeeded.current = next.id;
      success.current(next.account);
    }
  };
  const replyError = (reply: Reply<unknown>) => {
    if (reply.status === 401) {
      unauthorized();
      return true;
    }
    return false;
  };

  const start = async () => {
    if (locked.current || login || readingFile || !validId) return;
    locked.current = true;
    setStarting(true);
    setStartError(undefined);
    const version = ++operation.current;
    try {
      const r = props.existingAccountId
        ? await api.signInAgain(id, props.method.method, values)
        : await api.startLogin({
            id,
            extension: props.extension,
            method: props.method.method,
            input: values,
          });
      if (!live.current) {
        // Left while the start was in flight: the login it began is cancelled like any other.
        if (r.body?.login?.state === "pending") void api.cancelLogin(r.body.login.id);
        return;
      }
      if (version !== operation.current) return;
      if (replyError(r)) return;
      const why = problem(r);
      if (why || !r.body?.login)
        setStartError(signInRunning(r) ? t("alreadyRunning") : (why ?? t("invalidReply")));
      else accept(r.body.login);
    } finally {
      if (live.current && version === operation.current) {
        setStarting(false);
        setValues({});
        locked.current = false;
      }
    }
  };

  useEffect(() => {
    if (!started.current && (props.newAccountId || props.existingAccountId) && inputs.length === 0) {
      started.current = true;
      void start();
    }
  }, []);

  useEffect(() => {
    if (login?.state !== "pending" || pollStopped) return;
    let disposed = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let fetching = false;
    let failures = 0;
    let stopped = false;
    const poll = async () => {
      if (disposed || stopped || document.visibilityState !== "visible" || fetching) return;
      fetching = true;
      const version = operation.current;
      try {
        const r = await api.login(login.id);
        if (disposed || !live.current || version !== operation.current) return;
        if (replyError(r)) return;
        const why = problem(r) ?? (!r.body?.login ? t("invalidReply") : undefined);
        if (why) {
          failures++;
          if (r.status === 404 || failures >= 3) {
            stopped = true;
            setPollStopped(r.status === 404 ? why : t("pollStopped", { reason: why }));
            setPollError(undefined);
          } else setPollError(why);
        } else if (r.body?.login) {
          failures = 0;
          setPollError(undefined);
          stopped = r.body.login.state !== "pending";
          accept(r.body.login);
        }
      } finally {
        fetching = false;
        if (!disposed && !stopped && document.visibilityState === "visible")
          timer = setTimeout(poll, 2000 * 2 ** failures);
      }
    };
    const visibility = () => {
      clearTimeout(timer);
      if (document.visibilityState === "visible") {
        setNow(Date.now());
        void poll();
      }
    };
    if (document.visibilityState === "visible") timer = setTimeout(poll, 2000);
    document.addEventListener("visibilitychange", visibility);
    return () => {
      disposed = true;
      clearTimeout(timer);
      document.removeEventListener("visibilitychange", visibility);
    };
  }, [login?.id, login?.state, pollStopped, api, unauthorized]);

  useEffect(() => {
    if (!deadline || login?.state !== "pending" || pollStopped) return;
    const timer = setInterval(() => {
      if (document.visibilityState === "visible") setNow(Date.now());
    }, 1000);
    return () => clearInterval(timer);
  }, [deadline, login?.state, pollStopped]);

  const submit = async () => {
    if (!login || locked.current || readingFile) return;
    locked.current = true;
    setBusy(true);
    setError(undefined);
    const version = ++operation.current;
    try {
      const r = await api.submitLoginInput(login.id, answer);
      if (!live.current || version !== operation.current || replyError(r)) return;
      setError(problem(r));
      if (!problem(r)) setAnswer({});
    } finally {
      if (live.current && version === operation.current) {
        setBusy(false);
        locked.current = false;
      }
    }
  };
  const cancel = async () => {
    if (!login || locked.current || cancelling) return;
    locked.current = true;
    setBusy(true);
    setError(undefined);
    const version = ++operation.current;
    try {
      const r = await api.cancelLogin(login.id);
      if (!live.current || version !== operation.current || replyError(r)) return;
      const why = problem(r);
      setError(why);
      if (!why) {
        waiting.current = undefined;
        setCancelling(true);
        setAnswer({});
      }
    } finally {
      if (live.current && version === operation.current) {
        setBusy(false);
        locked.current = false;
      }
    }
  };
  const reset = () => props.onReset(login?.state === "done");

  const prompt = login?.state === "pending" && !pollStopped ? login.prompt : undefined;
  const url =
    safeLink(prompt?.kind === "browser" ? prompt.url : prompt?.verification_uri_complete) ??
    safeLink(prompt?.verification_uri);
  const promptInputs =
    prompt?.inputs ??
    (prompt?.kind === "browser" ? [{ name: "redirect_url", label: t("addressOrCode") }] : []);
  const remaining = Math.max(0, Math.ceil(((deadline ?? now) - now) / 1000));
  const terminalFailure = login?.state === "failed" || startError || pollStopped;
  const focusState = [
    starting,
    cancelling,
    login?.state,
    promptKey.current,
    error,
    pollError,
    startError,
    pollStopped,
  ].join("/");
  useLayoutEffect(() => {
    const element = root.current;
    if (!element) return;
    const target =
      (error || pollError) && !terminalFailure
        ? element.querySelector<HTMLElement>('[role="alert"]')
        : (element.querySelector<HTMLElement>("input:not([hidden]):not(:disabled)") ??
          (prompt?.kind === "device"
            ? element.querySelector<HTMLElement>("[data-login-cancel] button")
            : null) ??
          element.querySelector<HTMLElement>("button:not(:disabled)") ??
          element.querySelector<HTMLElement>('[role="status"], [role="alert"]'));
    if (target) {
      if (target.matches('[role="status"], [role="alert"]')) target.tabIndex = -1;
      target.focus();
    }
  }, [focusState]);
  return (
    <section ref={root} class="login-flow stack" data-part="flow">
      {!login && !starting && !startError && (
        <form
          class="stack"
          onSubmit={(e) => {
            e.preventDefault();
            void start();
          }}
        >
          {!props.existingAccountId && !props.newAccountId && (
            <div class="field" data-part="field">
              <label>
                <span class="label">{t("accountId")}</span>
                <input
                  data-slot="input"
                  type="text"
                  required
                  maxLength={128}
                  pattern="[A-Za-z0-9][A-Za-z0-9._\x2D]{0,127}"
                  value={accountId}
                  aria-invalid={!validId}
                  aria-describedby={!validId ? idHint : undefined}
                  onInput={(e) => setAccountId(e.currentTarget.value)}
                />
              </label>
              {!validId && (
                <small id={idHint} class="hint">
                  {t("invalidId")}
                </small>
              )}
            </div>
          )}
          <LoginFields
            fields={inputs}
            values={values}
            disabled={starting}
            onReading={fileReading}
            onChange={(name, value) => setValues((all) => ({ ...all, [name]: value }))}
          />
          <div class="actions">
            <Button type="submit" variant="primary" disabled={readingFile || !validId}>
              {t(props.existingAccountId ? "signInAgain" : "add")}
            </Button>
            <Button onClick={reset}>{t("cancel")}</Button>
          </div>
        </form>
      )}
      {starting && (
        <Notice tone="muted">{t(props.method.kind === "browser" ? "generating" : "waiting")}</Notice>
      )}
      {prompt && !cancelling && (
        <>
          {(prompt.kind === "browser" || prompt.kind === "device") && (
            <p>{t(prompt.kind === "browser" ? "authorize" : "device")}</p>
          )}
          {prompt.message && <p>{p.text(prompt.message)}</p>}
          {prompt.kind === "device" && prompt.user_code && (
            <div class="stack">
              <span>{t("deviceCode")}</span>
              <strong class="mono" data-part="device-code" aria-live="polite" aria-atomic="true">
                {prompt.user_code}
              </strong>
              <div>
                <CopyButton value={prompt.user_code}>{t("copyCode")}</CopyButton>
              </div>
            </div>
          )}
          {url && (
            <>
              <a href={url} target="_blank" rel="noopener noreferrer" class="mono">
                {url}
              </a>
              <div class="actions">
                <Button
                  onClick={() => {
                    window.open(url, "_blank", "noopener,noreferrer");
                  }}
                >
                  {t("open")}
                </Button>
                <CopyButton value={url}>{t("copy")}</CopyButton>
              </div>
            </>
          )}
          {deadline !== undefined && (
            <p>
              {t("remaining", {
                time: `${Math.floor(remaining / 60)}:${String(remaining % 60).padStart(2, "0")}`,
              })}
            </p>
          )}
          {promptInputs.length > 0 && (
            <form
              class="stack"
              onSubmit={(e) => {
                e.preventDefault();
                void submit();
              }}
            >
              <LoginFields
                key={promptKey.current}
                fields={promptInputs}
                values={answer}
                disabled={busy}
                onReading={fileReading}
                onChange={(name, value) => setAnswer((all) => ({ ...all, [name]: value }))}
              />
              <div>
                <Button type="submit" variant="primary" disabled={busy || readingFile}>
                  {t("submit")}
                </Button>
              </div>
            </form>
          )}
        </>
      )}
      {login?.state === "pending" && !pollStopped && (
        <>
          <Notice tone="muted">{t(cancelling ? "cancelling" : "waiting")}</Notice>
          <div data-login-cancel="">
            <Button onClick={() => void cancel()} disabled={busy || cancelling}>
              {t("cancel")}
            </Button>
          </div>
        </>
      )}
      {error && <Notice tone="danger">{p.text(error)}</Notice>}
      {pollError && <Notice tone="danger">{p.text(pollError)}</Notice>}
      {login?.state === "done" && (
        <>
          <Notice tone="ok">{t("success")}</Notice>
          <div class="actions">
            <Button
              onClick={() => {
                location.hash = "/accounts";
              }}
            >
              {t("viewAccounts")}
            </Button>
            {!props.existingAccountId && <Button onClick={reset}>{t("another")}</Button>}
          </div>
        </>
      )}
      {terminalFailure && (
        <>
          <Notice tone="danger">
            {t("failed", {
              reason: p.text(pollStopped ?? startError ?? login?.error?.message) ?? t("noReason"),
            })}
          </Notice>
          <div>
            <Button onClick={reset}>{t("startOver")}</Button>
          </div>
        </>
      )}
      {login?.state === "cancelled" && (
        <>
          <Notice tone="muted">{t("cancelled")}</Notice>
          <div>
            <Button onClick={reset}>{t("startOver")}</Button>
          </div>
        </>
      )}
    </section>
  );
}

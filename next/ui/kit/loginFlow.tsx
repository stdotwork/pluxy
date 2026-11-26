import type { TargetedInputEvent } from "preact";
import { useEffect, useId, useLayoutEffect, useRef, useState } from "preact/hooks";
import { type Login, type LoginInput, type LoginMethod, problem } from "./api";
import type { Reply } from "./host";
import { providerName } from "./parts";
import { usePrivacy } from "./privacy";
import { strings } from "./strings";
import { Button, Notice, useCopy, useKit } from "./ui";

const t = strings("loginFlow", {
  moreOptions: "More options",
  loadFromFile: "Load from file",
  readFailed: "Could not read the file. Choose it again.",
  continue: "Continue",
  generating: "Generating sign-in link…",
  waiting: "Waiting for sign-in…",
  authorize: "Open the sign-in page and sign in there.",
  device: "Enter this code on the sign-in page",
  copyCode: "Copy code",
  open: "Open sign-in page",
  copy: "Copy link",
  copied: "Copied",
  copyFailed: "Could not copy. Select and copy the text instead.",
  pasteAddress: "Paste the address you land on",
  remaining: "Time left: {time}",
  failed: "Sign-in did not finish: {reason}",
  alreadyRunning: "A sign-in for this account is already running.",
  differentAccount:
    "That was a different {provider} account, so this one keeps its old sign-in. To use the other one, add it as a new account.",
  details: "Details",
  noReason: "No reason given.",
  wasCancelled: "It was cancelled.",
  invalidReply: "pluxy returned no sign-in data. Try again.",
  pollStopped: "Could not check the sign-in three times in a row. {reason}",
  tryAgain: "Try again",
  cancel: "Cancel",
  cancelling: "Cancelling…",
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
        <span class="label">{label}</span>
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
            aria-label={t("loadFromFile")}
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
          <Button variant="quiet" disabled={props.disabled || reading} onClick={() => file.current?.click()}>
            {t("loadFromFile")}
          </Button>
        </div>
      )}
      {error && <Notice tone="danger">{error}</Notice>}
    </div>
  );
}

/** The inputs a method asks for: the required ones, then the optional ones under More options. */
function LoginFields(props: {
  fields: LoginInput[];
  values: Record<string, string>;
  disabled: boolean;
  onChange(name: string, value: string): void;
  onReading(name: string, reading: boolean): void;
}) {
  const field = (f: LoginInput) => (
    <LoginField
      key={f.name}
      field={f}
      value={props.values[f.name] ?? ""}
      disabled={props.disabled}
      onChange={(value) => props.onChange(f.name, value)}
      onReading={(reading) => props.onReading(f.name, reading)}
    />
  );
  const optional = props.fields.filter((f) => f.optional);
  return (
    <>
      {props.fields.filter((f) => !f.optional).map(field)}
      {optional.length > 0 && (
        <details class="login-more" data-part="more-options">
          <summary>{t("moreOptions")}</summary>
          <div class="stack">{optional.map(field)}</div>
        </details>
      )}
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
  /** Names of the inputs the account keeps in its settings; a sign-in again may leave them empty. */
  keptInputs?: string[];
  /** Called with the account once the sign-in is done: the caller shows what comes next. */
  onSuccess(accountId: string): void;
  /** Called to leave this flow (Cancel, Try again): the caller shows its methods again. */
  onReset(): void;
}

/** An input may be left empty when the method says so, or when a sign-in again keeps its value
 * in the account's settings (a secret is never kept there, so it is always asked for). */
export function optionalInput(field: LoginInput, kept: string[]): boolean {
  return field.optional === true || (!field.secret && kept.includes(field.name));
}

/** Whether core refused a start because a sign-in for that account is already running. Core's
 * reply carries no field for it beyond the code `conflict`, so the sentence is matched too. */
export function signInRunning(r: Reply<unknown>): boolean {
  return r.status === 409 && r.error?.code === "conflict" && /already running/.test(r.error.message);
}

/** Whether a sign-in again ended because it signed in to another account than the one named: core's
 * message for it (cred.ErrIdentity) says so, and carries no field of its own. */
export function differentAccount(message?: string): boolean {
  return /signed in to a different account/.test(message ?? "");
}

/** Shared by adding an account and signing in to an existing account again. Adding sends no ID:
 * core makes the account's. A sign-in still waiting when the flow unmounts or the page is hidden
 * for good is cancelled, as Cancel does. */
export function LoginFlow(props: LoginFlowProps) {
  const { api, unauthorized } = useKit();
  const p = usePrivacy();
  const [values, setValues] = useState<Record<string, string>>({});
  const [reading, setReading] = useState<Record<string, boolean>>({});
  const [answer, setAnswer] = useState<Record<string, string>>({});
  const [login, setLogin] = useState<Login>();
  const [busy, setBusy] = useState(false);
  const [starting, setStarting] = useState(false);
  const [error, setError] = useState<string>();
  const [pollError, setPollError] = useState<string>();
  // The poll gave up: core's reason, and whether it failed three times in a row (else the login is gone).
  const [pollStopped, setPollStopped] = useState<{ reason: string; repeated: boolean }>();
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
  success.current = props.onSuccess;
  const kept = props.keptInputs ?? [];
  const inputs = (props.method.inputs ?? []).map((field) => ({
    ...field,
    optional: optionalInput(field, kept),
  }));
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
    if (locked.current || login || readingFile) return;
    locked.current = true;
    setStarting(true);
    setStartError(undefined);
    const version = ++operation.current;
    try {
      const r = props.existingAccountId
        ? await api.signInAgain(props.existingAccountId, props.method.method, values)
        : await api.startLogin({
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
    if (!started.current && inputs.length === 0) {
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
            setPollStopped({ reason: why, repeated: r.status !== 404 });
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
  /** Cancel stops the sign-in and goes back to the methods at once; a refusal stays on screen. */
  const cancel = async () => {
    if (!login || locked.current) return;
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
        props.onReset();
      }
    } finally {
      if (live.current && version === operation.current) {
        setBusy(false);
        locked.current = false;
      }
    }
  };

  const prompt = login?.state === "pending" && !pollStopped ? login.prompt : undefined;
  const url =
    safeLink(prompt?.kind === "browser" ? prompt.url : prompt?.verification_uri_complete) ??
    safeLink(prompt?.verification_uri);
  const promptInputs =
    prompt?.inputs ??
    (prompt?.kind === "browser" ? [{ name: "redirect_url", label: t("pasteAddress") }] : []);
  const remaining = Math.max(0, Math.ceil(((deadline ?? now) - now) / 1000));
  const ended = login?.state === "failed" || login?.state === "cancelled";
  const terminalFailure = ended || startError || pollStopped;
  // Core's own words for why it ended, masked when shown.
  const reason =
    pollStopped?.reason ??
    startError ??
    (login?.state === "cancelled" ? t("wasCancelled") : login?.error?.message);
  const other = ended && !!props.existingAccountId && differentAccount(login?.error?.message);
  const focusState = [
    starting,
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
      {!login && !starting && !startError && inputs.length > 0 && (
        <form
          class="stack"
          onSubmit={(e) => {
            e.preventDefault();
            void start();
          }}
        >
          <LoginFields
            fields={inputs}
            values={values}
            disabled={starting}
            onReading={fileReading}
            onChange={(name, value) => setValues((all) => ({ ...all, [name]: value }))}
          />
          <div class="actions">
            <Button type="submit" variant="primary" disabled={readingFile}>
              {t("continue")}
            </Button>
            <Button onClick={props.onReset}>{t("cancel")}</Button>
          </div>
        </form>
      )}
      {starting && (
        <Notice tone="muted">{t(props.method.kind === "browser" ? "generating" : "waiting")}</Notice>
      )}
      {prompt && (
        <>
          {/* The provider's own words, when it gives some; else ours. */}
          {prompt.message ? (
            <p>{p.text(prompt.message)}</p>
          ) : (
            (prompt.kind === "browser" || prompt.kind === "device") && (
              <p>{t(prompt.kind === "browser" ? "authorize" : "device")}</p>
            )
          )}
          {prompt.kind === "device" && prompt.user_code && (
            <strong class="mono" data-part="device-code" aria-live="polite" aria-atomic="true">
              {prompt.user_code}
            </strong>
          )}
          <div class="actions">
            {prompt.kind === "device" && prompt.user_code && (
              <CopyButton value={prompt.user_code}>{t("copyCode")}</CopyButton>
            )}
            {url && (
              <>
                <Button
                  onClick={() => {
                    window.open(url, "_blank", "noopener,noreferrer");
                  }}
                >
                  {t("open")}
                </Button>
                {prompt.kind === "browser" && <CopyButton value={url}>{t("copy")}</CopyButton>}
              </>
            )}
          </div>
          {deadline !== undefined && (
            <p class="muted">
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
                  {t("continue")}
                </Button>
              </div>
            </form>
          )}
        </>
      )}
      {login?.state === "pending" && !pollStopped && (
        <>
          <Notice tone="muted">{t("waiting")}</Notice>
          <div data-login-cancel="">
            <Button onClick={() => void cancel()} disabled={busy}>
              {t("cancel")}
            </Button>
          </div>
        </>
      )}
      {error && <Notice tone="danger">{p.text(error)}</Notice>}
      {pollError && <Notice tone="danger">{p.text(pollError)}</Notice>}
      {terminalFailure && (
        <>
          <Notice tone="danger">
            {other
              ? t("differentAccount", { provider: providerName(props.extension) })
              : pollStopped?.repeated
                ? t("pollStopped", { reason: p.text(reason) ?? t("noReason") })
                : t("failed", { reason: p.text(reason) ?? t("noReason") })}
          </Notice>
          {other && reason && (
            <details class="login-details" data-part="login-details">
              <summary>{t("details")}</summary>
              <pre class="mono">{p.text(reason)}</pre>
            </details>
          )}
          <div>
            <Button onClick={props.onReset}>{t("tryAgain")}</Button>
          </div>
        </>
      )}
    </section>
  );
}

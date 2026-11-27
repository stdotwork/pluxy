// The address of a pluxy that is already running, wherever it is: the form the first launch screen
// and Where pluxy runs share.

import { useId, useState } from "preact/hooks";
import { Button, Notice } from "../../ui/kit/ui";
import { addressKind, FIRST, FORM, linkOf, plainError, plainReason } from "./connectionCopy";
import { type Connection, canChooseFiles, certificateChoose, keyEntry } from "./host";

function Field(props: {
  label: string;
  hint?: string;
  bad?: boolean;
  value: string;
  disabled?: boolean;
  onInput: (v: string) => void;
}) {
  const id = useId();
  return (
    <div class="field" data-part="field">
      <label class="label" for={id}>
        {props.label}
      </label>
      <input
        id={id}
        type="text"
        value={props.value}
        disabled={props.disabled}
        autocomplete="off"
        spellcheck={false}
        aria-describedby={props.hint ? `${id}-hint` : undefined}
        onInput={(e) => props.onInput(e.currentTarget.value)}
      />
      {props.hint && (
        <small class="hint" id={`${id}-hint`} data-bad={props.bad ? "" : undefined}>
          {props.hint}
        </small>
      )}
    </div>
  );
}

/** The origin of an address, as the native side keeps it, or undefined when it is none. */
function originOf(address?: string): string | undefined {
  try {
    return address ? new URL(address.trim()).origin : undefined;
  } catch {
    return undefined;
  }
}

/**
 * The address of a pluxy that is already running, wherever it is: the address decides whether
 * the app holds it as one on this computer or on another (addressKind), and the native key window
 * asks for the management key, so no key is ever typed here. Plain http to another computer that
 * is not a Tailscale address says who could read what is sent, and goes only once the person
 * ticks that they trust the network. A failure shows one plain line above the buttons and keeps
 * what was typed. onChange gets the connection once the key is accepted; while the form holds the
 * address the app is connected to, its button says so and does nothing.
 */
export function ConnectForm(props: {
  connection: Connection;
  onChange: (c: Connection) => void;
  submit: string;
  back?: () => void;
}) {
  const c = props.connection;
  const [address, setAddress] = useState(c.mode === "managed" ? "" : (c.url ?? ""));
  const [certificate, setCertificate] = useState(c.mode === "remote" ? (c.ca_file ?? "") : "");
  // The native side holds a plain http address of another computer only once it was trusted.
  const [trusted, setTrusted] = useState(c.mode === "remote" && linkOf(c.url) === "plain");
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<string>();
  const trustId = useId();
  const secure = /^https:/i.test(address.trim());
  // A certificate file belongs to an https address only; with another address it is not sent.
  const file = secure ? certificate.trim() : "";
  const kind = addressKind(address, file, trusted);
  // The form holds what the app is connected to now: there is nothing to connect.
  const current =
    c.signed_in &&
    !!c.target &&
    c.mode === kind.mode &&
    originOf(address) !== undefined &&
    originOf(address) === originOf(c.url) &&
    (file || undefined) === (c.mode === "remote" ? c.ca_file || undefined : undefined);
  async function choose() {
    setProblem(undefined);
    try {
      const path = await certificateChoose();
      if (path) setCertificate(path);
    } catch (e) {
      setProblem(plainError(String(e)));
    }
  }
  async function submit() {
    if (!kind.ok || !kind.mode || busy || current) return;
    setBusy(true);
    setProblem(undefined);
    try {
      const result = await keyEntry({
        mode: kind.mode,
        url: address.trim(),
        ca_file: kind.mode === "remote" && file ? file : undefined,
        trust_network: kind.needsTrust ? true : undefined,
      });
      if (result.status === "signed_in") props.onChange(result.connection);
      else if (result.status === "wrong_key")
        setProblem(plainReason("This management key was not accepted."));
      else if (result.status === "error")
        setProblem(
          plainError(result.connection.error, file) ??
            plainReason("Could not reach pluxy, or could not read its reply."),
        );
    } catch (e) {
      setProblem(plainError(String(e), file));
    } finally {
      setBusy(false);
    }
  }
  return (
    <form
      class="connect-form"
      aria-label={FIRST.there.title}
      onSubmit={(e) => {
        e.preventDefault();
        void submit();
      }}
    >
      <Field
        label={FORM.address}
        value={address}
        disabled={busy}
        hint={kind.hint}
        bad={!!address.trim() && !kind.ok && !kind.needsTrust}
        onInput={setAddress}
      />
      {kind.needsTrust && (
        <div class="connect-trust" data-part="trust">
          <Notice tone="warn">{FORM.plainHttp}</Notice>
          <label class="check" for={trustId}>
            <input
              id={trustId}
              type="checkbox"
              checked={trusted}
              disabled={busy}
              onChange={(e) => setTrusted(e.currentTarget.checked)}
            />
            <span>{FORM.trust}</span>
          </label>
        </div>
      )}
      {secure && (
        <details class="connect-certificate" open={!!certificate.trim()}>
          <summary>{FORM.certificate}</summary>
          <Field
            label={FORM.certificateFile}
            value={certificate}
            disabled={busy}
            hint={FORM.certificateHint}
            onInput={setCertificate}
          />
          {canChooseFiles() && (
            <Button onClick={choose} disabled={busy}>
              {FORM.choose}
            </Button>
          )}
        </details>
      )}
      {problem && <Notice tone="danger">{problem}</Notice>}
      <div class="connect-actions">
        <Button type="submit" variant={current ? "quiet" : "primary"} disabled={busy || !kind.ok || current}>
          {busy ? FORM.waiting : current ? FORM.connected : props.submit}
        </Button>
        {props.back && (
          <Button onClick={props.back} disabled={busy}>
            {FORM.back}
          </Button>
        )}
      </div>
      {!current && <p class="connect-note">{FORM.note}</p>}
    </form>
  );
}

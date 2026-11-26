// The address of a pluxy that is already running, wherever it is: the form the first launch screen
// and Where pluxy runs share.

import { useId, useState } from "preact/hooks";
import { Button, Notice } from "../../ui/kit/ui";
import { addressKind, FIRST, FORM, plainError, plainReason } from "./connectionCopy";
import { type Connection, keyEntry } from "./host";

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
    <label class="field" data-part="field" for={id}>
      <span class="label">{props.label}</span>
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
    </label>
  );
}

/**
 * The address of a pluxy that is already running, wherever it is: the address decides whether
 * the app holds it as one on this computer or on another (addressKind), and the native key window
 * asks for the management key, so no key is ever typed here. A failure shows one plain line above
 * the buttons and keeps what was typed. onChange gets the connection once the key is accepted.
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
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<string>();
  const secure = /^https:/i.test(address.trim());
  // A certificate file belongs to an https address only; with another address it is not sent.
  const file = secure ? certificate.trim() : "";
  const kind = addressKind(address, file);
  async function submit() {
    if (!kind.ok || !kind.mode || busy) return;
    setBusy(true);
    setProblem(undefined);
    try {
      const result = await keyEntry({
        mode: kind.mode,
        url: address.trim(),
        ca_file: kind.mode === "remote" && file ? file : undefined,
      });
      if (result.status === "signed_in") props.onChange(result.connection);
      else if (result.status === "wrong_key") setProblem(plainReason("core refused this key"));
      else if (result.status === "error")
        setProblem(
          plainError(result.connection.error, file) ??
            plainReason("core could not be reached or its reply could not be read"),
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
        bad={!!address.trim() && !kind.ok}
        onInput={setAddress}
      />
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
        </details>
      )}
      {problem && <Notice tone="danger">{problem}</Notice>}
      <div class="connect-actions">
        <Button type="submit" variant="primary" disabled={busy || !kind.ok}>
          {busy ? FORM.waiting : props.submit}
        </Button>
        {props.back && (
          <Button onClick={props.back} disabled={busy}>
            {FORM.back}
          </Button>
        )}
      </div>
      <p class="connect-note">{FORM.note}</p>
    </form>
  );
}

// The address of a pluxy that is already running, wherever it is: the fields the first launch
// screen and Where pluxy runs share, and Where pluxy runs' form around them.

import type { ComponentChildren } from "preact";
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

/** What the address form holds: its fields, and sending them on. */
export interface Connect {
  /** The address field, and for an https address the certificate file under a disclosure. */
  fields: ComponentChildren;
  /** Sends the address to the native key window; does nothing until the address is usable. */
  submit: () => Promise<void>;
  /** The address is usable. */
  ready: boolean;
  /** The key window is open. */
  busy: boolean;
  /** Why the last try failed, in plain words. */
  problem?: string;
}

/**
 * The address of a pluxy that is already running, wherever it is: the address decides whether
 * the app holds it as one on this computer or on another (addressKind), and the native key window
 * asks for the management key, so no key is ever typed here. A failure keeps what was typed.
 * onChange gets the connection once the key is accepted.
 */
export function useConnect(c: Connection, onChange: (c: Connection) => void): Connect {
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
      if (result.status === "signed_in") onChange(result.connection);
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
  const fields = (
    <>
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
    </>
  );
  return { fields, submit, ready: kind.ok, busy, problem };
}

/**
 * The address form with its own buttons, as Where pluxy runs shows it: a failure shows one plain
 * line above the buttons.
 */
export function ConnectForm(props: {
  connection: Connection;
  onChange: (c: Connection) => void;
  submit: string;
}) {
  const form = useConnect(props.connection, props.onChange);
  return (
    <form
      class="connect-form"
      aria-label={FIRST.there.title}
      onSubmit={(e) => {
        e.preventDefault();
        void form.submit();
      }}
    >
      {form.fields}
      {form.problem && <Notice tone="danger">{form.problem}</Notice>}
      <div class="connect-actions">
        <Button type="submit" variant="primary" disabled={form.busy || !form.ready}>
          {form.busy ? FORM.waiting : props.submit}
        </Button>
      </div>
      <p class="connect-note">{FORM.note}</p>
    </form>
  );
}

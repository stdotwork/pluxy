// The one button that fixes an account's condition, wherever the account is named: it signs in
// again, enables, clears a cooldown, or leads to the page that says what to do.

import { useState } from "preact/hooks";
import { type Account, problem } from "../../api";
import type { Reply } from "../../host";
import { useKit } from "../../ui";
import { accountHref, useCore } from "./core";
import { type Condition, FIX_LABEL, fixFor } from "./lib";
import { Btn, Confirm } from "./parts";

/**
 * The fix for an account's condition, if a button can fix it. primary: this is the screen's next
 * thing to do, so it is the one filled button. What goes wrong is said under the button.
 */
export function FixButton(props: { account: Account; c: Condition; primary?: boolean; quiet?: boolean }) {
  const { api, unauthorized } = useKit();
  const core = useCore();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const a = props.account;
  const fix = fixFor(a, props.c);
  if (!fix) return null;
  const kind = props.primary ? "primary" : props.quiet ? "quiet" : "plain";
  const name = core.naming.name(a.id);
  const run = async (call: () => Promise<Reply<unknown>>) => {
    setBusy(true);
    setError(undefined);
    const r = await call();
    setBusy(false);
    if (r.status === 401) return unauthorized();
    const why = problem(r);
    if (why) setError(core.naming.mask(why));
    else core.reload();
  };
  const label = `${FIX_LABEL[fix]}: ${name}`;
  let button = null;
  if (fix === "sign_in") {
    button = (
      <Btn kind={kind} href={accountHref(a.id, "sign-in")} label={label}>
        {FIX_LABEL[fix]}
      </Btn>
    );
  } else if (fix === "open") {
    button = (
      <Btn kind={kind} href={accountHref(a.id)} label={label}>
        {FIX_LABEL[fix]}
      </Btn>
    );
  } else if (fix === "extensions") {
    button = (
      <Btn kind={kind} href="#/extensions" label={label}>
        {FIX_LABEL[fix]}
      </Btn>
    );
  } else if (fix === "enable") {
    button = (
      <Btn kind={kind} disabled={busy} onClick={() => run(() => api.setDisabled(a.id, false))} label={label}>
        {FIX_LABEL[fix]}
      </Btn>
    );
  } else {
    button = (
      <Confirm
        kind={kind}
        ask="This lets requests reach it at once. It does not give back any quota."
        yes="Clear it"
        disabled={busy}
        label={label}
        onConfirm={() => run(() => api.resetCooldown(a.id))}
      >
        {FIX_LABEL[fix]}
      </Confirm>
    );
  }
  return (
    <span class="db-fix">
      {button}
      {error && (
        <span class="db-fix-error" role="alert">
          {error}
        </span>
      )}
    </span>
  );
}

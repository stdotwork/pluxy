// One account's details, opened from the Accounts list (accounts.tsx). The list passes
// AccountDetailProps and nothing else; it uses AccountState for its own state column.

import { useState } from "preact/hooks";
import { type Account, leftOut, providerMissing } from "../api";
import { accountIdentities, usePrivacy } from "../privacy";
import { rich, strings } from "../strings";
import {
  Badge,
  Button,
  ConfirmButton,
  Detail,
  Facts,
  Notice,
  StateBadge,
  useAction,
  useKit,
  when,
} from "../ui";
import { AccountEdit, AccountSettings } from "./accountSettings";
import { AccountTools } from "./accountTools";
import { credentialTools } from "./accountTools.helpers";

const t = strings("accountDetail", {
  state: "State",
  extension: "Extension",
  label: "Label",
  credential: "Type",
  comesFrom: "Comes from",
  fromConfig: "AI providers",
  fromLogin: "Auth files",
  needsLogin: "Sign-in required because",
  expires: "Expires",
  renewalFailed: "Last renewal failed",
  added: "Added",
  changed: "Changed",
  setInConfig: "This account is set in core's config; change it there.",
  enable: "Enable",
  disable: "Disable",
  remove: "Remove",
  noProvider: "No provider",
  noProviderHint:
    "{extension} is not running, so this account gets no requests. Install or enable it on Extensions.",
  leftOutBecause: "Left out because",
  leftOutUntil: "Left out until",
  bannedHint: "pluxy skips this account until you enable it again.",
  brokenHint:
    "pluxy cannot read this account's auth file, so it sends no requests. Fix or replace the file and restart pluxy, or remove the account (which deletes the file).",
  disabledHint: "This account is disabled, so it gets no requests. Enable it to use it again.",
  exhaustedHint: "This account's limit is used up, so it gets no requests until the limit resets.",
  exhaustedUntilHint:
    "This account's limit is used up, so it gets no requests until the limit resets ({time}).",
  signInHint: "The provider refused this account's sign-in, so it gets no requests. Sign in again to use it.",
  signInConfigHint:
    "The provider refused this account's credential, so it gets no requests. Replace it in core's config.",
  signIn: "Sign in again",
});

/**
 * An account's state as a badge: Disabled when it is turned off, the health state that keeps
 * requests away from it when there is one, else its state.
 */
export function AccountState(props: { a: Account; running?: ReadonlySet<string> }) {
  if (props.a.disabled) return <StateBadge state="disabled" of="account" />;
  const out = leftOut(props.a);
  if (out) return <StateBadge state={out} of="account" />;
  if (providerMissing(props.a, props.running)) return <Badge tone="warn">{t("noProvider")}</Badge>;
  return <StateBadge state={props.a.state} of="account" />;
}

export interface AccountDetailProps {
  account: Account;
  /** The extensions core runs, when known: an account of another one is shown as having no provider. */
  running?: ReadonlySet<string>;
  /** Closes the details. */
  onClose: () => void;
  /** Called after the account changed or was removed: the list loads the accounts again. */
  onChanged: () => void;
}

export function AccountDetail(props: AccountDetailProps) {
  const { api } = useKit();
  const p = usePrivacy();
  const a = props.account;
  const ids = accountIdentities(a);
  const act = useAction(props.onChanged);
  const remove = useAction(() => {
    props.onClose();
    props.onChanged();
  });
  const out = leftOut(a);
  // Enabling is what lifts a provider's ban, so a banned account offers Enable, not Disable.
  const off = a.disabled || out === "banned";
  // The sign-in form is in the tools below; the next step above opens it too.
  const [signInFor, setSignInFor] = useState<string>();
  const needsSignIn = !a.disabled && (a.state === "reauth_required" || out === "needs_login");
  const canSignIn = credentialTools(a).signIn;
  return (
    <Detail title={p.text(a.id, ids)} onClose={props.onClose}>
      <Facts
        rows={[
          [t("state"), <AccountState a={a} running={props.running} />],
          [t("extension"), a.extension && <span class="mono">{a.extension}</span>],
          [t("label"), p.id(a.label)],
          [t("credential"), a.kind],
          [t("comesFrom"), a.source === "config" ? t("fromConfig") : t("fromLogin")],
          [t("needsLogin"), p.text(a.reason, ids)],
          [t("leftOutBecause"), out && p.text(a.health?.why, ids)],
          [t("leftOutUntil"), out && when(a.health?.until)],
          [t("expires"), when(a.expires_at)],
          [t("renewalFailed"), p.text(a.renewal_error?.message, ids)],
          [t("added"), when(a.created_at)],
          [t("changed"), when(a.updated_at)],
        ]}
      />
      {providerMissing(a, props.running) && (
        <Notice tone="warn">{t("noProviderHint", { extension: a.extension ?? "" })}</Notice>
      )}
      {out === "banned" && <Notice tone="warn">{t("bannedHint")}</Notice>}
      {a.disabled && out !== "broken" && <Notice tone="muted">{t("disabledHint")}</Notice>}
      {out === "exhausted" && (
        <Notice tone="warn">
          {a.health?.until
            ? rich(t("exhaustedUntilHint"), { time: when(a.health.until) })
            : t("exhaustedHint")}
        </Notice>
      )}
      {needsSignIn && (
        <Notice tone="warn">
          {t(a.source === "config" ? "signInConfigHint" : "signInHint")}{" "}
          {canSignIn && <Button onClick={() => setSignInFor(a.id)}>{t("signIn")}</Button>}
        </Notice>
      )}
      {out === "broken" && <Notice tone="warn">{t("brokenHint")}</Notice>}
      {(act.error ?? remove.error) && <Notice tone="danger">{p.text(act.error ?? remove.error, ids)}</Notice>}
      {a.source === "config" ? (
        <>
          <p class="muted">{t("setInConfig")}</p>
          {/* Core lifts a ban from a config account too; nothing else about it changes here. */}
          {out === "banned" && (
            <div class="actions">
              <Button disabled={act.busy} onClick={() => act.run(() => api.setDisabled(a.id, false))}>
                {t("enable")}
              </Button>
            </div>
          )}
        </>
      ) : (
        <div class="actions">
          {/* Core refuses every change to a broken account but its removal. */}
          {out !== "broken" && (
            <>
              <AccountEdit account={a} onChanged={props.onChanged} disabled={act.busy || remove.busy} />
              <Button disabled={act.busy} onClick={() => act.run(() => api.setDisabled(a.id, !off))}>
                {off ? t("enable") : t("disable")}
              </Button>
            </>
          )}
          <ConfirmButton
            disabled={act.busy || remove.busy}
            onConfirm={() => remove.run(() => api.removeAccount(a.id))}
          >
            {t("remove")}
          </ConfirmButton>
        </div>
      )}
      {out !== "broken" && (
        <>
          <AccountSettings account={a} />
          <AccountTools
            key={a.id}
            account={a}
            onChanged={props.onChanged}
            signIn={{ open: signInFor === a.id, set: (open) => setSignInFor(open ? a.id : undefined) }}
          />
        </>
      )}
    </Detail>
  );
}

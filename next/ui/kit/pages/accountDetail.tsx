// One account's details, opened from the Accounts list (accounts.tsx). The list passes
// AccountDetailProps and nothing else; it uses AccountState for its own state column.

import { type Account, leftOut } from "../api";
import { accountIdentities, usePrivacy } from "../privacy";
import { strings } from "../strings";
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

const t = strings("accountDetail", {
  disabled: "disabled",
  state: "State",
  extension: "Extension",
  label: "Label",
  credential: "Credential",
  comesFrom: "Comes from",
  fromConfig: "core's config",
  fromLogin: "a login",
  needsLogin: "Needs login because",
  expires: "Expires",
  renewalFailed: "Last renewal failed",
  added: "Added",
  changed: "Changed",
  setInConfig: "This account is set in core's config; change it there.",
  enable: "Enable",
  disable: "Disable",
  remove: "Remove",
  banned: "banned",
  exhausted: "exhausted",
  needs_login: "needs login",
  leftOutBecause: "Left out because",
  leftOutUntil: "Left out until",
  bannedHint: "No request is sent to this account until you enable it again.",
});

/**
 * An account's state as a badge: "disabled" when it is turned off, the health state that keeps
 * requests away from it when there is one, else its state.
 */
export function AccountState(props: { a: Account }) {
  if (props.a.disabled) return <Badge tone="muted">{t("disabled")}</Badge>;
  const out = leftOut(props.a);
  if (out) return <Badge tone={out === "exhausted" ? "warn" : "danger"}>{t(out)}</Badge>;
  return <StateBadge state={props.a.state} />;
}

export interface AccountDetailProps {
  account: Account;
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
  return (
    <Detail title={p.text(a.id, ids)} onClose={props.onClose}>
      <Facts
        rows={[
          [t("state"), <AccountState a={a} />],
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
      {out === "banned" && <Notice tone="warn">{t("bannedHint")}</Notice>}
      {(act.error ?? remove.error) && <Notice tone="danger">{p.text(act.error ?? remove.error, ids)}</Notice>}
      {a.source === "config" ? (
        <p class="muted">{t("setInConfig")}</p>
      ) : (
        <div class="actions">
          <AccountEdit account={a} onChanged={props.onChanged} disabled={act.busy || remove.busy} />
          <Button disabled={act.busy} onClick={() => act.run(() => api.setDisabled(a.id, !off))}>
            {off ? t("enable") : t("disable")}
          </Button>
          <ConfirmButton
            disabled={act.busy || remove.busy}
            onConfirm={() => remove.run(() => api.removeAccount(a.id))}
          >
            {t("remove")}
          </ConfirmButton>
        </div>
      )}
      <AccountSettings account={a} />
      <AccountTools key={a.id} account={a} onChanged={props.onChanged} />
    </Detail>
  );
}

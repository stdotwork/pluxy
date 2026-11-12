// One account's details, opened from the Accounts list (accounts.tsx). The list passes
// AccountDetailProps and nothing else; it uses AccountState for its own state column.

import type { Account } from "../api";
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
});

/** An account's state as a badge: "disabled" when it is turned off, else its state. */
export function AccountState(props: { a: Account }) {
  return props.a.disabled ? (
    <Badge tone="muted">{t("disabled")}</Badge>
  ) : (
    <StateBadge state={props.a.state} />
  );
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
  const a = props.account;
  const act = useAction(props.onChanged);
  const remove = useAction(() => {
    props.onClose();
    props.onChanged();
  });
  return (
    <Detail title={a.id} onClose={props.onClose}>
      <Facts
        rows={[
          [t("state"), <AccountState a={a} />],
          [t("extension"), a.extension && <span class="mono">{a.extension}</span>],
          [t("label"), a.label],
          [t("credential"), a.kind],
          [t("comesFrom"), a.source === "config" ? t("fromConfig") : t("fromLogin")],
          [t("needsLogin"), a.reason],
          [t("expires"), when(a.expires_at)],
          [t("renewalFailed"), a.renewal_error?.message],
          [t("added"), when(a.created_at)],
          [t("changed"), when(a.updated_at)],
        ]}
      />
      {(act.error ?? remove.error) && <Notice tone="danger">{act.error ?? remove.error}</Notice>}
      {a.source === "config" ? (
        <p class="muted">{t("setInConfig")}</p>
      ) : (
        <div class="actions">
          <AccountEdit account={a} onChanged={props.onChanged} disabled={act.busy || remove.busy} />
          <Button disabled={act.busy} onClick={() => act.run(() => api.setDisabled(a.id, !a.disabled))}>
            {a.disabled ? t("enable") : t("disable")}
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

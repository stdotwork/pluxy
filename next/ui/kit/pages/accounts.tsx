import { useState } from "preact/hooks";
import type { Account } from "../api";
import {
  Badge,
  Button,
  ConfirmButton,
  Detail,
  Empty,
  Facts,
  List,
  MasterDetail,
  Notice,
  PageHead,
  StateBadge,
  useAction,
  useKit,
  useLoad,
  when,
} from "../ui";

function AccountState(props: { a: Account }) {
  return props.a.disabled ? <Badge tone="muted">disabled</Badge> : <StateBadge state={props.a.state} />;
}

function AccountDetail(props: { a: Account; onClose: () => void; onChanged: () => void }) {
  const { api } = useKit();
  const { a } = props;
  const act = useAction(props.onChanged);
  const remove = useAction(() => {
    props.onClose();
    props.onChanged();
  });
  return (
    <Detail title={a.id} onClose={props.onClose}>
      <Facts
        rows={[
          ["State", <AccountState a={a} />],
          ["Extension", a.extension && <span class="mono">{a.extension}</span>],
          ["Label", a.label],
          ["Credential", a.kind],
          ["Comes from", a.source === "config" ? "core's config" : "a login"],
          ["Needs login because", a.reason],
          ["Expires", when(a.expires_at)],
          ["Last renewal failed", a.renewal_error?.message],
          ["Added", when(a.created_at)],
          ["Changed", when(a.updated_at)],
        ]}
      />
      {(act.error ?? remove.error) && <Notice tone="danger">{act.error ?? remove.error}</Notice>}
      {a.source === "config" ? (
        <p class="muted">This account is set in core's config; change it there.</p>
      ) : (
        <div class="actions">
          <Button disabled={act.busy} onClick={() => act.run(() => api.setDisabled(a.id, !a.disabled))}>
            {a.disabled ? "Enable" : "Disable"}
          </Button>
          {a.refreshable && (
            <Button disabled={act.busy} onClick={() => act.run(() => api.refresh(a.id))}>
              Renew now
            </Button>
          )}
          <ConfirmButton
            disabled={act.busy || remove.busy}
            onConfirm={() => remove.run(() => api.removeAccount(a.id))}
          >
            Remove
          </ConfirmButton>
        </div>
      )}
    </Detail>
  );
}

export function AccountsPage() {
  const { api } = useKit();
  const accounts = useLoad(api.accounts);
  const [selected, setSelected] = useState<string>();
  const list = accounts.data?.accounts ?? [];
  const chosen = list.find((a) => a.id === selected);

  return (
    <>
      <PageHead
        title="Accounts"
        actions={
          <Button onClick={accounts.reload} disabled={accounts.loading}>
            Refresh
          </Button>
        }
      />
      {accounts.error && <Notice tone="danger">{accounts.error}</Notice>}
      <MasterDetail
        list={
          accounts.data && list.length === 0 ? (
            <Empty>
              No accounts yet. Add them in core's config, or through a provider extension's login.
            </Empty>
          ) : (
            <List<Account>
              label="Accounts"
              items={list}
              id={(a) => a.id}
              selected={selected}
              onSelect={setSelected}
              columns={[
                { label: "Account", value: (a) => a.id, mono: true },
                { label: "Extension", value: (a) => a.extension ?? "", mono: true },
                { label: "State", value: (a) => <AccountState a={a} /> },
                { label: "Label", value: (a) => a.label ?? "" },
                { label: "Credential", value: (a) => a.kind ?? "" },
                { label: "From", value: (a) => (a.source === "config" ? "config" : "login") },
              ]}
            />
          )
        }
        detail={
          chosen && (
            <AccountDetail a={chosen} onClose={() => setSelected(undefined)} onChanged={accounts.reload} />
          )
        }
      />
    </>
  );
}

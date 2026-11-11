// The Accounts page: the list of accounts. An account's details are accountDetail.tsx.

import { useState } from "preact/hooks";
import type { Account } from "../api";
import type { PageDef } from "../shell";
import { strings } from "../strings";
import { Button, Empty, List, MasterDetail, Notice, PageHead, useKit, useLoad } from "../ui";
import { AccountDetail, AccountState } from "./accountDetail";

const t = strings("accounts", {
  title: "Accounts",
  short: "Accts",
  tip: "Auth files & credentials",
  refresh: "Refresh",
  empty: "No accounts yet. Add them in core's config, or through a provider extension's login.",
  list: "Accounts",
  account: "Account",
  extension: "Extension",
  state: "State",
  label: "Label",
  credential: "Credential",
  from: "From",
  fromConfig: "config",
  fromLogin: "login",
});

export function AccountsPage() {
  const { api } = useKit();
  const accounts = useLoad(api.accounts);
  const [selected, setSelected] = useState<string>();
  const list = accounts.data?.accounts ?? [];
  const chosen = list.find((a) => a.id === selected);

  return (
    <>
      <PageHead
        title={t("title")}
        actions={
          <Button onClick={accounts.reload} disabled={accounts.loading}>
            {t("refresh")}
          </Button>
        }
      />
      {accounts.error && <Notice tone="danger">{accounts.error}</Notice>}
      <MasterDetail
        list={
          accounts.data && list.length === 0 ? (
            <Empty>{t("empty")}</Empty>
          ) : (
            <List<Account>
              label={t("list")}
              items={list}
              id={(a) => a.id}
              selected={selected}
              onSelect={setSelected}
              columns={[
                { label: t("account"), value: (a) => a.id, mono: true },
                { label: t("extension"), value: (a) => a.extension ?? "", mono: true },
                { label: t("state"), value: (a) => <AccountState a={a} /> },
                { label: t("label"), value: (a) => a.label ?? "" },
                { label: t("credential"), value: (a) => a.kind ?? "" },
                {
                  label: t("from"),
                  value: (a) => (a.source === "config" ? t("fromConfig") : t("fromLogin")),
                },
              ]}
            />
          )
        }
        detail={
          chosen && (
            <AccountDetail
              account={chosen}
              onClose={() => setSelected(undefined)}
              onChanged={accounts.reload}
            />
          )
        }
      />
    </>
  );
}

export const accountsPage = (): PageDef => ({
  id: "accounts",
  group: "gateway",
  title: t("title"),
  short: t("short"),
  tip: t("tip"),
  render: () => <AccountsPage />,
});

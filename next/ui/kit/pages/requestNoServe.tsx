// What an open request that no account served says to do: which accounts serve which models, and
// the two ways to make the tool's request work: the tool's own model setting, or a model alias on
// an account. A coding tool asks for a model of its own choosing until it is told which one, and
// an account of another service serves none of those.

import type { ServedModel } from "../api";
import { accountHref } from "../parts";
import { rich, strings } from "../strings";
import { useKit, useLoad } from "../ui";

const t = strings("requests.noServe", {
  loading: "Reading the models pluxy serves…",
  serves: "These accounts serve:",
  one: "{account}: {models}",
  more: "{models} and {count} more",
  addAlias: "Add an alias",
  addAliasFor: "Add an alias on {account}",
  openAccount: "Open {account}",
  fix: "To make this request work, set the tool's model to one of these ({tools} has the text for each tool), or add a model alias to an account: an alias makes {model} go to one of that account's models.",
  tools: "Tools",
  none: "pluxy serves no models yet. Add an account, and then set the tool's model to one of its models.",
  addAccount: "Add an account",
});

/** How many models one account's line names before it says how many more. */
const NAMED = 5;

/** The models each account serves, in the order accounts first appear. */
export function modelsByAccount(models: ServedModel[]): { account: string; models: string[] }[] {
  const by = new Map<string, string[]>();
  for (const m of models) {
    for (const account of m.accounts) {
      const list = by.get(account) ?? [];
      list.push(m.id);
      by.set(account, list);
    }
  }
  return [...by].map(([account, list]) => ({ account, models: list }));
}

/** The address of an account's page with its model alias form open and the alias filled in. */
export const aliasHref = (accountId: string, alias: string) =>
  `${accountHref(accountId)}?alias=${encodeURIComponent(alias)}`;

/**
 * The help under "No account serves {model}". name turns an account's ID or public ID into its
 * name, idOf into the ID its page is at. editable says whether pluxy can change the account (an
 * account set in the settings file has its aliases there): one that cannot only links to its page.
 */
export function NoServeHelp(props: {
  model: string;
  name: (account: string) => string;
  idOf: (account: string) => string;
  editable: (account: string) => boolean;
}) {
  const { api } = useKit();
  const served = useLoad(api.servedModels);
  if (served.phase === "loading") return <p class="muted">{t("loading")}</p>;
  const rows = modelsByAccount(served.data?.models ?? []);
  if (served.data && rows.length === 0) {
    return (
      <p class="request-next">
        {t("none")} <a href="#/add-account">{t("addAccount")}</a>
      </p>
    );
  }
  return (
    <>
      {rows.length > 0 && (
        <>
          <p>{t("serves")}</p>
          <ul class="request-left-out">
            {rows.map((r) => {
              const names = r.models.slice(0, NAMED).join(", ");
              const models =
                r.models.length > NAMED
                  ? t("more", { models: names, count: r.models.length - NAMED })
                  : names;
              return (
                <li key={r.account}>
                  {t("one", { account: props.name(r.account), models })}{" "}
                  {props.editable(r.account) ? (
                    <a href={aliasHref(props.idOf(r.account), props.model)}>
                      {rows.length > 1 ? t("addAliasFor", { account: props.name(r.account) }) : t("addAlias")}
                    </a>
                  ) : (
                    <a href={accountHref(props.idOf(r.account))}>
                      {t("openAccount", { account: props.name(r.account) })}
                    </a>
                  )}
                </li>
              );
            })}
          </ul>
        </>
      )}
      <p class="request-next">
        {rich(t("fix", { model: props.model }), { tools: <a href="#/tools">{t("tools")}</a> })}
      </p>
    </>
  );
}

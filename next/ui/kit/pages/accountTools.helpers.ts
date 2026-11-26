import type { Account } from "../api";

/** Config accounts can only change credentials through core's config. */
export function credentialTools(account: Pick<Account, "source" | "state" | "refreshable">) {
  return {
    refresh: account.source === "login" && account.refreshable,
    signIn: account.source === "login" && account.state === "reauth_required",
  };
}

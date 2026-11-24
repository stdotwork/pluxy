// What direction A's screens share about accounts: the accounts with how each stands now, their
// usage windows, the names requests are shown under, and the one-press fix for a stuck account.

import { useMemo, useState } from "preact/hooks";
import { type Account, problem, runningExtensions } from "../../api";
import { accountIdentities, usePrivacy } from "../../privacy";
import { useKit } from "../../ui";
import { type Polled, useNow, usePoll, useWindows, type Windows } from "./data";
import { FIX_LABEL, type Fix, type Standing, standingOf } from "./lib";
import { Btn, ConfirmBtn } from "./parts";

export interface Row {
  account: Account;
  standing: Standing;
  /** The account's name as shown: its ID, masked in privacy mode. */
  name: string;
  /** Its label (often an e-mail address), masked in privacy mode. */
  label?: string;
}

export interface Fleet {
  accounts: Polled<{ accounts: Account[] }>;
  rows: Row[];
  now: number;
  windows: ReadonlyMap<string, Windows>;
  pendingWindows: ReadonlySet<string>;
  refreshWindows(ids?: string[]): void;
  /** The name to show for an account a request names by its public ID. */
  nameOf(publicId: string | undefined): string;
  /** The account a request names by its public ID, when core still has it. */
  byPublicId(publicId: string | undefined): Account | undefined;
  reload(): void;
}

/** How many accounts' windows a screen reads by itself; past that the person asks (r). */
const AUTO_WINDOWS = 48;

/** The accounts, read again every 15 s, with the clock that drives their countdowns. */
export function useFleet(): Fleet {
  const { api } = useKit();
  const p = usePrivacy();
  const accounts = usePoll(api.accounts, 15_000);
  const status = usePoll(api.status, 60_000);
  const now = useNow(1000);
  const list = accounts.data?.accounts;
  const running = useMemo(() => runningExtensions(status.data), [status.data]);
  const auto = useMemo(() => list?.slice(0, AUTO_WINDOWS), [list]);
  const { windows, pending, refresh } = useWindows(auto);
  const rows = useMemo(
    () =>
      (list ?? []).map((account) => {
        const ids = accountIdentities(account);
        return {
          account,
          standing: standingOf(account, now, running),
          name: p.text(account.id, ids),
          label: p.id(account.label),
        };
      }),
    [list, now, running, p],
  );
  const byPublic = useMemo(() => {
    const map = new Map<string, Row>();
    for (const r of rows) if (r.account.public_id) map.set(r.account.public_id, r);
    return map;
  }, [rows]);
  return {
    accounts,
    rows,
    now,
    windows,
    pendingWindows: pending,
    refreshWindows: refresh,
    nameOf: (id) => (id ? (byPublic.get(id)?.name ?? p.id(id)) : "–"),
    byPublicId: (id) => (id ? byPublic.get(id)?.account : undefined),
    reload: accounts.reload,
  };
}

/** Runs an account's fix and says why when core refuses. */
export function useFix(done: () => void) {
  const { api, unauthorized } = useKit();
  const [busy, setBusy] = useState<string>();
  const [error, setError] = useState<string>();
  const run = async (a: Account, fix: Fix) => {
    setBusy(a.id);
    setError(undefined);
    const reply =
      fix === "clear-cooldown"
        ? await api.resetCooldown(a.id)
        : fix === "enable"
          ? await api.setDisabled(a.id, false)
          : await api.removeAccount(a.id);
    setBusy(undefined);
    if (reply.status === 401) return unauthorized();
    const why = problem(reply);
    if (why) setError(`${FIX_LABEL[fix]} did not work on ${a.id}: ${why}`);
    done();
  };
  return { busy, error, run, clearError: () => setError(undefined) };
}

/**
 * The one action that gets a stuck account serving again, next to the account it acts on.
 * Signing in and a missing provider are places to go; the others act here, the ones that cannot
 * be taken back after a second press.
 */
export function FixButton(props: {
  row: Row;
  fix: ReturnType<typeof useFix>;
  primary?: boolean;
  small?: boolean;
  /** On the account's own detail, signing in is done here, not by going there. */
  onSignIn?: () => void;
}) {
  const { account, standing } = props.row;
  const fix = standing.fix;
  if (!fix) return null;
  const variant = props.primary ? "primary" : "plain";
  const busy = props.fix.busy === account.id;
  if (fix === "sign-in") {
    if (props.onSignIn)
      return (
        <Btn variant={variant} small={props.small} icon="signIn" onClick={props.onSignIn}>
          {FIX_LABEL[fix]}
        </Btn>
      );
    return (
      <Btn
        variant={variant}
        small={props.small}
        icon="signIn"
        href={`#/accounts/${encodeURIComponent(account.id)}/sign-in`}
      >
        {FIX_LABEL[fix]}
      </Btn>
    );
  }
  if (fix === "extensions")
    return (
      <Btn variant={variant} small={props.small} href="#/extensions">
        {FIX_LABEL[fix]}
      </Btn>
    );
  if (fix === "enable")
    return (
      <Btn
        variant={variant}
        small={props.small}
        icon="power"
        disabled={busy}
        onClick={() => props.fix.run(account, fix)}
      >
        {FIX_LABEL[fix]}
      </Btn>
    );
  return (
    <ConfirmBtn
      variant={fix === "remove" ? "danger" : "plain"}
      small={props.small}
      icon={fix === "remove" ? "trash" : "cooling"}
      disabled={busy}
      ask={fix === "remove" ? "Remove for good?" : "Clear now?"}
      onConfirm={() => props.fix.run(account, fix)}
    >
      {FIX_LABEL[fix]}
    </ConfirmBtn>
  );
}

/** Only the names: what a screen that lists requests needs of the accounts, without their windows. */
export function useAccountNames(): {
  nameOf(publicId: string | undefined): string;
  idOf(publicId: string | undefined): string | undefined;
} {
  const { api } = useKit();
  const p = usePrivacy();
  const accounts = usePoll(api.accounts, 60_000);
  const byPublic = useMemo(() => {
    const map = new Map<string, Account>();
    for (const a of accounts.data?.accounts ?? []) if (a.public_id) map.set(a.public_id, a);
    return map;
  }, [accounts.data]);
  return {
    nameOf: (id) => {
      if (!id) return "–";
      const a = byPublic.get(id);
      return a ? p.text(a.id, accountIdentities(a)) : p.id(id);
    },
    idOf: (id) => (id ? byPublic.get(id)?.id : undefined),
  };
}

// Setup's step 1, Your AI accounts: which kinds of account the person has (each a provider pluxy
// adds), then signing in to one through Add an account's own form.

import type { ComponentChildren } from "preact";
import { useState } from "preact/hooks";
import type { Account, Package, Setup, SetupPick } from "../api";
import { accountCondition } from "../parts";
import { stateText } from "../states";
import { strings } from "../strings";
import { Badge, Button, Notice } from "../ui";
import { AddAccountPage } from "./addAccount";
import {
  type Choices,
  canServe,
  isTicked,
  joinNames,
  kindGroups,
  kindTitle,
  type PickStatus,
  pickStatuses,
  type Results,
  runningKinds,
  signInsToName,
  toInstall,
} from "./setup.helpers";

const t = strings("setupAccounts", {
  tick: "Tick the kinds of account you have.",
  added: "Added",
  addedStopped: "Added, not running",
  openExtensions: "Open Extensions",
  more: "More ({count})",
  hideMore: "Hide more",
  add: "Add {names}",
  addNothing: "Add",
  adding: "Adding {names}…",
  tickOne: "Tick at least one.",
  failed: "Could not add {title}: {error}.",
  addedList: "Added: {names}.",
  addAnother: "Add another kind",
  none: "This copy of pluxy has no kinds of account to add.",
  signIn: "{label}. Sign in below with the same account.",
  connected: { one: "{count} account connected.", other: "{count} accounts connected." },
  connectedSome: {
    one: "{count} account connected; {others} more cannot take requests.",
    other: "{count} accounts connected; {others} more cannot take requests.",
  },
  oneAccount: "1 account, {state}.",
  noneServe: "{count} accounts, none can take requests.",
  seeThem: "See them on Accounts",
  addAccount: "Add another account",
  noReason: "no reason given",
  noAccount: "No account yet.",
});

/** A quiet button that shows or hides what is under it, saying which. */
export function Toggle(props: { open: boolean; onToggle: () => void; children: ComponentChildren }) {
  return (
    <button
      type="button"
      class="btn"
      data-part="button"
      data-slot="button"
      data-variant="quiet"
      aria-expanded={props.open ? "true" : "false"}
      onClick={props.onToggle}
    >
      {props.children}
    </button>
  );
}

/**
 * The step's folded summary, and the line in it: the accounts that can take requests are
 * connected; the others are said as they are.
 */
export function accountsSummary(accounts: Account[] | undefined, running?: ReadonlySet<string>): string {
  const list = accounts ?? [];
  if (list.length === 0) return t("noAccount");
  const usable = list.filter((a) => canServe(a, running)).length;
  const others = list.length - usable;
  if (usable > 0)
    return others > 0 ? t("connectedSome", { count: usable, others }) : t("connected", { count: usable });
  const [only] = list;
  if (only && list.length === 1) {
    const kind = accountCondition(only, running).kind;
    return t("oneAccount", { state: stateText(kind === "off" ? "disabled" : kind, "account").toLowerCase() });
  }
  return t("noneServe", { count: list.length });
}

function KindRow(props: {
  pick: SetupPick;
  status: PickStatus;
  ticked: boolean;
  why?: string;
  busy: boolean;
  onTick: (ticked: boolean) => void;
}) {
  const { pick, status } = props;
  const title = kindTitle(pick);
  const why = props.why ?? pick.because;
  const added = status.state === "running" || status.state === "stopped";
  const error = status.state === "install-failed" ? status.error : undefined;
  const text = (
    <span class="setup-pick-text">
      <strong>{title}</strong>
      {pick.summary && <span class="muted">{pick.summary}</span>}
      {why && !added && <span class="muted">{why}</span>}
    </span>
  );
  return (
    <li
      class="setup-pick"
      data-part="setup-pick"
      data-state={
        status.state === "running"
          ? "added"
          : status.state === "stopped"
            ? "stopped"
            : status.state === "install-failed"
              ? "failed"
              : props.ticked
                ? "ticked"
                : "offered"
      }
    >
      {added ? (
        <div class="setup-pick-row">
          {text}
          {status.state === "running" ? (
            <Badge tone="ok">{t("added")}</Badge>
          ) : (
            <span class="setup-pick-stopped">
              <Badge tone="warn">{t("addedStopped")}</Badge>
              <a href="#/extensions">{t("openExtensions")}</a>
            </span>
          )}
        </div>
      ) : (
        <label class="setup-pick-row setup-pick-check">
          <input
            type="checkbox"
            checked={props.ticked}
            disabled={props.busy}
            onChange={(e) => props.onTick(e.currentTarget.checked)}
          />
          {text}
        </label>
      )}
      {status.state === "install-failed" && (
        <p class="setup-pick-error" role="alert">
          {t("failed", { title, error: (error ?? t("noReason")).replace(/\.$/, "") })}
        </p>
      )}
    </li>
  );
}

/** Which kinds of account to add, and Add. */
function Kinds(props: {
  setup: Setup;
  packages?: Package[];
  choices: Choices;
  results: Results;
  needed: Record<string, string>;
  adding: boolean;
  addError?: string;
  onTick: (id: string, ticked: boolean) => void;
  onAdd: () => void;
}) {
  const { setup, results } = props;
  const [more, setMore] = useState(false);
  const needed = new Set(Object.keys(props.needed));
  const groups = kindGroups(setup, needed);
  const statuses = new Map(pickStatuses(setup, results, props.packages).map((s) => [s.pick.id, s.status]));
  const ticked = (pick: SetupPick) => isTicked(pick, props.choices, results);
  const sending = toInstall(setup, props.choices, results);
  const titles = sending.map((id) => {
    const pick = setup.picks.find((p) => p.id === id);
    return pick ? kindTitle(pick) : id;
  });
  const anyAdded = [...statuses.values()].some((s) => s.state === "running" || s.state === "stopped");
  const row = (pick: SetupPick) => (
    <KindRow
      key={pick.id}
      pick={pick}
      status={statuses.get(pick.id) ?? { state: "offered" }}
      ticked={ticked(pick)}
      why={props.needed[pick.id]}
      busy={props.adding}
      onTick={(on) => props.onTick(pick.id, on)}
    />
  );
  return (
    <div class="setup-kinds">
      <p>{t("tick")}</p>
      <ul class="setup-picks">
        {groups.found.map(row)}
        {groups.main.map(row)}
        {more && groups.more.map(row)}
      </ul>
      {groups.more.length > 0 && (
        <div>
          <Toggle open={more} onToggle={() => setMore(!more)}>
            {more ? t("hideMore") : t("more", { count: groups.more.length })}
          </Toggle>
        </div>
      )}
      <div class="actions">
        <Button
          variant="primary"
          busy={props.adding}
          disabled={props.adding || sending.length === 0}
          onClick={props.onAdd}
        >
          {props.adding
            ? t("adding", { names: joinNames(titles) })
            : sending.length > 0
              ? t("add", { names: joinNames(titles) })
              : t("addNothing")}
        </Button>
        {sending.length === 0 && !anyAdded && <span class="muted">{t("tickOne")}</span>}
      </div>
      {props.addError && (
        <Notice tone="danger">
          {t("failed", { title: joinNames(titles), error: props.addError.replace(/\.$/, "") })}
        </Notice>
      )}
    </div>
  );
}

/** Step 1's open content, without its actions. */
export function AccountsStep(props: {
  setup: Setup;
  packages?: Package[];
  accounts?: Account[];
  /** The extensions core runs, for which accounts can take requests. */
  running?: ReadonlySet<string>;
  /** pluxy had accounts when the page opened: the form waits for Add another account. */
  hadAccounts: boolean;
  choices: Choices;
  results: Results;
  /** Picks an import needs, with why. */
  needed: Record<string, string>;
  adding: boolean;
  addError?: string;
  /** The account added on this page, once one was. */
  signedIn?: string;
  onTick: (id: string, ticked: boolean) => void;
  onAdd: () => void;
  onSignedIn: (id: string) => void;
}) {
  const { setup } = props;
  const [another, setAnother] = useState(false);
  const [moreKinds, setMoreKinds] = useState(false);
  const running = runningKinds(setup, props.results, props.packages, new Set(Object.keys(props.needed)));
  const failed = pickStatuses(setup, props.results, props.packages).some(
    (s) => s.status.state === "install-failed",
  );
  const offered = setup.picks.some((p) => p.kind === "provider");
  const count = props.accounts?.length ?? 0;
  const formOpen = !props.hadAccounts || another || props.signedIn !== undefined;
  const kindsOpen = running.length === 0 || moreKinds || failed || props.adding;
  const names = running.map((kind) => kind.title ?? kind.id);
  if (running.length === 0 && (!offered || setup.registries === 0)) {
    return (
      <div class="setup-kinds">
        <p>{t("none")}</p>
        <div class="actions">
          <a class="btn" data-part="button" data-variant="quiet" href="#/extensions">
            {t("openExtensions")}
          </a>
        </div>
      </div>
    );
  }
  return (
    <>
      {kindsOpen ? (
        <Kinds
          setup={setup}
          packages={props.packages}
          choices={props.choices}
          results={props.results}
          needed={props.needed}
          adding={props.adding}
          addError={props.addError}
          onTick={props.onTick}
          onAdd={() => {
            setMoreKinds(false);
            props.onAdd();
          }}
        />
      ) : (
        <p class="setup-line">
          <span>{t("addedList", { names: joinNames(names) })}</span>
          <Button onClick={() => setMoreKinds(true)}>{t("addAnother")}</Button>
        </p>
      )}
      {count > 0 && (
        <p class="setup-line">
          <span>{accountsSummary(props.accounts, props.running)}</span>
          {formOpen || running.length === 0 ? (
            <a href="#/accounts">{t("seeThem")}</a>
          ) : (
            <Button onClick={() => setAnother(true)}>{t("addAccount")}</Button>
          )}
        </p>
      )}
      {running.length > 0 && (
        <div class="setup-sign-in">
          {count === 0 &&
            signInsToName(setup, running).map((signIn) => (
              <p key={`${signIn.provider}/${signIn.from}`} class="muted">
                {t("signIn", { label: signIn.label.replace(/\.$/, "") })}
              </p>
            ))}
          {formOpen && (
            <div class="setup-embed">
              <AddAccountPage embedded providers={running} onSuccess={props.onSignedIn} />
            </div>
          )}
        </div>
      )}
    </>
  );
}

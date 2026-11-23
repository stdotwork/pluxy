// Setup: what a new install shows first. One page, read from top to bottom: what core found on
// its machine, the providers it offers, Start, an account, the clients, Finish. Each part shows
// its state, and a part whose work is done says so in one line. Opened again later, it is a
// status of the same things.

import type { ComponentChildren } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";
import {
  type ImportReport,
  type ImportRow,
  type Package,
  problem,
  type Setup,
  type SetupClient,
  type SetupImport,
  type SetupLastImport,
  type SetupPick,
} from "../api";
import type { Reply } from "../host";
import type { PageDef } from "../shell";
import { rich, strings } from "../strings";
import {
  Badge,
  Button,
  Card,
  Empty,
  Notice,
  PageHead,
  useAction,
  useCopy,
  useKit,
  useLoad,
  when,
} from "../ui";
import { AddAccountPage } from "./addAccount";
import { KeyNotice } from "./configKeys";
import {
  type Choices,
  hasProvider,
  isInstalled,
  isTicked,
  onThisMachine,
  providerPicks,
  type Results,
  type Stopped,
  scanState,
  signInsToName,
  splitClients,
  start,
  startInstalls,
  stoppedPicks,
  toInstall,
  withKey,
} from "./setup.helpers";

const t = strings("setup", {
  title: "Set up pluxy",
  short: "Setup",
  tip: "What pluxy found, and what is still missing",
  introFirst: "pluxy looks at this machine and offers what fits.",
  introUntilStart: "Nothing is installed until you press Start, and nothing on this page is required.",
  introOptional: "Nothing on this page is required.",
  introLater: "Setup is finished. This page shows the same things as they are now.",
  loading: "Loading…",
  retry: "Retry",

  found: "Found on this machine",
  foundRemote: "Found on the machine pluxy runs on",
  lookAgain: "Look again",
  looking: "Looking…",
  lookingFirst: "pluxy is looking at this machine. This takes a few seconds.",
  neverLooked: "pluxy has not looked at this machine yet.",
  nothingFound: "No client, sign-in or CLIProxyAPI setup was found.",
  clientFound: "{name} {version}",
  signInHint: "pluxy read nothing from this sign-in. It only saw that it is there.",
  importFound: {
    one: "A CLIProxyAPI setup with {count} account",
    other: "A CLIProxyAPI setup with {count} accounts",
  },
  importWhere:
    "It is at {config}. pluxy can bring its settings, client keys and accounts across. It first shows what would change, and writes nothing until you confirm.",
  importOld: "It is at {config}. To bring it across, run this where pluxy is installed:",
  importCommand: "Or use the command",
  importCommandHow:
    "Run this where pluxy is installed. It does the same, and with --dry-run only reports what it would do.",
  importDone: "Brought across",
  importDoneWhere: "It is at {config}, and was brought across on {when}.",
  importDoneCounts: "Of its settings and auth files: {counts}.",
  "importDone.brought": "{count} brought across",
  "importDone.changed": "{count} different in pluxy",
  "importDone.not_brought": "{count} not brought across",
  importAgain: "Bring it across again",
  importAgainHow:
    "Run this where pluxy is installed. It writes the file's settings over any you changed in pluxy since, and adds the client keys, accounts and chains pluxy does not have: the ones it has stay as they are. With --dry-run it only reports what it would do.",
  bring: "Bring it across",
  bringChecking: "Checking…",
  bringNow: "Bring it across now",
  bringing: "Bringing it across…",
  bringCancel: "Cancel",
  bringClose: "Close",
  bringDry: "Nothing is written yet. This is what bringing it across would do:",
  bringDone: "Done. This is what was brought across:",
  "list.brought.dry": "Comes across ({count})",
  "list.brought.done": "Brought across ({count})",
  "list.changed": "Different in pluxy ({count})",
  "list.not_brought.dry": "Does not come across ({count})",
  "list.not_brought.done": "Not brought across ({count})",
  rowAs: "Becomes",
  rowFrom: "Was",
  rowTo: "In pluxy",
  rowWhy: "Why",
  rowKeep: "To keep it",
  needPick: "{title} is not installed, and {why}.",
  needLater: "You can install it once the setup is across.",
  needTicked: "It is ticked under Providers: press {button} to install it.",
  needElsewhere: "Install it on the Extensions page.",
  "refused.400":
    "pluxy could not read the CLIProxyAPI setup: {message}. Check that the file is still there and that pluxy may read it, then press {look}. Nothing was changed.",
  "refused.422":
    "pluxy will not bring this setup across: {message}. Change that in the CLIProxyAPI setup, then try again. Nothing was changed.",
  "refused.409":
    "This pluxy runs on a config.yaml, which it already reads in CLIProxyAPI's own format, and an import writes pluxy's JSON config. To move over, make that config with the command below, start pluxy on it, then bring the setup across. Nothing was changed.",
  "refused.404": "This pluxy is an older one that cannot bring a setup across from this page.",
  "refused.0.dry":
    "pluxy did not answer, so nothing was read or changed. Check that it is running, then try again.",
  "refused.0.real":
    "pluxy did not answer, so this page cannot tell whether the import ran. Check that pluxy is running, then press {button} again: it shows what is left to bring.",
  refusedOther: "pluxy could not bring the setup across: {message}.",
  listens: "pluxy listens on {address}.",
  reachable: "Other machines on the network can reach pluxy at this address.",
  lookedAt: "Looked {when}.",

  providers: "Providers",
  providersHint: "A provider lets pluxy use one kind of account, such as a Claude subscription.",
  tickHint: "Tick the ones you have an account for.",
  noPicks: "pluxy has no provider to offer here. The Extensions page lists what can be installed.",
  noRegistry:
    "This build has no extension registry, so pluxy has no provider to offer. From next/ in the source tree, run go run ./cmd/pluxy-pack -out <dir>, then put the bundled folder beside pluxy or start core with -bundled <dir>/bundled/index.json.",
  installed: "Installed",
  installedNotRunning: "Installed, not running",
  installedFailed: "{title} was installed but did not start: {error}",
  installedDisabled: "{title} is installed but turned off.",
  installedStopped: "{title} is installed but is not running.",
  installedFailedWhere: "Look at it on the Extensions page.",
  failed: "Could not install {title}: {error}",
  failedRetry: "Press {button} to try again.",

  startTitle: "Start",
  start: "Start",
  install: "Install",
  startWithout: "Start without a provider",
  willInstall: "{button} installs: {names}.",
  willInstallLater: "This installs {names}.",
  installing: "Installing…",
  nothingTicked:
    "Nothing is ticked, so pluxy will run with no provider. You can add one later on the Extensions page.",
  nothingTickedNoRegistry: "This build has no extension registry, so pluxy will run with no provider.",
  noneRunning:
    "{names} is installed but not running, so pluxy will run with no provider. You can turn it on or see what went wrong on the {extensions} page.",
  nothingMore: "Nothing more to install.",
  stillLooking: "pluxy is still looking at this machine. What it suggests shows here in a few seconds.",
  allInstalled: "Every provider on the list is installed.",

  account: "Connect an account",
  accountHint: "An account is your sign-in with a provider. pluxy sends requests through it.",
  accountAfter: "This comes after {button}: an account signs in through a provider.",
  accountTick: "This comes after a provider: tick one above and press {button}.",
  accountStopped:
    "This comes after a provider, and the one installed is not running. Look at it on the {extensions} page.",
  accountNoProvider:
    "This comes after a provider, and pluxy runs none yet. Install one on the {extensions} page.",
  accountNoRegistry:
    "This comes after a provider, and this build has no extension registry to install one from.",
  extensions: "Extensions",
  accountsConnected: { one: "{count} account connected.", other: "{count} accounts connected." },
  accountsLink: "See them on Accounts",
  accountSkip: "You can skip this and add an account later on the Add Account page.",
  signInFound: "{label}. You can sign in to the same account here.",

  clients: "Point a client at pluxy",
  clientsHint:
    "A client is a coding tool, such as Claude Code, that sends its requests through pluxy. pluxy changes no client's files from this page: it shows the text, and you paste it.",
  noClients: "No client was found on this machine.",
  "state.configured": "Points at this pluxy",
  "state.not-configured": "Does not point at pluxy yet",
  "state.needs-update": "Its settings need an update to work with this pluxy",
  "state.refused":
    "pluxy would not change {config}: it holds something pluxy does not expect. That is fine here, because you paste the text yourself.",
  notFound: "Not found on this machine",
  showPaste: "Show what to paste",
  hidePaste: "Hide",
  otherClients: { one: "Other clients ({count})", other: "Other clients ({count})" },
  pasteLoading: "Loading the text…",
  pasteInto: "Put this in {path}:",
  pasteAddress: "The text uses {address}, the address of pluxy on its own machine.",
  pasteModel:
    "Where the text says {placeholder}, a model's name goes: one your accounts serve. An account's Models button on Accounts lists them, and so does {command}.",
  copy: "Copy",
  copied: "Copied",
  copyFailed: "Could not copy. Select the text and copy it yourself.",
  keyWhere:
    "Where the text says {placeholder}, a client key goes: the password a client uses to reach pluxy. Make one here, or use one you already have.",
  makeKey: "Make a client key",
  newKey: "New client key",
  keyInText:
    "The new key is in the text above, where {placeholder} stood. Copy the text while this notice is open.",

  finishTitle: "Finish",
  finish: "Finish",
  finishHint:
    "This ends setup and opens the Dashboard. You can come back to this page at any time: it stays in the navigation.",
});

/** A button that copies text, and says so. */
function Copy(props: { value: string }) {
  const { state, copy } = useCopy(props.value);
  return (
    <>
      <Button onClick={copy}>{state === "copied" ? t("copied") : t("copy")}</Button>
      {state === "failed" && <Notice tone="danger">{t("copyFailed")}</Notice>}
    </>
  );
}

/** Text to copy, in the kit's code style, with its Copy button. */
function Code(props: { value: string }) {
  return (
    <div class="setup-code">
      <pre>{props.value}</pre>
      <div class="actions">
        <Copy value={props.value} />
      </div>
    </div>
  );
}

/** The command that does what the row's button does. */
export function importCommand(found: SetupImport): string {
  return `pluxy import cpa ${found.config}${authDir(found) ? ` --auth-dir ${authDir(found)}` : ""}`;
}

/**
 * The auth folder to name in an import: the one the scan counted accounts in. With none counted
 * the import reads the folder the config names, and says so when it is not there.
 */
function authDir(found: SetupImport): string | undefined {
  return found.accounts > 0 ? found.auth_dir : undefined;
}

/** Where bringing a found CLIProxyAPI setup across stands. */
export interface ImportState {
  /** The config of the setup this is about. */
  config: string;
  busy?: "checking" | "importing";
  /** The dry run's report, or once done the import's. */
  report?: ImportReport;
  done?: boolean;
  /** Why core refused, in words. */
  problem?: string;
  /** What the refusal's words point to: the import command, or the one that makes a JSON config. */
  show?: "command" | "init";
}

/** A refused import in words a person can act on; nothing for a reply that is no refusal. */
export function importProblem(reply: Reply<unknown>, dryRun: boolean): Pick<ImportState, "problem" | "show"> {
  if (reply.status >= 200 && reply.status < 300) return {};
  const message = (reply.error?.message ?? problem(reply) ?? "").replace(/\.$/, "");
  switch (reply.status) {
    case 0:
      return { problem: t(dryRun ? "refused.0.dry" : "refused.0.real", { button: t("bring") }) };
    case 400:
      return { problem: t("refused.400", { message, look: t("lookAgain") }) };
    case 404:
      return { problem: t("refused.404"), show: "command" };
    case 409:
      return { problem: t("refused.409"), show: "init" };
    case 422:
      return { problem: t("refused.422", { message }) };
    default:
      return { problem: t("refusedOther", { message }) };
  }
}

/** The provider picks a report says the imported accounts need, as the page can tick them. */
export function neededPicks(setup: Setup, report: ImportReport): Choices {
  const offered = new Set(providerPicks(setup).map((pick) => pick.id));
  return Object.fromEntries(
    report.needs.filter((need) => offered.has(need.pick)).map((need) => [need.pick, true]),
  );
}

function ReportList(props: { title: string; rows: ImportRow[] }) {
  if (props.rows.length === 0) return null;
  const line = (label: string, value?: string) =>
    value && (
      <span>
        <span class="muted">{label}:</span> {value}
      </span>
    );
  return (
    <details open>
      <summary>{props.title}</summary>
      <ul class="setup-report-rows">
        {props.rows.map((row) => (
          <li key={`${row.what}/${row.name ?? ""}`}>
            <strong class="mono">{[row.what, row.name].filter(Boolean).join(" ")}</strong>
            {line(t("rowAs"), row.as)}
            {line(t("rowFrom"), row.from)}
            {line(t("rowTo"), row.to)}
            {line(t("rowWhy"), row.why)}
            {line(t("rowKeep"), row.keep_with)}
          </li>
        ))}
      </ul>
    </details>
  );
}

/** An import report in its own three lists, and what the imported accounts still need. */
function Report(props: { setup: Setup; report: ImportReport; done?: boolean }) {
  const { report, setup } = props;
  const when = props.done ? "done" : "dry";
  const button = setup.first_run ? t("start") : t("install");
  return (
    <div class="setup-report stack" data-part="setup-report" data-state={when}>
      <span role="status">{props.done ? t("bringDone") : t("bringDry")}</span>
      <ReportList title={t(`list.brought.${when}`, { count: report.brought.length })} rows={report.brought} />
      <ReportList title={t("list.changed", { count: report.changed.length })} rows={report.changed} />
      <ReportList
        title={t(`list.not_brought.${when}`, { count: report.not_brought.length })}
        rows={report.not_brought}
      />
      {report.needs.map((need) => {
        const pick = providerPicks(setup).find((p) => p.id === need.pick);
        return (
          <Notice key={need.pick} tone="warn">
            {t("needPick", { title: pick?.title || need.pick, why: need.why })}{" "}
            {!props.done ? t("needLater") : pick ? t("needTicked", { button }) : t("needElsewhere")}
          </Notice>
        );
      })}
    </div>
  );
}

/** What core kept of a setup's import, in words: each list's count, a list with no row left out. */
function importCounts(last: SetupLastImport): string {
  return (["brought", "changed", "not_brought"] as const)
    .filter((list) => list === "brought" || last[list] > 0)
    .map((list) => t(`importDone.${list}`, { count: last[list] }))
    .join(", ");
}

/**
 * A found CLIProxyAPI setup: where it is, and bringing it across in two steps. Once core has a
 * record of its import the row says so and offers no button: the command can still run it again.
 */
function FoundImport(props: {
  setup: Setup;
  found: SetupImport;
  state?: ImportState;
  onBring?: (found: SetupImport, dryRun: boolean) => void;
  onClose?: () => void;
}) {
  const { found, state } = props;
  const old = state?.show === "command";
  // What this page is in the middle of comes first: its report stays until it is closed.
  const last = state ? undefined : found.last_import;
  const where = <span class="mono">{found.config}</span>;
  return (
    <li
      data-part="setup-import"
      data-state={state?.done ? "done" : state?.report ? "report" : last ? "brought" : "found"}
    >
      <div class="setup-row-head">
        <span class="setup-pick-text">
          <strong>{t("importFound", { count: found.accounts })}</strong>
          {last ? (
            <span class="muted">
              {rich(t("importDoneWhere"), { config: where, when: when(last.at) ?? last.at })}{" "}
              {t("importDoneCounts", { counts: importCounts(last) })}
            </span>
          ) : (
            <span class="muted">
              {rich(t(old || !props.onBring ? "importOld" : "importWhere"), { config: where })}
            </span>
          )}
        </span>
        {last && <Badge tone="ok">{t("importDone")}</Badge>}
        {!last && props.onBring && !old && !state?.report && (
          <div class="actions">
            <Button disabled={state?.busy !== undefined} onClick={() => props.onBring?.(found, true)}>
              {state?.busy ? t("bringChecking") : t("bring")}
            </Button>
          </div>
        )}
      </div>
      {state?.problem && <Notice tone="danger">{state.problem}</Notice>}
      {state?.show === "init" && <Code value="pluxy init" />}
      {state?.report && <Report setup={props.setup} report={state.report} done={state.done} />}
      {state?.report && !state.done && (
        <div class="actions">
          <Button disabled={state.busy !== undefined} onClick={() => props.onBring?.(found, false)}>
            {state.busy ? t("bringing") : t("bringNow")}
          </Button>
          <Button variant="quiet" disabled={state.busy !== undefined} onClick={props.onClose}>
            {t("bringCancel")}
          </Button>
        </div>
      )}
      {state?.done && (
        <div class="actions">
          <Button onClick={props.onClose}>{t("bringClose")}</Button>
        </div>
      )}
      {!last && (old || !props.onBring) ? (
        <Code value={importCommand(found)} />
      ) : (
        !state?.report && (
          <details class="setup-others">
            <summary>{t(last ? "importAgain" : "importCommand")}</summary>
            <div class="setup-code-how stack">
              <span class="muted">{t(last ? "importAgainHow" : "importCommandHow")}</span>
              <Code value={importCommand(found)} />
            </div>
          </details>
        )
      )}
    </li>
  );
}

/** What the Found part needs to bring a CLIProxyAPI setup across. */
export interface ImportProps {
  state?: ImportState;
  onBring: (found: SetupImport, dryRun: boolean) => void;
  onClose: () => void;
}

/** What core found: clients, sign-ins, an earlier setup, and where core listens. */
export function Found(props: {
  setup: Setup;
  remote: boolean;
  busy?: boolean;
  error?: string;
  bring?: ImportProps;
  onLook: () => void;
}) {
  const { setup } = props;
  const state = scanState(setup);
  const clients = splitClients(setup).found;
  const nothing = clients.length + setup.found.sign_ins.length + setup.found.imports.length === 0;
  const looking = state === "first" || state === "again";
  return (
    <Card
      title={props.remote ? t("foundRemote") : t("found")}
      actions={
        <Button onClick={props.onLook} disabled={looking || props.busy}>
          {looking ? t("looking") : t("lookAgain")}
        </Button>
      }
    >
      {props.error && <Notice tone="danger">{props.error}</Notice>}
      {state === "first" ? (
        <Notice tone="muted">{t("lookingFirst")}</Notice>
      ) : (
        <ul class="setup-rows" data-part="setup-found" aria-busy={looking}>
          {clients.map((client) => (
            <li key={`client/${client.id}`}>
              <strong>{t("clientFound", { name: client.name, version: client.version ?? "" }).trim()}</strong>
              <span class="muted">{clientWords(client)}</span>
            </li>
          ))}
          {setup.found.sign_ins.map((signIn) => (
            <li key={`sign-in/${signIn.provider}/${signIn.from}`}>
              <strong>{signIn.label}</strong>
              <span class="muted">{t("signInHint")}</span>
            </li>
          ))}
          {setup.found.imports.map((found) => (
            <FoundImport
              key={`import/${found.config}`}
              setup={setup}
              found={found}
              state={props.bring?.state?.config === found.config ? props.bring.state : undefined}
              onBring={props.bring?.onBring}
              onClose={props.bring?.onClose}
            />
          ))}
          {nothing && <li class="muted">{state === "never" ? t("neverLooked") : t("nothingFound")}</li>}
          {setup.found.core.listen && (
            <li>
              <span>
                {rich(t("listens"), { address: <span class="mono">{setup.found.core.listen}</span> })}
                {setup.found.core.reachable_from_network && ` ${t("reachable")}`}
              </span>
            </li>
          )}
        </ul>
      )}
      {setup.scanned_at && state !== "first" && (
        <small class="hint">{t("lookedAt", { when: when(setup.scanned_at) ?? setup.scanned_at })}</small>
      )}
    </Card>
  );
}

/** A client's state in words. */
function clientWords(client: SetupClient): string {
  if (client.state === "refused") return t("state.refused", { config: client.config });
  return t(`state.${client.state}`);
}

/** The provider picks as a checklist, each with its outcome beside it. */
export function Providers(props: {
  setup: Setup;
  choices: Choices;
  results: Results;
  /** The installed picks that do not run, by pick ID. */
  stopped?: Record<string, Stopped>;
  busy?: boolean;
  onTick: (id: string, ticked: boolean) => void;
}) {
  const picks = providerPicks(props.setup);
  const button = props.setup.first_run ? t("start") : t("install");
  return (
    <Card title={t("providers")}>
      {picks.length === 0 ? (
        props.setup.registries === 0 ? (
          <Notice tone="warn">{t("noRegistry")}</Notice>
        ) : (
          <Empty>{t("noPicks")}</Empty>
        )
      ) : (
        <>
          <p class="muted setup-hint">
            {t("providersHint")}{" "}
            {picks.every((pick) => isInstalled(pick, props.results)) ? t("allInstalled") : t("tickHint")}
          </p>
          <ul class="setup-rows">
            {picks.map((pick) => (
              <PickRow key={pick.id} pick={pick} button={button} {...props} />
            ))}
          </ul>
        </>
      )}
    </Card>
  );
}

function PickRow(props: {
  pick: SetupPick;
  button: string;
  choices: Choices;
  results: Results;
  stopped?: Record<string, Stopped>;
  busy?: boolean;
  onTick: (id: string, ticked: boolean) => void;
}) {
  const { pick } = props;
  const installed = isInstalled(pick, props.results);
  const result = props.results[pick.id];
  // Core keeps a package whose program did not start, and one that was turned off or has failed
  // since: an installed pick can be one that does not run.
  const stopped =
    props.stopped?.[pick.id] ??
    (installed && result?.status === "error"
      ? ({ why: "start", error: result.error } as Stopped)
      : undefined);
  const notRunning = stopped !== undefined;
  const title = pick.title || pick.id;
  return (
    <li
      data-part="setup-pick"
      data-state={installed ? "installed" : result?.status === "error" ? "failed" : "offered"}
    >
      <div class="setup-row-head">
        <label class="check">
          <input
            type="checkbox"
            checked={installed || isTicked(pick, props.choices, props.results)}
            disabled={installed || props.busy}
            onChange={(e) => props.onTick(pick.id, e.currentTarget.checked)}
          />
          <span class="setup-pick-text">
            <strong>{pick.title || pick.id}</strong>
            {pick.summary && <span class="muted">{pick.summary}</span>}
            {pick.because && !installed && <small class="hint">{pick.because}</small>}
          </span>
        </label>
        {installed && !notRunning && <Badge tone="ok">{t("installed")}</Badge>}
        {notRunning && <Badge tone="warn">{t("installedNotRunning")}</Badge>}
      </div>
      {notRunning ? (
        <Notice tone="danger">
          {stopped.why === "disabled"
            ? t("installedDisabled", { title })
            : stopped.why === "failed" && !stopped.error
              ? t("installedStopped", { title })
              : t("installedFailed", { title, error: stopped.error ?? "" })}
          <br />
          {t("installedFailedWhere")}
        </Notice>
      ) : (
        !installed &&
        result?.status === "error" && (
          <Notice tone="danger">
            {t("failed", { title: pick.title || pick.id, error: result.error ?? "" })}
            <br />
            {t("failedRetry", { button: props.button })}
          </Notice>
        )
      )}
    </li>
  );
}

/** The one primary button: it installs what is ticked, or starts with nothing. */
export function Start(props: {
  setup: Setup;
  choices: Choices;
  results: Results;
  busy?: boolean;
  error?: string;
  provider: boolean;
  stopped?: Record<string, Stopped>;
  onStart: () => void;
  onFinish: () => void;
}) {
  const { setup } = props;
  const picks = providerPicks(setup);
  const ids = toInstall(setup, props.choices, props.results);
  const names = picks.filter((pick) => ids.includes(pick.id)).map((pick) => pick.title || pick.id);
  const button = setup.first_run ? t("start") : t("install");
  let line: ComponentChildren;
  let action: ComponentChildren = null;
  if (ids.length > 0) {
    const list = names.join(", ");
    line = props.busy
      ? t("installing")
      : setup.first_run
        ? t("willInstall", { button, names: list })
        : t("willInstallLater", { names: list });
    action = (
      <Button variant="primary" disabled={props.busy} onClick={props.onStart}>
        {button}
      </Button>
    );
  } else if (!props.provider && setup.first_run && scanState(setup) === "first") {
    // What to tick comes with the first scan's end: until then an empty list is not a choice.
    line = t("stillLooking");
  } else if (!props.provider && setup.first_run) {
    // With every pick installed there is nothing to tick: what holds pluxy back is on Extensions.
    const down = picks.filter((pick) => props.stopped?.[pick.id]).map((pick) => pick.title || pick.id);
    line =
      setup.registries === 0
        ? t("nothingTickedNoRegistry")
        : down.length > 0 && picks.every((pick) => isInstalled(pick, props.results))
          ? rich(t("noneRunning"), {
              names: down.join(", "),
              extensions: <a href="#/extensions">{t("extensions")}</a>,
            })
          : t("nothingTicked");
    action = (
      <Button variant="primary" disabled={props.busy} onClick={props.onFinish}>
        {t("startWithout")}
      </Button>
    );
  } else if (props.error) {
    line = t("nothingMore");
  } else {
    // With nothing left to install the part has nothing to say: the picks say "Installed", and
    // after setup it comes back when a provider is ticked.
    return null;
  }
  return (
    <Card title={setup.first_run ? t("startTitle") : t("install")}>
      {props.error && <Notice tone="danger">{props.error}</Notice>}
      <div class="setup-line">
        <span role="status">{line}</span>
        {action}
      </div>
    </Card>
  );
}

/** The Add Account page's sign-in, once a provider is installed and core has no account. */
export function Account(props: {
  setup: Setup;
  choices: Choices;
  results: Results;
  count?: number;
  provider: boolean;
  error?: string;
  onAdded: () => void;
}) {
  const { setup } = props;
  const button = setup.first_run ? t("start") : t("install");
  // What leads to an account while core runs no provider: the button, when it would install one;
  // a tick first, when a provider is left to tick; another page, when the installed ones do not
  // run or none is offered.
  const picks = providerPicks(setup);
  const extensions = <a href="#/extensions">{t("extensions")}</a>;
  const before =
    toInstall(setup, props.choices, props.results).length > 0
      ? t("accountAfter", { button })
      : picks.some((pick) => !isInstalled(pick, props.results))
        ? t("accountTick", { button })
        : picks.length > 0
          ? rich(t("accountStopped"), { extensions })
          : setup.registries === 0
            ? t("accountNoRegistry")
            : rich(t("accountNoProvider"), { extensions });
  return (
    <Card title={t("account")}>
      {props.error && <Notice tone="danger">{props.error}</Notice>}
      {props.count === undefined ? null : props.count > 0 ? (
        <p class="setup-done">
          {t("accountsConnected", { count: props.count })} <a href="#/accounts">{t("accountsLink")}</a>.
        </p>
      ) : !props.provider ? (
        <p class="muted setup-hint">
          {t("accountHint")} {before}
        </p>
      ) : (
        <>
          <p class="muted setup-hint">
            {t("accountHint")} {t("accountSkip")}
          </p>
          {signInsToName(setup, props.results).map((signIn) => (
            <Notice key={`${signIn.provider}/${signIn.from}`} tone="muted">
              {t("signInFound", { label: signIn.label })}
            </Notice>
          ))}
          <AddAccountPage embedded onSuccess={props.onAdded} />
        </>
      )}
    </Card>
  );
}

/** What a host adds to a client's row: a button that opens one of the host's own pages. */
export interface ClientAction {
  label: string;
  /** The ID of the page the button opens. */
  page: string;
}

/**
 * What the build that shows the page adds to it. The panel adds nothing; the desktop app offers
 * to set a client up on its own computer.
 */
export interface SetupOptions {
  /**
   * The action for a client found installed, by its ID in core's scan; nothing for no action. The
   * page asks for none for a client whose settings file was refused: there only the paste text works.
   */
  clientAction?: (client: string) => ClientAction | undefined;
  /** One sentence under the clients' heading, shown while a client has an action: what it does. */
  clientActionHint?: string;
}

/** What core's paste text holds where a model's name goes (next/internal/core/setup_clients.go). */
const MODEL_PLACEHOLDER = "<model-id>";

/** The files to paste for one client, and a button that makes the client key they need. */
function PasteText(props: { client: SetupClient }) {
  const { api, unauthorized } = useKit();
  const paste = useLoad(() => api.clientPaste(props.client.id));
  const [key, setKey] = useState<string>();
  const [making, setMaking] = useState(false);
  const [error, setError] = useState<string>();
  const make = async () => {
    setMaking(true);
    setError(undefined);
    const reply = await api.addClientKey();
    setMaking(false);
    if (reply.status === 401) return unauthorized();
    setError(problem(reply));
    if (reply.body) setKey(reply.body.value);
  };
  const data = paste.data;
  return (
    <div class="setup-paste stack" data-part="setup-paste">
      {paste.error && (
        <>
          <Notice tone="danger">{paste.error}</Notice>
          <div>
            <Button onClick={paste.reload}>{t("retry")}</Button>
          </div>
        </>
      )}
      {paste.loading && !data && <Notice tone="muted">{t("pasteLoading")}</Notice>}
      {data && (
        <>
          {data.note && <p class="setup-hint">{data.note}</p>}
          {data.files.map((file) => (
            <div key={file.path} class="setup-file">
              <span>{rich(t("pasteInto"), { path: <span class="mono">{file.path}</span> })}</span>
              <Code value={withKey(file.content, data.key, key)} />
            </div>
          ))}
          <small class="hint">
            {rich(t("pasteAddress"), { address: <span class="mono">{data.base_url}</span> })}
          </small>
          {data.files.some((file) => file.content.includes(MODEL_PLACEHOLDER)) && (
            <small class="hint">
              {rich(t("pasteModel"), {
                placeholder: <span class="mono">{MODEL_PLACEHOLDER}</span>,
                command: <span class="mono">pluxy model list</span>,
              })}
            </small>
          )}
          {error && <Notice tone="danger">{error}</Notice>}
          {key ? (
            <>
              <p class="setup-hint">
                {rich(t("keyInText"), { placeholder: <span class="mono">{data.key}</span> })}
              </p>
              <KeyNotice title={t("newKey")} value={key} close={() => setKey(undefined)} seen={() => {}} />
            </>
          ) : (
            <div class="setup-line">
              <span class="muted">
                {rich(t("keyWhere"), { placeholder: <span class="mono">{data.key}</span> })}
              </span>
              <Button disabled={making} onClick={make}>
                {t("makeKey")}
              </Button>
            </div>
          )}
        </>
      )}
    </div>
  );
}

function ClientRow(props: { client: SetupClient; action?: ClientAction }) {
  const { client, action } = props;
  const [open, setOpen] = useState(false);
  return (
    <li data-part="setup-client" data-state={client.installed ? client.state : "not-found"}>
      <div class="setup-row-head">
        <span class="setup-pick-text">
          <strong>
            {client.name}
            {client.version && <span class="mono muted"> {client.version}</span>}
          </strong>
          {!client.installed ? (
            <span class="muted">{t("notFound")}</span>
          ) : client.state === "configured" ? (
            <span>
              <Badge tone="ok">{clientWords(client)}</Badge>
            </span>
          ) : (
            <span class="muted">{clientWords(client)}</span>
          )}
        </span>
        <div class="actions">
          {action && (
            <Button
              onClick={() => {
                location.hash = `#/${action.page}`;
              }}
            >
              {action.label}
            </Button>
          )}
          <Button onClick={() => setOpen(!open)}>{open ? t("hidePaste") : t("showPaste")}</Button>
        </div>
      </div>
      {open && <PasteText client={client} />}
    </li>
  );
}

/** Each client with its state and the text that points it at core. */
export function Clients(props: { setup: Setup; options?: SetupOptions }) {
  const { found, others } = splitClients(props.setup);
  const scanning = scanState(props.setup) === "first";
  // A refused file is one the host's own page would not write either, so its button would lead nowhere.
  const actions = new Map(
    found.map((client) => [
      client.id,
      client.state === "refused" ? undefined : props.options?.clientAction?.(client.id),
    ]),
  );
  const hint = props.options?.clientActionHint;
  return (
    <Card title={t("clients")}>
      <p class="muted setup-hint">{t("clientsHint")}</p>
      {hint && found.some((client) => actions.get(client.id)) && <p class="muted setup-hint">{hint}</p>}
      {found.length > 0 ? (
        <ul class="setup-rows">
          {found.map((client) => (
            <ClientRow key={client.id} client={client} action={actions.get(client.id)} />
          ))}
        </ul>
      ) : (
        !scanning && <p class="setup-hint">{t("noClients")}</p>
      )}
      {others.length > 0 && (
        <details class="setup-others">
          <summary>{t("otherClients", { count: others.length })}</summary>
          <ul class="setup-rows">
            {others.map((client) => (
              <ClientRow key={client.id} client={client} />
            ))}
          </ul>
        </details>
      )}
    </Card>
  );
}

export function SetupPage(props: { options?: SetupOptions }) {
  const { api, host, unauthorized, extensionsChanged } = useKit();
  const loaded = useLoad(api.setup);
  const accounts = useLoad(api.accounts);
  const packages = useLoad(api.packages);
  // The last good reply stays on screen while the next one loads, and when one fails.
  const kept = useRef<Setup | undefined>(undefined);
  if (loaded.data) kept.current = loaded.data;
  const setup = kept.current;
  const [choices, setChoices] = useState<Choices>({});
  const [results, setResults] = useState<Results>({});
  const [applying, setApplying] = useState(false);
  const [applyError, setApplyError] = useState<string>();
  const [bringing, setBringing] = useState<ImportState>();
  // Look again also reads the accounts again: a sign-in left running (a device code, a browser
  // tab) can finish while this page stays in view, with no focus change to say so.
  const look = useAction(() => {
    accounts.reload();
    loaded.reload();
  });
  const finish = useAction(() => {
    loaded.reload();
    location.hash = "#/";
  });

  // A sign-in can finish somewhere else (a browser tab, another window): the accounts are read
  // again when this page is looked at again.
  useEffect(() => {
    const again = () => {
      if (document.visibilityState === "visible") accounts.reload();
    };
    window.addEventListener("focus", again);
    document.addEventListener("visibilitychange", again);
    return () => {
      window.removeEventListener("focus", again);
      document.removeEventListener("visibilitychange", again);
    };
  }, [accounts.reload]);

  // While core scans, read its answer again every second, as long as the page is visible.
  const scanning = setup?.scanning === true;
  useEffect(() => {
    if (!scanning || loaded.loading) return;
    const timer = setTimeout(() => {
      if (document.visibilityState === "visible") loaded.reload();
    }, 1000);
    const onVisible = () => {
      if (document.visibilityState === "visible") loaded.reload();
    };
    document.addEventListener("visibilitychange", onVisible);
    return () => {
      clearTimeout(timer);
      document.removeEventListener("visibilitychange", onVisible);
    };
  }, [scanning, loaded.loading, loaded.reload]);

  // A core that never looked (an install from before setup existed) looks once when the page opens.
  const asked = useRef(false);
  const never = setup !== undefined && scanState(setup) === "never";
  useEffect(() => {
    if (!never || asked.current) return;
    asked.current = true;
    look.run(api.scanSetup);
  }, [never]);

  const onStart = async () => {
    if (!setup || applying) return;
    setApplying(true);
    setApplyError(undefined);
    const done = await start(api, setup, choices, results);
    setApplying(false);
    if (done.reply.status === 401) return unauthorized();
    setApplyError(problem(done.reply));
    setResults(done.results);
    loaded.reload();
    packages.reload();
    extensionsChanged();
  };

  // A dry run first, whose report the page shows; then, when the person confirms, the import.
  const onBring = async (found: SetupImport, dryRun: boolean) => {
    if (!setup || bringing?.busy) return;
    if (!dryRun && !bringing?.report) return;
    const config = found.config;
    setBringing((s) => ({
      config,
      report: s?.config === config ? s.report : undefined,
      busy: dryRun ? "checking" : "importing",
    }));
    const reply = await api.importSetup(config, authDir(found), dryRun);
    if (reply.status === 401) return unauthorized();
    const report = reply.body;
    if (!report || reply.status >= 300) {
      // The report goes too: what a later try would do is for a new dry run to say.
      return setBringing({ config, ...importProblem(reply, dryRun) });
    }
    if (dryRun) return setBringing({ config, report });
    setBringing({ config, report, done: true });
    setChoices((c) => ({ ...c, ...neededPicks(setup, report) }));
    loaded.reload();
    accounts.reload();
    packages.reload();
  };

  if (!setup) {
    return (
      <>
        <PageHead title={t("title")} />
        {loaded.error ? (
          <>
            <Notice tone="danger">{loaded.error}</Notice>
            <Button onClick={loaded.reload}>{t("retry")}</Button>
          </>
        ) : (
          <Notice tone="muted">{t("loading")}</Notice>
        )}
      </>
    );
  }
  return (
    <SetupView
      setup={setup}
      options={props.options}
      remote={!onThisMachine(host.address)}
      choices={choices}
      results={results}
      provider={hasProvider(setup, results, packages.data?.packages)}
      packages={packages.data?.packages}
      accounts={accounts.data?.accounts.length}
      applying={applying}
      finishing={finish.busy}
      looking={look.busy}
      errors={{
        look: look.error ?? (loaded.loading ? undefined : loaded.error),
        apply: applyError,
        accounts: accounts.error,
        finish: finish.error,
      }}
      bring={{
        state: bringing,
        onBring,
        onClose: () => setBringing(undefined),
      }}
      onLook={() => look.run(api.scanSetup)}
      onTick={(id, ticked) => setChoices((c) => ({ ...c, [id]: ticked }))}
      onStart={onStart}
      onAdded={accounts.reload}
      onFinish={() => finish.run(api.finishSetup)}
    />
  );
}

/** The page's parts, top to bottom, from what the page holds. */
export function SetupView(props: {
  setup: Setup;
  /** What the build adds to the page. */
  options?: SetupOptions;
  /** Core runs on another machine than the one showing the page. */
  remote: boolean;
  choices: Choices;
  results: Results;
  /** Core runs a provider. */
  provider: boolean;
  /** Core's installed packages, once it has listed them: they say which installed pick does not run. */
  packages?: Package[];
  /** How many accounts core has; undefined until it has said. */
  accounts?: number;
  applying?: boolean;
  finishing?: boolean;
  looking?: boolean;
  errors?: { look?: string; apply?: string; accounts?: string; finish?: string };
  /** Bringing a found CLIProxyAPI setup across; without it the row names only the command. */
  bring?: ImportProps;
  onLook: () => void;
  onTick: (id: string, ticked: boolean) => void;
  onStart: () => void;
  onAdded: () => void;
  onFinish: () => void;
}) {
  const { setup, choices, results, errors } = props;
  // One primary button at a time: Finish takes it once Start has nothing left to do.
  const finishNext = toInstall(setup, choices, results).length === 0 && props.provider;
  const stopped = stoppedPicks(setup, results, props.packages);
  return (
    <>
      <PageHead title={t("title")} />
      <div class="setup stack">
        <p class="muted setup-hint">
          {!setup.first_run
            ? t("introLater")
            : `${t("introFirst")} ${t(startInstalls(setup, results) ? "introUntilStart" : "introOptional")}`}
        </p>
        <Found
          setup={setup}
          remote={props.remote}
          busy={props.looking}
          error={errors?.look}
          bring={props.bring}
          onLook={props.onLook}
        />
        <Providers
          setup={setup}
          choices={choices}
          results={results}
          stopped={stopped}
          busy={props.applying}
          onTick={props.onTick}
        />
        <Start
          setup={setup}
          choices={choices}
          results={results}
          busy={props.applying || props.finishing}
          error={errors?.apply}
          provider={props.provider}
          stopped={stopped}
          onStart={props.onStart}
          onFinish={props.onFinish}
        />
        <Account
          setup={setup}
          choices={choices}
          results={results}
          count={props.accounts}
          provider={props.provider}
          error={errors?.accounts}
          onAdded={props.onAdded}
        />
        <Clients setup={setup} options={props.options} />
        {setup.first_run && (
          <Card title={t("finishTitle")}>
            {errors?.finish && <Notice tone="danger">{errors.finish}</Notice>}
            <div class="setup-line">
              <span class="muted">{t("finishHint")}</span>
              <Button
                variant={finishNext ? "primary" : "quiet"}
                disabled={props.finishing || props.applying}
                onClick={props.onFinish}
              >
                {t("finish")}
              </Button>
            </div>
          </Card>
        )}
      </div>
    </>
  );
}

export const setupPage = (options?: SetupOptions): PageDef => ({
  id: "setup",
  group: "control",
  title: t("title"),
  short: t("short"),
  tip: t("tip"),
  render: () => <SetupPage options={options} />,
});

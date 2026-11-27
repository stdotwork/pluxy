// Setup: one short guided setup in three steps, one open at a time: the person's AI accounts (which
// kinds, then signing in), their coding tool, and one test request that proves an account answers.
// What pluxy found on this computer shows in the step that uses it; a CLIProxyAPI setup to import
// is one line above the steps (setupImport.tsx). Every step can be skipped, and every way to end
// setup is the one finish. The panel and the desktop app show this page; the app adds its own
// tool buttons through SetupOptions.

import type { ComponentChildren } from "preact";
import { useEffect, useMemo, useRef, useState } from "preact/hooks";
import { type ImportReport, problem, runningExtensions, type SetupClient } from "../api";
import { Icon } from "../icons";
import { refreshWord, StatePane, tryAgainWord } from "../parts";
import type { PageDef } from "../shell";
import { strings } from "../strings";
import { Button, Notice, PageHead, useKit, useLoad } from "../ui";
import {
  type Choices,
  canServe,
  connectedTools,
  joinNames,
  onThisMachine,
  type Results,
  start,
  toInstall,
} from "./setup.helpers";
import { AccountsStep, accountsSummary } from "./setupAccounts";
import { CheckStep, checkSummary, type TestOutcome } from "./setupCheck";
import { ImportLine } from "./setupImport";
import { ToolListView, type ToolsOptions, useMadeKeys, useToolsState } from "./tools";
import { toolName } from "./tools.helpers";

const t = strings("setup", {
  title: "Set up pluxy",
  titleLater: "Setup",
  tip: "The guided setup",
  introFirst:
    "pluxy lets your coding tools use your AI accounts. Three short steps set it up, and you can skip any of them.",
  introLater: "Review or change your accounts and coding tools.",
  loading: "Checking what pluxy already has…",
  failed: "Could not read setup.",
  scanning: "Looking at this computer… This takes a few seconds.",
  steps: "Setup steps",
  step1: "Your AI accounts",
  step2: "Your coding tool",
  step3: "Check it works",
  open: "Open",
  continue: "Continue",
  skip: "Skip",
  done: "Done",
  toolsUse: { one: "{tools} uses pluxy.", other: "{tools} use pluxy." },
  noTool: "No tool connected yet.",
  here: "Detected on this computer.",
  network: "Other computers on your network can reach pluxy.",
  scanFailed: "Could not look at this computer: {error}.",
  finishLater: "Leave setup for now",
  staysUnder: "It won't open by itself again; it stays under Settings.",
  backHome: "Back to Home",
  finishFailed: "Could not finish setup: {error}.",
});

/** What a host adds to the Setup page; the panel adds nothing. */
export interface SetupOptions {
  /** The host's own button at the end of a tool's row in step 2 (see ToolsOptions.toolAction). */
  toolAction?: ToolsOptions["toolAction"];
  /** Tools the host reads itself, shown as ordinary rows in step 2. */
  extraTools?: SetupClient[];
  /** Overrides the page's own answer to whether pluxy runs on another computer. */
  isRemote?: boolean;
  /** Links to the host's own pages for tools on this computer, shown when pluxy is elsewhere. */
  localPages?: ToolsOptions["localPages"];
  /** Runs on step 2's Refresh, so the host can read its own tools again. */
  onRefresh?: () => void;
  /** Lines the host puts above and under step 2's list (see ToolsOptions.notes). */
  notes?: ToolsOptions["notes"];
  /** Called after setup is finished, before the page goes to Home. */
  onFinish?: () => void;
}

type StepNo = 1 | 2 | 3;

function Step(props: {
  n: StepNo;
  title: string;
  summary: ComponentChildren;
  done: boolean;
  open: boolean;
  onOpen: () => void;
  heading: (node: HTMLHeadingElement | null) => void;
  children: ComponentChildren;
}) {
  const id = `setup-step-${props.n}`;
  return (
    <li
      class="setup-step"
      data-part="setup-step"
      data-state={props.open ? "open" : props.done ? "done" : "todo"}
    >
      <span class="setup-step-mark" aria-hidden="true">
        {props.done ? <Icon name="check" /> : props.n}
      </span>
      <section class="setup-step-body" aria-labelledby={id}>
        <div class="setup-step-head">
          <h2 id={id} ref={props.heading} tabIndex={-1}>
            {props.title}
            {props.done && <span class="sr-only"> ({t("done")})</span>}
          </h2>
          {!props.open && (
            <Button onClick={props.onOpen} describedBy={id}>
              {t("open")}
            </Button>
          )}
        </div>
        {props.open ? (
          <div class="setup-step-open">{props.children}</div>
        ) : (
          <p class="setup-step-summary">{props.summary}</p>
        )}
      </section>
    </li>
  );
}

/**
 * Step 2's list of tools. It holds the keys made in it, so a key's value is gone once the step
 * folds, as it is when the Tools page's key notice closes.
 */
function ToolsStep(props: Omit<Parameters<typeof ToolListView>[0], "made">) {
  const made = useMadeKeys();
  return <ToolListView {...props} made={made} />;
}

/** Continue once the step is done, else Skip; both open the next step. */
function Next(props: { done: boolean; onNext: () => void; children?: ComponentChildren }) {
  return (
    <div class="actions setup-next">
      <Button variant={props.done ? "primary" : "quiet"} onClick={props.onNext}>
        {props.done ? t("continue") : t("skip")}
      </Button>
      {props.children}
    </div>
  );
}

export function SetupPage(props: { options?: SetupOptions }) {
  const { api, host, unauthorized, extensionsChanged } = useKit();
  const options = props.options;
  const tools = useToolsState();
  const { setup: loaded, accounts } = tools;
  const packages = useLoad(api.packages);
  const status = useLoad(api.status);
  const setup = loaded.data;
  const accountList = accounts.data?.accounts;
  const running = useMemo(() => runningExtensions(status.data), [status.data]);
  const [choices, setChoices] = useState<Choices>({});
  const [results, setResults] = useState<Results>({});
  const [needed, setNeeded] = useState<Record<string, string>>({});
  const [adding, setAdding] = useState(false);
  const [addError, setAddError] = useState<string>();
  const [signedIn, setSignedIn] = useState<string>();
  const [outcome, setOutcome] = useState<TestOutcome>();
  const [finishing, setFinishing] = useState(false);
  const [finishError, setFinishError] = useState<string>();
  // null until the person opens a step: the page then shows the step it opened with.
  const [moved, setOpen] = useState<StepNo | null>(null);
  const headings = useRef<Record<number, HTMLHeadingElement | null>>({});
  const focusNext = useRef(false);
  const started = useRef<{ hadAccounts: boolean; open?: StepNo }>(undefined);
  const remote = options?.isRemote ?? !onThisMachine(host.address);

  // A sign-in can finish somewhere else (a browser tab, a device code), and so can a coding tool's
  // settings (pasted into its file on pluxy's computer): the accounts are read, and pluxy asked to
  // look at its tools again, when this page is looked at again.
  // Which accounts can take requests depends on the extensions core runs: those are read again too.
  useEffect(() => {
    const again = () => {
      if (document.visibilityState !== "visible") return;
      accounts.reload();
      status.reload();
      void tools.scan();
    };
    window.addEventListener("focus", again);
    document.addEventListener("visibilitychange", again);
    return () => {
      window.removeEventListener("focus", again);
      document.removeEventListener("visibilitychange", again);
    };
  }, [accounts.reload, status.reload, tools.scan]);

  const tools2 = connectedTools(setup, options?.extraTools);
  const done: Record<StepNo, boolean> = {
    1: (accountList ?? []).some((a) => canServe(a, running)),
    2: tools2.length > 0,
    3: outcome?.kind === "passed",
  };

  // Which step is open is decided once, when the page has what it needs: on a first run the first
  // step not done, afterwards none. After that only the person moves it.
  // It waits for the extensions core runs too: an account whose provider is stopped is not done.
  if (!started.current && setup && accountList && (status.data || status.phase === "failed")) {
    started.current = {
      hadAccounts: accountList.length > 0,
      open: setup.first_run ? (!done[1] ? 1 : !done[2] ? 2 : 3) : undefined,
    };
  }
  const open = moved ?? started.current?.open;
  const go = (n: StepNo) => {
    focusNext.current = true;
    setOpen(n);
  };
  useEffect(() => {
    if (!focusNext.current || !open) return;
    focusNext.current = false;
    headings.current[open]?.focus();
  }, [open]);

  const add = async () => {
    if (!setup || adding) return;
    const ids = toInstall(setup, choices, results);
    if (ids.length === 0) return;
    setAdding(true);
    setAddError(undefined);
    const out = await start(api, ids, results);
    setAdding(false);
    if (out.reply.status === 401) return unauthorized();
    if (!out.reply.body) setAddError(problem(out.reply));
    setResults(out.results);
    loaded.reload();
    packages.reload();
    status.reload();
    extensionsChanged();
  };

  const imported = (report: ImportReport) => {
    const offered = new Set(setup?.picks.filter((p) => p.kind === "provider").map((p) => p.id));
    const needs = report.needs.filter((need) => offered.has(need.pick));
    setChoices((c) => ({ ...c, ...Object.fromEntries(needs.map((need) => [need.pick, true])) }));
    setNeeded((n) => ({ ...n, ...Object.fromEntries(needs.map((need) => [need.pick, need.why])) }));
    loaded.reload();
    accounts.reload();
    packages.reload();
  };

  const finish = async () => {
    if (finishing) return;
    setFinishing(true);
    setFinishError(undefined);
    const r = await api.finishSetup();
    setFinishing(false);
    if (r.status === 401) return unauthorized();
    const why = problem(r);
    if (why !== undefined) return setFinishError(why.replace(/\.$/, ""));
    options?.onFinish?.();
    location.hash = "#/";
  };

  const refresh = async () => {
    accounts.reload();
    await tools.scan();
    options?.onRefresh?.();
  };

  if (!setup || !accountList) {
    const failed = (loaded.phase === "failed" && !setup) || (accounts.phase === "failed" && !accountList);
    return (
      <>
        <PageHead title={t("title")} />
        {failed ? (
          <StatePane
            kind="failed"
            title={t("failed")}
            details={loaded.error ?? accounts.error}
            primary={{
              label: tryAgainWord(),
              onClick: () => {
                loaded.reload();
                accounts.reload();
              },
            }}
          />
        ) : (
          <StatePane kind="loading" title={t("loading")} />
        )}
      </>
    );
  }

  const first = setup.first_run;
  const names = tools2.map(toolName);
  const stepProps = (n: StepNo) => ({
    n,
    done: done[n],
    open: open === n,
    onOpen: () => go(n),
    heading: (node: HTMLHeadingElement | null) => {
      headings.current[n] = node;
    },
  });
  return (
    <>
      <PageHead title={first ? t("title") : t("titleLater")} />
      <div class="setup stack">
        <p class="setup-intro">{first ? t("introFirst") : t("introLater")}</p>
        {!setup.scanned_at && (setup.scanning || tools.scanBusy) && (
          <p class="muted setup-scanning">{t("scanning")}</p>
        )}
        {!setup.scanned_at && !setup.scanning && !tools.scanBusy && tools.scanError && (
          <div class="setup-scan-failed">
            <Notice tone="danger">{t("scanFailed", { error: tools.scanError.replace(/\.$/, "") })}</Notice>
            <div class="actions">
              <Button onClick={() => void tools.scan()}>{tryAgainWord()}</Button>
            </div>
          </div>
        )}
        <ImportLine setup={setup} onImported={imported} onRescan={tools.rescan} />
        <ol class="setup-steps" aria-label={t("steps")}>
          <Step {...stepProps(1)} title={t("step1")} summary={accountsSummary(accountList, running)}>
            <AccountsStep
              setup={setup}
              packages={packages.data?.packages}
              accounts={accountList}
              running={running}
              hadAccounts={started.current?.hadAccounts ?? false}
              choices={choices}
              results={results}
              needed={needed}
              adding={adding}
              addError={addError}
              remote={remote}
              signedIn={signedIn}
              onTick={(id, on) => setChoices((c) => ({ ...c, [id]: on }))}
              onAdd={add}
              onSignedIn={(id) => {
                setSignedIn(id);
                accounts.reload();
              }}
            />
            <Next done={done[1]} onNext={() => go(2)} />
          </Step>
          <Step
            {...stepProps(2)}
            title={t("step2")}
            summary={
              names.length > 0 ? t("toolsUse", { count: names.length, tools: joinNames(names) }) : t("noTool")
            }
          >
            {!remote && <p class="muted">{t("here")}</p>}
            {setup.found.core.reachable_from_network && <p class="muted">{t("network")}</p>}
            <ToolsStep
              toolAction={options?.toolAction}
              extraTools={options?.extraTools}
              localPages={options?.localPages}
              notes={options?.notes}
              remote={remote}
              state={tools}
              onAddAccount={() => go(1)}
            />
            <Next done={done[2]} onNext={() => go(3)}>
              <Button onClick={refresh}>{refreshWord()}</Button>
            </Next>
          </Step>
          <Step {...stepProps(3)} title={t("step3")} summary={checkSummary(outcome)}>
            <CheckStep
              accounts={accountList}
              running={running}
              added={signedIn}
              outcome={outcome}
              onOutcome={setOutcome}
              onAddAccount={() => go(1)}
              onAccountsChanged={accounts.reload}
              finishing={finishing}
              onFinish={finish}
            />
          </Step>
        </ol>
        {finishError && <Notice tone="danger">{t("finishFailed", { error: finishError })}</Notice>}
        <p class="setup-later">
          {first ? (
            <>
              <Button disabled={finishing} onClick={finish}>
                {t("finishLater")}
              </Button>
              <span class="muted">{t("staysUnder")}</span>
            </>
          ) : (
            <a class="btn" data-part="button" data-variant="quiet" href="#/">
              {t("backHome")}
            </a>
          )}
        </p>
      </div>
    </>
  );
}

export const setupPage = (options?: SetupOptions): PageDef => ({
  id: "setup",
  dest: "settings",
  kind: "topic",
  title: t("titleLater"),
  tip: t("tip"),
  render: () => <SetupPage options={options} />,
});

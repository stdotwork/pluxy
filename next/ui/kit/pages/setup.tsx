// Setup: one short guided setup in three steps: the person's AI accounts (which kinds, then signing
// in), their coding tool, and one test request that proves an account answers. What pluxy found on
// this computer shows in the step that uses it; a CLIProxyAPI setup to import is offered with the
// accounts (setupImport.tsx). Every step can be skipped, and every way to end setup is the one
// finish. The panel and the desktop app show this page; the app adds its own tool buttons through
// SetupOptions.
//
// On a first run it is a focused window (focus.tsx), one step per screen with Back and Continue and
// "Finish later" at the top, which leaves a card on Home (SetupLaterCard). Afterwards it is a
// checklist among the Settings topics: each step one row, opened in place.

import type { ComponentChildren } from "preact";
import { useContext, useEffect, useMemo, useRef, useState } from "preact/hooks";
import { type ImportReport, problem, runningExtensions, type SetupClient } from "../api";
import { Focus, FocusedContext } from "../focus";
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
import { forgetLater, markLaterTested, rememberLater } from "./setupLater";
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
  lead1: "Your coding tools' requests go through these accounts. Add the kinds you have, then sign in.",
  lead2: "Point a coding tool at pluxy, so its requests go through your accounts.",
  lead3: "Send one short request through an account to see that it answers.",
  stepOf: "Step {n} of 3",
  open: "Open",
  continue: "Continue",
  back: "Back",
  done: "Done",
  toolsUse: { one: "{tools} uses pluxy.", other: "{tools} use pluxy." },
  noTool: "No tool connected yet.",
  here: "Detected on this computer.",
  network: "Other computers on your network can reach pluxy.",
  scanFailed: "Could not look at this computer: {error}.",
  finishLater: "Finish later",
  staysUnder: "Finish later leaves a reminder on Home. Setup stays under Settings.",
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
const STEPS: StepNo[] = [1, 2, 3];
const TITLES: Record<StepNo, "step1" | "step2" | "step3"> = { 1: "step1", 2: "step2", 3: "step3" };
const LEADS: Record<StepNo, "lead1" | "lead2" | "lead3"> = { 1: "lead1", 2: "lead2", 3: "lead3" };

/** One of the checklist's rows: the step's mark, title and what it came to, opened in place. */
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
          <div class="setup-step-text">
            <h2 id={id} ref={props.heading} tabIndex={-1}>
              {props.title}
              {props.done && <span class="sr-only"> ({t("done")})</span>}
            </h2>
            {!props.open && <p class="setup-step-summary">{props.summary}</p>}
          </div>
          {!props.open && (
            <Button onClick={props.onOpen} describedBy={id}>
              {t("open")}
            </Button>
          )}
        </div>
        {props.open && <div class="setup-step-open">{props.children}</div>}
      </section>
    </li>
  );
}

/** The first run's steps above the card: where the person is, and a way to any step. */
function Stepper(props: { open: StepNo; done: Record<StepNo, boolean>; onGo: (n: StepNo) => void }) {
  return (
    <ol class="setup-stepper" data-part="setup-stepper" aria-label={t("steps")}>
      {STEPS.map((n) => {
        const state = props.open === n ? "open" : props.done[n] ? "done" : "todo";
        return (
          <li key={n} data-state={state}>
            <button
              type="button"
              aria-current={props.open === n ? "step" : undefined}
              onClick={() => props.onGo(n)}
            >
              <span class="setup-stepper-mark" aria-hidden="true">
                {props.done[n] && props.open !== n ? <Icon name="check" size={12} /> : n}
              </span>
              <span class="setup-stepper-title">
                {t(TITLES[n])}
                {props.done[n] && <span class="sr-only"> ({t("done")})</span>}
              </span>
            </button>
          </li>
        );
      })}
    </ol>
  );
}

/** The first run's one step on screen: what it is, its content, and the way back and on. */
function Screen(props: {
  n: StepNo;
  heading: (node: HTMLHeadingElement | null) => void;
  above?: ComponentChildren;
  children: ComponentChildren;
}) {
  const id = `setup-step-${props.n}`;
  return (
    <section
      class="setup-screen"
      data-part="setup-step"
      data-step={props.n}
      data-state="open"
      aria-labelledby={id}
    >
      <div class="focus-head">
        <p class="focus-eyebrow">{t("stepOf", { n: props.n })}</p>
        <h2 id={id} ref={props.heading} tabIndex={-1}>
          {t(TITLES[props.n])}
        </h2>
        <p class="focus-lead">{t(LEADS[props.n])}</p>
      </div>
      {props.above}
      {props.children}
    </section>
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

/**
 * Continue opens the next step: quiet until the step is done, filled after. On the first run it is
 * the card's foot, with Back at its start.
 */
function Next(props: {
  done: boolean;
  onNext: () => void;
  back?: () => void;
  focused: boolean;
  children?: ComponentChildren;
}) {
  return (
    <div class={props.focused ? "focus-foot setup-next" : "actions setup-next"}>
      {props.back && (
        <span class="focus-foot-start">
          <Button variant="ghost" onClick={props.back}>
            {t("back")}
          </Button>
        </span>
      )}
      {props.children}
      <Button variant={props.done ? "primary" : "quiet"} onClick={props.onNext}>
        {t("continue")}
      </Button>
    </div>
  );
}

export function SetupPage(props: { options?: SetupOptions }) {
  const kit = useKit();
  const { api, host, unauthorized, extensionsChanged } = kit;
  const focused = useContext(FocusedContext);
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

  // The shell shows Setup in the focused window only on a first run: it is told what core says.
  const firstRun = setup?.first_run;
  useEffect(() => {
    if (firstRun !== undefined) kit.setupFirstRun?.(firstRun);
  }, [firstRun]);

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

  /** Ends setup; later: the person chose Finish later, so Home keeps a card for it. */
  const finish = async (later = false) => {
    if (finishing) return;
    setFinishing(true);
    setFinishError(undefined);
    const r = await api.finishSetup();
    setFinishing(false);
    if (r.status === 401) return unauthorized();
    const why = problem(r);
    if (why !== undefined) return setFinishError(why.replace(/\.$/, ""));
    if (later && !(done[1] && done[2] && done[3])) rememberLater(host, done[3]);
    else forgetLater(host);
    options?.onFinish?.();
    location.hash = "#/";
    kit.setupFirstRun?.(false);
  };

  const refresh = async () => {
    accounts.reload();
    await tools.scan();
    options?.onRefresh?.();
  };

  if (!setup || !accountList) {
    const failed = (loaded.phase === "failed" && !setup) || (accounts.phase === "failed" && !accountList);
    const pane = failed ? (
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
    );
    if (focused)
      return (
        <Focus part="setup-focus" width="wide">
          <h1 class="sr-only" data-part="page-title" tabIndex={-1}>
            {t("title")}
          </h1>
          {pane}
        </Focus>
      );
    return (
      <>
        <PageHead title={t("title")} />
        {pane}
      </>
    );
  }

  const first = setup.first_run;
  const names = tools2.map(toolName);
  const scanNotes = (
    <>
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
    </>
  );
  const importLine = <ImportLine setup={setup} onImported={imported} onRescan={tools.rescan} />;
  const heading = (n: StepNo) => (node: HTMLHeadingElement | null) => {
    headings.current[n] = node;
  };
  const finishNotice = finishError && (
    <Notice tone="danger">{t("finishFailed", { error: finishError })}</Notice>
  );

  // Each step's content; on the first run back is the way to the step before.
  const content = (n: StepNo, back?: () => void): ComponentChildren => {
    if (n === 1)
      return (
        <>
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
          {first && importLine}
          <Next done={done[1]} onNext={() => go(2)} back={back} focused={first} />
        </>
      );
    if (n === 2)
      return (
        <>
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
          <Next done={done[2]} onNext={() => go(3)} back={back} focused={first}>
            <Button onClick={refresh}>{refreshWord()}</Button>
          </Next>
        </>
      );
    return (
      <CheckStep
        accounts={accountList}
        running={running}
        added={signedIn}
        outcome={outcome}
        onOutcome={(next) => {
          setOutcome(next);
          if (next?.kind === "passed") markLaterTested(host);
        }}
        onAddAccount={() => go(1)}
        onAccountsChanged={accounts.reload}
        finishing={finishing}
        onFinish={() => finish()}
        back={back}
      />
    );
  };

  if (first) {
    const at = open ?? 1;
    return (
      <Focus
        part="setup-focus"
        width="wide"
        bar={
          <Button variant="ghost" disabled={finishing} onClick={() => finish(true)}>
            {t("finishLater")}
          </Button>
        }
        above={
          <div class="focus-above setup-top">
            <div class="setup-top-text">
              <h1 data-part="page-title" tabIndex={-1}>
                {t("title")}
              </h1>
              <p class="muted">{t("introFirst")}</p>
            </div>
            <Stepper open={at} done={done} onGo={go} />
          </div>
        }
        below={<p>{t("staysUnder")}</p>}
      >
        <Screen
          key={at}
          n={at}
          heading={heading(at)}
          above={
            <>
              {scanNotes}
              {finishNotice}
            </>
          }
        >
          {content(at, at > 1 ? () => go((at - 1) as StepNo) : undefined)}
        </Screen>
      </Focus>
    );
  }

  const stepProps = (n: StepNo) => ({
    n,
    done: done[n],
    open: open === n,
    onOpen: () => go(n),
    heading: heading(n),
  });
  return (
    <>
      <PageHead title={t("titleLater")} />
      <div class="setup stack">
        <p class="setup-intro">{t("introLater")}</p>
        {scanNotes}
        {importLine}
        <ol class="setup-steps" aria-label={t("steps")}>
          <Step {...stepProps(1)} title={t("step1")} summary={accountsSummary(accountList, running)}>
            {content(1)}
          </Step>
          <Step
            {...stepProps(2)}
            title={t("step2")}
            summary={
              names.length > 0 ? t("toolsUse", { count: names.length, tools: joinNames(names) }) : t("noTool")
            }
          >
            {content(2)}
          </Step>
          <Step {...stepProps(3)} title={t("step3")} summary={checkSummary(outcome)}>
            {content(3)}
          </Step>
        </ol>
        {finishNotice}
        <p class="setup-later">
          <a class="btn" data-part="button" data-variant="quiet" href="#/">
            {t("backHome")}
          </a>
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

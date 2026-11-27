// Where pluxy runs: how the app reaches pluxy, in four parts. Now says the state, the address and
// who runs it, with the one action the state calls for. Where is the two choices again. This app
// holds the settings for opening and closing the app. Versions and advanced holds what only an
// owner needs. The native side has three modes, and this page keeps all three reachable while it
// shows two choices:
// - managed ("On this computer"): the app runs a pluxy of its own, one of the versions it installed,
//   adopted or copied in from the one that came with the app, and stops it when the app quits;
// - local: a pluxy already running on this computer, which the app only connects to;
// - remote: a pluxy on another computer, over https.
// The address typed decides between local and remote (addressKind). Every word is in
// connectionCopy.ts, apart from the settings rows' (startup.tsx, window.tsx) and the versions'.

import { useEffect, useId, useRef, useState } from "preact/hooks";
import { Badge, Button, Notice, PageHead, useCopy, useKit } from "../../ui/kit/ui";
import { ConnectForm } from "./connectForm";
import {
  ACTIONS,
  type Act,
  FIRST,
  leaves,
  paneOf,
  plainError,
  stateHeading,
  WHERE,
  whoRuns,
} from "./connectionCopy";
import {
  type Connection,
  connectionSet,
  coreAdopt,
  coreInstall,
  coreRestart,
  coreRollback,
  coreStart,
  coreVersions,
  keyEntry,
  type Versions,
} from "./host";
import { StartupRows } from "./startup";
import { CloseRow, useWindowPrefs } from "./window";

const VERSIONS = {
  title: "Versions of pluxy on this computer",
  none: "No version of pluxy is installed yet.",
  noList: "Set a release list below to install one.",
  kind: { bundled: "Came with the app", signed: "Signed release", adopted: "Your own program" },
  running: "Running",
  previous: "Previous",
  switchTo: "Switch to this version",
  install: "Install {version}",
  installAgain: "Install again",
  useProgram: "Use this program",
  waiting: "Answer the confirmation window pluxy opened.",
  done: "Done",
  cancelled: "Cancelled: nothing changed",
  failed: "Failed: {reason}",
  didNotStart: "pluxy {version} did not start: {reason}",
  process: "Process {pid}",
  releaseList: "Release list",
  releaseListHint: "The full path of a list of signed pluxy releases.",
  ownProgram: "Your own pluxy program",
  ownProgramHint: "The full path of a pluxy program to copy in and run.",
  save: "Save",
  saving: "Saving…",
  key: "Key",
  keyKept: "Kept on this computer, so the app signs in by itself.",
  keySession: "Kept until the app quits.",
  enterAgain: "Enter it again",
  certificate: "Certificate file",
  address: "Address",
  version: "Version",
};

const fill = (text: string, values: Record<string, string | number>) =>
  text.replace(/\{(\w+)\}/g, (m, name: string) => (Object.hasOwn(values, name) ? String(values[name]) : m));

/** The address with a button that copies it. */
function Address(props: { value: string }) {
  const { state, copy } = useCopy(props.value);
  return (
    <span class="where-address">
      <code class="mono" title={props.value}>
        {props.value}
      </code>
      <Button label={`${WHERE.copy} ${props.value}`} onClick={copy}>
        {state === "copied" ? WHERE.copied : WHERE.copy}
      </Button>
    </span>
  );
}

/** The state, who runs it, and the one action the state calls for. */
function Now(props: { connection: Connection; onChange: (c: Connection) => void }) {
  const c = props.connection;
  const { host } = useKit();
  const pane = paneOf(c);
  const [busy, setBusy] = useState<Act | "sign-out">();
  const [problem, setProblem] = useState<string>();
  const [asked, setAsked] = useState(false);
  useEffect(() => setAsked(false), [c]);
  const address = c.target ?? (c.mode === "managed" ? undefined : c.url);
  const version = c.mode === "managed" ? c.core?.version : undefined;
  const who = whoRuns(c);

  // The action the state calls for: the pane's own button where there is one, else Restart for
  // the app's own pluxy and Sign out for one it connects to.
  let act: Act | "sign-out" | undefined;
  if (pane) act = pane.primary === "connect" || pane.primary === "where" ? undefined : pane.primary;
  else if (c.mode === "managed") act = "restart";
  else if (host.features.has("signOut")) act = "sign-out";

  async function run(what: Act | "sign-out") {
    setBusy(what);
    setProblem(undefined);
    try {
      if (what === "sign-out") host.signOut();
      else if (what === "adopt") {
        await coreAdopt();
        setAsked(true);
      } else if (what === "key") {
        const result = await keyEntry();
        if (result.status === "signed_in") props.onChange(result.connection);
        else if (result.status === "wrong_key")
          setProblem(plainError("This management key was not accepted."));
        else if (result.status === "error") setProblem(plainError(result.connection.error, c.ca_file));
      } else {
        const next = await (what === "restart" ? coreRestart() : coreStart());
        if (next.error) setProblem(plainError(next.error, c.ca_file));
        props.onChange(next);
      }
    } catch (e) {
      setProblem(plainError(String(e), c.ca_file));
    } finally {
      setBusy(undefined);
    }
  }
  const label = (what: Act | "sign-out") => {
    if (what === "sign-out") return WHERE.signOut;
    const waiting = what === "adopt" && asked;
    return busy === what || waiting ? (ACTIONS[what].busy ?? ACTIONS[what].label) : ACTIONS[what].label;
  };
  const message = problem ?? (pane ? undefined : plainError(c.error, c.ca_file));
  return (
    <section class="where-section" aria-labelledby="where-now">
      <h2 id="where-now">{WHERE.now}</h2>
      <p class="where-state">
        <b>{stateHeading(c)}</b>
      </p>
      {pane?.lines.map((line) => (
        <p key={line} class="where-line">
          {line}
        </p>
      ))}
      {pane?.details && (
        <details class="where-details">
          <summary>Details</summary>
          <pre class="mono">{pane.details}</pre>
        </details>
      )}
      <dl class="where-facts">
        {address && (
          <>
            <dt>{VERSIONS.address}</dt>
            <dd>
              <Address value={address} />
            </dd>
          </>
        )}
        {version && (
          <>
            <dt>{VERSIONS.version}</dt>
            <dd>
              <code class="mono">{version}</code>
            </dd>
          </>
        )}
      </dl>
      {who && <p class="where-line">{who}</p>}
      {act && (
        <div class="where-action">
          <Button
            variant={act === "restart" || act === "sign-out" ? "quiet" : "primary"}
            disabled={busy !== undefined || (act === "adopt" && asked)}
            onClick={() => run(act)}
          >
            {label(act)}
          </Button>
          {message && <span class="where-message">{message}</span>}
        </div>
      )}
      {!act && message && <Notice tone="danger">{message}</Notice>}
    </section>
  );
}

/** The two choices again: only the picked one shows its controls. */
function Where(props: { connection: Connection; onChange: (c: Connection) => void }) {
  const c = props.connection;
  const [choice, setChoice] = useState<"here" | "there">(c.mode === "managed" ? "here" : "there");
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<string>();
  const name = useId();
  async function switchHere() {
    setBusy(true);
    setProblem(undefined);
    try {
      const next = await connectionSet({ mode: "managed" });
      if (next.error) setProblem(plainError(next.error));
      props.onChange(next);
    } catch (e) {
      setProblem(plainError(String(e)));
    } finally {
      setBusy(false);
    }
  }
  const stops = leaves(c);
  return (
    <section class="where-section" aria-labelledby="where-where">
      <h2 id="where-where">{WHERE.where}</h2>
      <div class="where-choices" role="radiogroup" aria-labelledby="where-where">
        <div class="where-choice" data-chosen={choice === "here" ? "" : undefined}>
          <label class="where-choice-head">
            <input type="radio" name={name} checked={choice === "here"} onChange={() => setChoice("here")} />
            <span>
              <b>{FIRST.here.title}</b>
              <small>{FIRST.here.about}</small>
            </span>
          </label>
          {choice === "here" && c.mode !== "managed" && (
            <div class="where-choice-body">
              {stops && <p class="where-line">{stops}</p>}
              <div class="where-action">
                <Button variant="primary" disabled={busy} onClick={switchHere}>
                  {busy ? WHERE.switching : WHERE.switchHere}
                </Button>
                {problem && <span class="where-message">{problem}</span>}
              </div>
            </div>
          )}
        </div>
        <div class="where-choice" data-chosen={choice === "there" ? "" : undefined}>
          <label class="where-choice-head">
            <input
              type="radio"
              name={name}
              checked={choice === "there"}
              onChange={() => setChoice("there")}
            />
            <span>
              <b>{FIRST.there.title}</b>
              <small>{FIRST.there.about}</small>
            </span>
          </label>
          {choice === "there" && (
            <div class="where-choice-body">
              {c.mode === "managed" && stops && <p class="where-line">{stops}</p>}
              <ConnectForm connection={c} onChange={props.onChange} submit="Connect" />
            </div>
          )}
        </div>
      </div>
    </section>
  );
}

/** Opening and closing the app. Rows that do nothing in the current mode are not drawn. */
function ThisApp(props: { connection: Connection }) {
  const managed = props.connection.mode === "managed";
  const window = useWindowPrefs();
  return (
    <section class="where-section" aria-labelledby="where-app">
      <h2 id="where-app">{WHERE.thisApp}</h2>
      <StartupRows managed={managed} tray={window.prefs?.tray} />
      <CloseRow managed={managed} window={window} />
    </section>
  );
}

/** The app's own pluxy versions: what is installed and runs, what the release list offers, and a
 * program waiting to be adopted. Each change opens the native confirmation. */
function VersionList(props: { connection: Connection }) {
  const c = props.connection;
  const [v, setV] = useState<Versions>();
  // Why the versions could not be read, and why the app refused the last change asked for: a
  // later successful read clears only the first.
  const [loadError, setLoadError] = useState<string>();
  const [refused, setRefused] = useState<string>();
  // The last outcome's number when a change was asked for: every button waits for its
  // confirmation until a later outcome arrives.
  const [waiting, setWaiting] = useState<number>();
  const latest = useRef(0);
  // The connection changes when a change has ended (pluxy-connection): read the versions again.
  // Only the latest read counts.
  useEffect(() => {
    const n = ++latest.current;
    coreVersions().then(
      (next) => {
        if (n !== latest.current) return;
        setV(next);
        setLoadError(undefined);
      },
      (e) => n === latest.current && setLoadError(String(e)),
    );
  }, [c]);
  const seq = v?.outcome?.seq ?? 0;
  const asked = waiting !== undefined && seq <= waiting;
  async function ask(run: () => Promise<void>) {
    setRefused(undefined);
    setWaiting(seq);
    try {
      await run();
    } catch (e) {
      setWaiting(undefined);
      setRefused(String(e));
    }
  }
  if (!v) return loadError ? <Notice tone="danger">{loadError}</Notice> : null;
  const running = c.core?.state === "running" ? c.core.version : undefined;
  const installed = new Set(v.installed.map((i) => i.id));
  const available = new Set(v.available.map((a) => a.version));
  const offered = v.available.filter((a) => !installed.has(a.version));
  const outcome = v.outcome;
  return (
    <div class="where-versions">
      <h3>{VERSIONS.title}</h3>
      <ul class="where-list">
        {c.core?.adopt && (
          <li>
            <span class="where-list-name">
              <code class="mono">{c.core.adopt}</code>
              <small>{VERSIONS.kind.adopted}</small>
            </span>
            <span class="where-list-actions">
              <Button disabled={asked} onClick={() => ask(coreAdopt)}>
                {VERSIONS.useProgram}
              </Button>
            </span>
          </li>
        )}
        {v.installed.map((i) => (
          <li key={i.id}>
            <span class="where-list-name">
              <code class="mono">{i.id}</code>
              <small title={i.adopted_from}>{VERSIONS.kind[i.kind]}</small>
              {i.id === running && <Badge tone="ok">{VERSIONS.running}</Badge>}
              {i.id === v.previous && <Badge tone="muted">{VERSIONS.previous}</Badge>}
            </span>
            <span class="where-list-actions">
              {i.id !== running && (
                <Button disabled={asked} onClick={() => ask(() => coreRollback(i.id))}>
                  {VERSIONS.switchTo}
                </Button>
              )}
              {i.kind === "signed" && available.has(i.id) && (
                <Button disabled={asked} onClick={() => ask(() => coreInstall(i.id))}>
                  {VERSIONS.installAgain}
                </Button>
              )}
            </span>
          </li>
        ))}
        {offered.map((a) => (
          <li key={a.version}>
            <span class="where-list-name">
              <code class="mono">{a.version}</code>
            </span>
            <span class="where-list-actions">
              <Button disabled={asked} onClick={() => ask(() => coreInstall(a.version))}>
                {fill(VERSIONS.install, { version: a.version })}
              </Button>
            </span>
          </li>
        ))}
      </ul>
      {v.installed.length === 0 && !c.core?.adopt && (
        <p class="where-line">
          {VERSIONS.none} {!v.source && VERSIONS.noList}
        </p>
      )}
      {c.core?.pid !== undefined && c.core.state === "running" && (
        <p class="where-line">{fill(VERSIONS.process, { pid: c.core.pid })}</p>
      )}
      {v.bundled?.problem && <Notice tone="danger">{v.bundled.problem}</Notice>}
      {v.failed && (
        <Notice tone="danger">
          {fill(VERSIONS.didNotStart, { version: v.failed.version, reason: v.failed.reason })}
        </Notice>
      )}
      {v.source_error && <Notice tone="danger">{v.source_error}</Notice>}
      {asked && <p class="where-line">{VERSIONS.waiting}</p>}
      {outcome && !asked && (
        <p class="where-line">
          {outcome.result === "done"
            ? VERSIONS.done
            : outcome.result === "cancelled"
              ? VERSIONS.cancelled
              : fill(VERSIONS.failed, { reason: outcome.detail ?? "" })}
        </p>
      )}
      {loadError && <Notice tone="danger">{loadError}</Notice>}
      {refused && <Notice tone="danger">{refused}</Notice>}
    </div>
  );
}

function PathField(props: { label: string; hint: string; value: string; onInput: (v: string) => void }) {
  const id = useId();
  return (
    <div class="field" data-part="field">
      <label class="label" for={id}>
        {props.label}
      </label>
      <input
        id={id}
        type="text"
        value={props.value}
        autocomplete="off"
        spellcheck={false}
        aria-describedby={`${id}-hint`}
        onInput={(e) => props.onInput(e.currentTarget.value)}
      />
      <small class="hint" id={`${id}-hint`}>
        {props.hint}
      </small>
    </div>
  );
}

/** The release list and the program of one's own, for the pluxy the app runs. */
function OwnSources(props: { connection: Connection; onChange: (c: Connection) => void }) {
  const c = props.connection;
  const [source, setSource] = useState(c.core_source ?? "");
  const [program, setProgram] = useState(c.core_path ?? "");
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<string>();
  const changed = source !== (c.core_source ?? "") || program !== (c.core_path ?? "");
  async function save() {
    setBusy(true);
    setProblem(undefined);
    try {
      const next = await connectionSet({
        mode: "managed",
        core_source: source || undefined,
        core_path: program || undefined,
      });
      if (next.error) setProblem(plainError(next.error));
      props.onChange(next);
    } catch (e) {
      setProblem(plainError(String(e)));
    } finally {
      setBusy(false);
    }
  }
  return (
    <form
      class="where-owner"
      aria-label={VERSIONS.releaseList}
      onSubmit={(e) => {
        e.preventDefault();
        void save();
      }}
    >
      <PathField
        label={VERSIONS.releaseList}
        hint={VERSIONS.releaseListHint}
        value={source}
        onInput={setSource}
      />
      <PathField
        label={VERSIONS.ownProgram}
        hint={VERSIONS.ownProgramHint}
        value={program}
        onInput={setProgram}
      />
      {problem && <Notice tone="danger">{problem}</Notice>}
      <Button type="submit" disabled={busy || !changed}>
        {busy ? VERSIONS.saving : VERSIONS.save}
      </Button>
    </form>
  );
}

/** What only an owner needs: for the pluxy the app runs, its versions; for one it connects to, how its key is kept. */
function Advanced(props: { connection: Connection; onChange: (c: Connection) => void }) {
  const c = props.connection;
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<string>();
  async function again() {
    setBusy(true);
    setProblem(undefined);
    try {
      const result = await keyEntry();
      if (result.status === "signed_in") props.onChange(result.connection);
      else if (result.status === "wrong_key") setProblem(plainError("This management key was not accepted."));
      else if (result.status === "error") setProblem(plainError(result.connection.error, c.ca_file));
    } catch (e) {
      setProblem(plainError(String(e), c.ca_file));
    } finally {
      setBusy(false);
    }
  }
  return (
    <details class="where-advanced">
      <summary>{WHERE.advanced}</summary>
      <div class="where-advanced-body">
        {c.mode === "managed" && (
          <>
            <VersionList connection={c} />
            <OwnSources connection={c} onChange={props.onChange} />
          </>
        )}
        {(c.mode === "local" || c.mode === "remote") && (
          <dl class="where-facts">
            <dt>{VERSIONS.key}</dt>
            <dd class="where-inline">
              {c.remembered ? VERSIONS.keyKept : VERSIONS.keySession}
              {c.signed_in && (
                <Button disabled={busy} onClick={again}>
                  {busy ? ACTIONS.key.busy : VERSIONS.enterAgain}
                </Button>
              )}
              {problem && <span class="where-message">{problem}</span>}
            </dd>
            {c.ca_file && (
              <>
                <dt>{VERSIONS.certificate}</dt>
                <dd>
                  <code class="mono">{c.ca_file}</code>
                </dd>
              </>
            )}
          </dl>
        )}
      </div>
    </details>
  );
}

export function ConnectionPage(props: { connection: Connection; onChange: (c: Connection) => void }) {
  const c = props.connection;
  return (
    <>
      <PageHead title={WHERE.title} />
      <div class="where" data-mode={c.mode}>
        <Now connection={c} onChange={props.onChange} />
        <Where connection={c} onChange={props.onChange} />
        <ThisApp connection={c} />
        <Advanced connection={c} onChange={props.onChange} />
      </div>
    </>
  );
}

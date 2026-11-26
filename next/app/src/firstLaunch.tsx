// What the window shows until it is connected to a pluxy.
// - First launch (no place chosen yet): the whole window, no frame: one question and its two
//   answers. "On this computer" starts the pluxy the app runs, with nothing to type; "Connect to a
//   pluxy that is already running" asks for its address, then the native key window asks for its
//   management key.
// - Every later case (stopped, failed, not installed, not signed in, not answering): the app's
//   frame with its destinations shown but not choosable, and the case in the pane (NotRunning).
// The words and the choice of case are in connectionCopy.ts.

import type { ComponentChildren } from "preact";
import { useEffect, useState } from "preact/hooks";
import { DESTINATIONS } from "../../ui/kit/destinations";
import { Frame } from "../../ui/kit/frame";
import { Icon } from "../../ui/kit/icons";
import { StatePane } from "../../ui/kit/parts";
import { rich } from "../../ui/kit/strings";
import type { ThemeState } from "../../ui/kit/theme/state";
import { Notice } from "../../ui/kit/ui";
import { ConnectForm } from "./connectForm";
import { ConnectionPage } from "./connection";
import {
  ACTIONS,
  type Act,
  FIRST,
  FORM,
  PANE,
  paneOf,
  plainError,
  plainReason,
  WHERE,
} from "./connectionCopy";
import { connectionStatus } from "./frameStatus";
import { type Connection, connectionSet, coreAdopt, coreRestart, coreStart, keyEntry } from "./host";

/** A turning mark: something is going on, and the app waits for it. */
export function Spinner() {
  return (
    <span class="spinner" aria-hidden="true">
      <Icon name="refresh" />
    </span>
  );
}

/** The first launch screen: two answers, and nothing else until one is chosen. */
export function FirstLaunch(props: { connection: Connection; onChange: (c: Connection) => void }) {
  const [step, setStep] = useState<"choose" | "connect">("choose");
  const [starting, setStarting] = useState(false);
  const [problem, setProblem] = useState<string>();
  async function here() {
    setStarting(true);
    setProblem(undefined);
    try {
      props.onChange(await connectionSet({ mode: "managed" }));
    } catch (e) {
      setProblem(plainError(String(e)));
      setStarting(false);
    }
  }
  return (
    <main class="first" data-part="first-launch">
      <p class="first-brand">pluxy</p>
      {starting ? (
        <section role="status" aria-live="polite">
          <h1>{FIRST.startingHeading}</h1>
          <p class="first-lead first-waiting">
            <Spinner /> {FIRST.startingAbout}
          </p>
        </section>
      ) : step === "connect" ? (
        <section>
          <h1>{FIRST.there.title}</h1>
          <ConnectForm
            connection={props.connection}
            onChange={props.onChange}
            submit={FORM.continue}
            back={() => setStep("choose")}
          />
        </section>
      ) : (
        <section>
          <h1>{FIRST.heading}</h1>
          <p class="first-lead">{FIRST.lead}</p>
          {props.connection.error && <p class="first-quiet">{FIRST.damaged}</p>}
          {problem && <p class="first-quiet">{problem}</p>}
          <div class="first-choices">
            <div class="first-option">
              <button
                type="button"
                class="btn first-choice"
                data-part="button"
                data-variant="primary"
                onClick={() => void here()}
              >
                <b>{FIRST.here.title}</b>
                <span>{FIRST.here.about}</span>
              </button>
              <p class="first-note">{FIRST.here.note}</p>
            </div>
            <div class="first-option">
              <button
                type="button"
                class="btn first-choice"
                data-part="button"
                data-variant="quiet"
                onClick={() => setStep("connect")}
              >
                <b>{FIRST.there.title}</b>
                <span>{FIRST.there.about}</span>
              </button>
            </div>
          </div>
        </section>
      )}
    </main>
  );
}

// ---- not running ----------------------------------------------------------------------------------------

/** The pane for a connection that cannot show pages: what happened, and the one way on. */
export function NotRunningPane(props: {
  connection: Connection;
  onChange: (c: Connection) => void;
  /** The app runs in safe mode: a link to Add-ons, where a broken add-on is turned off. */
  safeMode?: boolean;
}) {
  const c = props.connection;
  const pane = paneOf(c);
  const [busy, setBusy] = useState<Act>();
  const [problem, setProblem] = useState<string>();
  // The confirmation window of a program to adopt is open: the connection changes when it ends.
  const [asked, setAsked] = useState(false);
  useEffect(() => setAsked(false), [c]);
  if (!pane) return null;
  async function run(act: Act) {
    setBusy(act);
    setProblem(undefined);
    try {
      if (act === "adopt") {
        await coreAdopt();
        setAsked(true);
      } else if (act === "key") {
        const result = await keyEntry();
        if (result.status === "signed_in") props.onChange(result.connection);
        else if (result.status === "wrong_key") setProblem(plainReason("core refused this key"));
        else if (result.status === "error") setProblem(plainError(result.connection.error, c.ca_file));
      } else {
        const next = await (act === "restart" ? coreRestart() : coreStart());
        if (next.error) setProblem(plainError(next.error, c.ca_file));
        props.onChange(next);
      }
    } catch (e) {
      setProblem(plainError(String(e), c.ca_file));
    } finally {
      setBusy(undefined);
    }
  }
  const button = (act: Act) => {
    const label = ACTIONS[act].label;
    if (act === "connect" || act === "where") return { label, href: "#/connection" };
    const waiting = act === "adopt" && asked;
    const working = busy === act || waiting;
    return {
      label: working ? (ACTIONS[act].busy ?? label) : label,
      onClick: () => run(act),
      disabled: busy !== undefined || waiting,
    };
  };
  const failed = pane.tone === "danger";
  const mark = pane.working ? "refresh" : failed ? undefined : pane.key === "signed-out" ? "needsYou" : "off";
  const lines = pane.lines.map((line) =>
    pane.key === "missing" && pane.mono && line === PANE.missingWithProgram ? (
      <p key={line}>{rich(line, { path: <code class="mono">{pane.mono}</code> })}</p>
    ) : (
      <p key={line}>{line}</p>
    ),
  );
  return (
    <div
      class="not-running"
      data-tone={pane.tone}
      data-case={pane.key}
      data-working={pane.working ? "" : undefined}
    >
      <StatePane
        kind={failed ? "failed" : "empty"}
        title={pane.heading}
        mark={mark}
        details={pane.details}
        primary={button(pane.primary)}
        secondary={pane.secondary ? button(pane.secondary) : undefined}
      >
        {pane.mono && pane.key !== "missing" && (
          <p>
            <code class="mono">{pane.mono}</code>
          </p>
        )}
        {lines}
        {problem && problem !== pane.details && <Notice tone="danger">{problem}</Notice>}
      </StatePane>
      {props.safeMode && (
        <p class="not-running-safe">
          {PANE.safeMode} <a href="#/device">{PANE.openAddOns}</a>
        </p>
      )}
    </div>
  );
}

/** The first segment of the address's hash: which of the frame's pages is open. */
function routeOf(hash: string): "connection" | "device" | "state" {
  const first = hash.replace(/^#\/?/, "").split(/[/?]/)[0];
  return first === "connection" || first === "device" ? first : "state";
}

/**
 * A place was chosen and the window cannot show pluxy's pages: the frame stays, its destinations
 * shown but not choosable, and the pane says which case it is. Where pluxy runs, and this device's
 * add-ons, need no pluxy, so they open inside the same frame, with a way back.
 */
export function NotRunning(props: {
  connection: Connection;
  onChange: (c: Connection) => void;
  themes: ThemeState;
  brand: string;
  safeMode?: boolean;
  /** This device's add-ons page, which needs no pluxy. */
  device: ComponentChildren;
}) {
  const c = props.connection;
  const [route, setRoute] = useState(() => routeOf(location.hash));
  useEffect(() => {
    const on = () => setRoute(routeOf(location.hash));
    window.addEventListener("hashchange", on);
    return () => window.removeEventListener("hashchange", on);
  }, []);
  const status = connectionStatus(c, props.onChange);
  return (
    <Frame
      themes={props.themes}
      brand={props.brand}
      destinations={DESTINATIONS.map((id) => ({ id }))}
      status={status}
      title={route === "connection" ? WHERE.title : route === "device" ? "Add-ons" : status.word}
      above={route === "state" ? undefined : [{ label: status.word, href: "#/" }]}
      page={route}
    >
      {route === "connection" ? (
        <ConnectionPage connection={c} onChange={props.onChange} />
      ) : route === "device" ? (
        props.device
      ) : (
        <NotRunningPane connection={c} onChange={props.onChange} safeMode={props.safeMode} />
      )}
    </Frame>
  );
}

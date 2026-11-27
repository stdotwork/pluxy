// What the app puts on the rows of the Tools page and of the guided setup's step 2: the button that
// installs or turns on a tool's add-on (one at a time for each add-on, however often the row is
// drawn again), and, on a tool's page opened from setup, the way back. The words are the app's own
// (A3); the rows, their states and their other words are the kit's.

import type { ComponentChildren } from "preact";
import { useEffect, useState } from "preact/hooks";
import type { Reply } from "../../ui/kit/host";
import { strings } from "../../ui/kit/strings";
import { Button, Notice } from "../../ui/kit/ui";
import { cancelled, sentence } from "./clientPage";
import { type DeviceCatalog, device } from "./device";
import { plainReply, useShown } from "./toolWords";

const t = strings("toolRows", {
  install: "Install add-on",
  installing: "Installing…",
  turnOn: "Turn on add-on",
  answer: "Answer the confirmation window to go on.",
  installFailed: "Could not install the add-on. {reason}",
  turnOnFailed: "Could not turn on the add-on. {reason}",
  checking: "Checking add-ons…",
  settingUp: "Setting up pluxy.",
  backToSetup: "Back to setup",
});

export type AddonStep = "install" | "enable";

interface AddonStatus {
  /** The step that is running now. */
  working?: AddonStep;
  /** The step that failed last, and why. */
  failed?: { step: AddonStep; reason: string };
}

const statuses = new Map<string, AddonStatus>();
const watchers = new Set<() => void>();

function setStatus(pkg: string, status?: AddonStatus) {
  if (status) statuses.set(pkg, status);
  else statuses.delete(pkg);
  for (const watch of [...watchers]) watch();
}

/** What an add-on's install or turn-on is doing, for every row that shows the add-on. */
function useAddonStatus(pkg: string): AddonStatus {
  const [, again] = useState(0);
  useEffect(() => {
    const watch = () => again((n) => n + 1);
    watchers.add(watch);
    return () => {
      watchers.delete(watch);
    };
  }, []);
  return statuses.get(pkg) ?? {};
}

/**
 * Runs one step of an add-on, unless one is running for it already. A person's cancel of the
 * confirmation window ends it quietly; any other failure is kept, with its reason, until the
 * step is pressed again. changed runs once the step is over, so the page reads the device again.
 */
export async function runAddon(
  pkg: string,
  step: AddonStep,
  call: () => Promise<Reply<DeviceCatalog>>,
  changed: () => void,
) {
  if (statuses.get(pkg)?.working) return;
  setStatus(pkg, { working: step });
  let reply: Reply<DeviceCatalog>;
  try {
    reply = plainReply(await call());
  } catch (e) {
    reply = { status: 0, error: { code: "native", message: String(e) } };
  }
  const message = reply.error?.message;
  if (message === undefined || cancelled(message)) setStatus(pkg);
  else setStatus(pkg, { failed: { step, reason: message } });
  changed();
}

/** Where the add-on's step stands, for a test. */
export const addonStatus = (pkg: string): AddonStatus => statuses.get(pkg) ?? {};

/** The button at the end of a tool's row that moves its add-on on, with the line under the row. */
export function AddonAction(props: {
  tool: string;
  pkg: string;
  step: AddonStep;
  /** Where an install comes from. */
  registry?: string;
  version?: string;
  changed: () => void;
}) {
  const status = useAddonStatus(props.pkg);
  const shown = useShown();
  const working = status.working;
  const label =
    props.step === "install" ? (working === "install" ? t("installing") : t("install")) : t("turnOn");
  // Nothing is returned: the button's own busy mark would sit on the words "Installing…".
  const press = () => {
    void runAddon(
      props.pkg,
      props.step,
      props.step === "install"
        ? () => device.install(props.registry ?? "", props.pkg, props.version)
        : () => device.setEnabled(props.pkg, true),
      props.changed,
    );
  };
  const failed = status.failed?.step === props.step ? status.failed.reason : undefined;
  return (
    <>
      <span
        class="addon-action"
        data-part="addon-action"
        data-row={props.tool}
        data-package={props.pkg}
        data-step={props.step}
      >
        <Button variant="quiet" disabled={!!working} onClick={press}>
          {label}
        </Button>
      </span>
      {working === "enable" && (
        <span class="addon-note muted" data-part="addon-note" role="status">
          {t("answer")}
        </span>
      )}
      {failed !== undefined && !working && (
        <span class="addon-note" data-part="addon-note" data-tone="danger" role="alert">
          {t(props.step === "install" ? "installFailed" : "turnOnFailed", {
            reason: sentence(shown.text(failed)),
          })}
        </span>
      )}
    </>
  );
}

/** What stands in place of a button while the app still reads which add-ons it has or is offered. */
export const Checking = () => (
  <span class="muted addon-checking" data-part="addon-checking">
    {t("checking")}
  </span>
);

// ---- the way back to setup ---------------------------------------------------------------------

let fromSetup = false;

/** A tool's page was opened from setup's step 2: its page shows the way back. */
export const openedFromSetup = () => {
  fromSetup = true;
};

/** Forgets that: on setup itself, and once the person leaves the tool's page. */
export const leftSetup = () => {
  fromSetup = false;
};

/** Whether the tool page being opened was opened from setup. */
export const cameFromSetup = () => fromSetup;

/**
 * The strip above a tool's page opened from setup, with the way back, so the person returns to
 * step 3 once the tool is connected. It is only in memory: leaving the page forgets it.
 */
export function SetupReturn(): ComponentChildren {
  const [from] = useState(cameFromSetup);
  useEffect(() => {
    const onHash = () => {
      if (location.hash.replace(/^#\/?/, "") === "setup") leftSetup();
    };
    window.addEventListener("hashchange", onHash);
    return () => {
      window.removeEventListener("hashchange", onHash);
      leftSetup();
    };
  }, []);
  if (!from) return null;
  return (
    <div data-part="setup-return">
      <Notice tone="muted">
        {t("settingUp")} <a href="#/setup">{t("backToSetup")}</a>
      </Notice>
    </div>
  );
}

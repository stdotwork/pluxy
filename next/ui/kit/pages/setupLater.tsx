// What "Finish later" leaves behind: a note in the host's prefs, kept per pluxy (its address), and
// while it is there a card at the top of Home that says what setup still has to do and leads back
// to it. Finishing setup any other way, or Hide, takes the note away. Core keeps only whether the
// first run ended, so the note is this device's.

import { useEffect, useState } from "preact/hooks";
import { runningExtensions } from "../api";
import type { Host } from "../host";
import { Icon } from "../icons";
import { strings } from "../strings";
import { Button, useKit } from "../ui";
import { canServe, connectedTools } from "./setup.helpers";

const t = strings("setupLater", {
  title: "Finish setting up pluxy",
  left: { one: "{count} step left.", other: "{count} steps left." },
  step1: "Your AI accounts",
  step2: "Your coding tool",
  step3: "Check it works",
  done: "done",
  todo: "to do",
  resume: "Continue setup",
  hide: "Hide",
});

const PREF = "setup-later";

/** The note's value: the pluxy it is about, so another pluxy's Home does not show it. */
const about = (host: Host) => host.address ?? "this";

export function rememberLater(host: Host) {
  host.prefs.set(PREF, about(host));
}

export function forgetLater(host: Host) {
  host.prefs.set(PREF, null);
}

/** Whether the person left setup with Finish later on this pluxy, and has not hidden the card since. */
export function laterPending(host: Host): boolean {
  return host.prefs.get(PREF) === about(host);
}

/**
 * Which of setup's steps are done, as Home can tell: an account that can take requests, a tool
 * that uses pluxy. Whether a test passed is not kept, so the third is never done here; finishing
 * through it takes the card away.
 */
export function stepsDone(input: {
  accounts: Parameters<typeof canServe>[0][];
  running?: ReadonlySet<string>;
  setup?: Parameters<typeof connectedTools>[0];
}): [boolean, boolean, boolean] {
  return [
    input.accounts.some((a) => canServe(a, input.running)),
    connectedTools(input.setup).length > 0,
    false,
  ];
}

/** Home's card while setup was left for later: the three steps, what is left, and the way back. */
export function SetupLaterCard() {
  const { host, api } = useKit();
  const [pending, setPending] = useState(() => laterPending(host));
  const [done, setDone] = useState<[boolean, boolean, boolean]>();
  useEffect(() => {
    if (!pending) return;
    let live = true;
    Promise.all([api.setup(), api.accounts(), api.status()]).then(([setup, accounts, status]) => {
      if (!live) return;
      // A pluxy that cannot say (an older one, or none answering) gets no card.
      if (!setup.body || !accounts.body) return;
      setDone(
        stepsDone({
          accounts: accounts.body.accounts,
          running: runningExtensions(status.body),
          setup: setup.body,
        }),
      );
    });
    return () => {
      live = false;
    };
  }, [pending, api]);
  if (!pending || !done) return null;
  const left = done.filter((d) => !d).length;
  const titles = [t("step1"), t("step2"), t("step3")];
  return (
    <section class="card setup-later-card" data-part="setup-later" aria-labelledby="setup-later-title">
      <div class="setup-later-text">
        <h2 id="setup-later-title">{t("title")}</h2>
        <p class="muted">{t("left", { count: left })}</p>
      </div>
      <ol class="setup-later-steps">
        {titles.map((title, i) => (
          <li key={title} data-state={done[i] ? "done" : "todo"}>
            <span class="setup-later-mark" aria-hidden="true">
              {done[i] ? <Icon name="check" size={12} /> : i + 1}
            </span>
            {title}
            <span class="sr-only"> ({done[i] ? t("done") : t("todo")})</span>
          </li>
        ))}
      </ol>
      <div class="actions setup-later-actions">
        <a class="btn" data-part="button" data-variant="quiet" href="#/setup">
          {t("resume")}
        </a>
        <Button
          variant="ghost"
          onClick={() => {
            forgetLater(host);
            setPending(false);
          }}
        >
          {t("hide")}
        </Button>
      </div>
    </section>
  );
}

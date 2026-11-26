// What closing the window does, with the old app's labels: the Close behavior setting on the
// Connection page, and the prompts closing shows. The native side decides (window.rs) and calls
// window.__pluxyClosePrompt to show a prompt: with no argument when the choice is Ask every time,
// with "no-tray" when there is no tray and quitting would stop the app's own pluxy. The answer goes
// back through window_close with the prompt's kind. Minimize to Tray destroys the window and its
// webviews; the tray opens a new one.

import { useEffect, useLayoutEffect, useRef, useState } from "preact/hooks";
import { Button, Card, Choices, Notice, Switch } from "../../ui/kit/ui";
import {
  type CloseBehavior,
  type ClosePromptKind,
  type WindowPreferences,
  windowClose,
  windowGet,
  windowSet,
} from "./host";

const CHOICES = [
  { value: "ask" as const, label: "Ask every time" },
  { value: "minimize-to-tray" as const, label: "Minimize to tray" },
  { value: "exit" as const, label: "Exit app" },
];

export function WindowSettings() {
  const [prefs, setPrefs] = useState<WindowPreferences>();
  const [choice, setChoice] = useState<CloseBehavior>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const [saved, setSaved] = useState(false);
  useEffect(() => {
    let live = true;
    windowGet().then(
      (p) => {
        if (!live) return;
        setPrefs(p);
        setChoice(p.close_behavior);
      },
      (e) => {
        if (live) setError(String(e));
      },
    );
    return () => {
      live = false;
    };
  }, []);
  async function save() {
    if (!choice) return;
    setBusy(true);
    setError(undefined);
    setSaved(false);
    try {
      const p = await windowSet(choice);
      setPrefs(p);
      setChoice(p.close_behavior);
      setSaved(true);
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <Card title="Window">
      <form
        data-part="window-preferences"
        aria-label="Window preferences"
        onSubmit={(e) => {
          e.preventDefault();
          void save();
        }}
      >
        <fieldset disabled={busy || !prefs}>
          <Choices
            label="Close behavior"
            value={choice}
            choices={CHOICES}
            onChange={(v) => {
              setChoice(v);
              setSaved(false);
            }}
          />
          <p class="hint">
            Choose whether closing the window exits the app or keeps it running in the system tray.
          </p>
          <Button type="submit" variant="primary" disabled={!prefs || choice === prefs.close_behavior}>
            {busy ? "Saving…" : "Save"}
          </Button>
        </fieldset>
        {prefs && !prefs.tray && (
          <div data-part="no-tray">
            <Notice tone="warn">
              No system tray found, so closing the window quits the app.
              {prefs.ask_without_tray &&
                " When the app runs pluxy itself, it asks first, because quitting stops pluxy."}
            </Notice>
          </div>
        )}
        {error && <Notice tone="danger">{error}</Notice>}
        {saved && <Notice tone="ok">Saved.</Notice>}
      </form>
    </Card>
  );
}

declare global {
  interface Window {
    /** Shows the close prompt; the native side calls it when the choice is Ask every time, and
     * with "no-tray" when there is no tray and closing would quit and stop the app's own pluxy.
     * A prompt that does not answer true within 3 s: Ask's closes the window to the tray; the
     * no-tray one leaves the window open. */
    __pluxyClosePrompt?: (kind?: "no-tray") => boolean;
  }
}

/** What closing asks: "What would you like to do?" when the choice is Ask, and "Close and stop
 * pluxy?" when there is no tray in managed mode, where Keep open (or Escape) sends nothing. */
export function ClosePrompt() {
  const dialog = useRef<HTMLDialogElement>(null);
  const [open, setOpen] = useState(false);
  const [kind, setKind] = useState<ClosePromptKind>("ask");
  const [remember, setRemember] = useState(false);
  const [busy, setBusy] = useState<CloseBehavior>();
  const [error, setError] = useState<string>();
  // A layout effect, so the handler is set in the same render that puts the window's content on
  // the page. Preact runs a plain effect later, after the next frame or a 35 ms timer; a close
  // request in between found no handler and closed the window to the tray without asking
  // (lib.rs ask_on_close).
  useLayoutEffect(() => {
    window.__pluxyClosePrompt = (asked) => {
      setKind(asked === "no-tray" ? "no-tray" : "ask");
      setOpen(true);
      return true;
    };
    return () => {
      delete window.__pluxyClosePrompt;
    };
  }, []);
  useEffect(() => {
    const d = dialog.current;
    if (!d) return;
    if (open && !d.open) d.showModal();
    if (!open && d.open) d.close();
  }, [open]);
  function dismiss() {
    setOpen(false);
    setRemember(false);
    setError(undefined);
  }
  async function choose(action: Exclude<CloseBehavior, "ask">) {
    setBusy(action);
    setError(undefined);
    try {
      await windowClose(action, remember, kind);
      dismiss();
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(undefined);
    }
  }
  const choice = (action: Exclude<CloseBehavior, "ask">, variant: string, label: string, doing: string) => (
    <button
      type="button"
      class="btn"
      data-part="button"
      data-variant={variant}
      data-choice={action}
      disabled={busy !== undefined}
      onClick={() => void choose(action)}
    >
      {busy === action ? doing : label}
    </button>
  );
  const cancel = (label: string) => (
    <button
      type="button"
      class="btn"
      data-part="button"
      data-variant={kind === "no-tray" ? "primary" : "quiet"}
      data-choice="cancel"
      disabled={busy !== undefined}
      onClick={dismiss}
    >
      {label}
    </button>
  );
  const title = kind === "no-tray" ? "Close and stop pluxy?" : "What would you like to do?";
  return (
    <dialog
      ref={dialog}
      data-part="close-prompt"
      data-kind={kind}
      aria-label={title}
      style={{ padding: 0, border: "none", background: "transparent", maxWidth: "30rem" }}
      onCancel={(e) => {
        e.preventDefault();
        if (busy === undefined) dismiss();
      }}
    >
      {kind === "no-tray" ? (
        <Card title={title}>
          <p>
            This desktop has no tray, so closing the window stops pluxy, and your coding tools stop getting
            answers.
          </p>
          {error && <Notice tone="danger">{error}</Notice>}
          <Switch label="Don't ask again" checked={remember} onChange={setRemember} />
          <p class="actions">
            {cancel("Keep open")} {choice("exit", "danger", "Close and stop pluxy", "Closing…")}
          </p>
        </Card>
      ) : (
        <Card title={title}>
          <p>Exit the app, or keep pluxy running in the system tray.</p>
          {error && <Notice tone="danger">{error}</Notice>}
          <Switch label="Remember this choice" checked={remember} onChange={setRemember} />
          <p class="actions">
            {choice("minimize-to-tray", "primary", "Minimize to tray", "Minimizing…")}{" "}
            {choice("exit", "danger", "Exit app", "Exiting…")} {cancel("Cancel")}
          </p>
        </Card>
      )}
    </dialog>
  );
}

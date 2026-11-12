// What closing the window does, with the old app's labels: the Close behavior setting on the
// Connection page, and the prompt that Ask every time shows. The native side decides (window.rs)
// and, when the choice is Ask every time, calls window.__pluxyClosePrompt to show the prompt;
// the answer goes back through window_close. Minimize to Tray destroys the window and its
// webviews; the tray opens a new one.

import { useEffect, useLayoutEffect, useRef, useState } from "preact/hooks";
import { Button, Card, Choices, Notice, Switch } from "../../ui/kit/ui";
import { type CloseBehavior, type WindowPreferences, windowClose, windowGet, windowSet } from "./host";

const CHOICES = [
  { value: "ask" as const, label: "Ask every time" },
  { value: "minimize-to-tray" as const, label: "Minimize to Tray" },
  { value: "exit" as const, label: "Exit App" },
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
            <Notice tone="warn">No system tray was found, so closing the window quits pluxy.</Notice>
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
    /** Shows the close prompt; the native side calls it when the choice is Ask every time. */
    __pluxyClosePrompt?: () => boolean;
  }
}

/** "What would you like to do?" when the person closes the window and the choice is Ask. */
export function ClosePrompt() {
  const dialog = useRef<HTMLDialogElement>(null);
  const [open, setOpen] = useState(false);
  const [remember, setRemember] = useState(false);
  const [busy, setBusy] = useState<CloseBehavior>();
  const [error, setError] = useState<string>();
  // A layout effect runs in the same task as the first render, so the window never shows its
  // shell without being able to ask. A plain effect runs after a paint, and a close in between
  // went to the tray without asking.
  useLayoutEffect(() => {
    window.__pluxyClosePrompt = () => {
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
      await windowClose(action, remember);
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
  return (
    <dialog
      ref={dialog}
      data-part="close-prompt"
      aria-label="What would you like to do?"
      style={{ padding: 0, border: "none", background: "transparent", maxWidth: "30rem" }}
      onCancel={(e) => {
        e.preventDefault();
        if (busy === undefined) dismiss();
      }}
    >
      <Card title="What would you like to do?">
        <p>Exit the app, or keep pluxy running in the system tray.</p>
        {error && <Notice tone="danger">{error}</Notice>}
        <Switch label="Remember this choice" checked={remember} onChange={setRemember} />
        <p class="actions">
          {choice("minimize-to-tray", "primary", "Minimize to Tray", "Minimizing…")}{" "}
          {choice("exit", "danger", "Exit App", "Exiting…")}{" "}
          <button
            type="button"
            class="btn"
            data-part="button"
            data-variant="quiet"
            data-choice="cancel"
            disabled={busy !== undefined}
            onClick={dismiss}
          >
            Cancel
          </button>
        </p>
      </Card>
    </dialog>
  );
}

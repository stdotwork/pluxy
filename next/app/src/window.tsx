// What closing the window does: the setting on Where pluxy runs, and the prompts closing shows. The
// native side decides (window.rs) and calls window.__pluxyClosePrompt to show a prompt: with no
// argument when the choice is Ask, with "no-tray" when there is no tray and quitting would stop the
// app's own pluxy. The answer goes back through window_close with the prompt's kind. Keeping the
// app in the tray destroys the window and its webviews; the tray opens a new one.

import { useEffect, useLayoutEffect, useRef, useState } from "preact/hooks";
import { SettingRow, useSetting } from "../../ui/kit/parts";
import { Card, Choices, Notice, Switch } from "../../ui/kit/ui";
import {
  type CloseBehavior,
  type ClosePromptKind,
  type Connection,
  connectionGet,
  type WindowPreferences,
  windowClose,
  windowGet,
  windowSet,
} from "./host";
import { Unread } from "./startup";

export const CLOSING = {
  setting: "When you close the window",
  keep: "Keep in the tray",
  quit: "Quit",
  quitStops: "Quit (stops pluxy)",
  ask: "Ask",
  cancel: "Cancel",
  aboutManaged: "Keep in the tray keeps your coding tools working. Quit stops pluxy.",
  aboutOther: "pluxy keeps running either way; this only decides whether the app stays in the tray.",
  noTray: "This desktop has no tray, so closing the window quits the app.",
  noTrayManaged: " It asks first, because that stops pluxy.",
  notRead: "The app could not read this setting. {error}",
  askTitle: "Keep pluxy in the tray?",
  askAboutOther: "pluxy keeps running either way.",
  remember: "Remember this choice",
  stopTitle: "Close and stop pluxy?",
  stopAbout:
    "This desktop has no tray, so closing the window stops pluxy, and your coding tools stop getting answers.",
  dontAsk: "Don't ask again",
  keepOpen: "Keep open",
  closeAndStop: "Close and stop pluxy",
};

const CHOICES = [
  { value: "minimize-to-tray" as const, label: CLOSING.keep },
  { value: "exit" as const, label: CLOSING.quit },
  { value: "ask" as const, label: CLOSING.ask },
];

/** What the native side says about closing, read once; reload reads it again. */
export function useWindowPrefs() {
  const [prefs, setPrefs] = useState<WindowPreferences>();
  const [error, setError] = useState<string>();
  const live = useRef(true);
  const load = () => {
    setError(undefined);
    windowGet().then(
      (p) => live.current && setPrefs(p),
      (e) => live.current && setError(String(e)),
    );
  };
  useEffect(() => {
    live.current = true;
    load();
    return () => {
      live.current = false;
    };
  }, []);
  return { prefs, setPrefs, error, load };
}

/** When you close the window: Keep in the tray, Quit or Ask. With no tray there is nothing to choose. */
export function CloseRow(props: { managed: boolean; window: ReturnType<typeof useWindowPrefs> }) {
  const { prefs, setPrefs, error, load } = props.window;
  const setting = useSetting<CloseBehavior | undefined>(prefs?.close_behavior, async (choice) => {
    if (!choice) return undefined;
    const reply = await windowSet(choice);
    setPrefs(reply);
    return undefined;
  });
  const reason =
    prefs && !prefs.tray
      ? CLOSING.noTray + (props.managed && prefs.ask_without_tray ? CLOSING.noTrayManaged : "")
      : undefined;
  const row = (
    <SettingRow
      name={CLOSING.setting}
      description={props.managed ? CLOSING.aboutManaged : CLOSING.aboutOther}
      saving={setting.saving}
      error={error !== undefined ? CLOSING.notRead.replace("{error}", error) : setting.error}
      disabled={reason}
    >
      {prefs ? (
        <Choices
          label={CLOSING.setting}
          value={setting.saving ? setting.value : prefs.close_behavior}
          choices={CHOICES}
          onChange={(v) => void setting.set(v)}
        />
      ) : (
        <Unread error={error} retry={load} />
      )}
    </SettingRow>
  );
  // Once the native side has answered, the row is findable as the window preferences (the native
  // window test picks the choice in it, and reads what it says when there is no tray).
  return prefs ? <div data-part="window-preferences">{row}</div> : row;
}

declare global {
  interface Window {
    /** Shows the close prompt; the native side calls it when the choice is Ask, and with "no-tray"
     * when there is no tray and closing would quit and stop the app's own pluxy. A prompt that does
     * not answer true within 3 s: Ask's closes the window to the tray; the no-tray one leaves the
     * window open. */
    __pluxyClosePrompt?: (kind?: "no-tray") => boolean;
  }
}

/** What closing asks: "Keep pluxy in the tray?" when the choice is Ask, and "Close and stop pluxy?"
 * when there is no tray in managed mode, where Keep open (or Escape) sends nothing. */
export function ClosePrompt() {
  const dialog = useRef<HTMLDialogElement>(null);
  const [open, setOpen] = useState(false);
  const [kind, setKind] = useState<ClosePromptKind>("ask");
  const [remember, setRemember] = useState(false);
  const [busy, setBusy] = useState<CloseBehavior>();
  const [error, setError] = useState<string>();
  // Whether the app runs pluxy itself, which decides what Quit says it stops. Read when the prompt
  // opens; until it is known the words claim nothing.
  const [managed, setManaged] = useState<boolean>();
  const readMode = () =>
    connectionGet().then(
      (c: Connection | null) => setManaged(c?.mode === "managed"),
      () => {},
    );
  useEffect(() => {
    void readMode();
  }, []);
  // A layout effect, so the handler is set in the same render that puts the window's content on
  // the page. Preact runs a plain effect later, after the next frame or a 35 ms timer; a close
  // request in between found no handler and closed the window to the tray without asking
  // (lib.rs ask_on_close).
  useLayoutEffect(() => {
    window.__pluxyClosePrompt = (asked) => {
      setKind(asked === "no-tray" ? "no-tray" : "ask");
      setOpen(true);
      void readMode();
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
  const choice = (action: Exclude<CloseBehavior, "ask">, variant: string, label: string) => (
    <button
      type="button"
      class="btn"
      data-part="button"
      data-variant={variant}
      data-choice={action}
      disabled={busy !== undefined}
      onClick={() => void choose(action)}
    >
      {label}
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
  const title = kind === "no-tray" ? CLOSING.stopTitle : CLOSING.askTitle;
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
          <p>{CLOSING.stopAbout}</p>
          {error && <Notice tone="danger">{error}</Notice>}
          <Switch label={CLOSING.dontAsk} checked={remember} onChange={setRemember} />
          <p class="actions">
            {cancel(CLOSING.keepOpen)} {choice("exit", "danger", CLOSING.closeAndStop)}
          </p>
        </Card>
      ) : (
        <Card title={title}>
          {managed !== undefined && <p>{managed ? CLOSING.aboutManaged : CLOSING.askAboutOther}</p>}
          {error && <Notice tone="danger">{error}</Notice>}
          <Switch label={CLOSING.remember} checked={remember} onChange={setRemember} />
          <p class="actions">
            {choice("minimize-to-tray", "primary", CLOSING.keep)}{" "}
            {choice("exit", "danger", managed ? CLOSING.quitStops : CLOSING.quit)} {cancel(CLOSING.cancel)}
          </p>
        </Card>
      )}
    </dialog>
  );
}

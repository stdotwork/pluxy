// The app's own startup settings, as rows on Where pluxy runs: each saves when it is changed, says
// Saving… while it does, and goes back with the reason when the save fails. A row that does
// nothing in the current mode is not drawn. Until the native side has answered no control is drawn,
// so a row never shows a value nobody read.

import { useEffect, useRef, useState } from "preact/hooks";
import { SettingRow, useSetting } from "../../ui/kit/parts";
import { Button, Notice, Switch } from "../../ui/kit/ui";
import { type StartupPreferences, type StartupView, startupGet, startupSet } from "./host";

export const STARTUP = {
  signIn: "Open at sign-in",
  signInAbout: "Opens this app when you sign in to the computer.",
  conflict: "Another program owns the sign-in entry, so the app leaves it alone.",
  localBuild: "This local build opens only with its start command, not at sign-in.",
  tray: "Start in the tray",
  trayAbout: "At sign-in, starts in the tray instead of opening this window.",
  trayNeedsSignIn: "Turn on Open at sign-in first.",
  noTray: "This desktop has no tray.",
  withApp: "Start pluxy with the app",
  withAppAbout: "Starts pluxy when the app opens. Takes effect the next time it opens.",
  reading: "Reading…",
  notRead: "The app could not read this setting. {error}",
  tryAgain: "Try again",
};

const preferencesOf = (v: StartupView): StartupPreferences => ({
  autostart_enabled: v.autostart_enabled,
  silent_start_enabled: v.silent_start_enabled,
  start_core_on_launch: v.start_core_on_launch,
});

/** What a row of settings that is still being read, or could not be read, shows in place of its control. */
export function Unread(props: { error?: string; retry: () => void }) {
  return props.error === undefined ? (
    <span class="setting-pending">{STARTUP.reading}</span>
  ) : (
    <Button onClick={props.retry}>{STARTUP.tryAgain}</Button>
  );
}

/** Open at sign-in, Start in the tray and Start pluxy with the app. tray is whether the desktop has one, when known. */
export function StartupRows(props: { managed: boolean; tray?: boolean }) {
  const [view, setView] = useState<StartupView>();
  const [readError, setReadError] = useState<string>();
  const live = useRef(true);
  const load = () => {
    setReadError(undefined);
    startupGet().then(
      (v) => live.current && setView(v),
      (e) => live.current && setReadError(String(e)),
    );
  };
  useEffect(() => {
    live.current = true;
    load();
    return () => {
      live.current = false;
    };
  }, []);
  // The row last changed, which shows Saving… and a failed save's reason.
  const [touched, setTouched] = useState<keyof StartupPreferences>();
  const saving = useSetting<StartupView | undefined>(view, async (next) => {
    if (!next) return undefined;
    const reply = await startupSet(preferencesOf(next));
    if (live.current) setView(reply);
    return undefined;
  });
  const shown = saving.saving ? saving.value : view;
  const change = (key: keyof StartupPreferences, value: boolean) => {
    if (!shown) return;
    setTouched(key);
    const next = { ...shown, [key]: value };
    // The tray choice means nothing without the sign-in entry.
    if (key === "autostart_enabled" && !value) next.silent_start_enabled = false;
    void saving.set(next);
  };
  const row = (key: keyof StartupPreferences, name: string, description: string, disabled?: string) => (
    <SettingRow
      name={name}
      description={description}
      saving={saving.saving && touched === key}
      error={
        readError !== undefined
          ? STARTUP.notRead.replace("{error}", readError)
          : touched === key
            ? saving.error
            : undefined
      }
      disabled={disabled}
    >
      {shown ? (
        <Switch label={name} checked={shown[key]} onChange={(v) => change(key, v)} />
      ) : (
        <Unread error={readError} retry={load} />
      )}
    </SettingRow>
  );
  const trayReason =
    props.tray === false
      ? STARTUP.noTray
      : shown?.autostart_enabled === false
        ? STARTUP.trayNeedsSignIn
        : undefined;
  // A local build offers no sign-in entry, so neither choice that needs one is drawn.
  const signIn = shown?.sign_in !== false;
  return (
    <>
      {signIn ? (
        row("autostart_enabled", STARTUP.signIn, STARTUP.signInAbout)
      ) : (
        <SettingRow name={STARTUP.signIn} description={STARTUP.localBuild}>
          {null}
        </SettingRow>
      )}
      {shown?.registration_conflict && <Notice tone="warn">{STARTUP.conflict}</Notice>}
      {props.managed && signIn && row("silent_start_enabled", STARTUP.tray, STARTUP.trayAbout, trayReason)}
      {props.managed && row("start_core_on_launch", STARTUP.withApp, STARTUP.withAppAbout)}
    </>
  );
}

// The Keys section's management key: Replace key, which asks first, and what came of it. The new
// key shows once, in the web panel only: the desktop app replaces it natively, shows it in a
// window of its own and never gives it to the page.

import { useEffect, useRef, useState } from "preact/hooks";
import { problem } from "../api";
import { SettingRow } from "../parts";
import { usePrivacy } from "../privacy";
import { rich, strings } from "../strings";
import { Button, ConfirmButton, Notice, useCopy, useKit } from "../ui";

const t = strings("configKeys", {
  management: "Management key",
  managementAbout:
    "Replace it if someone else may have seen it. Every other window and command that uses the old key must sign in again.",
  replace: "Replace key",
  confirmReplace: "Yes, replace key",
  unavailable: "Replace it with {command} on the computer pluxy runs on.",
  newManagement: "New management key",
  newManagementNote: "This browser already uses it. Copy it now: it is not shown again.",
  replacedApart: "Replaced. The app uses the new key and showed it once in its own window.",
  noAnswer:
    "pluxy did not answer, so the key may have been replaced. Sign in again with the old key. If it is refused, run {command} on the computer pluxy runs on to see the new one.",
  once: "Copy this key now. pluxy hides it once you close this notice or leave the tab.",
  copy: "Copy",
  copied: "Copied",
  close: "Close",
  copyFailed: "Could not copy. Select the key and copy it manually.",
});

/** The command that shows the management key on pluxy's computer, and the one that replaces it. */
const SHOW_KEY = "pluxy key management --show-key";
const ROTATE_KEY = "pluxy key rotate-management";

/** What the last Replace key came to. */
export type Replaced =
  | { kind: "key"; id: number; value: string; seen: boolean }
  | { kind: "apart"; warning?: string }
  | { kind: "noAnswer" }
  | { kind: "failed"; error: string };

/**
 * Replace key, owned by the page, so a reply that comes after leaving the Settings tab is still
 * shown on coming back. onReplaced runs once core has a new key (the settings file changed).
 */
export function useReplaceKey(onReplaced: () => void) {
  const { api, unauthorized } = useKit();
  const [outcome, setOutcome] = useState<Replaced>();
  const next = useRef(0);
  const replace = async () => {
    setOutcome(undefined);
    const reply = await api.rotateManagementKey();
    if (reply.status === 401) return unauthorized();
    if (reply.status === 0) return setOutcome({ kind: "noAnswer" });
    const why = problem(reply);
    if (why !== undefined) return setOutcome({ kind: "failed", error: why });
    // The app's native confirmation answers 204 with nothing when the person cancels it.
    if (!reply.body) return;
    const value = reply.body.value;
    setOutcome(
      value
        ? { kind: "key", id: ++next.current, value, seen: false }
        : { kind: "apart", warning: reply.error?.message },
    );
    onReplaced();
  };
  return {
    outcome,
    replace,
    /** The key notice was shown. */
    seen: () => setOutcome((o) => (o?.kind === "key" ? { ...o, seen: true } : o)),
    close: () => setOutcome(undefined),
    /** Leaving the tab: a key that was shown goes; one not shown yet waits for the next visit. */
    left: () => setOutcome((o) => (o?.kind === "key" && o.seen ? undefined : o)),
  };
}
export type ReplaceKey = ReturnType<typeof useReplaceKey>;

/**
 * A new key, shown once: its value, Copy and Close. note says what to do with it (by default,
 * copy it before closing). The Setup page shows a new client key with it too.
 */
export function KeyNotice(props: {
  title: string;
  value: string;
  note?: string;
  close: () => void;
  seen: () => void;
}) {
  const { state, copy } = useCopy(props.value);
  const ref = useRef<HTMLElement>(null);
  useEffect(() => {
    ref.current?.focus();
    props.seen();
  }, []);
  return (
    <section
      ref={ref}
      tabIndex={-1}
      class="stack config-key-notice"
      data-part="key-notice"
      aria-label={props.title}
    >
      <strong>{props.title}</strong>
      <span>{props.note ?? t("once")}</span>
      <code data-part="key-value">{props.value}</code>
      <div class="actions">
        <Button onClick={copy}>{t("copy")}</Button>
        <Button onClick={props.close}>{t("close")}</Button>
        {state === "copied" && <span role="status">{t("copied")}</span>}
      </div>
      {state === "failed" && <Notice tone="danger">{t("copyFailed")}</Notice>}
    </section>
  );
}

const command = (c: string) => <code class="mono">{c}</code>;

/** The management key's row, and under it what the last Replace key came to. */
export function ManagementKey(props: { state: ReplaceKey; configKey?: string; disabled?: string }) {
  const { host } = useKit();
  const p = usePrivacy();
  const can = host.features.has("rotateManagementKey");
  const o = props.state.outcome;
  const why = props.disabled ?? (can ? undefined : t("unavailable", { command: ROTATE_KEY }));
  return (
    <>
      <SettingRow
        name={t("management")}
        description={t("managementAbout")}
        configKey={props.configKey}
        disabled={why}
      >
        <ConfirmButton
          variant="quiet"
          confirmLabel={t("confirmReplace")}
          disabled={!!why || o?.kind === "noAnswer" || o?.kind === "key"}
          onConfirm={props.state.replace}
        >
          {t("replace")}
        </ConfirmButton>
      </SettingRow>
      {o?.kind === "key" && (
        <KeyNotice
          key={o.id}
          title={t("newManagement")}
          note={t("newManagementNote")}
          value={o.value}
          close={props.state.close}
          seen={props.state.seen}
        />
      )}
      {o?.kind === "apart" && <Notice tone="ok">{t("replacedApart")}</Notice>}
      {o?.kind === "apart" && o.warning && <Notice tone="warn">{p.text(o.warning)}</Notice>}
      {o?.kind === "noAnswer" && (
        <Notice tone="warn">
          {rich(t("noAnswer", { command: "{command}" }), { command: command(SHOW_KEY) })}
        </Notice>
      )}
      {o?.kind === "failed" && <Notice tone="danger">{p.text(o.error)}</Notice>}
    </>
  );
}

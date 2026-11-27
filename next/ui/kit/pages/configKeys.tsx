// The Keys section's management key: Replace key, which asks first, and what came of it. The new
// key shows once, in the web panel only: the desktop app replaces it natively, shows it in a
// window of its own and never gives it to the page.

import { useEffect, useRef, useState } from "preact/hooks";
import { problem } from "../api";
import type { Host } from "../host";
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

/** What the page does when Replace key comes to something: core has a new key, or refused with 422. */
interface ReplaceEvents {
  replaced: () => void;
  refused: () => void;
}

/**
 * Replace key's state for one host, kept outside the page: a reply that comes after the person
 * left Config (the only showing of a new key, in the panel) waits there for their next visit.
 */
interface KeyStore {
  pending: boolean;
  outcome?: Replaced;
  next: number;
  /** The open page's, while one is open. */
  events?: ReplaceEvents;
  listeners: Set<() => void>;
}
const stores = new WeakMap<Host, KeyStore>();
function keyStore(host: Host): KeyStore {
  let store = stores.get(host);
  if (!store) {
    store = { pending: false, next: 0, listeners: new Set() };
    stores.set(host, store);
  }
  return store;
}

/**
 * Replace key. Its state outlives the page, so a reply that comes after leaving the Settings tab,
 * or Config itself, is still shown on coming back. events.replaced runs once core has a new key
 * (the settings file changed), events.refused when core refused with 422 (the file may not parse).
 */
export function useReplaceKey(events: ReplaceEvents) {
  const { host, api, unauthorized } = useKit();
  const store = keyStore(host);
  store.events = events;
  const mine = useRef(events);
  mine.current = events;
  const [, redraw] = useState(0);
  useEffect(() => {
    const listener = () => redraw((n) => n + 1);
    store.listeners.add(listener);
    return () => {
      store.listeners.delete(listener);
      if (store.events === mine.current) store.events = undefined;
    };
  }, [store]);
  const update = (change: (s: KeyStore) => Partial<KeyStore>) => {
    Object.assign(store, change(store));
    for (const listener of store.listeners) listener();
  };
  const settle = (outcome: Replaced | undefined) => update(() => ({ pending: false, outcome }));
  const replace = async () => {
    update(() => ({ pending: true, outcome: undefined }));
    const reply = await api.rotateManagementKey();
    if (reply.status === 401) {
      settle(undefined);
      return unauthorized();
    }
    if (reply.status === 0) return settle({ kind: "noAnswer" });
    const why = problem(reply);
    if (why !== undefined) {
      settle({ kind: "failed", error: why });
      if (reply.status === 422) store.events?.refused();
      return;
    }
    // The app's native confirmation answers 204 with nothing when the person cancels it.
    if (!reply.body) return settle(undefined);
    const value = reply.body.value;
    settle(
      value
        ? { kind: "key", id: ++store.next, value, seen: false }
        : { kind: "apart", warning: reply.error?.message },
    );
    store.events?.replaced();
  };
  return {
    outcome: store.outcome,
    /** A replacement is on its way. */
    pending: store.pending,
    replace,
    /** The key notice was shown. */
    seen: () =>
      update((s) => ({ outcome: s.outcome?.kind === "key" ? { ...s.outcome, seen: true } : s.outcome })),
    close: () => update(() => ({ outcome: undefined })),
    /** Leaving the tab or the page: a key that was shown goes; one not shown yet waits for the next visit. */
    left: () =>
      update((s) => ({ outcome: s.outcome?.kind === "key" && s.outcome.seen ? undefined : s.outcome })),
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
  // The app asks in its own native confirmation: asking on the page too would ask twice.
  const asks = !host.features.has("confirmsKeyReplacement");
  const o = props.state.outcome;
  const why = props.disabled ?? (can ? undefined : t("unavailable", { command: ROTATE_KEY }));
  const off = !!why || props.state.pending || o?.kind === "noAnswer" || o?.kind === "key";
  // What came of it shows under the row, which may be at the bottom of the window: bring it into view.
  const said = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (o && o.kind !== "key") said.current?.scrollIntoView?.({ block: "nearest" });
  }, [o]);
  return (
    <>
      <SettingRow
        name={t("management")}
        description={t("managementAbout")}
        configKey={props.configKey}
        disabled={why}
      >
        {asks ? (
          <ConfirmButton
            variant="quiet"
            confirmLabel={t("confirmReplace")}
            disabled={off}
            onConfirm={props.state.replace}
          >
            {t("replace")}
          </ConfirmButton>
        ) : (
          <Button variant="quiet" disabled={off} onClick={props.state.replace}>
            {t("replace")}
          </Button>
        )}
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
      <div ref={said} class="config-key-said">
        {o?.kind === "apart" && <Notice tone="ok">{t("replacedApart")}</Notice>}
        {o?.kind === "apart" && o.warning && <Notice tone="warn">{p.text(o.warning)}</Notice>}
        {o?.kind === "noAnswer" && (
          <Notice tone="warn">
            {rich(t("noAnswer", { command: "{command}" }), { command: command(SHOW_KEY) })}
          </Notice>
        )}
        {o?.kind === "failed" && <Notice tone="danger">{p.text(o.error)}</Notice>}
      </div>
    </>
  );
}

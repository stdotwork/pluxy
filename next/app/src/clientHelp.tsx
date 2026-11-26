// What the tool pages share about what is missing: where a key comes from and why Apply is off
// (on the pages not yet on the shared parts: OpenCode, Oh My Pi, Grok Build, Hermes Agent, Pi), why
// Start is off, and the add-on-not-installed notice that links to Add-ons.

import type { ComponentChildren } from "preact";
import { rich, strings } from "../../ui/kit/strings";
import { type Loaded, Notice } from "../../ui/kit/ui";

const t = strings("clientHelp", {
  noKeys: "No keys yet: add one.",
  keyFrom: "Make a key in {config}, then add it here.",
  loadingKeys: "Loading keys…",
  config: "pluxy's settings",
  needKey: "Choose a key above: this button stays off until one is chosen.",
  needFolder: "Start stays off until you choose a folder.",
  deviceName: "Add-ons",
});

/** Words for where a client key comes from, with a link to the page that makes one. */
export const keySource = (): ComponentChildren =>
  rich(t("keyFrom"), { config: <a href="#/config">{t("config")}</a> });

/**
 * The key card's empty state: the keys loaded and there is none yet, and where one comes from.
 * Before the first read it says the keys are loading; after a failed first read it says nothing,
 * as the page shows the error.
 */
export function NoKeys(props: { secrets: Loaded<unknown> }) {
  if (props.secrets.phase === "loading") return <Notice tone="muted">{t("loadingKeys")}</Notice>;
  if (props.secrets.phase === "failed" && !props.secrets.data) return null;
  return (
    <p class="muted">
      {t("noKeys")} {keySource()}
    </p>
  );
}

/** Why a button that needs a key is off, while none is chosen. */
export function NeedsKey(props: { missing: boolean }) {
  return props.missing ? <small class="hint">{t("needKey")}</small> : null;
}

/** What Start waits for, while the working directory is empty (undefined once it is not). */
export const folderWait = (cwd: string): string | undefined => (cwd.trim() ? undefined : t("needFolder"));

/** A notice text with "{device}" turned into a link to the Add-ons page (#/device). */
export const withDeviceLink = (text: string): ComponentChildren =>
  rich(text, { device: <a href="#/device">{t("deviceName")}</a> });

/** Whether an error says the client's tool (package) is not installed on this device: the case the
 * amber notice already explains, so the page shows no raw code for it. */
export const toolMissing = (error: string | undefined, pkg: string): boolean =>
  !!error && error.includes(`${pkg} is not installed on this device`);

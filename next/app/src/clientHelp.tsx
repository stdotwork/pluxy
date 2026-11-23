// What the client pages (Claude Code, Codex, Claude Desktop, OpenCode, Oh My Pi, Grok Build, Hermes
// Agent, Pi) share about what is missing: where a client key comes from, why Apply or Start is
// switched off, and the tool-not-installed notice that links to Device extensions.

import type { ComponentChildren } from "preact";
import { rich, strings } from "../../ui/kit/strings";

const t = strings("clientHelp", {
  noKeys: "No keys yet: add one.",
  keyFrom:
    "A client key is made on the {config} page under Client API Keys, or with the command pluxy key add.",
  config: "Config Panel",
  needKey: "Choose a client key above: this button stays off until one is chosen.",
  needFolder: "Start stays off until you enter the folder to start in.",
  deviceName: "Device extensions",
});

/** Words for where a client key comes from, with a link to the page that makes one. */
export const keySource = (): ComponentChildren =>
  rich(t("keyFrom"), { config: <a href="#/config">{t("config")}</a> });

/** The key card's empty state: there is no key yet, and where one comes from. */
export function NoKeys() {
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

/** A notice text with "{device}" turned into a link to the Device extensions page. */
export const withDeviceLink = (text: string): ComponentChildren =>
  rich(text, { device: <a href="#/device">{t("deviceName")}</a> });

/** Whether an error says the client's tool (package) is not installed on this device: the case the
 * amber notice already explains, so the page shows no raw code for it. */
export const toolMissing = (error: string | undefined, pkg: string): boolean =>
  !!error && error.includes(`${pkg} is not installed on this device`);

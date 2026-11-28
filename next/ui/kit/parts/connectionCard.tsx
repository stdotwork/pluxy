// ConnectionCard: "Point a tool at pluxy", as EasyCLI's Home has it. The API the tool speaks
// (OpenAI, Claude or Gemini), the base URL for that API with Copy, a client key's name and masked
// value (pluxy shows a key's value once, when it is made: Tools makes and lists them), and the
// coding tools on this computer already set up, each a chip that leads to Tools.

import { useId, useState } from "preact/hooks";
import type { ClientKey } from "../api/config";
import type { SetupClient } from "../api/setup";
import { Icon } from "../icons";
import { usePrivacy } from "../privacy";
import { strings } from "../strings";
import { Button, useCopy } from "../ui";
import { ProviderMark } from "./providerMark";

const t = strings("parts.connectionCard", {
  title: "Point a tool at pluxy",
  tools: "Tools",
  speaks: "The API your tool speaks",
  openai: "OpenAI",
  claude: "Claude",
  gemini: "Gemini",
  baseUrl: "Base URL",
  openaiNote: "OpenAI-compatible",
  claudeNote: "Anthropic Messages",
  geminiNote: "Gemini API",
  copy: "Copy",
  copied: "Copied",
  notCopied: "Not copied",
  key: "Key",
  keyFor: "Key · {name}",
  keys: "Keys",
  keyHelp: "pluxy shows a key once, when it is made. Make one or see them all on Tools.",
  noAddress: "This page cannot tell pluxy's address. Tools shows the address pluxy listens on.",
  setUp: "Set up: {name}",
  notSetUp: "Not set up yet: {name}",
  toolsHere: "Coding tools here",
});

/** The API a tool speaks, and the base URL each takes from pluxy's address (origin). */
export type Speaks = "openai" | "claude" | "gemini";

export function baseUrlFor(origin: string, speaks: Speaks): string {
  const o = origin.replace(/\/+$/, "");
  return speaks === "openai" ? `${o}/v1` : o;
}

const NOTE: Record<Speaks, () => string> = {
  openai: () => t("openaiNote"),
  claude: () => t("claudeNote"),
  gemini: () => t("geminiNote"),
};

const SPEAKS: { value: Speaks; mark: string }[] = [
  { value: "openai", mark: "openai" },
  { value: "claude", mark: "pluxy/claude" },
  { value: "gemini", mark: "pluxy/gemini" },
];

/**
 * The card. address is pluxy's address for tools (host.address), absent when the page cannot
 * tell it. keys are the client keys once read; clients the coding tools core found (GET /setup),
 * once read. Either missing leaves its line out.
 */
export function ConnectionCard(props: { address?: string; keys?: ClientKey[]; clients?: SetupClient[] }) {
  const p = usePrivacy();
  const id = useId();
  const [speaks, setSpeaks] = useState<Speaks>("openai");
  const url = props.address ? baseUrlFor(props.address, speaks) : undefined;
  const copy = useCopy(url ?? "");
  const key = props.keys?.[0];
  const known = (props.clients ?? []).filter((c) => c.state !== "unchecked");
  const setUp = known.filter((c) => c.state === "configured");
  const notYet = known.filter((c) => c.installed && c.state !== "configured");
  return (
    <section class="connection-card card" data-part="connection-card" aria-labelledby={`${id}-t`}>
      <header class="side-card-head">
        <h2 id={`${id}-t`}>{t("title")}</h2>
        <a class="side-card-link" href="#/tools">
          {t("tools")}
          <Icon name="chevron" size={12} />
        </a>
      </header>
      <div class="connection-speaks segments" role="radiogroup" aria-label={t("speaks")}>
        {SPEAKS.map((s) => (
          <label key={s.value} class="segment">
            <input
              type="radio"
              name={`${id}-speaks`}
              value={s.value}
              checked={speaks === s.value}
              onChange={() => setSpeaks(s.value)}
            />
            <span>
              <ProviderMark extension={s.mark} size="small" />
              {t(s.value)}
            </span>
          </label>
        ))}
      </div>
      {url ? (
        <div class="connection-field home-address">
          <div class="connection-label">
            <span class="home-address-label">{t("baseUrl")}</span>
            <span class="connection-note">{NOTE[speaks]()}</span>
          </div>
          <div class="connection-value">
            <code class="mono" title={url}>
              {url}
            </code>
            <Button onClick={copy.copy}>
              <span aria-live="polite">
                {copy.state === "copied" ? t("copied") : copy.state === "failed" ? t("notCopied") : t("copy")}
              </span>
            </Button>
          </div>
        </div>
      ) : (
        <p class="connection-note">{t("noAddress")}</p>
      )}
      {key && (
        <div class="connection-field">
          <div class="connection-label">
            <span>{key.label ? t("keyFor", { name: p.id(key.label) }) : t("key")}</span>
            <a href="#/tools">
              {t("keys")}
              <Icon name="chevron" size={12} />
            </a>
          </div>
          <div class="connection-value" title={t("keyHelp")}>
            <Icon name="key" size={14} />
            <code class="mono">{key.masked}</code>
          </div>
        </div>
      )}
      {(setUp.length > 0 || notYet.length > 0) && (
        <ul class="connection-tools" aria-label={t("toolsHere")}>
          {setUp.map((c) => (
            <li key={c.id}>
              <a
                class="tool-chip"
                data-state="configured"
                href="#/tools"
                aria-label={t("setUp", { name: c.name })}
              >
                <Icon name="check" size={12} />
                {c.name}
              </a>
            </li>
          ))}
          {notYet.map((c) => (
            <li key={c.id}>
              <a
                class="tool-chip"
                data-state="not-configured"
                href="#/tools"
                aria-label={t("notSetUp", { name: c.name })}
              >
                <Icon name="plus" size={12} />
                {c.name}
              </a>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

// Settings: the rest of pluxy, as a plain list of where each thing is. Those pages are today's,
// shown inside this shell; what is chosen per device (appearance, privacy mode, signing out) is here.

import { useEffect, useState } from "preact/hooks";
import type { PageDef } from "../../shell";
import type { ThemeState } from "../../theme/state";
import type { Session } from "../session";
import { Btn, Section, Words } from "./parts";

/** Today's pages that sit under Settings, in the order they are listed, each with what it is for. */
export const SETTINGS_PAGES: [id: string, name: string, about: string][] = [
  ["routing", "Routing", "Which account a request goes to first, and the fallback chains."],
  ["config", "Config", "pluxy's settings, its client and management keys, and the config file itself."],
  ["extensions", "Extensions", "The providers pluxy runs, and installing more."],
  ["logs", "Logs", "What pluxy wrote down, and one request's log by its trace."],
  ["quota", "Quota", "Every account's usage windows on one page."],
  ["system", "System", "The version pluxy runs and the models it serves."],
  ["themes", "Themes", "The colours, type and layout of the panel."],
  ["setup", "Set up pluxy", "The guide a new install opens with, again."],
];

type Appearance = "light" | "dark" | "system";

export function Settings(props: { session: Session; themes: ThemeState }) {
  const { session, themes } = props;
  const [appearance, setAppearance] = useState<Appearance>(themes.choices.appearance ?? "system");
  useEffect(() => themes.subscribe(() => setAppearance(themes.choices.appearance ?? "system")), [themes]);
  const have = new Map<string, PageDef>(session.pages.map((p) => [p.id, p]));
  const extensionPages = session.pages.filter((p) => p.group === "extensions");
  return (
    <article class="db-page db-settings">
      <header class="db-page-head">
        <div>
          <h1>Settings</h1>
          <p class="db-lead">The rest of pluxy: how it routes, what it runs and what it wrote down.</p>
        </div>
      </header>
      <div class="db-spread">
        <div>
          <ul class="db-index">
            {SETTINGS_PAGES.filter(([id]) => have.has(id)).map(([id, name, about]) => (
              <li key={id}>
                <a class="db-name" href={`#/${id}`}>
                  {name}
                </a>
                <p>{about}</p>
              </li>
            ))}
          </ul>
          {extensionPages.length > 0 && (
            <Section title="Pages from extensions">
              <ul class="db-index">
                {extensionPages.map((p) => (
                  <li key={p.id}>
                    <a class="db-name" href={`#/${p.id}`}>
                      {p.title}
                    </a>
                  </li>
                ))}
              </ul>
            </Section>
          )}
        </div>
        <div>
          <Section title="On this device">
            <div class="db-setting">
              <div>
                <b>Appearance</b>
                <p>A light page, a dark one, or whichever your system uses.</p>
              </div>
              <Words
                label="Appearance"
                value={appearance}
                options={[
                  { value: "light", label: "Light" },
                  { value: "dark", label: "Dark" },
                  { value: "system", label: "Follow the system" },
                ]}
                onChange={(v) => themes.set({ appearance: v })}
              />
            </div>
            <div class="db-setting">
              <div>
                <b>Privacy mode</b>
                <p>Hides account e-mail addresses and names while you share your screen.</p>
              </div>
              <Words
                label="Privacy mode"
                value={session.privacyOn ? "on" : "off"}
                options={[
                  { value: "off", label: "Off" },
                  { value: "on", label: "On" },
                ]}
                onChange={(v) => session.setPrivacy(v === "on")}
              />
            </div>
            {session.canSignOut && (
              <div class="db-setting">
                <div>
                  <b>Sign out</b>
                  <p>Forgets the management key on this device.</p>
                </div>
                <Btn icon="signOut" onClick={session.signOut}>
                  Sign out
                </Btn>
              </div>
            )}
          </Section>
        </div>
      </div>
    </article>
  );
}

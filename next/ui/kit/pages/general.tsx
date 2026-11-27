// General: what a person sets for this window or browser, and signing out. Each setting saves the
// moment it changes. The desktop app has its Sign out on its Where pluxy runs page, beside the
// connection it ends, so it turns this one off.

import { SettingRow, useSavedTick } from "../parts";
import { usePrivacyOn } from "../privacy";
import type { PageDef } from "../shell";
import { rich, strings } from "../strings";
import { Choices, ConfirmButton, PageHead, Switch, useKit, useThemeState } from "../ui";
import { SettingGroup, SettingsColumn } from "./settingGroup";

const t = strings("general", {
  title: "General",
  tip: "Look, hiding names and signing out",
  look: "Look",
  privacy: "Privacy",
  thisBrowser: "This browser",
  appearance: "Appearance",
  appearanceAbout: "Light, dark, or whatever your system uses.",
  light: "Light",
  dark: "Dark",
  system: "Follow the system",
  darkOnly: "{name} is dark only. Pick another theme in {link}.",
  lightOnly: "{name} is light only. Pick another theme in {link}.",
  themes: "Themes",
  hideNames: "Hide names",
  hideNamesAbout: "Hides account e-mail addresses and names on every page, for sharing your screen.",
  signOut: "Sign out",
  signOutAbout: "This browser forgets the management key. You need it to sign in again.",
});

/** What a build changes in General. */
export interface GeneralOptions {
  /** Whether General offers Sign out where the host can. Default: true. */
  signOut?: boolean;
}

function Appearance() {
  const themes = useThemeState();
  const saved = useSavedTick();
  const theme = themes.current.theme;
  const only = theme.appearance === "both" ? undefined : theme.appearance;
  return (
    <SettingRow name={t("appearance")} description={t("appearanceAbout")} saved={saved.saved}>
      {only ? (
        // The theme has one appearance, so there is nothing to choose: say why, and where to go.
        <span class="muted">
          {rich(t(only === "dark" ? "darkOnly" : "lightOnly", { name: theme.name, link: "{link}" }), {
            link: <a href="#/themes">{t("themes")}</a>,
          })}
        </span>
      ) : (
        <Choices
          label={t("appearance")}
          value={themes.choices.appearance}
          choices={[
            { value: "light", label: t("light") },
            { value: "dark", label: t("dark") },
            { value: "system", label: t("system") },
          ]}
          onChange={(appearance) => {
            themes.set({ appearance });
            saved.flash();
          }}
        />
      )}
    </SettingRow>
  );
}

export function GeneralPage(props: { options?: GeneralOptions }) {
  const { host, privacy } = useKit();
  const hidden = usePrivacyOn(privacy);
  const saved = useSavedTick();
  return (
    <>
      <PageHead title={t("title")} />
      <SettingsColumn>
        <SettingGroup title={t("look")}>
          <Appearance />
        </SettingGroup>
        <SettingGroup title={t("privacy")}>
          <SettingRow name={t("hideNames")} description={t("hideNamesAbout")} saved={saved.saved}>
            <Switch
              label={t("hideNames")}
              checked={hidden}
              onChange={(on) => {
                privacy.set(on);
                saved.flash();
              }}
            />
          </SettingRow>
        </SettingGroup>
        {props.options?.signOut !== false && host.features.has("signOut") && (
          <SettingGroup title={t("thisBrowser")}>
            <SettingRow name={t("signOut")} description={t("signOutAbout")}>
              <ConfirmButton variant="quiet" onConfirm={() => host.signOut()}>
                {t("signOut")}
              </ConfirmButton>
            </SettingRow>
          </SettingGroup>
        )}
      </SettingsColumn>
    </>
  );
}

export const generalPage = (options?: GeneralOptions): PageDef => ({
  id: "settings",
  dest: "settings",
  kind: "topic",
  section: "device",
  title: t("title"),
  tip: t("tip"),
  render: () => <GeneralPage options={options} />,
});

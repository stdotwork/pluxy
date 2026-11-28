import type { ComponentChildren } from "preact";
import { useState } from "preact/hooks";
import { Icon } from "../icons";
import { SettingsSearchContext } from "../parts";
import { strings } from "../strings";

const t = strings("settingGroup", {
  search: "Search settings",
  none: "No setting matches that.",
});

export function SettingsColumn(props: { children: ComponentChildren; class?: string; search?: boolean }) {
  return (
    <div
      class={props.class ? `settings-column ${props.class}` : "settings-column"}
      data-part="settings-column"
    >
      {props.search ? <SettingsSearchScope>{props.children}</SettingsSearchScope> : props.children}
    </div>
  );
}

export function SettingsSearchScope(props: { children: ComponentChildren }) {
  const [query, setQuery] = useState("");
  const asked = query.trim();
  return (
    <div class="settings-groups" data-searching={asked ? "" : undefined}>
      <label class="settings-search">
        <Icon name="search" size={14} />
        <input
          data-slot="input"
          type="search"
          aria-label={t("search")}
          placeholder={t("search")}
          value={query}
          onInput={(e) => setQuery(e.currentTarget.value)}
        />
      </label>
      <p class="settings-none" role="status">
        {asked ? t("none") : ""}
      </p>
      <SettingsSearchContext.Provider value={asked}>{props.children}</SettingsSearchContext.Provider>
    </div>
  );
}

export function SettingGroup(props: {
  title?: string;
  about?: ComponentChildren;
  actions?: ComponentChildren;
  id?: string;
  card?: boolean;
  children: ComponentChildren;
}) {
  return (
    <section class="setting-group" data-part="settings-section" id={props.id} aria-label={props.title}>
      {(props.title || props.actions) && (
        <header class="setting-group-head">
          {props.title && <h2>{props.title}</h2>}
          {props.actions && <div class="actions">{props.actions}</div>}
        </header>
      )}
      {props.about && <p class="setting-group-about">{props.about}</p>}
      {props.card === false ? (
        <div class="setting-group-body">{props.children}</div>
      ) : (
        <div class="setting-group-card" data-part="setting-card">
          {props.children}
        </div>
      )}
    </section>
  );
}

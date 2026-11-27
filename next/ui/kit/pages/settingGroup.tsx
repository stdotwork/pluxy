// The settings pattern the Settings pages share: a column of groups, each a small heading and a
// rounded card of rows split by hairlines (SettingRow, or a piece of the page's own). A setting
// saves the moment it changes, and says Saved beside it.

import type { ComponentChildren } from "preact";
import { useId } from "preact/hooks";

/** The column the groups sit in: centred, no wider than a row stays easy to read. */
export function SettingsColumn(props: { children: ComponentChildren; class?: string }) {
  return (
    <div
      class={props.class ? `settings-column ${props.class}` : "settings-column"}
      data-part="settings-column"
    >
      {props.children}
    </div>
  );
}

/**
 * One group: title (with actions at its end), an optional sentence under it, and the card of rows.
 * id is the address a link can scroll to.
 */
export function SettingGroup(props: {
  title?: string;
  about?: ComponentChildren;
  actions?: ComponentChildren;
  id?: string;
  /** Whether the rows sit in the rounded card. false: what the group holds stands on the page, as the Themes gallery does. */
  card?: boolean;
  children: ComponentChildren;
}) {
  const heading = useId();
  return (
    <section
      class="setting-group"
      data-part="settings-section"
      id={props.id}
      aria-labelledby={props.title ? heading : undefined}
    >
      {(props.title || props.actions) && (
        <header class="setting-group-head">
          {props.title && <h2 id={heading}>{props.title}</h2>}
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

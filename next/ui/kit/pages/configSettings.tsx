import type { CoreSettings } from "../api/settings";
import { Button, Card, Empty } from "../ui";
import { configText as t } from "./configStrings";
import { changeSetting, fields, SettingControl, type SettingsTab, type Value } from "./settingsFields";

export { changeSetting, type SettingsTab, settingsChange } from "./settingsFields";

export function ConfigSettings(props: {
  tab: SettingsTab;
  search: string;
  hasOtherResults?: boolean;
  value: CoreSettings;
  onChange: (v: CoreSettings) => void;
  dirty: boolean;
  busy: boolean;
  save: () => void;
  discard: () => void;
}) {
  const query = props.search.trim().toLowerCase();
  const shown = (
    query
      ? [
          ...new Map(
            Object.values(fields())
              .flat()
              .map((f) => [f.key, f]),
          ).values(),
        ]
      : fields()[props.tab]
  ).filter((f) => `${f.label} ${f.key}`.toLowerCase().includes(query));
  return (
    <form
      class="stack"
      onSubmit={(e) => {
        e.preventDefault();
        props.save();
      }}
    >
      <fieldset disabled={props.busy} class="config-fields">
        {shown.length === 0 && !props.hasOtherResults && <Empty>{t("noResults")}</Empty>}
        {shown.map((field) => {
          const set = (v: Value) => props.onChange(changeSetting(props.value, field.key, v));
          return (
            <Card key={field.key}>
              <SettingControl field={field} settings={props.value} set={set} />
              <small class="mono muted">{field.key}</small>
            </Card>
          );
        })}
      </fieldset>
      <div class="actions">
        <Button type="submit" variant="primary" disabled={props.busy || !props.dirty}>
          {t("saveChanges")}
        </Button>
        <Button disabled={props.busy || !props.dirty} onClick={props.discard}>
          {t("discard")}
        </Button>
      </div>
    </form>
  );
}

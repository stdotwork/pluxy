// What the Requests and Usage tabs share: the pane that says no record is kept.

import { StatePane } from "../parts";
import { strings } from "../strings";

const t = strings("activity.record", {
  title: "pluxy is not keeping a record of requests.",
  text: "Turn it on, and requests show here from then on.",
  turnOn: "Turn it on in Config",
});

/** The reason Save as spreadsheet is disabled while no record is kept. */
export const recordOffReason = () => t("title");

/** Both tabs, in place of everything else, while core is told not to record requests. */
export function RecordOff() {
  return (
    <StatePane kind="empty" title={t("title")} primary={{ label: t("turnOn"), href: "#/config/logging" }}>
      {t("text")}
    </StatePane>
  );
}

// Manage Clients: which agent client pages the window's navigation lists, with the old app's
// labels. Left alone it is automatic, the clients whose tool is installed on this device; the person
// can pick the clients to keep, search them, and go back to automatic. At least one client must stay
// picked. The choice is kept in the host's prefs (clientListItems.ts), on this device, like the
// theme: only this window reads it, so it needs no native side.

import { useState } from "preact/hooks";
import type { PageDef } from "../../ui/kit/shell";
import { strings } from "../../ui/kit/strings";
import { Badge, Button, Card, Empty, Notice, PageHead } from "../../ui/kit/ui";
import { automatic, type Client, encode, matching, savable, toggled } from "./clientListItems";

const t = strings("clients", {
  title: "Manage Clients",
  short: "Clients",
  tip: "Choose which agent clients the navigation lists",
  description:
    "Choose the clients to keep in the navigation, then save. Installed clients come first. The client you are on stays listed while you are on it.",
  listed: { one: "{count} client in the navigation", other: "{count} clients in the navigation" },
  search: "Search clients",
  detected: "Client detected",
  noResults: "No matching clients found",
  selected: { one: "{count} client selected", other: "{count} clients selected" },
  minimum: "Keep at least one client visible.",
  automatic: "Restore automatic display",
  automaticHint: "Clients whose tool is installed on this device are shown by default.",
  save: "Save",
  saved: "Saved.",
});

/** The Manage Clients count: how many clients the navigation lists now (not how many this page shows). */
export const listedText = (count: number) => t("listed", { count });

function ClientList(props: {
  clients: Client[];
  installed: Set<string>;
  saved: string[] | undefined;
  listed: number;
  onSave: (ids: string[] | undefined) => void;
}) {
  const { clients, installed, saved } = props;
  const [draft, setDraft] = useState(saved);
  const [query, setQuery] = useState("");
  const [done, setDone] = useState(false);
  const automaticIds = automatic(clients, installed);
  const shown = draft ?? automaticIds;
  const change = (next: string[] | undefined) => {
    setDraft(next);
    setDone(false);
  };
  const matches = matching(clients, installed, query);
  const dirty = encode(draft) !== encode(saved);
  return (
    <div class="stack" data-part="client-list">
      <PageHead title={t("title")} />
      <p class="muted">{t("description")}</p>
      <Card>
        <p class="muted" data-part="client-count">
          {listedText(props.listed)}
        </p>
        <label class="field" data-part="field">
          <span class="label">{t("search")}</span>
          <input type="search" value={query} onInput={(e) => setQuery(e.currentTarget.value)} />
        </label>
        {matches.length === 0 ? (
          <Empty>{t("noResults")}</Empty>
        ) : (
          <ul class="stack" data-part="client-options">
            {matches.map((c) => (
              <li key={c.id} data-client={c.id}>
                <label class="check">
                  <input
                    type="checkbox"
                    aria-label={c.title}
                    checked={shown.includes(c.id)}
                    onChange={(e) => change(toggled(draft, automaticIds, c.id, e.currentTarget.checked))}
                  />
                  <strong>{c.title}</strong>
                  {installed.has(c.id) && <Badge tone="ok">{t("detected")}</Badge>}
                </label>
              </li>
            ))}
          </ul>
        )}
        <div class="actions">
          <Button disabled={draft === undefined} onClick={() => change(undefined)}>
            {t("automatic")}
          </Button>
          <small class="muted">{t("automaticHint")}</small>
        </div>
      </Card>
      {!savable(draft) ? (
        <Notice tone="warn">{t("minimum")}</Notice>
      ) : (
        <p class="muted" role="status">
          {t("selected", { count: shown.length })}
        </p>
      )}
      <div class="actions">
        <Button
          variant="primary"
          disabled={!savable(draft) || !dirty}
          onClick={() => {
            props.onSave(draft);
            setDone(true);
          }}
        >
          {t("save")}
        </Button>
        {done && !dirty && <span class="muted">{t("saved")}</span>}
      </div>
    </div>
  );
}

/** The Manage Clients page: clients is every client page, installed the IDs whose tool is on this
 * device, saved the saved choice (undefined is automatic) and listed how many the navigation
 * shows. onSave gets the new choice. */
export const clientListPage = (
  clients: Client[],
  installed: Set<string>,
  saved: string[] | undefined,
  listed: number,
  onSave: (ids: string[] | undefined) => void,
): PageDef => ({
  id: "clients",
  group: "control",
  title: t("title"),
  short: t("short"),
  tip: t("tip"),
  render: () => (
    <ClientList clients={clients} installed={installed} saved={saved} listed={listed} onSave={onSave} />
  ),
});

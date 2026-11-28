import { useEffect, useRef, useState } from "preact/hooks";
import type { Reply } from "../../ui/kit/host";
import { strings } from "../../ui/kit/strings";
import { Button, Choices, Facts, Notice, useAction, useLoad, whenText } from "../../ui/kit/ui";
import { cancelled } from "./clientPage";
import type { Selection } from "./environment";
import { reply } from "./host";
import { nativeReply, useShown } from "./toolWords";

const t = strings("backups", {
  intro: "A backup is a copy of the file as it is on disk now. The app keeps the newest {keep}.",
  create: "Create backup",
  versions: "Saved copies",
  empty: "No backups yet. Create backup saves a copy of the file as it is now.",
  select: "Choose a copy to see what restoring it would change.",
  loading: "Loading…",
  files: { one: "{count} file", other: "{count} files" },
  present: "Present in backup · {size} B",
  absent: "Absent in backup; removed on restore",
  same: "The files are already the same as this copy.",
  unreadable: "Cannot compare settings; restoring replaces the whole file.",
  adds: "Restore adds",
  changes: "Restore changes",
  removes: "Restore removes",
  more: "{count} more settings",
  created: "Created on restore",
  restore: "Restore",
  restoreHint:
    "Puts back the files as this copy has them and removes the ones it does not have. It makes no backup first.",
  delete: "Delete",
  deleteHint: "Deletes only this copy. The files and the other copies stay as they are.",
  id: "Copy",
});

interface Version {
  id: string;
  created: number;
  files: { selected: string; path: string | null; target: string; exists: boolean; size: number }[];
}

interface Diff {
  same: boolean;
  files: {
    target: string;
    exists: boolean;
    now_exists: boolean;
    same: boolean;
    readable: boolean;
    changes: { path: string[]; restore: "adds" | "changes" | "removes" }[];
    more: number;
  }[];
}

let tickets = 0;
/** A fresh ticket for a backup write's program_call. */
const newTicket = () => `backups-${Date.now().toString(36)}-${++tickets}`;

const fill = (s: string, v: Record<string, string | number>) =>
  s.replace(/\{(\w+)\}/g, (_, k) => String(v[k] ?? ""));

/** The backup browser of the writer tool pkg. pins gives the files a backup or restore is of: the
 * pins of the page's last preview (the tool refuses them if a file changed since), or nothing.
 * environment, when given, is the environment every call goes to (environment.ts: null for this
 * computer, or a WSL distribution and user); pages without one leave it out. The page puts it under
 * a heading of its own (Backups, under Settings file, key and backups). onBusy hears when a write
 * starts and ends, so the page can hold its file choice meanwhile. */
export function Backups(props: {
  pkg: string;
  pins: () => Promise<unknown[] | undefined>;
  disabled?: boolean;
  onChange?: () => void;
  environment?: Selection;
  onBusy?: (busy: boolean) => void;
}) {
  const shown = useShown();
  const tickets = useRef(new Set<string>());
  useEffect(() => {
    const open = tickets.current;
    return () => {
      for (const ticket of open) reply("program_cancel", { ticket });
      open.clear();
    };
  }, []);
  /** A call of the add-on; a write's (ticket set) is withdrawn if the page goes away first. */
  const tool = async <T,>(op: string, input: Record<string, unknown>, ticket?: string) => {
    if (ticket) tickets.current.add(ticket);
    try {
      return await nativeReply<T>("program_call", {
        package: props.pkg,
        op,
        input,
        ...(ticket ? { ticket } : {}),
        ...(props.environment !== undefined ? { environment: props.environment } : {}),
      });
    } finally {
      if (ticket) tickets.current.delete(ticket);
    }
  };
  const list = useLoad(() => tool<{ backups: Version[]; keep: number }>("backup-list", {}));
  const [selected, setSelected] = useState<string>();
  const [diff, setDiff] = useState<Diff>();
  const inspect = useAction(() => {});
  // The backup whose diff is wanted: a reply for any other (selected before it) is dropped.
  const wanted = useRef<string | undefined>(undefined);
  const show = (id: string) => {
    wanted.current = id;
    setSelected(id);
    setDiff(undefined);
    inspect.run(async () => {
      const r = await tool<Diff>("backup-diff", { backup: id });
      if (wanted.current !== id) return { status: 200 };
      if (r.body) setDiff(r.body);
      return r;
    });
  };
  // Manual Backup changes no file: only the list reloads, the new backup selected.
  const made = useRef<string | undefined>(undefined);
  const create = useAction(() => {
    list.reload();
    if (made.current) show(made.current);
  });
  // A restore changed the files: the diff and the page's own preview are read again.
  const write = useAction(() => {
    list.reload();
    if (selected) show(selected);
    props.onChange?.();
  });
  const remove = useAction(() => {
    setSelected(undefined);
    setDiff(undefined);
    list.reload();
  });
  const pinned = async <T,>(op: string, extra: Record<string, unknown>): Promise<Reply<T>> => {
    const targets = await props.pins();
    if (!targets)
      return { status: 400, error: { code: "refused", message: "the settings file could not be read" } };
    return tool<T>(op, { ...extra, targets }, newTicket());
  };
  const versions = list.data?.backups ?? [];
  const version = versions.find((v) => v.id === selected);
  const writing = create.busy || write.busy || remove.busy;
  const busy = writing || props.disabled;
  const onBusy = useRef(props.onBusy);
  onBusy.current = props.onBusy;
  useEffect(() => onBusy.current?.(writing), [writing]);
  // Gone while a write waits: the page holds its choices no longer.
  useEffect(() => () => onBusy.current?.(false), []);
  // The exact time: versions are told apart by it.
  const when = (v: Version) =>
    (Number.isFinite(v.created) && whenText(new Date(v.created).toISOString(), "exact")) || String(v.created);

  // A cancel in the add-on's window is no error.
  const said = (e: string | undefined) => (e && !cancelled(e) ? e : undefined);
  const errors = [list.error, said(create.error), said(write.error), said(remove.error)]
    .filter((e): e is string => !!e)
    .map((e) => shown.text(e));

  return (
    <div class="backups">
      <p class="muted">{fill(t("intro"), { keep: list.data?.keep ?? 10 })}</p>
      {list.phase === "loading" && <p class="muted">{t("loading")}</p>}
      {errors.map((e) => (
        <Notice key={e} tone="danger">
          {e}
        </Notice>
      ))}
      <div>
        <Button
          onClick={() =>
            create.run(async () => {
              const r = await pinned<{ backup: string }>("backup-create", {});
              made.current = r.body?.backup;
              return r;
            })
          }
          disabled={busy}
        >
          {t("create")}
        </Button>
      </div>
      {versions.length === 0 ? (
        list.data && !list.loading && <p class="muted">{t("empty")}</p>
      ) : (
        <Choices
          label={t("versions")}
          value={version?.id}
          choices={versions.map((v) => ({
            value: v.id,
            label: when(v),
            note: t("files", { count: v.files.length }),
          }))}
          onChange={(id) => {
            if (id) show(id);
          }}
        />
      )}
      {versions.length > 0 && !version && <p class="muted">{t("select")}</p>}
      {version && (
        <div key={version.id} class="backups-version">
          <Facts rows={[[t("id"), when(version)]]} />
          {inspect.error && <Notice tone="danger">{shown.text(inspect.error)}</Notice>}
          {version.files.map((f, i) => {
            const d = diff?.files[i];
            return (
              <div key={f.target} class="backups-file">
                <Facts
                  rows={[
                    [shown.path(f.target), f.exists ? fill(t("present"), { size: f.size }) : t("absent")],
                  ]}
                />
                {d && !d.same && !d.now_exists && <p class="muted">{t("created")}</p>}
                {d && !d.same && !d.readable && <p class="muted">{t("unreadable")}</p>}
                {d && d.changes.length > 0 && (
                  <ul>
                    {d.changes.map((c) => (
                      <li key={JSON.stringify([c.restore, c.path])}>
                        {t(c.restore)} <code>{shown.text(c.path.join("."))}</code>
                      </li>
                    ))}
                    {d.more > 0 && <li>{fill(t("more"), { count: d.more })}</li>}
                  </ul>
                )}
              </div>
            );
          })}
          {diff?.same && <p class="muted">{t("same")}</p>}
          <div class="backups-actions">
            <Button
              busy={write.busy}
              disabled={busy || !diff || diff.same}
              onClick={() => write.run(() => pinned("backup-restore", { backup: version.id }))}
            >
              {t("restore")}
            </Button>
            <small class="hint">{t("restoreHint")}</small>
          </div>
          <div class="backups-actions">
            <Button
              busy={remove.busy}
              disabled={busy}
              onClick={() => remove.run(() => tool("backup-delete", { backup: version.id }, newTicket()))}
            >
              {t("delete")}
            </Button>
            <small class="hint">{t("deleteHint")}</small>
          </div>
        </div>
      )}
    </div>
  );
}

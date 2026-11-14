// The backup browser every config writer's window page shows (Manual Backup, the backup versions,
// what restoring one would change, Restore this version, Delete this version), through the writer
// tool's standard backup operations (next/app-tools/README.md, lib/transactions: backup-list,
// backup-diff, backup-create, backup-restore, backup-delete). The bytes stay in the app's vault:
// this page gets each version's ID, when it was saved, its files' paths, presence and sizes, and
// the names of the settings a restore would add, change or remove, never a value. Each write asks
// in the tool's native confirmation. Labels follow the old app's backup dialog.

import { invoke } from "@tauri-apps/api/core";
import { useState } from "preact/hooks";
import { strings } from "../../ui/kit/strings";
import {
  Button,
  Card,
  Choices,
  ConfirmButton,
  Empty,
  Facts,
  Notice,
  useAction,
  useLoad,
} from "../../ui/kit/ui";

const t = strings("backups", {
  title: "Backup and restore",
  intro:
    "Save the configuration on disk, or restore a manual backup. Pending edits are not included in backups. The newest {keep} manual backups are kept.",
  create: "Manual Backup",
  versions: "Backup versions",
  empty: "No manual backups yet. Use Manual Backup to save the current files on disk.",
  select: "Select a backup to inspect its files.",
  files: "{count} files",
  present: "Present in backup · {size} B",
  absent: "Absent in backup; removed on restore",
  same: "Configuration is already up to date.",
  unreadable: "Its settings cannot be compared; restoring replaces the whole file.",
  adds: "Restore adds",
  changes: "Restore changes",
  removes: "Restore removes",
  more: "{count} more settings",
  created: "Created on restore",
  restore: "Restore this version",
  restoreHint:
    "Custom contents in the listed files will be replaced. Files absent in the backup will be removed. No automatic backup is created.",
  delete: "Delete this version",
  deleteHint: "This deletes only this backup version. Current configuration and other backups are unchanged.",
  id: "ID",
});

export type Reply<T> = { status: number; body?: T; error?: { code: string; message: string } };

/** A window command's answer as the kit's loaders take it. */
export async function reply<T>(cmd: string, args?: Record<string, unknown>): Promise<Reply<T>> {
  try {
    return { status: 200, body: await invoke<T>(cmd, args) };
  } catch (e) {
    return { status: 400, error: { code: "refused", message: String(e) } };
  }
}

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

const fill = (s: string, v: Record<string, string | number>) =>
  s.replace(/\{(\w+)\}/g, (_, k) => String(v[k] ?? ""));

/** The backup browser of the writer tool pkg. pins previews the files a backup or restore is of
 * (fresh pins, from the page's own selection), or gives nothing when they cannot be read. */
export function Backups(props: {
  pkg: string;
  pins: () => Promise<unknown[] | undefined>;
  disabled?: boolean;
  onChange?: () => void;
}) {
  const tool = <T,>(op: string, input: Record<string, unknown>) =>
    reply<T>("program_call", { package: props.pkg, op, input });
  const list = useLoad(() => tool<{ backups: Version[]; keep: number }>("backup-list", {}));
  const [selected, setSelected] = useState<string>();
  const [diff, setDiff] = useState<Diff>();
  const inspect = useAction(() => {});
  const show = (id: string) => {
    setSelected(id);
    setDiff(undefined);
    inspect.run(async () => {
      const r = await tool<Diff>("backup-diff", { backup: id });
      if (r.body) setDiff(r.body);
      return r;
    });
  };
  const done = () => {
    list.reload();
    if (selected) show(selected);
    props.onChange?.();
  };
  const write = useAction(done);
  const remove = useAction(() => {
    setSelected(undefined);
    setDiff(undefined);
    list.reload();
  });
  const pinned = async (op: string, extra: Record<string, unknown>) => {
    const targets = await props.pins();
    if (!targets)
      return { status: 400, error: { code: "refused", message: "the settings file could not be read" } };
    return tool(op, { ...extra, targets });
  };
  const versions = list.data?.backups ?? [];
  const version = versions.find((v) => v.id === selected);
  const busy = write.busy || remove.busy || props.disabled;
  const when = (v: Version) => new Date(v.created).toLocaleString();

  return (
    <Card title={t("title")}>
      <p class="muted">{fill(t("intro"), { keep: list.data?.keep ?? 10 })}</p>
      {list.error && <Notice tone="danger">{list.error}</Notice>}
      {write.error && <Notice tone="danger">{write.error}</Notice>}
      {remove.error && <Notice tone="danger">{remove.error}</Notice>}
      <Button onClick={() => write.run(() => pinned("backup-create", {}))} disabled={busy}>
        {t("create")}
      </Button>
      {versions.length === 0 ? (
        !list.loading && <Empty>{t("empty")}</Empty>
      ) : (
        <Choices
          label={t("versions")}
          value={version?.id}
          choices={versions.map((v) => ({
            value: v.id,
            label: when(v),
            note: fill(t("files"), { count: v.files.length }),
          }))}
          onChange={(id) => {
            if (id) show(id);
          }}
        />
      )}
      {versions.length > 0 && !version && <p class="muted">{t("select")}</p>}
      {version && (
        <>
          <Facts rows={[[t("id"), version.id]]} />
          {inspect.error && <Notice tone="danger">{inspect.error}</Notice>}
          {version.files.map((f, i) => {
            const d = diff?.files[i];
            return (
              <div key={f.target} class="stack">
                <Facts rows={[[f.target, f.exists ? fill(t("present"), { size: f.size }) : t("absent")]]} />
                {d && !d.same && !d.now_exists && <p class="muted">{t("created")}</p>}
                {d && !d.same && !d.readable && <p class="muted">{t("unreadable")}</p>}
                {d && d.changes.length > 0 && (
                  <ul>
                    {d.changes.map((c) => (
                      <li key={`${c.restore} ${c.path.join(".")}`}>
                        {t(c.restore)} <code>{c.path.join(".")}</code>
                      </li>
                    ))}
                    {d.more > 0 && <li>{fill(t("more"), { count: d.more })}</li>}
                  </ul>
                )}
              </div>
            );
          })}
          {diff?.same && <p class="muted">{t("same")}</p>}
          <ConfirmButton
            disabled={busy || !diff || diff.same}
            onConfirm={() => write.run(() => pinned("backup-restore", { backup: version.id }))}
          >
            {t("restore")}
          </ConfirmButton>
          <small class="hint">{t("restoreHint")}</small>
          <ConfirmButton
            disabled={busy}
            onConfirm={() => remove.run(() => tool("backup-delete", { backup: version.id }))}
          >
            {t("delete")}
          </ConfirmButton>
          <small class="hint">{t("deleteHint")}</small>
        </>
      )}
    </Card>
  );
}

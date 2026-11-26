// Bringing accounts across from CLIProxyAPI: its auth files, one POST /accounts/import each, with
// a line per account added or file left out. Shown at the end of Add an account.

import { useRef, useState } from "preact/hooks";
import { problem } from "../api";
import { providerName } from "../parts";
import { usePrivacy } from "../privacy";
import { strings } from "../strings";
import { Button, type Tone, useKit } from "../ui";

const t = strings("authImport", {
  ask: "Have auth files from CLIProxyAPI?",
  choose: "Import files…",
  added: "Added {name} ({provider})",
  leftOut: "Left out, not supported yet: {settings}",
  notAdded: "Not added: {reason}",
  json: "Choose .json files.",
  tooLarge: "{file} is larger than 1 MB.",
  results: "What was imported",
});

/** The largest auth file core imports (legacy.MaxAuthFile in next/internal/legacy/auth.go). */
export const MAX_AUTH_FILE = 1 << 20;

/** Why a file is refused before it is sent, or nothing when it may be sent. */
export function fileProblem(file: { name: string; size: number }): string | undefined {
  if (!file.name.toLowerCase().endsWith(".json")) return t("json");
  if (file.size > MAX_AUTH_FILE) return t("tooLarge", { file: file.name });
  return undefined;
}

export function AuthImport() {
  const { api, unauthorized } = useKit();
  const p = usePrivacy();
  const input = useRef<HTMLInputElement>(null);
  const [busy, setBusy] = useState(false);
  // The lines stay until the page is left.
  const [lines, setLines] = useState<{ tone: Tone; text: string }[]>([]);
  const run = async (files: File[]) => {
    setBusy(true);
    const out: { tone: Tone; text: string }[] = [];
    for (const file of files) {
      const refused = fileProblem(file);
      if (refused) {
        out.push({ tone: "danger", text: refused });
        continue;
      }
      try {
        const r = await api.importAuthFile(file.name, await file.text());
        if (r.status === 401) return unauthorized();
        const why = problem(r);
        if (why !== undefined || !r.body) {
          out.push({ tone: "danger", text: t("notAdded", { reason: why ?? file.name }) });
          continue;
        }
        for (const a of r.body.imported) {
          out.push({
            tone: "ok",
            text: t("added", { name: p.id(a.id), provider: providerName(a.extension) }),
          });
          if (a.ignored?.length)
            out.push({ tone: "muted", text: t("leftOut", { settings: a.ignored.join(", ") }) });
        }
        for (const reason of r.body.skipped)
          out.push({ tone: "danger", text: t("notAdded", { reason: p.text(reason) ?? reason }) });
      } catch (error) {
        out.push({ tone: "danger", text: t("notAdded", { reason: String(error) }) });
      }
    }
    setLines((old) => [...old, ...out]);
    setBusy(false);
  };
  return (
    <section class="auth-import" data-part="auth-import">
      <p class="muted">
        {t("ask")}{" "}
        <Button disabled={busy} busy={busy} onClick={() => input.current?.click()}>
          {t("choose")}
        </Button>
      </p>
      <input
        ref={input}
        type="file"
        accept=".json"
        multiple
        hidden
        onChange={(e) => {
          const files = [...(e.currentTarget.files ?? [])];
          e.currentTarget.value = "";
          if (files.length) void run(files);
        }}
      />
      {lines.length > 0 && (
        <ul class="auth-import-lines" data-part="upload-results" aria-label={t("results")}>
          {lines.map((l, i) => (
            <li key={i} data-tone={l.tone}>
              {l.text}
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

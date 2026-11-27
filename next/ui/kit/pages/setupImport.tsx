// The one line above Setup's steps when core found a CLIProxyAPI setup on its computer: a preview of
// what importing it would do, then the import, each only when the person asks. Nothing is sent
// with dry_run false unless a preview of the same config is on screen.

import { useState } from "preact/hooks";
import { type ImportReport, type ImportRow, problem, type Setup, type SetupImport } from "../api";
import type { Reply } from "../host";
import { usePrivacy } from "../privacy";
import { rich, strings } from "../strings";
import { Button, Notice, Time, useCopy, useKit } from "../ui";
import { joinNames, kindTitle } from "./setup.helpers";

const t = strings("setupImport", {
  found: {
    one: "Found a CLIProxyAPI setup with {count} account. pluxy can bring it and its settings across; nothing changes until you confirm.",
    other:
      "Found a CLIProxyAPI setup with {count} accounts. pluxy can bring them and its settings across; nothing changes until you confirm.",
  },
  imported: "Imported a CLIProxyAPI setup {when}.",
  preview: "Preview import",
  importAgain: "Import again",
  would: "Importing would bring across {a}, set {b} differently in pluxy and leave out {c}.",
  willImport: "Will import ({count})",
  setDifferently: "Set differently in pluxy ({count})",
  leftOut: "Left out ({count})",
  importedList: "Imported ({count})",
  importNow: "Import now",
  cancel: "Cancel",
  done: "Imported.",
  needs: "The imported accounts need {names}. They are ticked in step 1.",
  close: "Close",
  keep: "To keep it: {how}",
  "refused.400":
    "Could not read the CLIProxyAPI setup: {message}. Fix the file, then try again. Nothing changed.",
  "refused.422": "This setup cannot be imported: {message}. Nothing changed.",
  "refused.409": "This pluxy keeps its settings in config.yaml, which import cannot change. Nothing changed.",
  "refused.404": "This pluxy is too old to import from here.",
  "refused.0.preview": "pluxy did not answer, so nothing was read or changed.",
  "refused.0.import":
    "pluxy did not answer, so it is not known whether the import ran. Press Refresh to see.",
  refusedOther: "pluxy could not import the setup: {message}.",
  tryAgain: "Try again",
  refresh: "Refresh",
  details: "More details",
  "details.404": "Run this where pluxy is installed to import the setup:",
  "details.409":
    "Run this where pluxy is installed to make a config.json, start pluxy on it, then import again:",
  copy: "Copy",
  copied: "Copied",
  copyFailed: "Could not copy. Select the text and copy it yourself.",
});

/** The command that does what Import now does, for a pluxy too old to import from here. */
export function importCommand(found: SetupImport): string {
  return `pluxy import cpa ${found.config}${authDir(found) ? ` --auth-dir ${authDir(found)}` : ""}`;
}

/**
 * The auth folder to name in an import: the one the scan counted accounts in. With none counted
 * the import reads the folder the config names.
 */
function authDir(found: SetupImport): string | undefined {
  return found.accounts > 0 ? found.auth_dir : undefined;
}

/** Why core refused an import, as the line shows it. */
export interface Refusal {
  status: number;
  message: string;
  /** The refused call was the preview (true) or the import (false). */
  preview: boolean;
}

/** The words for a refusal. */
export function refusalWords(r: Refusal): string {
  const message = r.message.replace(/\.$/, "");
  switch (r.status) {
    case 0:
      return t(r.preview ? "refused.0.preview" : "refused.0.import");
    case 400:
      return t("refused.400", { message });
    case 404:
      return t("refused.404");
    case 409:
      return t("refused.409");
    case 422:
      return t("refused.422", { message });
    default:
      return t("refusedOther", { message });
  }
}

/** Where the import stands on this page. */
interface ImportState {
  config: string;
  busy?: "preview" | "import";
  /** The preview's report, or once done the import's. */
  report?: ImportReport;
  done?: boolean;
  refusal?: Refusal;
}

function Code(props: { value: string }) {
  const { state, copy } = useCopy(props.value);
  return (
    <div class="setup-code">
      {/* biome-ignore lint/a11y/noNoninteractiveTabindex: a long line scrolls sideways, and keys must reach it */}
      <pre tabIndex={0}>{props.value}</pre>
      <div class="actions">
        <Button onClick={copy}>{state === "copied" ? t("copied") : t("copy")}</Button>
      </div>
      {state === "failed" && <Notice tone="danger">{t("copyFailed")}</Notice>}
    </div>
  );
}

function ReportList(props: { title: string; rows: ImportRow[] }) {
  const p = usePrivacy();
  if (props.rows.length === 0) return null;
  return (
    <details class="setup-report-list">
      <summary>{props.title}</summary>
      <ul class="setup-report-rows">
        {props.rows.map((row) => (
          <li key={`${row.what}/${row.name ?? ""}`}>
            <span class="mono">{[row.what, row.name].filter(Boolean).join(" ")}</span>
            {row.why && <span class="muted">{p.text(row.why)}</span>}
            {row.keep_with && <span class="muted">{t("keep", { how: p.text(row.keep_with) })}</span>}
          </li>
        ))}
      </ul>
    </details>
  );
}

/** A report's three folded lists. */
function Report(props: { report: ImportReport; done?: boolean }) {
  const { report } = props;
  return (
    <div class="setup-report" data-part="setup-report" data-state={props.done ? "done" : "preview"}>
      <p role="status">
        {props.done
          ? t("done")
          : t("would", { a: report.brought.length, b: report.changed.length, c: report.not_brought.length })}
      </p>
      <ReportList
        title={t(props.done ? "importedList" : "willImport", { count: report.brought.length })}
        rows={report.brought}
      />
      <ReportList title={t("setDifferently", { count: report.changed.length })} rows={report.changed} />
      <ReportList title={t("leftOut", { count: report.not_brought.length })} rows={report.not_brought} />
    </div>
  );
}

/**
 * The import line. onImported runs after an import that worked, with its report (the page ticks
 * what it needs and reads again); onRescan asks core to look again and reads setup again.
 */
export function ImportLine(props: {
  setup: Setup;
  onImported: (report: ImportReport) => void;
  onRescan: () => Promise<unknown>;
}) {
  const { api, unauthorized } = useKit();
  const found = props.setup.found.imports[0];
  const [state, setState] = useState<ImportState>();
  if (!found) return null;
  const mine = state?.config === found.config ? state : undefined;

  const run = async (preview: boolean) => {
    if (mine?.busy) return;
    // Import now only ever follows a preview of this same config.
    if (!preview && !mine?.report) return;
    const config = found.config;
    setState({ config, report: preview ? undefined : mine?.report, busy: preview ? "preview" : "import" });
    const reply: Reply<ImportReport> = await api.importSetup(config, authDir(found), preview);
    if (reply.status === 401) return unauthorized();
    if (!reply.body || reply.status >= 300) {
      // The preview goes too: what a later try would do is for a new preview to say.
      return setState({
        config,
        refusal: { status: reply.status, message: reply.error?.message ?? problem(reply) ?? "", preview },
      });
    }
    setState({ config, report: reply.body, done: !preview });
    if (!preview) props.onImported(reply.body);
  };
  const again = async () => {
    setState(undefined);
    await props.onRescan();
    await run(true);
  };

  const last = !mine ? found.last_import : undefined;
  const refusal = mine?.refusal;
  const needs = mine?.done
    ? (mine.report?.needs ?? []).map((need) => {
        const pick = props.setup.picks.find((p) => p.id === need.pick);
        return pick ? kindTitle(pick) : need.pick;
      })
    : [];
  return (
    <section
      class="setup-import"
      data-part="setup-import"
      data-state={mine?.done ? "done" : mine?.report ? "preview" : last ? "imported" : "found"}
    >
      {!mine?.done && (
        <div class="setup-import-line">
          {last ? (
            <p>{rich(t("imported"), { when: <Time value={last.at} /> })}</p>
          ) : (
            <p>
              {t("found", { count: found.accounts })}{" "}
              <span class="setup-import-path mono muted" title={found.config}>
                {found.config}
              </span>
            </p>
          )}
          {!mine?.report && (
            <Button
              busy={mine?.busy === "preview"}
              disabled={mine?.busy !== undefined}
              onClick={() => run(true)}
            >
              {last ? t("importAgain") : t("preview")}
            </Button>
          )}
        </div>
      )}
      {refusal && (
        <div class="setup-import-refusal">
          <Notice tone="danger">{refusalWords(refusal)}</Notice>
          {(refusal.status === 400 || (refusal.status === 0 && !refusal.preview)) && (
            <div class="actions">
              <Button onClick={refusal.status === 400 ? again : () => props.onRescan()}>
                {refusal.status === 400 ? t("tryAgain") : t("refresh")}
              </Button>
            </div>
          )}
          {(refusal.status === 404 || refusal.status === 409) && (
            <details class="setup-import-details">
              <summary>{t("details")}</summary>
              <p class="muted">{t(refusal.status === 404 ? "details.404" : "details.409")}</p>
              <Code value={refusal.status === 404 ? importCommand(found) : "pluxy init"} />
            </details>
          )}
        </div>
      )}
      {mine?.report && <Report report={mine.report} done={mine.done} />}
      {mine?.done && needs.length > 0 && <p>{t("needs", { names: joinNames(needs) })}</p>}
      {mine?.report && !mine.done && (
        <div class="actions">
          <Button
            variant="primary"
            busy={mine.busy === "import"}
            disabled={mine.busy !== undefined}
            onClick={() => run(false)}
          >
            {t("importNow")}
          </Button>
          <Button variant="quiet" disabled={mine.busy !== undefined} onClick={() => setState(undefined)}>
            {t("cancel")}
          </Button>
        </div>
      )}
      {mine?.done && (
        <div class="actions">
          <Button onClick={() => setState(undefined)}>{t("close")}</Button>
        </div>
      )}
    </section>
  );
}

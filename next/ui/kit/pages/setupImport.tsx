// The one line above Setup's steps when core found a CLIProxyAPI setup on its computer: a preview of
// what importing it would do, then the import, each only when the person asks. Nothing is sent
// with dry_run false unless a preview is on screen that came from the same config, auth folder,
// account-file count and import record; Import now sends exactly what that preview sent. Any change
// to what core found drops an open preview; a background scan that finds the same setup keeps it,
// and never drops an in-flight call, a refusal or a completed report for the same config.

import { useRef, useState } from "preact/hooks";
import { type ImportReport, type ImportRow, problem, type Setup, type SetupImport } from "../api";
import type { Reply } from "../host";
import { providerName } from "../parts";
import { type Masker, usePrivacy } from "../privacy";
import { rich, strings } from "../strings";
import { Button, Notice, Time, useCopy, useKit } from "../ui";
import { joinNames, kindTitle, providerPicks } from "./setup.helpers";

const t = strings("setupImport", {
  found: {
    one: "Found a CLIProxyAPI setup with {count} account file. pluxy can bring its accounts and settings across; nothing changes until you confirm.",
    other:
      "Found a CLIProxyAPI setup with {count} account files. pluxy can bring its accounts and settings across; nothing changes until you confirm.",
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
  notOffered: {
    one: "This pluxy does not offer {names}, so the accounts that need it cannot be used yet.",
    other: "This pluxy does not offer {names}, so the accounts that need them cannot be used yet.",
  },
  openExtensions: "Open Extensions",
  notRun: "The import did not run: pluxy has no record of it.",
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

/**
 * What a preview is bound to: the config, the auth folder an import of it names, the number of
 * account files the scan counted there, and the last recorded import. Any change is another setup
 * and drops an open preview; a newer scan that found the same setup keeps it.
 */
function setupKey(found: SetupImport): string {
  return JSON.stringify([found.config, authDir(found) ?? "", found.accounts, found.last_import?.at ?? ""]);
}

/** Where the import stands on this page, for the setup it started from (key). */
interface ImportState {
  key: string;
  /** What the preview sent, which Import now sends again. */
  config: string;
  authDir?: string;
  busy?: "preview" | "import";
  /** The preview's report, or once done the import's. */
  report?: ImportReport;
  done?: boolean;
  refusal?: Refusal;
  /** For a call whose reply was lost or found not run: core's last import record before it. */
  lastBefore?: string;
  /** Refresh after a lost reply found no new record: the import did not run. */
  notRun?: boolean;
}

/**
 * Whether state still belongs to found. An open preview must match setupKey so Import now cannot
 * confirm one setup and import another; in-flight calls, refusals and completed reports stay for
 * the same config across background scans (a lost Import now or notRun clears once core records a
 * newer import).
 */
function isMine(state: ImportState | undefined, found: SetupImport): boolean {
  if (!state || state.config !== found.config) return false;
  if (state.busy) return true;
  if (state.report && !state.done) return state.key === setupKey(found);
  if ((state.refusal && !state.refusal.preview && state.refusal.status === 0) || state.notRun) {
    if (found.last_import !== undefined && found.last_import.at !== state.lastBefore) return false;
  }
  return true;
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

function ReportList(props: { title: string; rows: ImportRow[]; mask: Masker }) {
  const p = usePrivacy();
  const { mask } = props;
  if (props.rows.length === 0) return null;
  return (
    <details class="setup-report-list">
      <summary>{props.title}</summary>
      <ul class="setup-report-rows">
        {props.rows.map((row) => (
          <li key={`${row.what}/${row.name ?? ""}`}>
            {/* An auth file's name is an account's identifier (core makes the account's ID from it). */}
            <span class="mono">{[mask(row.what), p.id(row.name)].filter(Boolean).join(" ")}</span>
            {row.why && <span class="muted">{mask(row.why)}</span>}
            {row.keep_with && <span class="muted">{t("keep", { how: mask(row.keep_with) })}</span>}
          </li>
        ))}
      </ul>
    </details>
  );
}

/**
 * The names a report's rows hold, as identities to hide in its text while privacy mode is on: each
 * file's name, and the name without its extension, which core uses for the account's ID.
 */
function reportIdentities(report: ImportReport): string[] {
  const names = [...report.brought, ...report.changed, ...report.not_brought].flatMap((row) =>
    row.name ? [row.name, row.name.replace(/\.[A-Za-z0-9]+$/, "")] : [],
  );
  return [...new Set(names)];
}

/** A report's three folded lists. */
function Report(props: { report: ImportReport; done?: boolean }) {
  const { report } = props;
  const p = usePrivacy();
  const mask = p.masker(reportIdentities(report));
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
        mask={mask}
      />
      <ReportList
        title={t("setDifferently", { count: report.changed.length })}
        rows={report.changed}
        mask={mask}
      />
      <ReportList
        title={t("leftOut", { count: report.not_brought.length })}
        rows={report.not_brought}
        mask={mask}
      />
    </div>
  );
}

/**
 * The import line. onImported runs after an import that worked, with its report (the page ticks
 * what it needs and reads again); onRescan asks core to look again and gives the setup read once
 * that scan is over (nothing when it failed).
 */
export function ImportLine(props: {
  setup: Setup;
  onImported: (report: ImportReport) => void;
  onRescan: () => Promise<Setup | undefined>;
}) {
  const { api, unauthorized } = useKit();
  const found = props.setup.found.imports[0];
  const [state, setState] = useState<ImportState>();
  // A call in flight, also while what it started from is not yet the setup on screen.
  const busy = useRef(false);
  // Try again or Refresh waits for a new scan.
  const [rescanning, setRescanning] = useState(false);
  if (!found) return null;
  const mine = isMine(state, found) ? state : undefined;

  // A preview of what setup found (target), or the import of the preview on screen.
  const run = async (preview: boolean, setup: Setup = props.setup) => {
    const target = setup.found.imports[0];
    if (busy.current || !target) return;
    // Import now only ever follows a preview of this same setup, and sends what it sent.
    if (!preview && (!mine?.report || mine.done)) return;
    const sent = preview
      ? { key: setupKey(target), config: target.config, authDir: authDir(target) }
      : { key: mine?.key ?? "", config: mine?.config ?? "", authDir: mine?.authDir };
    busy.current = true;
    setState({ ...sent, report: preview ? undefined : mine?.report, busy: preview ? "preview" : "import" });
    const reply: Reply<ImportReport> = await api.importSetup(sent.config, sent.authDir, preview);
    busy.current = false;
    if (reply.status === 401) return unauthorized();
    if (!reply.body || reply.status >= 300) {
      // The preview goes too: what a later try would do is for a new preview to say.
      return setState({
        ...sent,
        refusal: { status: reply.status, message: reply.error?.message ?? problem(reply) ?? "", preview },
        lastBefore: target.last_import?.at,
      });
    }
    setState({ ...sent, report: reply.body, done: !preview });
    if (!preview) props.onImported(reply.body);
  };
  // Try again: look again, then preview what the new scan found.
  const again = async () => {
    setRescanning(true);
    const fresh = await props.onRescan();
    setRescanning(false);
    if (fresh) await run(true, fresh);
  };
  // Refresh after Import now lost its reply: a new record of an import says it ran; none, that it
  // did not.
  const refresh = async () => {
    const lastBefore = mine?.lastBefore;
    setRescanning(true);
    const fresh = await props.onRescan();
    setRescanning(false);
    // A scan that failed says so itself; the line keeps saying the result is not known.
    if (!fresh) return;
    const target = fresh.found.imports[0];
    if (!target || (target.last_import !== undefined && target.last_import.at !== lastBefore)) {
      return setState(undefined);
    }
    setState({
      key: setupKey(target),
      config: target.config,
      authDir: authDir(target),
      notRun: true,
      lastBefore: target.last_import?.at,
    });
  };

  const last = !mine ? found.last_import : undefined;
  const refusal = mine?.refusal;
  const offered = new Set(providerPicks(props.setup).map((pick) => pick.id));
  const needs = mine?.done ? (mine.report?.needs ?? []) : [];
  const ticked = needs.flatMap((need) => {
    const pick = props.setup.picks.find((p) => p.id === need.pick && offered.has(p.id));
    return pick ? [kindTitle(pick)] : [];
  });
  // A kind this pluxy does not offer, by its name: never its package ID.
  const missing = needs.filter((need) => !offered.has(need.pick)).map((need) => providerName(need.pick));
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
      {mine?.notRun && <Notice tone="warn">{t("notRun")}</Notice>}
      {refusal && (
        <div class="setup-import-refusal">
          <Notice tone="danger">{refusalWords(refusal)}</Notice>
          {(refusal.status === 400 || (refusal.status === 0 && !refusal.preview)) && (
            <div class="actions">
              <Button
                busy={rescanning}
                disabled={rescanning}
                onClick={refusal.status === 400 ? again : refresh}
              >
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
      {ticked.length > 0 && <p>{t("needs", { names: joinNames(ticked) })}</p>}
      {missing.length > 0 && (
        <p>
          {t("notOffered", { count: missing.length, names: joinNames(missing) })}{" "}
          <a href="#/extensions">{t("openExtensions")}</a>
        </p>
      )}
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

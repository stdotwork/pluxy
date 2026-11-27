// The Config page's Settings file tab: the file as text, with passwords and keys as placeholders,
// a find bar, Check for mistakes, and Review and save, which shows what changes before it saves.

import { useEffect, useMemo, useRef, useState } from "preact/hooks";
import { problem } from "../api";
import type { ConfigDocument } from "../api/config";
import { lineDiff } from "../diff";
import { usePrivacy } from "../privacy";
import { Button, Notice, useKit } from "../ui";
import { ConfigDialog } from "./configDialog";
import { findAccount, hideSecrets } from "./configSource.helpers";
import { fileError, configText as t } from "./configStrings";

export interface ConfigSourceProps {
  busy: boolean;
  setBusy: (busy: boolean) => void;
  base: ConfigDocument;
  /** An account to select in the text when it is shown, once for each account asked. */
  account?: string;
  text: string;
  setText: (text: string) => void;
  saved: (doc: ConfigDocument) => void;
  reload: () => void;
  /** A save core refused with 422: the page reads the file again, as it may no longer parse. */
  refused: () => void;
}

/** Selects the text from start to end and scrolls the editor so that it is in view, centred. */
function showRange(editor: HTMLTextAreaElement, start: number, end: number) {
  editor.setSelectionRange(start, end);
  const before = editor.value.slice(0, start);
  const row = before.split("\n").length - 1;
  const column = start - (before.lastIndexOf("\n") + 1);
  const style = getComputedStyle(editor);
  editor.scrollTop = row * Number.parseFloat(style.lineHeight) - editor.clientHeight / 2;
  // The text does not wrap: a long line, such as core's config written on one line, scrolls sideways.
  const ctx = document.createElement("canvas").getContext("2d");
  if (ctx) {
    ctx.font = style.font;
    editor.scrollLeft = column * ctx.measureText("0").width - editor.clientWidth / 2;
  }
}

/**
 * The review's lines. An added line shows a key, password or other secret typed into the file
 * hidden, so the review never shows it a second time, also not while sharing the screen; the line
 * still shows as changed. The removed and unchanged lines come from the file as core showed it,
 * with its secrets already placeholders.
 */
function reviewLines(before: string, after: string, format: ConfigDocument["format"]) {
  const hidden = hideSecrets(after, format);
  let at = 0;
  return lineDiff(before, after).map((line) => {
    if (line.change === "remove") return line;
    const text = line.change === "add" ? (hidden[at] ?? line.text) : line.text;
    at++;
    return { ...line, text };
  });
}

/** What a check or a save that core refused says: a mistake in the file, or why it failed. */
function refusal(status: number, why: string) {
  return status === 400 || status === 422
    ? why
      ? t("mistake", { error: fileError(why) })
      : t("mistakeEmpty")
    : why;
}

export function ConfigSource(props: ConfigSourceProps) {
  const { api, unauthorized } = useKit();
  const p = usePrivacy();
  const { busy, setBusy } = props;
  const [error, setError] = useState<string>();
  const [message, setMessage] = useState<string>();
  const [review, setReview] = useState(false);
  const [refreshAsk, setRefreshAsk] = useState(false);
  const [query, setQuery] = useState("");
  const [match, setMatch] = useState({ query: "", text: "", index: -1 });
  const editor = useRef<HTMLTextAreaElement>(null);
  const diff = useRef<HTMLPreElement>(null);
  // The review opens at the first line that changed, which may be far down a long file.
  useEffect(() => {
    const first = diff.current?.querySelector<HTMLElement>("[data-change]");
    if (review && first && diff.current)
      diff.current.scrollTop = Math.max(
        0,
        first.offsetTop - diff.current.offsetTop - diff.current.clientHeight / 3,
      );
  }, [review]);
  const dirty = props.text !== props.base.content;
  const matches = useMemo(() => {
    const found: number[] = [];
    if (!query) return found;
    const haystack = props.text.toLowerCase();
    const needle = query.toLowerCase();
    for (let at = haystack.indexOf(needle); at !== -1; at = haystack.indexOf(needle, at + needle.length))
      found.push(at);
    return found;
  }, [query, props.text]);
  const index = matches.length && match.query === query && match.text === props.text ? match.index : -1;
  const selectMatch = (n: number) => {
    if (!matches.length) return;
    const selected = (n + matches.length) % matches.length;
    setMatch({ query, text: props.text, index: selected });
    const start = matches[selected] ?? 0;
    if (editor.current) showRange(editor.current, start, start + query.length);
  };
  const clear = () => {
    setError(undefined);
    setMessage(undefined);
  };
  // Open at the account asked for, once the editor can take a selection (it is off while busy).
  const shownFor = useRef<string | undefined>(undefined);
  const [missing, setMissing] = useState(false);
  useEffect(() => {
    if (!props.account) {
      shownFor.current = undefined;
      setMissing(false);
      return;
    }
    const field = editor.current;
    if (busy || !field || shownFor.current === props.account) return;
    shownFor.current = props.account;
    const found = findAccount(props.text, props.account);
    setMissing(!found);
    if (!found) return;
    field.focus();
    showRange(field, found.start, found.end);
  }, [props.account, busy]);
  const check = async () => {
    setBusy(true);
    clear();
    const reply = await api.validateConfig(props.text);
    setBusy(false);
    if (reply.status === 401) return unauthorized();
    const why = problem(reply);
    if (why !== undefined) setError(why);
    else if (!reply.body?.valid)
      setError(reply.body?.error ? t("mistake", { error: fileError(reply.body.error) }) : t("mistakeEmpty"));
    else setMessage([t("noMistakes"), ...(reply.body.notices ?? [])].join("\n"));
  };
  const save = async () => {
    setBusy(true);
    clear();
    const reply = await api.saveConfig(props.text, props.base.hash);
    setBusy(false);
    if (reply.status === 401) return unauthorized();
    const why = problem(reply);
    setReview(false);
    if (why !== undefined) {
      // The edits stay in the editor: nothing is lost.
      setError(reply.status === 409 ? t("conflict") : refusal(reply.status, reply.error?.message ?? ""));
      if (reply.status === 422) props.refused();
    } else if (reply.body) {
      props.saved(reply.body);
      setMessage(t("saved"));
    }
  };
  return (
    <div class="stack config-source">
      <p class="muted config-hint">{t("hint")}</p>
      {missing && <Notice tone="muted">{t("accountNotFound")}</Notice>}
      <div class="config-find actions">
        <input
          data-slot="input"
          type="search"
          aria-label={t("find")}
          placeholder={t("find")}
          value={query}
          onInput={(e) => {
            setQuery(e.currentTarget.value);
            setMatch({ query: "", text: "", index: -1 });
          }}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault();
              selectMatch(e.shiftKey ? (index < 0 ? matches.length - 1 : index - 1) : index + 1);
            }
          }}
        />
        <Button
          disabled={!matches.length}
          onClick={() => selectMatch(index < 0 ? matches.length - 1 : index - 1)}
        >
          {t("previous")}
        </Button>
        <Button disabled={!matches.length} onClick={() => selectMatch(index + 1)}>
          {t("next")}
        </Button>
        <span role="status" class="config-find-count">
          {query &&
            (!matches.length
              ? t("noMatches")
              : index < 0
                ? t("found", { count: matches.length })
                : t("matches", { current: index + 1, count: matches.length }))}
        </span>
      </div>
      <textarea
        ref={editor}
        data-part="code"
        class="config-editor"
        aria-label={t("editor")}
        spellcheck={false}
        disabled={busy}
        value={props.text}
        onInput={(e) => {
          props.setText(e.currentTarget.value);
          clear();
        }}
      />
      {error && <Notice tone="danger">{p.text(error)}</Notice>}
      {message && <Notice tone="ok">{p.text(message)}</Notice>}
      <div class="actions">
        <Button disabled={busy} onClick={check}>
          {t("check")}
        </Button>
        {dirty && (
          <Button
            variant="primary"
            disabled={busy}
            onClick={() => {
              clear();
              setReview(true);
            }}
          >
            {t("reviewSave")}
          </Button>
        )}
        <Button
          disabled={busy}
          onClick={() => {
            if (dirty) return setRefreshAsk(true);
            clear();
            props.reload();
          }}
        >
          {t("refresh")}
        </Button>
        {dirty && (
          <Button
            disabled={busy}
            onClick={() => {
              props.setText(props.base.content);
              clear();
            }}
          >
            {t("discardEdits")}
          </Button>
        )}
      </div>
      {review && (
        <ConfigDialog
          title={t("review")}
          close={() => {
            if (!busy) setReview(false);
          }}
        >
          <div class="config-diff-legend">
            <span data-change="remove">− {t("removed")}</span>
            <span data-change="add">+ {t("added")}</span>
          </div>
          <pre data-part="diff" class="config-diff" ref={diff}>
            {reviewLines(props.base.content, props.text, props.base.format).map((line, i) => (
              <span
                key={i}
                data-part="diff-line"
                data-change={line.change === "same" ? undefined : line.change}
              >
                {line.change === "add" ? "+ " : line.change === "remove" ? "− " : "  "}
                {line.text}
                {!line.text.endsWith("\n") && <small>{`\n\\ ${t("noNewline")}\n`}</small>}
              </span>
            ))}
          </pre>
          <div class="actions">
            <Button variant="primary" disabled={busy} onClick={save}>
              {t("save")}
            </Button>
            <Button disabled={busy} onClick={() => setReview(false)}>
              {t("cancel")}
            </Button>
          </div>
        </ConfigDialog>
      )}
      {refreshAsk && (
        <ConfigDialog title={t("refreshQuestion")} close={() => setRefreshAsk(false)}>
          <div class="actions">
            <Button
              variant="danger"
              onClick={() => {
                setRefreshAsk(false);
                clear();
                props.reload();
              }}
            >
              {t("refresh")}
            </Button>
            <Button onClick={() => setRefreshAsk(false)}>{t("cancel")}</Button>
          </div>
        </ConfigDialog>
      )}
    </div>
  );
}

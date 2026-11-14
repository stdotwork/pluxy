import { useEffect, useMemo, useRef, useState } from "preact/hooks";
import { problem } from "../api";
import type { ConfigDocument } from "../api/config";
import { lineDiff } from "../diff";
import { usePrivacy } from "../privacy";
import { Button, Card, Notice, useKit } from "../ui";
import { ConfigDialog } from "./configDialog";
import { findAccount } from "./configSource.helpers";
import { configText as t } from "./configStrings";

export interface ConfigSourceProps {
  loading: boolean;
  setBusy: (busy: boolean) => void;
  base: ConfigDocument;
  /** An account to select in the text when it is shown, once for each account asked. */
  account?: string;
  text: string;
  setText: (text: string) => void;
  saved: (doc: ConfigDocument) => void;
  reload: () => void;
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
export function ConfigSource(props: ConfigSourceProps) {
  const { api, unauthorized } = useKit();
  const p = usePrivacy();
  const busy = props.loading;
  const setBusy = props.setBusy;
  const [error, setError] = useState<string>();
  const [message, setMessage] = useState<string>();
  const [review, setReview] = useState(false);
  const [reloadConfirm, setReloadConfirm] = useState(false);
  const [query, setQuery] = useState("");
  const [match, setMatch] = useState({ query: "", text: "", index: -1 });
  const editor = useRef<HTMLTextAreaElement>(null);
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
  // Open at the account asked for, once the editor can take a selection (it is off while busy).
  const shownFor = useRef<string | undefined>(undefined);
  const [missing, setMissing] = useState<string>();
  useEffect(() => {
    if (!props.account) {
      shownFor.current = undefined;
      setMissing(undefined);
      return;
    }
    const field = editor.current;
    if (busy || !field || shownFor.current === props.account) return;
    shownFor.current = props.account;
    const found = findAccount(props.text, props.account);
    setMissing(found ? undefined : props.account);
    if (!found) return;
    field.focus();
    showRange(field, found.start, found.end);
  }, [props.account, busy]);
  const validate = async () => {
    setBusy(true);
    setError(undefined);
    setMessage(undefined);
    const reply = await api.validateConfig(props.text);
    setBusy(false);
    if (reply.status === 401) return unauthorized();
    const why = problem(reply);
    if (why) setError(why);
    else if (!reply.body?.valid) setError(reply.body?.error);
    else setMessage([t("valid"), ...(reply.body.notices ?? [])].join("\n"));
  };
  const save = async () => {
    setBusy(true);
    setError(undefined);
    setMessage(undefined);
    const reply = await api.saveConfig(props.text, props.base.hash);
    setBusy(false);
    if (reply.status === 401) return unauthorized();
    const why = problem(reply);
    if (why) {
      setError(reply.status === 409 ? t("conflict") : why);
      setReview(false);
    } else if (reply.body) {
      props.saved(reply.body);
      setReview(false);
      setMessage(t("saved"));
    }
  };
  return (
    <div class="stack">
      <Card>
        <p class="muted">{t("sourceHint")}</p>
        {missing && <Notice tone="muted">{t("accountNotFound", { id: p.text(missing) })}</Notice>}
        <div class="config-source-search actions">
          <input
            type="search"
            aria-label={t("searchConfig")}
            placeholder={t("searchConfig")}
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
          <span role="status">
            {query &&
              (matches.length ? t("matches", { current: index + 1, count: matches.length }) : t("noResults"))}
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
            setError(undefined);
            setMessage(undefined);
          }}
        />
      </Card>
      {error && <Notice tone="danger">{p.text(error)}</Notice>}
      {message && <Notice tone="ok">{p.text(message)}</Notice>}
      <div class="actions">
        <Button disabled={busy} onClick={validate}>
          {t("validate")}
        </Button>
        <Button
          variant="primary"
          disabled={!dirty || busy}
          onClick={() => {
            setError(undefined);
            setReview(true);
          }}
        >
          {t("saveChanges")}
        </Button>
        <Button disabled={busy} onClick={() => (dirty ? setReloadConfirm(true) : props.reload())}>
          {t("reload")}
        </Button>
        <Button
          disabled={!dirty || busy}
          onClick={() => {
            props.setText(props.base.content);
            setError(undefined);
            setMessage(undefined);
          }}
        >
          {t("discard")}
        </Button>
      </div>
      {review && (
        <ConfigDialog
          title={t("review")}
          close={() => {
            if (!busy) setReview(false);
          }}
        >
          <h3>{t("showChanges")}</h3>
          <div class="config-diff-legend">
            <span>{t("before")} −</span>
            <span>{t("after")} +</span>
          </div>
          <pre data-part="diff" class="config-diff">
            {lineDiff(props.base.content, props.text).map((line, i) => (
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
      {reloadConfirm && (
        <ConfigDialog title={t("reload")} close={() => setReloadConfirm(false)}>
          <p>{t("reloadMessage")}</p>
          <div class="actions">
            <Button
              onClick={() => {
                setReloadConfirm(false);
                setError(undefined);
                setMessage(undefined);
                props.reload();
              }}
            >
              {t("reload")}
            </Button>
            <Button onClick={() => setReloadConfirm(false)}>{t("cancel")}</Button>
          </div>
        </ConfigDialog>
      )}
    </div>
  );
}

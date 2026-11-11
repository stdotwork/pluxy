import { useMemo, useRef, useState } from "preact/hooks";
import { problem } from "../api";
import type { ConfigDocument } from "../api/config";
import { lineDiff } from "../diff";
import { Button, Card, Notice, useKit } from "../ui";
import { ConfigDialog } from "./configDialog";
import { configText as t } from "./configStrings";

export interface ConfigSourceProps {
  loading: boolean;
  setBusy: (busy: boolean) => void;
  base: ConfigDocument;
  text: string;
  setText: (text: string) => void;
  saved: (doc: ConfigDocument) => void;
  reload: () => void;
}
export function ConfigSource(props: ConfigSourceProps) {
  const { api, unauthorized } = useKit();
  const busy = props.loading;
  const setBusy = props.setBusy;
  const [error, setError] = useState<string>();
  const [message, setMessage] = useState<string>();
  const [review, setReview] = useState(false);
  const [reloadConfirm, setReloadConfirm] = useState(false);
  const [query, setQuery] = useState("");
  const [match, setMatch] = useState(0);
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
  const index = matches.length ? match % matches.length : 0;
  const selectMatch = (n: number) => {
    if (!matches.length) return;
    const selected = (n + matches.length) % matches.length;
    setMatch(selected);
    const start = matches[selected] ?? 0;
    editor.current?.focus();
    editor.current?.setSelectionRange(start, start + query.length);
    if (editor.current) {
      const row = props.text.slice(0, start).split("\n").length - 1;
      const height = Number.parseFloat(getComputedStyle(editor.current).lineHeight);
      editor.current.scrollTop = row * height - editor.current.clientHeight / 2;
    }
  };
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
        <div class="config-source-search actions" data-part="field">
          <input
            type="search"
            aria-label={t("searchConfig")}
            placeholder={t("searchConfig")}
            value={query}
            onInput={(e) => {
              setQuery(e.currentTarget.value);
              setMatch(0);
            }}
            onKeyDown={(e) => {
              if (e.key === "Enter") {
                e.preventDefault();
                selectMatch(e.shiftKey ? index - 1 : index + 1);
              }
            }}
          />
          <Button disabled={!matches.length} onClick={() => selectMatch(index - 1)}>
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
      {error && <Notice tone="danger">{error}</Notice>}
      {message && <Notice tone="ok">{message}</Notice>}
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

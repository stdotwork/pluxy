// Small pieces the app's Updates, Add-ons and Launcher pages share: what a page says about itself in
// one line, the native error text behind a Details disclosure, a text field in a setting's row, and
// a call whose "cancelled" answer is the person's own choice.

import type { ComponentChildren } from "preact";
import { useId } from "preact/hooks";
import type { Reply } from "../../ui/kit/host";
import { usePrivacy } from "../../ui/kit/privacy";
import { strings } from "../../ui/kit/strings";

const t = strings("pageBits", { details: "Details" });

/** One quiet line under a page's title: what the page is for, or why it cannot do its job here. */
export function Intro(props: { children: ComponentChildren }) {
  return <p class="page-intro">{props.children}</p>;
}

/** The text the native side gave, for whoever needs it, behind a disclosure and masked in privacy mode. */
export function Details(props: { text: string }) {
  const p = usePrivacy();
  return (
    <details class="page-details">
      <summary>{t("details")}</summary>
      <pre class="mono">{p.text(props.text)}</pre>
    </details>
  );
}

/**
 * A text field for a setting's row, which already shows the setting's name: the label is only read
 * aloud (the row's styles hide it). lines makes it a box of several lines.
 */
export function TextField(props: {
  label: string;
  value: string;
  onInput: (value: string) => void;
  lines?: boolean;
  placeholder?: string;
  data?: string;
}) {
  const id = useId();
  const onInput = (e: { currentTarget: HTMLInputElement | HTMLTextAreaElement }) =>
    props.onInput(e.currentTarget.value);
  return (
    <div class="field" data-part="field">
      <label class="label" for={id}>
        {props.label}
      </label>
      {props.lines ? (
        <textarea id={id} rows={3} spellcheck={false} value={props.value} onInput={onInput} />
      ) : (
        <input
          id={id}
          type="text"
          data-part={props.data}
          autocomplete="off"
          spellcheck={false}
          placeholder={props.placeholder}
          value={props.value}
          onInput={onInput}
        />
      )}
    </div>
  );
}

/** Whether the native side says the person closed a confirmation window rather than that something failed. */
export const wasCancelled = (message?: string): boolean => !!message && /\bcancell?ed\b/i.test(message);

/** A call whose "cancelled" answer is the person's own choice: it shows nothing. */
export const askFirst = (call: () => Promise<Reply<unknown>>) => async (): Promise<Reply<unknown>> => {
  const r = await call();
  return wasCancelled(r.error?.message) ? { status: 200 } : r;
};

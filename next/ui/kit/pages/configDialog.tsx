import type { ComponentChildren } from "preact";
import { useEffect, useId, useRef } from "preact/hooks";

/** A modal uses the platform's focus trap and restores focus when closed. */
export function ConfigDialog(props: { title: string; close: () => void; children: ComponentChildren }) {
  const ref = useRef<HTMLDialogElement>(null);
  const id = useId();
  useEffect(() => {
    const dialog = ref.current;
    dialog?.showModal();
    return () => dialog?.close();
  }, []);
  return (
    <dialog
      ref={ref}
      data-part="dialog"
      class="config-dialog"
      aria-labelledby={id}
      onCancel={(e) => {
        e.preventDefault();
        props.close();
      }}
    >
      <h2 id={id}>{props.title}</h2>
      {props.children}
    </dialog>
  );
}

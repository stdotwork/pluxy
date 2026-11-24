import type { ComponentChildren } from "preact";
import { useEffect, useId, useRef } from "preact/hooks";

/** A modal uses the platform's focus trap, and gives focus back to the control that opened it when closed. */
export function ConfigDialog(props: { title: string; close: () => void; children: ComponentChildren }) {
  const ref = useRef<HTMLDialogElement>(null);
  const id = useId();
  useEffect(() => {
    const dialog = ref.current;
    // The control that opened the dialog, which gets focus back when it closes: a dialog that
    // is removed from the page does not return it by itself.
    const opener = document.activeElement;
    dialog?.showModal();
    return () => {
      dialog?.close();
      if (opener instanceof HTMLElement && opener !== document.body && opener.isConnected) opener.focus();
    };
  }, []);
  return (
    <dialog
      ref={ref}
      data-part="dialog"
      data-slot="dialog-popup"
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

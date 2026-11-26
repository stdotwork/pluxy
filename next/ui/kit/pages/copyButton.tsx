// A button that copies a value and says it did, for the pages that show one.

import { strings } from "../strings";
import { Button, useCopy } from "../ui";

const t = strings("copyButton", { copy: "Copy", copied: "Copied", failed: "Not copied" });

/**
 * Copies value. label is the visible word when it is not "Copy"; name is what a screen reader says
 * when several buttons read alike ("Copy gpt-5").
 */
export function CopyButton(props: { value: string; label?: string; name?: string }) {
  const { state, copy } = useCopy(props.value);
  return (
    <Button label={props.name} onClick={copy}>
      <span aria-live="polite">
        {state === "copied" ? t("copied") : state === "failed" ? t("failed") : (props.label ?? t("copy"))}
      </span>
    </Button>
  );
}

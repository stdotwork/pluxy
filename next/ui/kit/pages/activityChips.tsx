// The small parts Activity's pages share: a word as a chip (a failure, "2nd", "via backup"), a
// filter as a chip that comes off with a press, and the Filter button that offers the filters.

import type { ComponentChildren } from "preact";
import { useEffect, useId, useRef, useState } from "preact/hooks";
import { Icon } from "../icons";
import { strings } from "../strings";

const t = strings("activity.chips", {
  filter: "Filter",
  filterRequests: "Filter requests",
  remove: "Remove this filter",
  chip: "{kind}: {name}",
});

/**
 * A word as a chip. danger is a failure's word (never colour alone: the word is the chip); the
 * neutral one says what the list notes about a request, such as "2nd".
 */
export function WordChip(props: { tone?: "danger"; title?: string; children: ComponentChildren }) {
  return (
    <span class="word-chip" data-part="word-chip" data-tone={props.tone} title={props.title}>
      {props.children}
    </span>
  );
}

/** One applied filter, as a chip: "Account: studio-max ×". A press takes it off. */
export function FilterChip(props: {
  kind: string;
  name: string;
  onRemove: () => void;
  /** The data-part the chip carries: the Usage tab keeps its own, usage-chip. */
  part?: "filter-chip" | "usage-chip";
}) {
  return (
    <button
      type="button"
      class="filter-chip"
      data-part={props.part ?? "filter-chip"}
      title={t("remove")}
      onClick={props.onRemove}
    >
      {t("chip", { kind: props.kind, name: props.name })} <span aria-hidden="true">×</span>
      <span class="sr-only">{t("remove")}</span>
    </button>
  );
}

/** What the Filter button offers: one list of names to pick from, by what it filters. */
export interface FilterChoice<K extends string> {
  filter: K;
  /** "Account": the kind, in the chip and beside the list. */
  kind: string;
  /** "Every account": the list's name for assistive technology, and its empty choice. */
  every: string;
  options: { value: string; label: string }[];
  value: string;
}

/**
 * The Filter button: it opens a small panel with a list for each thing to filter by. A pick takes
 * effect at once (the page shows it as a chip), and Escape or a press outside closes the panel.
 */
export function FilterMenu<K extends string>(props: {
  choices: FilterChoice<K>[];
  onPick: (filter: K, value: string) => void;
}) {
  const [open, setOpen] = useState(false);
  const id = useId();
  const root = useRef<HTMLDivElement>(null);
  const button = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    if (!open) return;
    const away = (e: PointerEvent) => {
      if (e.target instanceof Node && !root.current?.contains(e.target)) setOpen(false);
    };
    const onEscape = (e: KeyboardEvent) => {
      if (e.key !== "Escape") return;
      e.preventDefault();
      setOpen(false);
      button.current?.focus();
    };
    document.addEventListener("pointerdown", away);
    document.addEventListener("keydown", onEscape);
    return () => {
      document.removeEventListener("pointerdown", away);
      document.removeEventListener("keydown", onEscape);
    };
  }, [open]);
  if (props.choices.length === 0) return null;
  const picked = props.choices.filter((c) => c.value !== "").length;
  return (
    <div class="filter-menu" data-part="filter-menu" ref={root}>
      <button
        type="button"
        class="filter-button"
        data-part="filter-button"
        ref={button}
        aria-expanded={open}
        aria-controls={`${id}-panel`}
        data-active={picked > 0 ? "" : undefined}
        onClick={() => setOpen(!open)}
      >
        <Icon name="plus" size={14} />
        {t("filter")}
      </button>
      {open && (
        <fieldset class="filter-panel" id={`${id}-panel`}>
          <legend class="sr-only">{t("filterRequests")}</legend>
          {props.choices.map((c) => {
            const known = c.options.some((o) => o.value === c.value);
            const options =
              c.value && !known ? [...c.options, { value: c.value, label: c.value }] : c.options;
            return (
              <label class="field" data-part="field" key={c.filter}>
                <span class="label">{c.kind}</span>
                <select
                  data-slot="select-trigger"
                  aria-label={c.every}
                  value={c.value}
                  onChange={(e) => props.onPick(c.filter, e.currentTarget.value)}
                >
                  <option value="">{c.every}</option>
                  {options.map((o) => (
                    <option key={o.value} value={o.value}>
                      {o.label}
                    </option>
                  ))}
                </select>
              </label>
            );
          })}
        </fieldset>
      )}
    </div>
  );
}

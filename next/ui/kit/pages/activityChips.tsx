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

export function WordChip(props: { tone?: "danger"; title?: string; children: ComponentChildren }) {
  return (
    <span class="word-chip" data-part="word-chip" data-tone={props.tone} title={props.title}>
      {props.children}
    </span>
  );
}

export function FilterChip(props: {
  kind: string;
  name: string;
  onRemove: () => void;
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

export interface FilterChoice<K extends string> {
  filter: K;
  kind: string;
  every: string;
  options: { value: string; label: string }[];
  value: string;
}

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

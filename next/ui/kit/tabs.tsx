import type { ComponentChildren } from "preact";
import { useId, useRef } from "preact/hooks";
import { useLayout } from "./ui";

export interface Tab {
  id: string;
  label: string;
}

/** Manual activation: arrows move focus, Enter or Space opens the focused tab. */
export function Tabs(props: {
  label: string;
  tabs: Tab[];
  active: string;
  onChange: (id: string) => void;
  children: ComponentChildren;
}) {
  const id = useId();
  const ref = useRef<HTMLDivElement>(null);
  const { tabs: layout } = useLayout();
  return (
    <div data-part="tabs" data-layout={layout} class="kit-tabs" ref={ref}>
      <div
        role="tablist"
        aria-label={props.label}
        aria-orientation={layout === "side" ? "vertical" : "horizontal"}
      >
        {props.tabs.map((tab, i) => (
          <button
            key={tab.id}
            id={id + tab.id}
            data-part="tab"
            type="button"
            role="tab"
            aria-selected={tab.id === props.active}
            aria-controls={`${id}-panel`}
            tabIndex={tab.id === props.active ? 0 : -1}
            onClick={() => props.onChange(tab.id)}
            onKeyDown={(e) => {
              const next = layout === "side" ? "ArrowDown" : "ArrowRight";
              const prev = layout === "side" ? "ArrowUp" : "ArrowLeft";
              let index = i;
              if (e.key === next) index = (i + 1) % props.tabs.length;
              else if (e.key === prev) index = (i - 1 + props.tabs.length) % props.tabs.length;
              else if (e.key === "Home") index = 0;
              else if (e.key === "End") index = props.tabs.length - 1;
              else return;
              e.preventDefault();
              ref.current?.querySelectorAll<HTMLButtonElement>('[role="tab"]')[index]?.focus();
            }}
          >
            {tab.label}
          </button>
        ))}
      </div>
      <div data-part="tab-panel" role="tabpanel" id={`${id}-panel`} aria-labelledby={id + props.active}>
        {props.children}
      </div>
    </div>
  );
}

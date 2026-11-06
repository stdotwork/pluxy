import { useId, useState } from "preact/hooks";
import type { Layout, LayoutOption } from "../theme/contract";
import { resolve } from "../theme/engine";
import { Button, Card, type Choice, Choices, Notice, PageHead, Switch, useThemeState } from "../ui";

const LAYOUT_CHOICES: { [K in LayoutOption]: { label: string; choices: Choice<Layout[K]>[] } } = {
  nav: {
    label: "Navigation",
    choices: [
      { value: "sidebar", label: "Sidebar" },
      { value: "rail", label: "Rail" },
      { value: "top", label: "Top" },
    ],
  },
  list: {
    label: "Lists",
    choices: [
      { value: "table", label: "Table" },
      { value: "cards", label: "Cards" },
    ],
  },
  detail: {
    label: "Details",
    choices: [
      { value: "side", label: "Beside" },
      { value: "below", label: "Below" },
    ],
  },
  width: {
    label: "Width",
    choices: [
      { value: "wide", label: "Wide" },
      { value: "full", label: "Full" },
      { value: "narrow", label: "Narrow" },
    ],
  },
};

const DENSITY: Choice<string>[] = [
  { value: "0.75", label: "Compact" },
  { value: "1", label: "Normal" },
  { value: "1.25", label: "Roomy" },
];

/** Marks the value the theme itself sets, so the person sees what a reset brings back. */
function noted<V extends string>(choices: Choice<V>[], themeValue: string | undefined): Choice<V>[] {
  return choices.map((c) => (c.value === themeValue ? { ...c, note: "· theme" } : c));
}

function ThemePicker() {
  const themes = useThemeState();
  const id = useId();
  return (
    <div class="themes" role="radiogroup" aria-labelledby={`${id}-l`}>
      <span class="label" id={`${id}-l`}>
        Theme
      </span>
      <div class="theme-grid">
        {themes.offered.map((o) => {
          // Each card previews the theme's own colours in its light (or only) appearance.
          const t = resolve(
            o.theme,
            { appearance: "system", layout: {}, css: "", safe: true },
            {
              systemDark: o.theme.appearance === "dark",
              reducedMotion: false,
            },
          ).tokens;
          return (
            <label key={o.theme.id} class="theme-card" data-part="card">
              <input
                type="radio"
                name={id}
                checked={themes.current.theme.id === o.theme.id}
                onChange={() => themes.select(o.theme.id)}
              />
              <span
                class="swatch"
                aria-hidden="true"
                style={{ background: t["color.bg"], borderColor: t["color.border"] }}
              >
                <span style={{ background: t["color.surface"] }} />
                <span style={{ background: t["color.accent"] }} />
                <span style={{ background: t["color.text"] }} />
              </span>
              <span class="theme-name">{o.theme.name}</span>
              <small class="muted">{o.theme.description ?? o.theme.id}</small>
              {!o.builtIn && <small class="muted mono">{o.theme.id} · imported</small>}
            </label>
          );
        })}
      </div>
    </div>
  );
}

function ImportTheme() {
  const themes = useThemeState();
  const [error, setError] = useState<string>();
  const current = themes.current;
  return (
    <Card title="Your themes">
      <label class="field" data-part="field">
        <span class="label">Import a theme file</span>
        <input
          type="file"
          accept=".json,application/json"
          onChange={async (e) => {
            const file = e.currentTarget.files?.[0];
            e.currentTarget.value = "";
            if (!file) return;
            setError(themes.importTheme(await file.text()));
          }}
        />
      </label>
      {error && <Notice tone="danger">Not imported: {error}</Notice>}
      <p class="muted">
        A theme is a JSON file of tokens, layout options and optional CSS. It cannot run code. The theme
        contract in pluxy's source, <span class="mono">next/ui/themes/README.md</span>, lists everything a
        theme can set.
      </p>
      {!current.builtIn && (
        <div class="actions">
          <Button variant="danger" onClick={() => themes.removeTheme(current.theme.id)}>
            Remove {current.theme.name}
          </Button>
        </div>
      )}
    </Card>
  );
}

export function ThemesPage() {
  const themes = useThemeState();
  const { theme, warnings } = themes.current;
  const r = themes.resolved;
  const single = theme.appearance !== "both";
  const allWarnings = [...warnings, ...r.warnings];

  return (
    <>
      <PageHead title="Themes" />
      <div class="stack">
        <Card>
          <ThemePicker />
          {allWarnings.length > 0 && (
            <Notice tone="warn">
              {theme.name}: {allWarnings.join("; ")}
            </Notice>
          )}
        </Card>

        <Card>
          <Choices
            label="Appearance"
            value={single ? theme.appearance : themes.choices.appearance}
            choices={
              single
                ? [
                    {
                      value: theme.appearance,
                      label: theme.appearance === "dark" ? "Dark only" : "Light only",
                    },
                  ]
                : [
                    { value: "system", label: "System" },
                    { value: "light", label: "Light" },
                    { value: "dark", label: "Dark" },
                  ]
            }
            onChange={(v) => themes.set({ appearance: v as "system" | "light" | "dark" })}
          />
        </Card>

        <Card
          title="Layout"
          actions={<Button onClick={() => themes.resetLayout()}>Use the theme's layout</Button>}
        >
          <p class="muted">Your choices here stay when you switch themes.</p>
          {(Object.keys(LAYOUT_CHOICES) as LayoutOption[]).map((option) => {
            const o = LAYOUT_CHOICES[option];
            return (
              <Choices<string>
                key={option}
                label={o.label}
                value={r.layout[option]}
                choices={noted(o.choices as Choice<string>[], theme.layout[option] ?? o.choices[0]?.value)}
                onChange={(v) => themes.setLayout(option, v as Layout[typeof option])}
              />
            );
          })}
          <Choices
            label="Density"
            value={r.tokens.density}
            choices={noted(DENSITY, theme.tokens.density ?? "1")}
            onChange={(v) => themes.set({ density: v })}
          />
        </Card>

        <Card title="Custom CSS">
          <Switch
            label="Safe mode"
            hint="Turns off all custom CSS, the theme's and yours. If custom CSS hides this switch, open the panel with ?safe at the end of its address."
            checked={themes.choices.safe}
            onChange={(v) => themes.setSafe(v)}
          />
          {theme.css && (
            <p class="muted">
              {theme.name} adds its own CSS{themes.choices.safe ? ", turned off by safe mode" : ""}.
            </p>
          )}
          <label class="field" data-part="field">
            <span class="label">Your CSS</span>
            <textarea
              class="mono"
              rows={6}
              spellcheck={false}
              placeholder={'[data-part="nav"] { letter-spacing: 0.04em; }'}
              value={themes.choices.css}
              onInput={(e) => themes.set({ css: e.currentTarget.value })}
            />
            <small class="hint">
              Applied on top of every theme, as you type. Style the data-part hooks the theme contract lists;
              other markup can change in any release.
            </small>
          </label>
        </Card>

        <ImportTheme />
      </div>
    </>
  );
}

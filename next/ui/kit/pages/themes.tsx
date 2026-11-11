import { useId, useState } from "preact/hooks";
import type { PageDef } from "../shell";
import { rich, strings } from "../strings";
import type { Layout, LayoutOption } from "../theme/contract";
import { resolve } from "../theme/engine";
import { Button, Card, type Choice, Choices, Notice, PageHead, Switch, useThemeState } from "../ui";

const t = strings("themes", {
  title: "Themes",
  short: "Theme",
  theme: "Theme",
  imported: "{id} · imported",
  yourThemes: "Your themes",
  importFile: "Import a theme file",
  notImported: "Not imported: {error}",
  importHelp:
    "A theme is a JSON file of tokens, layout options and optional CSS. It cannot run code. The theme contract in pluxy's source, {path}, lists everything a theme can set.",
  remove: "Remove {name}",
  appearance: "Appearance",
  darkOnly: "Dark only",
  lightOnly: "Light only",
  system: "System",
  light: "Light",
  dark: "Dark",
  layout: "Layout",
  tabs: "Tabs",
  side: "Side",
  resetLayout: "Use the theme's layout",
  choicesStay: "Your choices here stay when you switch themes.",
  navigation: "Navigation",
  sidebar: "Sidebar",
  rail: "Rail",
  top: "Top",
  lists: "Lists",
  table: "Table",
  cards: "Cards",
  details: "Details",
  beside: "Beside",
  below: "Below",
  width: "Width",
  wide: "Wide",
  full: "Full",
  narrow: "Narrow",
  density: "Density",
  compact: "Compact",
  normal: "Normal",
  roomy: "Roomy",
  themeNote: "· theme",
  customCss: "Custom CSS",
  safeMode: "Safe mode",
  safeModeHint:
    "Turns off all custom CSS, the theme's and yours. If custom CSS hides this switch, open the panel with ?safe at the end of its address.",
  ownCssOn: "{name} adds its own CSS.",
  ownCssOff: "{name} adds its own CSS, turned off by safe mode.",
  yourCss: "Your CSS",
  cssHint:
    "Applied on top of every theme, as you type. Style the data-part hooks the theme contract lists; other markup can change in any release.",
});

const layoutChoices = (): { [K in LayoutOption]: { label: string; choices: Choice<Layout[K]>[] } } => ({
  nav: {
    label: t("navigation"),
    choices: [
      { value: "sidebar", label: t("sidebar") },
      { value: "rail", label: t("rail") },
      { value: "top", label: t("top") },
    ],
  },
  list: {
    label: t("lists"),
    choices: [
      { value: "table", label: t("table") },
      { value: "cards", label: t("cards") },
    ],
  },
  detail: {
    label: t("details"),
    choices: [
      { value: "side", label: t("beside") },
      { value: "below", label: t("below") },
    ],
  },
  tabs: {
    label: t("tabs"),
    choices: [
      { value: "top", label: t("top") },
      { value: "side", label: t("side") },
    ],
  },
  width: {
    label: t("width"),
    choices: [
      { value: "wide", label: t("wide") },
      { value: "full", label: t("full") },
      { value: "narrow", label: t("narrow") },
    ],
  },
});

const density = (): Choice<string>[] => [
  { value: "0.75", label: t("compact") },
  { value: "1", label: t("normal") },
  { value: "1.25", label: t("roomy") },
];

/** Marks the value the theme itself sets, so the person sees what a reset brings back. */
function noted<V extends string>(choices: Choice<V>[], themeValue: string | undefined): Choice<V>[] {
  return choices.map((c) => (c.value === themeValue ? { ...c, note: t("themeNote") } : c));
}

function ThemePicker() {
  const themes = useThemeState();
  const id = useId();
  return (
    <div class="themes" role="radiogroup" aria-labelledby={`${id}-l`}>
      <span class="label" id={`${id}-l`}>
        {t("theme")}
      </span>
      <div class="theme-grid">
        {themes.offered.map((o) => {
          // Each card previews the theme's own colours in its light (or only) appearance.
          const tokens = resolve(
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
                style={{ background: tokens["color.bg"], borderColor: tokens["color.border"] }}
              >
                <span style={{ background: tokens["color.surface"] }} />
                <span style={{ background: tokens["color.accent"] }} />
                <span style={{ background: tokens["color.text"] }} />
              </span>
              <span class="theme-name">{o.theme.name}</span>
              <small class="muted">{o.theme.description ?? o.theme.id}</small>
              {!o.builtIn && <small class="muted mono">{t("imported", { id: o.theme.id })}</small>}
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
    <Card title={t("yourThemes")}>
      <label class="field" data-part="field">
        <span class="label">{t("importFile")}</span>
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
      {error && <Notice tone="danger">{t("notImported", { error })}</Notice>}
      <p class="muted">
        {rich(t("importHelp"), { path: <span class="mono">next/ui/themes/README.md</span> })}
      </p>
      {!current.builtIn && (
        <div class="actions">
          <Button variant="danger" onClick={() => themes.removeTheme(current.theme.id)}>
            {t("remove", { name: current.theme.name })}
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
  const layout = layoutChoices();

  return (
    <>
      <PageHead title={t("title")} />
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
            label={t("appearance")}
            value={single ? theme.appearance : themes.choices.appearance}
            choices={
              single
                ? [
                    {
                      value: theme.appearance,
                      label: theme.appearance === "dark" ? t("darkOnly") : t("lightOnly"),
                    },
                  ]
                : [
                    { value: "system", label: t("system") },
                    { value: "light", label: t("light") },
                    { value: "dark", label: t("dark") },
                  ]
            }
            onChange={(v) => themes.set({ appearance: v as "system" | "light" | "dark" })}
          />
        </Card>

        <Card
          title={t("layout")}
          actions={<Button onClick={() => themes.resetLayout()}>{t("resetLayout")}</Button>}
        >
          <p class="muted">{t("choicesStay")}</p>
          {(Object.keys(layout) as LayoutOption[]).map((option) => {
            const o = layout[option];
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
            label={t("density")}
            value={r.tokens.density}
            choices={noted(density(), theme.tokens.density ?? "1")}
            onChange={(v) => themes.set({ density: v })}
          />
        </Card>

        <Card title={t("customCss")}>
          <Switch
            label={t("safeMode")}
            hint={t("safeModeHint")}
            checked={themes.choices.safe}
            onChange={(v) => themes.setSafe(v)}
          />
          {theme.css && (
            <p class="muted">{t(themes.choices.safe ? "ownCssOff" : "ownCssOn", { name: theme.name })}</p>
          )}
          <label class="field" data-part="field">
            <span class="label">{t("yourCss")}</span>
            <textarea
              class="mono"
              rows={6}
              spellcheck={false}
              placeholder={'[data-part="nav"] { letter-spacing: 0.04em; }'}
              value={themes.choices.css}
              onInput={(e) => themes.set({ css: e.currentTarget.value })}
            />
            <small class="hint">{t("cssHint")}</small>
          </label>
        </Card>

        <ImportTheme />
      </div>
    </>
  );
}

export const themesPage = (): PageDef => ({
  id: "themes",
  group: "control",
  title: t("title"),
  short: t("short"),
  render: () => <ThemesPage />,
});

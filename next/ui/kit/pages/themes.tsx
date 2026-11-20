import { useEffect, useId, useState } from "preact/hooks";
import { useSave } from "../save";
import type { PageDef } from "../shell";
import { rich, strings } from "../strings";
import { type Layout, type LayoutOption, MAX_THEME_BYTES } from "../theme/contract";
import { resolve } from "../theme/engine";
import type { Listed } from "../theme/state";
import {
  Button,
  Card,
  type Choice,
  Choices,
  ConfirmButton,
  Notice,
  PageHead,
  Switch,
  useThemeState,
} from "../ui";

const t = strings("themes", {
  title: "Themes",
  short: "Theme",
  theme: "Theme",
  notRead: "Themes could not be read from core: {error}",
  notTurnedOn: "{name} was not turned on: {error}",
  unusable: "Cannot be used: {error}",
  export: "Export",
  exportLabel: "Export {name} as a theme file",
  remove: "Remove {name}",
  notRemoved: "{name} was not removed: {error}",
  add: "Add a theme",
  addHelp:
    "pluxy theme files and T3 Code theme files both work. {path} in pluxy's source lists everything a theme file can set.",
  file: "Theme file",
  paste: "Or paste a theme file's JSON",
  addButton: "Add",
  adding: "Adding…",
  added: "{name} was added and turned on.",
  notAdded: "Not added: {error}",
  tooLarge: "the file is over {limit} KiB",
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

/** A theme's own colours in its light (or only) appearance; none until its file has been read. */
function Swatch(props: { listed: Listed }) {
  const theme = props.listed.offered?.theme;
  if (!theme) return <span class="swatch" data-empty aria-hidden="true" />;
  const tokens = resolve(
    theme,
    { appearance: "system", layout: {}, css: "", safe: true },
    { systemDark: theme.appearance === "dark", reducedMotion: false },
  ).tokens;
  return (
    <span
      class="swatch"
      aria-hidden="true"
      style={{ background: tokens["color.bg"], borderColor: tokens["color.border"] }}
    >
      <span style={{ background: tokens["color.surface"] }} />
      <span style={{ background: tokens["color.accent"] }} />
      <span style={{ background: tokens["color.text"] }} />
    </span>
  );
}

function ThemePicker(props: { onProblem: (problem: string | undefined) => void }) {
  const themes = useThemeState();
  const id = useId();
  const listed = themes.listed;
  // The stored themes' previews need their files: read when the page opens, and when one is new.
  const unread = listed
    .filter((l) => !l.offered && !l.problem)
    .map((l) => l.id)
    .join(" ");
  useEffect(() => {
    void themes.loadFiles();
  }, [themes, unread]);
  return (
    <div class="themes" role="radiogroup" aria-labelledby={`${id}-l`}>
      <span class="label" id={`${id}-l`}>
        {t("theme")}
      </span>
      <div class="theme-grid">
        {listed.map((l) => (
          <label key={l.id} class="theme-card" data-part="card" data-unusable={l.problem ? "" : undefined}>
            <input
              type="radio"
              name={id}
              checked={themes.current.theme.id === l.id}
              disabled={l.problem !== undefined}
              onChange={async () => {
                props.onProblem(undefined);
                const error = await themes.select(l.id);
                if (error) props.onProblem(t("notTurnedOn", { name: l.name, error }));
              }}
            />
            <Swatch listed={l} />
            <span class="theme-name">{l.name}</span>
            <small class="muted">
              {l.problem ? t("unusable", { error: l.problem }) : (l.offered?.theme.description ?? l.id)}
            </small>
          </label>
        ))}
      </div>
    </div>
  );
}

/** Export and Remove, for the theme that is on. */
function CurrentTheme(props: {
  onProblem: (problem: string | undefined) => void;
  onExport: (name: string, text: string) => void;
}) {
  const themes = useThemeState();
  const { theme, builtIn } = themes.current;
  return (
    <div class="actions">
      <Button
        label={t("exportLabel", { name: theme.name })}
        onClick={() => {
          const file = themes.exportFile();
          props.onExport(file.name, file.text);
        }}
      >
        {t("export")}
      </Button>
      {!builtIn && (
        <ConfirmButton
          key={theme.id}
          onConfirm={async () => {
            props.onProblem(undefined);
            const error = await themes.removeTheme(theme.id);
            if (error) props.onProblem(t("notRemoved", { name: theme.name, error }));
          }}
        >
          {t("remove", { name: theme.name })}
        </ConfirmButton>
      )}
    </div>
  );
}

function AddTheme() {
  const themes = useThemeState();
  const id = useId();
  const [text, setText] = useState("");
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<{ error: string } | { added: string }>();

  /** A chosen file and pasted JSON go the same way. Returns whether the theme was added. */
  const add = async (source: string) => {
    setBusy(true);
    setResult(undefined);
    const error = await themes.addTheme(source);
    setBusy(false);
    setResult(error === undefined ? { added: themes.current.theme.name } : { error });
    return error === undefined;
  };

  return (
    <Card title={t("add")}>
      <p class="muted">{rich(t("addHelp"), { path: <span class="mono">next/ui/themes/README.md</span> })}</p>
      <label class="field" data-part="field">
        <span class="label">{t("file")}</span>
        <input
          type="file"
          accept=".json,application/json"
          disabled={busy}
          onChange={async (e) => {
            const file = e.currentTarget.files?.[0];
            e.currentTarget.value = "";
            if (!file) return;
            // Checked before the file is read: a theme file is a few KiB.
            if (file.size > MAX_THEME_BYTES) {
              setResult({ error: t("tooLarge", { limit: MAX_THEME_BYTES / 1024 }) });
              return;
            }
            await add(await file.text());
          }}
        />
      </label>
      <form
        class="field"
        data-part="field"
        onSubmit={async (e) => {
          e.preventDefault();
          if (await add(text)) setText("");
        }}
      >
        <label class="label" for={`${id}-json`}>
          {t("paste")}
        </label>
        <textarea
          id={`${id}-json`}
          class="mono"
          rows={6}
          spellcheck={false}
          placeholder={
            '{\n  "name": "Nightfall",\n  "appearance": "dark",\n  "canvas": "#1a1b26",\n  "accent": "#7aa2f7"\n}'
          }
          value={text}
          onInput={(e) => setText(e.currentTarget.value)}
        />
        <div class="actions">
          <Button type="submit" variant="primary" disabled={busy || text.trim() === ""}>
            {busy ? t("adding") : t("addButton")}
          </Button>
        </div>
      </form>
      {result && "error" in result && <Notice tone="danger">{t("notAdded", { error: result.error })}</Notice>}
      {result && "added" in result && <Notice tone="ok">{t("added", { name: result.added })}</Notice>}
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
  const [problem, setProblem] = useState<string>();
  const saved = useSave();

  return (
    <>
      <PageHead title={t("title")} />
      <div class="stack">
        <Card>
          <ThemePicker onProblem={setProblem} />
          {themes.problem && <Notice tone="danger">{t("notRead", { error: themes.problem })}</Notice>}
          {problem && <Notice tone="danger">{problem}</Notice>}
          {allWarnings.length > 0 && (
            <Notice tone="warn">
              {theme.name}: {allWarnings.join("; ")}
            </Notice>
          )}
          {saved.notice}
          <CurrentTheme
            onProblem={setProblem}
            onExport={(name, text) => saved.save(name, text, "application/json")}
          />
        </Card>

        <AddTheme />

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

import { useEffect, useId, useRef, useState } from "preact/hooks";
import { useSave } from "../save";
import type { PageDef } from "../shell";
import { rich, strings } from "../strings";
import { type Layout, type LayoutOption, MAX_THEME_BYTES } from "../theme/contract";
import { resolve } from "../theme/engine";
import type { Listed } from "../theme/state";
import { Button, Card, type Choice, Choices, Notice, PageHead, Switch, useThemeState } from "../ui";

const t = strings("themes", {
  title: "Themes",
  theme: "Theme",
  notRead: "Could not load themes from core: {error}. Refresh and try again.",
  currentUnusable:
    "{id} is turned on, but this page cannot use it: {error}. Remove it, or pick another theme.",
  notTurnedOn: "Could not apply {name}: {error}. Pick another theme or try again.",
  unusable: "Cannot be used: {error}",
  export: "Export",
  exportLabel: "Export {name} as a theme file",
  remove: "Remove {name}",
  confirmRemove: "Yes, remove {name}",
  cancel: "Cancel",
  notRemoved: "Could not remove {name}: {error}. Try again.",
  add: "Add a theme",
  addHelp:
    "pluxy theme files and T3 Code theme files both work. {path} in pluxy's source lists everything a theme file can set.",
  file: "Theme file",
  paste: "Or paste a theme file's JSON",
  addButton: "Add",
  adding: "Adding…",
  added: "Added and applied {name}.",
  replaced: "Added and applied {name}, replacing the existing theme.",
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
  resetLayout: "Reset layout",
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
  safeModeHint: "Turns off all custom CSS. If custom CSS hides this switch, add ?safe to the panel URL.",
  ownCssOn: "{name} adds its own CSS.",
  ownCssOff: "{name} adds its own CSS, turned off by safe mode.",
  yourCss: "Your CSS",
  cssHint: "Applies on top of every theme as you type. Target data-part hooks; other markup may change.",
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

/** What the person did last on the page that failed, and the calls that tell the page about it. */
interface Acts {
  /** The person does something new: what the page said about the last thing goes. */
  onAct: () => void;
  onProblem: (problem: string) => void;
}

/** Remove, which asks once more first. The question names the theme as it is written. */
function RemoveButton(props: { name: string; onConfirm: () => void }) {
  const [asking, setAsking] = useState(false);
  if (!asking) {
    return (
      <Button variant="danger" onClick={() => setAsking(true)}>
        {t("remove", { name: props.name })}
      </Button>
    );
  }
  return (
    <span class="confirm">
      <Button
        variant="danger"
        onClick={() => {
          setAsking(false);
          props.onConfirm();
        }}
      >
        {t("confirmRemove", { name: props.name })}
      </Button>
      <Button onClick={() => setAsking(false)}>{t("cancel")}</Button>
    </span>
  );
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

function ThemePicker(props: Acts) {
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
  const remove = async (l: Listed) => {
    props.onAct();
    const error = await themes.removeTheme(l.id);
    if (error) props.onProblem(t("notRemoved", { name: l.name, error }));
  };
  return (
    <div class="themes" role="radiogroup" aria-labelledby={`${id}-l`}>
      <span class="label" id={`${id}-l`}>
        {t("theme")}
      </span>
      <div class="theme-grid">
        {listed.map((l) =>
          l.problem ? (
            // A stored file the page cannot use: nothing to pick, and the one thing to do is remove it.
            <div key={l.id} class="theme-card" data-part="card" data-unusable="">
              <Swatch listed={l} />
              <span class="theme-name">{l.name}</span>
              <small class="muted">{t("unusable", { error: l.problem })}</small>
              <RemoveButton name={l.name} onConfirm={() => remove(l)} />
            </div>
          ) : (
            <label key={l.id} class="theme-card" data-part="card">
              <input
                type="radio"
                name={id}
                checked={themes.current.theme.id === l.id}
                onChange={async () => {
                  props.onAct();
                  const error = await themes.select(l.id);
                  if (error) props.onProblem(t("notTurnedOn", { name: l.name, error }));
                }}
              />
              <Swatch listed={l} />
              <span class="theme-name">{l.name}</span>
              <small class="muted">{l.offered?.theme.description ?? l.id}</small>
            </label>
          ),
        )}
      </div>
    </div>
  );
}

/** Export and Remove, for the theme that is on. */
function CurrentTheme(props: Acts & { onExport: (name: string, text: string) => void }) {
  const themes = useThemeState();
  const { theme, builtIn } = themes.current;
  return (
    <div class="actions">
      <Button
        label={t("exportLabel", { name: theme.name })}
        onClick={() => {
          props.onAct();
          const file = themes.exportFile();
          props.onExport(file.name, file.text);
        }}
      >
        {t("export")}
      </Button>
      {!builtIn && (
        <RemoveButton
          key={theme.id}
          name={theme.name}
          onConfirm={async () => {
            props.onAct();
            const error = await themes.removeTheme(theme.id);
            if (error) props.onProblem(t("notRemoved", { name: theme.name, error }));
          }}
        />
      )}
    </div>
  );
}

/** What the last add came to: why not, or the theme it added and whether it took another's place. */
type AddResult = { error: string } | { added: string; id: string; replaced: boolean };

function AddTheme(props: {
  result: AddResult | undefined;
  onResult: (result: AddResult) => void;
  onAct: () => void;
}) {
  const themes = useThemeState();
  const id = useId();
  const [text, setText] = useState("");
  const [busy, setBusy] = useState(false);
  const { result } = props;
  // What an add came to shows under the Add button, which often sits at the window's lower edge:
  // it is scrolled into view, so an add never looks as if nothing happened.
  const said = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (result) said.current?.scrollIntoView?.({ block: "nearest" });
  }, [result]);

  /** A chosen file and pasted JSON go the same way. Returns whether the theme was added. */
  const add = async (source: string) => {
    setBusy(true);
    props.onAct();
    const outcome = await themes.addTheme(source);
    setBusy(false);
    const { name, id: added } = themes.current.theme;
    props.onResult("error" in outcome ? outcome : { added: name, id: added, replaced: outcome.replaced });
    return !("error" in outcome);
  };

  const outcome =
    result &&
    ("error" in result ? (
      <Notice tone="danger">{t("notAdded", { error: result.error })}</Notice>
    ) : (
      // True only while the theme it added is the one that is on.
      result.id === themes.current.theme.id && (
        <Notice tone="ok">{t(result.replaced ? "replaced" : "added", { name: result.added })}</Notice>
      )
    ));
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
              props.onAct();
              props.onResult({ error: t("tooLarge", { limit: MAX_THEME_BYTES / 1024 }) });
              return;
            }
            // The pasted text was not what got added: a theme from a file leaves none behind.
            if (await add(await file.text())) setText("");
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
          onInput={(e) => {
            // What the page said about the last add was about other text.
            props.onAct();
            setText(e.currentTarget.value);
          }}
        />
        <div class="actions">
          <Button type="submit" variant="primary" disabled={busy || text.trim() === ""}>
            {busy ? t("adding") : t("addButton")}
          </Button>
        </div>
      </form>
      {outcome && <div ref={said}>{outcome}</div>}
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
  const [addResult, setAddResult] = useState<AddResult>();
  const saved = useSave();
  const acts: Acts = {
    onAct: () => {
      setProblem(undefined);
      setAddResult(undefined);
      saved.clear();
    },
    onProblem: setProblem,
  };

  return (
    <>
      <PageHead title={t("title")} />
      <div class="stack">
        <Card>
          <ThemePicker {...acts} />
          {themes.problem && <Notice tone="danger">{t("notRead", { error: themes.problem })}</Notice>}
          {themes.unusable && (
            <Notice tone="danger">
              {t("currentUnusable", { id: themes.unusable.id, error: themes.unusable.problem })}
            </Notice>
          )}
          {problem && <Notice tone="danger">{problem}</Notice>}
          {allWarnings.length > 0 && (
            <Notice tone="warn">
              {theme.name}: {allWarnings.join("; ")}
            </Notice>
          )}
          {saved.notice}
          <CurrentTheme {...acts} onExport={(name, text) => saved.save(name, text, "application/json")} />
        </Card>

        <AddTheme result={addResult} onResult={setAddResult} onAct={acts.onAct} />

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
  dest: "settings",
  kind: "topic",
  title: t("title"),
  render: () => <ThemesPage />,
});

// The Themes page: which theme is on (kept by pluxy, so every window shows the same one), then
// what is for this device alone: the layout, a theme file to add, and the person's own CSS. Every
// change applies and saves at once.

import type { ComponentChildren } from "preact";
import { useEffect, useId, useRef, useState } from "preact/hooks";
import { Icon } from "../icons";
import { SettingRow, tryAgainWord, useSavedTick } from "../parts";
import { useSave } from "../save";
import type { PageDef } from "../shell";
import { strings } from "../strings";
import { type Layout, type LayoutOption, MAX_THEME_BYTES } from "../theme/contract";
import type { Listed } from "../theme/state";
import { Button, type Choice, Choices, ConfirmButton, Notice, PageHead, Switch, useThemeState } from "../ui";
import { SettingGroup, SettingsColumn } from "./settingGroup";
import { PaletteDots, ThemePreview } from "./themePreview";

const t = strings("themes", {
  title: "Themes",
  tip: "Theme, layout and custom CSS",
  theme: "Theme",
  themeAbout:
    "The theme is kept by pluxy, so every window connected to it shows the same one. Everything below it is for this device only.",
  notRead: "Could not load pluxy's themes: {error}.",
  currentUnusable: "{name} cannot be used: {error}. Pick another theme or remove it.",
  notTurnedOn: "Could not turn on {name}: {error}.",
  notRemoved: "Could not remove {name}: {error}.",
  warnings: "{name} is on, but part of its file was not used: {warnings}.",
  unusable: "Cannot be used: {error}",
  bothAppearances: "Light and dark",
  lightOnly: "Light only",
  darkOnly: "Dark only",
  drop: "Drop a theme file here, or choose one.",
  save: "Save theme file",
  remove: "Remove",
  removeLabel: "Remove {name}",
  confirmRemove: "Yes, remove {name}",
  layout: "Layout",
  resetLayout: "Reset layout",
  frame: "Frame",
  frameAbout: "The page on a panel inset from the window, or flush with it.",
  inset: "Inset",
  flat: "Flat",
  navigation: "Navigation",
  navigationAbout: "Where the main pages are listed.",
  sidebar: "Sidebar",
  rail: "Rail",
  top: "Top",
  width: "Width",
  widthAbout: "How wide pages may grow.",
  wide: "Wide",
  full: "Full",
  narrow: "Narrow",
  density: "Density",
  densityAbout: "How much space is between things.",
  compact: "Compact",
  normal: "Normal",
  roomy: "Roomy",
  tabs: "Tabs",
  tabsAbout: "Where a page's tabs sit.",
  side: "Side",
  details: "Details",
  detailsAbout: "Where an opened item shows.",
  beside: "Beside",
  below: "Below",
  lists: "Lists",
  listsAbout: "Tables or cards, for the lists that use them.",
  table: "Table",
  cards: "Cards",
  themeNote: "· theme",
  add: "Add a theme",
  addAbout: "A pluxy or T3 Code theme file (.json).",
  file: "Theme file",
  paste: "Or paste a theme file",
  addButton: "Add",
  adding: "Adding…",
  added: "Added and turned on {name}.",
  replaced: "Added and turned on {name}, replacing the theme of the same name.",
  notAdded: "Not added: {error}",
  tooLarge: "The file is over {limit} KiB.",
  customCss: "Custom CSS",
  customCssAbout:
    "Turn it off if CSS hides a control. If this switch is hidden, add ?safe to the end of the page's address.",
  ownCssOn: "{name} adds its own CSS.",
  ownCssOff: "{name} adds its own CSS, turned off with custom CSS.",
  yourCss: "Your CSS",
  cssHint: "Applies as you type, on top of every theme. Style data-part hooks; other markup may change.",
});

type LayoutRow = { [K in LayoutOption]: { label: string; about: string; choices: Choice<Layout[K]>[] } };

const layoutChoices = (): LayoutRow => ({
  frame: {
    label: t("frame"),
    about: t("frameAbout"),
    choices: [
      { value: "inset", label: t("inset") },
      { value: "flat", label: t("flat") },
    ],
  },
  nav: {
    label: t("navigation"),
    about: t("navigationAbout"),
    choices: [
      { value: "sidebar", label: t("sidebar") },
      { value: "rail", label: t("rail") },
      { value: "top", label: t("top") },
    ],
  },
  width: {
    label: t("width"),
    about: t("widthAbout"),
    choices: [
      { value: "wide", label: t("wide") },
      { value: "full", label: t("full") },
      { value: "narrow", label: t("narrow") },
    ],
  },
  tabs: {
    label: t("tabs"),
    about: t("tabsAbout"),
    choices: [
      { value: "top", label: t("top") },
      { value: "side", label: t("side") },
    ],
  },
  detail: {
    label: t("details"),
    about: t("detailsAbout"),
    choices: [
      { value: "side", label: t("beside") },
      { value: "below", label: t("below") },
    ],
  },
  list: {
    label: t("lists"),
    about: t("listsAbout"),
    choices: [
      { value: "table", label: t("table") },
      { value: "cards", label: t("cards") },
    ],
  },
});

/** The layout rows in the order the page lists them; Density sits after Width. */
const LAYOUT_ORDER: LayoutOption[] = ["frame", "nav", "width", "tabs", "detail", "list"];

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

/** A setting of the page that takes effect at once, with its own "Saved". */
function QuickRow(props: {
  name: string;
  about: string;
  children: (flash: () => void) => ComponentChildren;
}) {
  const saved = useSavedTick();
  return (
    <SettingRow name={props.name} description={props.about} saved={saved.saved}>
      {props.children(saved.flash)}
    </SettingRow>
  );
}

const modes = (appearance: "light" | "dark" | "both") =>
  t(appearance === "both" ? "bothAppearances" : appearance === "light" ? "lightOnly" : "darkOnly");

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
    <div class="theme-grid" role="radiogroup" aria-labelledby={`${id}-l`}>
      <span class="sr-only" id={`${id}-l`}>
        {t("theme")}
      </span>
      {listed.map((l) =>
        l.problem ? (
          // A stored file the page cannot use: nothing to pick, and the one thing to do is remove it.
          <div key={l.id} class="theme-card" data-part="card" data-unusable="">
            <span class="mini-pair" data-empty="" aria-hidden="true" />
            <span class="theme-meta">
              <span class="theme-name">{l.name}</span>
            </span>
            <small class="theme-about">{t("unusable", { error: l.problem })}</small>
            <ConfirmButton
              label={t("removeLabel", { name: l.name })}
              confirmLabel={t("confirmRemove", { name: l.name })}
              onConfirm={() => remove(l)}
            >
              {t("remove")}
            </ConfirmButton>
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
            {l.offered ? (
              <ThemePreview theme={l.offered.theme} />
            ) : (
              <span class="mini-pair" data-empty="" aria-hidden="true" />
            )}
            <span class="theme-meta">
              <span class="theme-name">{l.name}</span>
              {l.offered && <PaletteDots theme={l.offered.theme} />}
            </span>
            <small class="theme-about">{l.offered?.theme.description ?? l.id}</small>
            {l.offered && <small class="theme-modes">{modes(l.offered.theme.appearance)}</small>}
            <span class="theme-check" aria-hidden="true">
              <Icon name="check" size={12} />
            </span>
          </label>
        ),
      )}
    </div>
  );
}

/** Save theme file, and Remove for a theme someone added, for the theme that is on. */
function CurrentTheme(props: Acts & { onSave: (name: string, text: string) => void }) {
  const themes = useThemeState();
  const { theme, builtIn } = themes.current;
  return (
    <>
      <Button
        onClick={() => {
          props.onAct();
          const file = themes.exportFile();
          props.onSave(file.name, file.text);
        }}
      >
        {t("save")}
      </Button>
      {!builtIn && (
        <ConfirmButton
          key={theme.id}
          label={t("removeLabel", { name: theme.name })}
          confirmLabel={t("confirmRemove", { name: theme.name })}
          onConfirm={async () => {
            props.onAct();
            const error = await themes.removeTheme(theme.id);
            if (error) props.onProblem(t("notRemoved", { name: theme.name, error }));
          }}
        >
          {t("remove")}
        </ConfirmButton>
      )}
    </>
  );
}

/** What the last add came to: why not, or the theme it added and whether it took another's place. */
type AddResult = { error: string; whole?: true } | { added: string; id: string; replaced: boolean };

function AddTheme(props: {
  result: AddResult | undefined;
  onResult: (result: AddResult) => void;
  onAct: () => void;
}) {
  const themes = useThemeState();
  const id = useId();
  const [text, setText] = useState("");
  const [busy, setBusy] = useState(false);
  const [over, setOver] = useState(false);
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

  /** A file chosen or dropped: refused when too large, else added like pasted text. */
  const addFile = async (file: File | undefined) => {
    if (!file) return;
    // Checked before the file is read: a theme file is a few KiB.
    if (file.size > MAX_THEME_BYTES) {
      props.onAct();
      props.onResult({ error: t("tooLarge", { limit: MAX_THEME_BYTES / 1024 }), whole: true });
      return;
    }
    // The pasted text was not what got added: a theme from a file leaves none behind.
    if (await add(await file.text())) setText("");
  };

  const outcome =
    result &&
    ("error" in result ? (
      <Notice tone="danger">{result.whole ? result.error : t("notAdded", { error: result.error })}</Notice>
    ) : (
      // True only while the theme it added is the one that is on.
      result.id === themes.current.theme.id && (
        <Notice tone="ok">{t(result.replaced ? "replaced" : "added", { name: result.added })}</Notice>
      )
    ));
  return (
    <>
      {/* biome-ignore lint/a11y/noStaticElementInteractions: dropping a file is a shortcut for pointers; the file field in it is the way for everyone else */}
      <div
        class="setting-piece theme-drop"
        data-over={over ? "" : undefined}
        onDragOver={(e) => {
          e.preventDefault();
          setOver(true);
        }}
        onDragLeave={() => setOver(false)}
        onDrop={(e) => {
          e.preventDefault();
          setOver(false);
          void addFile(e.dataTransfer?.files?.[0]);
        }}
      >
        <span class="theme-drop-text">{t("drop")}</span>
        <label class="field" data-part="field">
          <span class="label">{t("file")}</span>
          <input
            type="file"
            accept=".json,application/json"
            disabled={busy}
            onChange={async (e) => {
              const file = e.currentTarget.files?.[0];
              e.currentTarget.value = "";
              await addFile(file);
            }}
          />
        </label>
      </div>
      <form
        class="setting-piece field"
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
          rows={5}
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
      {outcome && (
        <div class="setting-piece" ref={said}>
          {outcome}
        </div>
      )}
    </>
  );
}

type NoticeKind = "warnings" | "unusable" | "load" | "action";
/** Least first: when two arrive together, the later one shows. */
const NOTICE_KINDS: NoticeKind[] = ["warnings", "unusable", "load", "action"];

/** The notice that came up last among those that are up: the page says one thing at a time. */
function useNewest(active: Record<NoticeKind, boolean>): NoticeKind | undefined {
  const order = useRef<NoticeKind[]>([]);
  order.current = order.current.filter((k) => active[k]);
  for (const k of NOTICE_KINDS) if (active[k] && !order.current.includes(k)) order.current.push(k);
  return order.current.at(-1);
}

export function ThemesPage() {
  const themes = useThemeState();
  const { theme, warnings } = themes.current;
  const r = themes.resolved;
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
  const newest = useNewest({
    warnings: allWarnings.length > 0,
    unusable: themes.unusable !== undefined,
    load: themes.problem !== undefined,
    action: problem !== undefined,
  });
  const unusableName = themes.unusable
    ? (themes.listed.find((l) => l.id === themes.unusable?.id)?.name ?? themes.unusable.id)
    : "";

  return (
    <>
      <PageHead title={t("title")} />
      <SettingsColumn class="theme-sections" search>
        <SettingGroup
          title={t("theme")}
          about={t("themeAbout")}
          card={false}
          actions={
            <CurrentTheme {...acts} onSave={(name, text) => saved.save(name, text, "application/json")} />
          }
        >
          {newest === "action" && <Notice tone="danger">{problem}</Notice>}
          {newest === "load" && (
            <Notice tone="danger">
              {t("notRead", { error: themes.problem ?? "" })}{" "}
              <Button onClick={() => themes.refresh()}>{tryAgainWord()}</Button>
            </Notice>
          )}
          {newest === "unusable" && themes.unusable && (
            <Notice tone="warn">
              {t("currentUnusable", { name: unusableName, error: themes.unusable.problem })}
            </Notice>
          )}
          {newest === "warnings" && (
            <Notice tone="warn">
              {t("warnings", { name: theme.name, warnings: allWarnings.join("; ") })}
            </Notice>
          )}
          <ThemePicker {...acts} />
          {saved.notice}
        </SettingGroup>

        <SettingGroup
          title={t("layout")}
          actions={<Button onClick={() => themes.resetLayout()}>{t("resetLayout")}</Button>}
        >
          {LAYOUT_ORDER.flatMap((option) => {
            const o = layout[option];
            const row = (
              <QuickRow key={option} name={o.label} about={o.about}>
                {(flash) => (
                  <Choices<string>
                    label={o.label}
                    value={r.layout[option]}
                    choices={noted(
                      o.choices as Choice<string>[],
                      theme.layout[option] ?? o.choices[0]?.value,
                    )}
                    onChange={(v) => {
                      themes.setLayout(option, v as Layout[typeof option]);
                      flash();
                    }}
                  />
                )}
              </QuickRow>
            );
            // Density is a token, not a layout option; it sits after Width.
            return option === "width"
              ? [
                  row,
                  <QuickRow key="density" name={t("density")} about={t("densityAbout")}>
                    {(flash) => (
                      <Choices
                        label={t("density")}
                        value={r.tokens.density}
                        choices={noted(density(), theme.tokens.density ?? "1")}
                        onChange={(v) => {
                          themes.set({ density: v });
                          flash();
                        }}
                      />
                    )}
                  </QuickRow>,
                ]
              : [row];
          })}
        </SettingGroup>

        <SettingGroup title={t("add")} about={t("addAbout")}>
          <AddTheme result={addResult} onResult={setAddResult} onAct={acts.onAct} />
        </SettingGroup>

        <SettingGroup title={t("customCss")}>
          <QuickRow name={t("customCss")} about={t("customCssAbout")}>
            {(flash) => (
              <Switch
                label={t("customCss")}
                checked={!themes.choices.safe}
                onChange={(on) => {
                  themes.setSafe(!on);
                  flash();
                }}
              />
            )}
          </QuickRow>
          {theme.css && (
            <p class="setting-piece muted">
              {t(themes.choices.safe ? "ownCssOff" : "ownCssOn", { name: theme.name })}
            </p>
          )}
          <label class="setting-piece field" data-part="field">
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
        </SettingGroup>
      </SettingsColumn>
    </>
  );
}

export const themesPage = (): PageDef => ({
  id: "themes",
  dest: "settings",
  kind: "topic",
  section: "device",
  title: t("title"),
  tip: t("tip"),
  render: () => <ThemesPage />,
});

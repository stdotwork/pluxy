// Stylesheet rules that break the CSS rules in cssLayers.test.ts and cssThemeLint.test.ts.
// Both tests read this list so they stay in step. List a rule by its exact selector; a new
// rule in the same file still fails.

export const LEGACY_DIALOG_RULES = {
  file: 'styles.css',
  reason: 'Upstream ZCode and Codex catalog dialogs, written before the no-!important and token rules; not yet migrated.',
  selectors: [
    '.zcode-subagent-dialog',
    '.zcode-subagent-dialog .config-dialog-heading',
    '.zcode-subagent-dialog .config-dialog-heading > div',
    '.zcode-subagent-dialog .config-dialog-heading h2',
    '.zcode-subagent-dialog .config-dialog-heading p',
    '.zcode-subagent-body',
    '.codex-catalog-models, .codex-catalog-editor, .zcode-subagent-body, .zcode-subagent-dialog',
    '.codex-catalog-models::-webkit-scrollbar, .codex-catalog-editor::-webkit-scrollbar, .zcode-subagent-body::-webkit-scrollbar, .zcode-subagent-dialog::-webkit-scrollbar',
    '.codex-catalog-models::-webkit-scrollbar-track, .codex-catalog-editor::-webkit-scrollbar-track, .zcode-subagent-body::-webkit-scrollbar-track, .zcode-subagent-dialog::-webkit-scrollbar-track',
    '.codex-catalog-models::-webkit-scrollbar-thumb, .codex-catalog-editor::-webkit-scrollbar-thumb, .zcode-subagent-body::-webkit-scrollbar-thumb, .zcode-subagent-dialog::-webkit-scrollbar-thumb',
    '.codex-catalog-models::-webkit-scrollbar-thumb:hover, .codex-catalog-editor::-webkit-scrollbar-thumb:hover, .zcode-subagent-body::-webkit-scrollbar-thumb:hover, .zcode-subagent-dialog::-webkit-scrollbar-thumb:hover',
    '.codex-catalog-models',
    '.codex-subagent-toggle',
  ],
};

export const isLegacyDialogRule = (file: string, selector: string) =>
  file === LEGACY_DIALOG_RULES.file && LEGACY_DIALOG_RULES.selectors.includes(selector.replace(/\s+/g, ' ').trim());

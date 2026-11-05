package tui

import "testing"

// Expected strings come from the shared glossary (docs/terms.md): provider
// display names, and "available" rather than "active" for a usable credential.

func TestOAuthProviderNamesUseGlossaryDisplayNames(t *testing.T) {
	want := map[string]string{
		"anthropic-auth-url":   "Claude",
		"codex-auth-url":       "Codex",
		"antigravity-auth-url": "Antigravity",
		"xai-auth-url":         "xAI",
		"meta-auth-url":        "Muse (Meta)",
	}
	got := map[string]string{}
	for _, p := range oauthProviders {
		got[p.apiPath] = p.name
	}
	for path, name := range want {
		if got[path] != name {
			t.Errorf("provider for %s = %q, want %q", path, got[path], name)
		}
	}
}

func TestAuthStatusWordingUsesGlossaryTerms(t *testing.T) {
	previous := currentLocale
	t.Cleanup(func() { SetLocale(previous) })

	cases := []struct {
		locale, key, want string
	}{
		{"en", "status_active", "available"},
		{"en", "status_disabled", "disabled"},
		{"en", "active_suffix", "enabled"},
		{"zh", "status_active", "可用"},
		{"zh", "status_disabled", "已停用"},
		{"zh", "active_suffix", "已启用"},
	}
	for _, tc := range cases {
		SetLocale(tc.locale)
		if got := T(tc.key); got != tc.want {
			t.Errorf("%s %s = %q, want %q", tc.locale, tc.key, got, tc.want)
		}
	}
}

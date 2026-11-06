package auth

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// A downstream websocket request for Codex must land on a websocket-enabled credential no matter
// which selection path the manager takes: the scheduler fast path for built-in selectors, or the
// selector path used for session affinity.
func TestPickNextMixedPrefersCodexWebsocketAuthOnEverySelectionPath(t *testing.T) {
	const model = "codex-ws-preference-model"
	for _, tc := range []struct {
		name     string
		selector Selector
	}{
		{name: "fast_path_round_robin", selector: &RoundRobinSelector{}},
		{name: "fast_path_fill_first", selector: &FillFirstSelector{}},
		{name: "session_affinity_round_robin", selector: NewSessionAffinitySelector(&RoundRobinSelector{})},
		{name: "session_affinity_fill_first", selector: NewSessionAffinitySelector(&FillFirstSelector{})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			manager := NewManager(nil, tc.selector, nil)
			manager.RegisterExecutor(schedulerProviderTestExecutor{provider: "codex"})

			// IDs sort the plain credential first, so a selector that ignores the websocket
			// preference picks it on the first attempt.
			plainID := "codex-ws-pref-a-plain-" + tc.name
			wsID := "codex-ws-pref-b-ws-" + tc.name
			for _, auth := range []*Auth{
				{ID: plainID, Provider: "codex", Status: StatusActive, Attributes: map[string]string{AttributeAPIKey: "api-key"}},
				{ID: wsID, Provider: "codex", Status: StatusActive, Attributes: map[string]string{"websockets": "true"}},
			} {
				if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
					t.Fatalf("register %s: %v", auth.ID, errRegister)
				}
				registry.GetGlobalRegistry().RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: model}})
			}
			t.Cleanup(func() {
				registry.GetGlobalRegistry().UnregisterClient(plainID)
				registry.GetGlobalRegistry().UnregisterClient(wsID)
			})

			wsCtx := cliproxyexecutor.WithDownstreamWebsocket(ctx)
			for attempt := 0; attempt < 3; attempt++ {
				picked, _, provider, errPick := manager.pickNextMixed(wsCtx, []string{"codex"}, model, cliproxyexecutor.Options{}, nil)
				if errPick != nil {
					t.Fatalf("attempt %d: pick: %v", attempt, errPick)
				}
				if picked == nil {
					t.Fatalf("attempt %d: picked nil auth", attempt)
				}
				if picked.ID != wsID {
					t.Fatalf("attempt %d: picked %q, want websocket-enabled auth %q", attempt, picked.ID, wsID)
				}
				if provider != "codex" {
					t.Fatalf("attempt %d: provider = %q, want codex", attempt, provider)
				}
			}

			// Without a downstream websocket both credentials stay in rotation.
			seen := map[string]bool{}
			for attempt := 0; attempt < 2; attempt++ {
				tried := map[string]struct{}{}
				for id := range seen {
					tried[id] = struct{}{}
				}
				picked, _, _, errPick := manager.pickNextMixed(ctx, []string{"codex"}, model, cliproxyexecutor.Options{}, tried)
				if errPick != nil {
					t.Fatalf("http attempt %d: pick: %v", attempt, errPick)
				}
				seen[picked.ID] = true
			}
			if !seen[plainID] || !seen[wsID] {
				t.Fatalf("http picks = %v, want both %q and %q", seen, plainID, wsID)
			}
		})
	}
}

func TestPickNextMixedTreatsCodexOAuthWithoutWebsocketFlagAsEnabled(t *testing.T) {
	const model = "codex-ws-default-model"
	for _, tc := range []struct {
		name     string
		selector Selector
	}{
		{name: "fast path", selector: &RoundRobinSelector{}},
		{name: "selector path", selector: NewSessionAffinitySelector(&RoundRobinSelector{})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := NewManager(nil, tc.selector, nil)
			manager.RegisterExecutor(schedulerProviderTestExecutor{provider: "codex"})
			oauth := &Auth{
				ID:       "codex-oauth-default",
				Provider: "codex",
				Status:   StatusActive,
				Metadata: map[string]any{"access_token": "oauth-token"},
			}
			apiKey := &Auth{
				ID:       "codex-api-key-default",
				Provider: "codex",
				Status:   StatusActive,
				Attributes: map[string]string{
					AttributeAPIKey: "api-key",
				},
			}
			for _, auth := range []*Auth{apiKey, oauth} {
				if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
					t.Fatalf("register %s: %v", auth.ID, errRegister)
				}
				registry.GetGlobalRegistry().RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: model}})
			}
			t.Cleanup(func() {
				registry.GetGlobalRegistry().UnregisterClient(apiKey.ID)
				registry.GetGlobalRegistry().UnregisterClient(oauth.ID)
			})

			picked, _, provider, errPick := manager.pickNextMixed(
				cliproxyexecutor.WithDownstreamWebsocket(context.Background()),
				[]string{"codex"}, model, cliproxyexecutor.Options{}, nil,
			)
			if errPick != nil {
				t.Fatalf("pick: %v", errPick)
			}
			if picked == nil || picked.ID != oauth.ID {
				t.Fatalf("picked auth = %#v, want OAuth auth %q", picked, oauth.ID)
			}
			if provider != "codex" {
				t.Fatalf("provider = %q, want codex", provider)
			}
		})
	}
}

func TestWebsocketsEnabledCodexDefaultsAndOverrides(t *testing.T) {
	tests := []struct {
		name string
		auth *Auth
		want bool
	}{
		{name: "oauth defaults on", auth: &Auth{Provider: "codex", Metadata: map[string]any{"access_token": "token"}}, want: true},
		{name: "api key defaults off", auth: &Auth{Provider: "codex", Attributes: map[string]string{AttributeAPIKey: "key"}}, want: false},
		{name: "custom base URL defaults off", auth: &Auth{Provider: "codex", Attributes: map[string]string{"base_url": "https://codex.example.com", "auth_kind": AuthKindOAuth}, Metadata: map[string]any{"access_token": "token"}}, want: false},
		{name: "attribute true wins", auth: &Auth{Provider: "codex", Attributes: map[string]string{"websockets": "true"}}, want: true},
		{name: "attribute false wins", auth: &Auth{Provider: "codex", Attributes: map[string]string{"websockets": "false"}, Metadata: map[string]any{"access_token": "token"}}, want: false},
		{name: "metadata true wins", auth: &Auth{Provider: "codex", Metadata: map[string]any{"websockets": true}}, want: true},
		{name: "metadata false wins", auth: &Auth{Provider: "codex", Metadata: map[string]any{"access_token": "token", "websockets": false}}, want: false},
		{name: "nil auth", auth: nil, want: false},
		{name: "bare codex uses ChatGPT endpoint", auth: &Auth{Provider: "codex"}, want: true},
		{name: "metadata API key kind", auth: &Auth{Provider: "codex", Metadata: map[string]any{"auth_kind": AuthKindAPIKey}}, want: false},
		{name: "attribute API key kind", auth: &Auth{Provider: "codex", Attributes: map[string]string{"auth_kind": AuthKindAPIKey}}, want: false},
		{name: "OAuth kind does not hide API key", auth: &Auth{Provider: "codex", Attributes: map[string]string{"auth_kind": AuthKindOAuth, "api_key": "key"}}, want: false},
		{name: "metadata API key", auth: &Auth{Provider: "codex", Metadata: map[string]any{"api_key": "key"}}, want: false},
		{name: "metadata base URL", auth: &Auth{Provider: "codex", Metadata: map[string]any{"base_url": "https://codex.example.com"}}, want: false},
		{name: "SIWC OAuth defaults on", auth: &Auth{Provider: "codex", Metadata: map[string]any{"use_siwc": true}}, want: true},
		{name: "empty base URL", auth: &Auth{Provider: " CoDeX ", Attributes: map[string]string{"base_url": "  "}, Metadata: map[string]any{"base_url": ""}}, want: true},
		{name: "api key explicit true", auth: &Auth{Provider: "codex", Attributes: map[string]string{"api_key": "key", "websockets": "true"}}, want: true},
		{name: "base URL metadata true string", auth: &Auth{Provider: "codex", Attributes: map[string]string{"base_url": "https://codex.example.com"}, Metadata: map[string]any{"websockets": " true "}}, want: true},
		{name: "metadata false string", auth: &Auth{Provider: "codex", Metadata: map[string]any{"websockets": " false "}}, want: false},
		{name: "attribute false overrides metadata true", auth: &Auth{Provider: "codex", Attributes: map[string]string{"websockets": "false"}, Metadata: map[string]any{"websockets": true}}, want: false},
		{name: "attribute true overrides metadata false", auth: &Auth{Provider: "codex", Attributes: map[string]string{"websockets": "true"}, Metadata: map[string]any{"websockets": false}}, want: true},
		{name: "invalid flag falls through to metadata", auth: &Auth{Provider: "codex", Attributes: map[string]string{"websockets": "invalid"}, Metadata: map[string]any{"websockets": false}}, want: false},
		{name: "invalid flags use OAuth default", auth: &Auth{Provider: "codex", Attributes: map[string]string{"websockets": "invalid"}, Metadata: map[string]any{"websockets": 1}}, want: true},
		{name: "null flag uses OAuth default", auth: &Auth{Provider: "codex", Metadata: map[string]any{"websockets": nil}}, want: true},
		{name: "xai explicit true", auth: &Auth{Provider: "xai", Metadata: map[string]any{"websockets": "true"}}, want: true},
		{name: "xai explicit false", auth: &Auth{Provider: "xai", Attributes: map[string]string{"websockets": "false"}}, want: false},
		{name: "xai invalid remains off", auth: &Auth{Provider: "xai", Metadata: map[string]any{"websockets": "invalid"}}, want: false},
		{name: "xai remains off", auth: &Auth{Provider: "xai", Metadata: map[string]any{"access_token": "token"}}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := WebsocketsEnabled(tt.auth); got != tt.want {
				t.Fatalf("WebsocketsEnabled() = %t, want %t", got, tt.want)
			}
		})
	}
}

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
				{ID: plainID, Provider: "codex", Status: StatusActive},
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

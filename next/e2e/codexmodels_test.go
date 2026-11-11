package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"slices"
	"testing"
)

func TestCodexClientModels(t *testing.T) {
	base := runCore(t, map[string]any{
		"listen": "127.0.0.1:0", "client_keys": []string{clientKey},
		"routing": map[string]any{"force_model_prefix": true},
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, "codex", "pluxy-extension.json"),
			"accounts": []any{map[string]any{
				"id": "codex", "credential": map[string]any{"kind": "api_key", "api_key": "catalog-test"},
				"models":          []any{map[string]any{"name": "gpt-6.1-sol", "alias": "coding"}, "gpt-6-luna", "gpt-6-astra", "gpt-6.1-terra", "unknown-model"},
				"excluded_models": []string{"gpt-6-astra"}, "prefix": "team",
			}},
		},
	})
	if got := listModels(t, base); !slices.Equal(got, []string{"team/coding", "team/gpt-6-luna", "team/gpt-6.1-terra", "team/unknown-model"}) {
		t.Fatalf("normal model list = %v", got)
	}
	for _, query := range []string{"?client_version=0.153.0", "?client_version=0.143.0", "?client_version=", "?client_version"} {
		req, _ := http.NewRequest("GET", base+"/v1/models"+query, nil)
		req.Header.Set("Authorization", "Bearer "+clientKey)
		req.Header.Set("Anthropic-Version", "2023-06-01")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var got struct{ Models []map[string]any }
		err = json.NewDecoder(resp.Body).Decode(&got)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != 200 || len(got.Models) != 3 {
			t.Fatalf("%s: status %d, Codex models = %+v; want three models in priority order", query, resp.StatusCode, got.Models)
		}
		if got.Models[0]["slug"] != "team/coding" || got.Models[1]["slug"] != "team/gpt-6.1-terra" || got.Models[2]["slug"] != "team/gpt-6-luna" {
			t.Fatalf("wrong priority order: %v, %v, %v", got.Models[0]["slug"], got.Models[1]["slug"], got.Models[2]["slug"])
		}
		m := got.Models[0]
		levels, _ := m["supported_reasoning_levels"].([]any)
		if m["slug"] != "team/coding" || m["context_window"] != float64(500000) || len(levels) == 0 {
			t.Fatalf("%s: Codex model slug=%v context=%v reasoning=%v", query, m["slug"], m["context_window"], levels)
		}
		wantLevels := 6
		if query == "?client_version=0.143.0" {
			wantLevels = 4
		}
		if len(levels) != wantLevels || m["minimal_client_version"] != "0.153.0" || m["default_reasoning_level"] != "low" {
			t.Fatalf("%s: reasoning levels=%v minimum client=%v default=%v", query, levels, m["minimal_client_version"], m["default_reasoning_level"])
		}
	}
}

func TestCodexCatalogNoEnabledAccounts(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		t.Run(fmt.Sprint(disabled), func(t *testing.T) {
			base := runCore(t, map[string]any{"listen": "127.0.0.1:0", "client_keys": []string{clientKey}, "management_key": managementKey, "credentials_dir": filepath.Join(t.TempDir(), "credentials"), "extension": map[string]any{"manifest": filepath.Join(binDir, "codex", "pluxy-extension.json"), "accounts": []any{}}})
			if disabled {
				var login loginReply
				if code := manage(t, base, "POST", "/accounts", map[string]any{"id": "off", "method": "api_key", "input": map[string]string{"api_key": "catalog-test"}}, &login); code != http.StatusAccepted {
					t.Fatalf("login status %d", code)
				}
				waitLogin(t, base, login)
				if code := manage(t, base, "PATCH", "/accounts/off", map[string]any{"disabled": true}, nil); code != http.StatusOK {
					t.Fatalf("disable status %d", code)
				}
			}
			req, _ := http.NewRequest("GET", base+"/v1/models?client_version=0.153.0", nil)
			req.Header.Set("Authorization", "Bearer "+clientKey)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			b, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != 200 || string(b) != `{"models":[]}` {
				t.Fatalf("no enabled accounts: status=%d body=%s", resp.StatusCode, b)
			}
		})
	}
}

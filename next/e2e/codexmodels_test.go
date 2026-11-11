package e2e

import (
	"encoding/json"
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
				"models":          []any{map[string]any{"name": "gpt-6.1-sol", "alias": "coding"}, "gpt-6-astra", "unknown-model"},
				"excluded_models": []string{"gpt-6-astra"}, "prefix": "team",
			}},
		},
	})
	if got := listModels(t, base); !slices.Equal(got, []string{"team/coding", "team/unknown-model"}) {
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
		if resp.StatusCode != 200 || len(got.Models) != 1 {
			t.Fatalf("%s: status %d, Codex models = %+v; want one aliased model", query, resp.StatusCode, got.Models)
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

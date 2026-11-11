package main

import (
	"encoding/json"
	"reflect"
	"testing"
)

func catalogForTest(t *testing.T, version string, models []map[string]any) []map[string]any {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"client_version": version, "models": models})
	raw, err := buildClientModels(b)
	if err != nil {
		t.Fatal(err)
	}
	var got struct{ Models []map[string]any }
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	return got.Models
}

func TestClientModelTemplates(t *testing.T) {
	var payload struct{ Models []map[string]json.RawMessage }
	if err := json.Unmarshal(clientModelsJSON, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Models) != 12 || len(clientTemplates()) != 12 {
		t.Fatalf("templates = %d, want 12", len(payload.Models))
	}
	for _, tc := range []struct {
		version string
		levels  int
	}{
		{"v0.143", 4}, {"0.143.0-beta", 4}, {"0.143.x", 6}, {"", 6}, {"malformed", 6},
	} {
		t.Run(tc.version, func(t *testing.T) {
			got := catalogForTest(t, tc.version, []map[string]any{{"name": "public", "upstream": "gpt-6.1-sol", "display_name": "Coding"}})
			if len(got) != 1 {
				t.Fatalf("models = %d", len(got))
			}
			m := got[0]
			if m["slug"] != "public" || m["display_name"] != "Coding" || len(m["supported_reasoning_levels"].([]any)) != tc.levels || m["minimal_client_version"] != "0.153.0" {
				t.Fatalf("slug=%v display=%v levels=%v min=%v", m["slug"], m["display_name"], m["supported_reasoning_levels"], m["minimal_client_version"])
			}
		})
	}
	// A future refresh must not leak runtime-only claims even if the template gains them.
	original := clientTemplates()["gpt-6.1-sol"]["cpa_capabilities"]
	clientTemplates()["gpt-6.1-sol"]["cpa_capabilities"] = json.RawMessage(`{"web_search":true}`)
	defer func() {
		if original == nil {
			delete(clientTemplates()["gpt-6.1-sol"], "cpa_capabilities")
		} else {
			clientTemplates()["gpt-6.1-sol"]["cpa_capabilities"] = original
		}
	}()
	got := catalogForTest(t, "", []map[string]any{{"name": "luna", "upstream": "gpt-6-luna"}, {"name": "astra", "upstream": "gpt-6-astra"}, {"name": "sol", "upstream": "gpt-6.1-sol"}})
	var names []any
	for _, m := range got {
		names = append(names, m["slug"])
		if _, ok := m["cpa_capabilities"]; ok {
			t.Error("cpa_capabilities leaked")
		}
	}
	if !reflect.DeepEqual(names, []any{"sol", "astra", "luna"}) {
		t.Fatalf("priority order = %v", names)
	}
}

func TestClientModelsSharedProvider(t *testing.T) {
	got := catalogForTest(t, "", []map[string]any{{"name": "shared", "upstream": "gpt-6.1-sol", "non_codex": true}})[0]
	want := map[string]any{"prefer_websockets": false, "service_tiers": []any{}, "apply_patch_tool_type": nil, "tool_mode": "direct", "use_responses_lite": false, "supports_reasoning_effort_updates": false, "supports_image_detail_original": false, "upgrade": nil, "availability_nux": nil, "supports_search_tool": false}
	for k, v := range want {
		if !reflect.DeepEqual(got[k], v) {
			t.Errorf("shared %s = %v, want %v", k, got[k], v)
		}
	}
	// A shared name must not mutate the decoded template for later pure-Codex requests.
	pure := catalogForTest(t, "", []map[string]any{{"name": "pure", "upstream": "gpt-6.1-sol"}})[0]
	if pure["prefer_websockets"] != true || pure["supports_search_tool"] != true {
		t.Fatal("shared reply changed pure template")
	}
}

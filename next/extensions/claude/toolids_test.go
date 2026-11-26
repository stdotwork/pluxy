package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// An ID Anthropic does not take is made to fit 64 bytes: a call and its result still pair, two
// different IDs stay different, and a valid short ID keeps today's base64 form.
func TestRepairToolIDsFitsSixtyFour(t *testing.T) {
	longPortable := strings.Repeat("x", 80)
	longInvalid := strings.Repeat("a.", 25)
	otherInvalid := strings.Repeat("a.", 25) + "b"
	in := []any{
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "tool_use", "id": longPortable, "name": "f", "input": map[string]any{}},
			map[string]any{"type": "tool_use", "id": longInvalid, "name": "f", "input": map[string]any{}},
			map[string]any{"type": "tool_use", "id": otherInvalid, "name": "f", "input": map[string]any{}},
			map[string]any{"type": "tool_use", "id": "ok_id", "name": "f", "input": map[string]any{}},
		}},
		map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": longPortable, "content": "1"},
			map[string]any{"type": "tool_result", "tool_use_id": longInvalid, "content": "2"},
			map[string]any{"type": "tool_result", "tool_use_id": otherInvalid, "content": "3"},
			map[string]any{"type": "tool_result", "tool_use_id": "ok_id", "content": "4"},
		}},
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	out, changed := repairToolIDs(raw)
	if !changed {
		t.Fatal("over-long IDs were left alone")
	}
	var got []struct {
		Content []map[string]json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, m := range got {
		for _, b := range m.Content {
			var typ, id string
			_ = json.Unmarshal(b["type"], &typ)
			field := map[string]string{"tool_use": "id", "tool_result": "tool_use_id"}[typ]
			if field == "" {
				continue
			}
			_ = json.Unmarshal(b[field], &id)
			if len(id) > maxToolID {
				t.Errorf("ID %q is %d bytes, over %d", id, len(id), maxToolID)
			}
			if !portableID(id) {
				t.Errorf("ID %q is not one Anthropic takes", id)
			}
			if strings.Contains(id, ".") {
				t.Errorf("ID %q still holds a dot", id)
			}
			ids = append(ids, id)
		}
	}
	if len(ids) != 8 {
		t.Fatalf("got %d IDs, want 8", len(ids))
	}
	for i := 0; i < 4; i++ {
		if ids[i] != ids[i+4] {
			t.Errorf("call and result do not pair: %q vs %q", ids[i], ids[i+4])
		}
	}
	if ids[0] == ids[1] || ids[1] == ids[2] || ids[0] == ids[2] {
		t.Errorf("different client IDs became one: %v", ids[:3])
	}
	if ids[3] != "ok_id" {
		t.Errorf("a valid ID changed to %q", ids[3])
	}
	// The same body is repaired the same way on the next request, so a conversation that is
	// replayed maps its calls the same way.
	if again, _ := repairToolIDs(raw); string(again) != string(out) {
		t.Errorf("repair is not deterministic:\n%s\n%s", out, again)
	}
}

// The short-ID form today's core gives stays: the prefix and the ID in unpadded URL-safe base64.
func TestRepairToolIDsKeepsBase64ForShortIDs(t *testing.T) {
	raw := json.RawMessage(`[{"role":"assistant","content":[{"type":"tool_use","id":"fc:2","name":"f","input":{}}]}]`)
	out, changed := repairToolIDs(raw)
	if !changed || !strings.Contains(string(out), `"id":"cpa_tid_v1_ZmM6Mg"`) {
		t.Errorf("body = %s, want the short ID encoded as before", out)
	}
}

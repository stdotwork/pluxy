package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/sdk"
)

// clientModelsJSON is refreshed by hand as documented in next/README.md. It belongs only
// to this program; ordinary model listing does not decode it or send it to core.
//
//go:embed client_models.json
var clientModelsJSON []byte

var clientTemplates = sync.OnceValue(func() map[string]map[string]json.RawMessage {
	var payload struct{ Models []map[string]json.RawMessage }
	if err := json.Unmarshal(clientModelsJSON, &payload); err != nil {
		panic(err)
	}
	out := make(map[string]map[string]json.RawMessage, len(payload.Models))
	for _, m := range payload.Models {
		var slug string
		_ = json.Unmarshal(m["slug"], &slug)
		out[slug] = m
	}
	return out
})

func clientModels(call sdk.Call, out *sdk.Stream) error {
	body, err := buildClientModels(call.Request.Native.Body)
	if err != nil {
		return err
	}
	if err := out.StartBody("codex.models", "application/json; charset=utf-8"); err != nil {
		return err
	}
	if err := out.Body(string(body)); err != nil {
		return err
	}
	return out.Finish(llm.Finish{Reason: llm.FinishStop}, nil)
}

func buildClientModels(body []byte) ([]byte, error) {
	var wire struct {
		Version string `json:"client_version"`
		Models  []struct {
			Name        string `json:"name"`
			Upstream    string `json:"upstream"`
			DisplayName string `json:"display_name"`
			NonCodex    bool   `json:"non_codex"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		return nil, &llm.Error{Code: llm.CodeInvalidRequest, Scope: llm.ScopeRequest, Message: "invalid Codex catalog request"}
	}
	entries := make([]map[string]json.RawMessage, 0, len(wire.Models))
	templates := clientTemplates()
	for _, n := range wire.Models {
		template, ok := templates[n.Upstream]
		if !ok {
			continue
		}
		m := make(map[string]json.RawMessage, len(template))
		for k, v := range template {
			m[k] = v
		}
		m["slug"], _ = json.Marshal(n.Name)
		if n.DisplayName != "" {
			m["display_name"], _ = json.Marshal(n.DisplayName)
		}
		delete(m, "cpa_capabilities")
		filterClientReasoning(m, wire.Version)
		if n.NonCodex {
			for k, v := range map[string]json.RawMessage{
				"prefer_websockets": raw(false), "service_tiers": raw([]any{}),
				"apply_patch_tool_type": raw(nil), "tool_mode": raw("direct"), "use_responses_lite": raw(false),
				"supports_reasoning_effort_updates": raw(false), "upgrade": raw(nil), "availability_nux": raw(nil), "supports_search_tool": raw(false),
			} {
				m[k] = v
			}
			if _, ok := m["supports_image_detail_original"]; ok {
				m["supports_image_detail_original"] = raw(false)
			}
		}
		entries = append(entries, m)
	}
	slices.SortStableFunc(entries, func(a, b map[string]json.RawMessage) int {
		var x, y int
		_ = json.Unmarshal(a["priority"], &x)
		_ = json.Unmarshal(b["priority"], &y)
		if x < y {
			return -1
		}
		if x > y {
			return 1
		}
		return 0
	})
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(map[string]any{"models": entries}); err != nil {
		return nil, err
	}
	return bytes.TrimSpace(buf.Bytes()), nil
}

func filterClientReasoning(m map[string]json.RawMessage, version string) {
	var levels []map[string]json.RawMessage
	if json.Unmarshal(m["supported_reasoning_levels"], &levels) != nil {
		return
	}
	allowed := map[string]bool{"none": true, "minimal": true, "low": true, "medium": true, "high": true, "xhigh": true}
	if extendedClientReasoning(version) {
		allowed["max"], allowed["ultra"] = true, true
	}
	kept := make([]map[string]json.RawMessage, 0, len(levels))
	present := map[string]bool{}
	for _, l := range levels {
		var effort string
		_ = json.Unmarshal(l["effort"], &effort)
		effort = strings.ToLower(strings.TrimSpace(effort))
		if !allowed[effort] {
			continue
		}
		l["effort"], _ = json.Marshal(effort)
		present[effort] = true
		kept = append(kept, l)
	}
	m["supported_reasoning_levels"], _ = json.Marshal(kept)
	if len(kept) == 0 {
		delete(m, "default_reasoning_level")
		return
	}
	var def string
	_ = json.Unmarshal(m["default_reasoning_level"], &def)
	def = strings.ToLower(strings.TrimSpace(def))
	if !present[def] {
		_ = json.Unmarshal(kept[0]["effort"], &def)
	}
	m["default_reasoning_level"], _ = json.Marshal(def)
}

// Empty and unparseable versions keep modern levels, as today's core does.
func extendedClientReasoning(version string) bool {
	v := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(version), "v"), "V")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	var parts []int64
	for _, p := range strings.Split(v, ".") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		n, err := strconv.ParseInt(p, 10, 64)
		if err != nil || n < 0 {
			return true
		}
		parts = append(parts, n)
	}
	if len(parts) == 0 {
		return true
	}
	for i, n := range []int64{0, 144, 0} {
		var actual int64
		if i < len(parts) {
			actual = parts[i]
		}
		if actual != n {
			return actual > n
		}
	}
	return true
}

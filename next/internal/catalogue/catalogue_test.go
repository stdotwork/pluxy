package catalogue

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"
)

// TestEmbeddedSnapshotCoversBuiltinLists verifies that every model named in any of the
// four providers' built-in fallback lists resolves in the embedded models.dev snapshot
// with a positive context window and non-nil capabilities, except for the explicit list of
// built-in model IDs that models.dev does not carry today. It fails both when an unlisted
// model is missing and when a listed model is found in the snapshot.
func TestEmbeddedSnapshotCoversBuiltinLists(t *testing.T) {
	t.Parallel()
	cat := New(Options{})

	files := []string{
		"../../extensions/claude/models.json",
		"../../extensions/codex/models.json",
		"../../extensions/gemini/models.json",
		"../../extensions/gemini/vertex-models.json",
		"../../extensions/kimi/models.json",
		"../../extensions/xai/models.json",
		"../../extensions/meta/models.json",
	}

	// Model IDs present in pluxy's built-in fallback lists that models.dev does not carry today.
	wantMissing := map[string]bool{
		"codex-auto-review":                true,
		"kimi-k2.8":                        true,
		"kimi-k2.8-code":                   true,
		"kimi-k3-256k":                     true,
		"grok-4.7-build-fast":              true,
		"grok-3-mini-fast":                 true,
		"grok-composer-2.5-fast":           true,
		"muse-wonder-1.0-contributor":      true,
		"muse-wonder-full-1.0":             true,
		"muse-wonder-full-1.0-contributor": true,
	}
	seenMissing := map[string]bool{}

	for _, rel := range files {
		raw, err := os.ReadFile(rel)
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		ids := extractBuiltinModelIDs(t, rel, raw)
		if len(ids) == 0 {
			t.Fatalf("%s: extracted 0 model IDs", rel)
		}
		for _, id := range ids {
			m, ok := cat.Lookup(id)
			if !ok {
				if wantMissing[id] {
					seenMissing[id] = true
					continue
				}
				t.Errorf("%s: model %q missing from embedded snapshot", rel, id)
				continue
			}
			if wantMissing[id] {
				t.Errorf("%s: model %q is now present in embedded snapshot; remove it from wantMissing", rel, id)
			}
			if m.ContextLength <= 0 {
				t.Errorf("%s: model %q has context_length %d, want > 0", rel, id, m.ContextLength)
			}
			if m.Capabilities == nil {
				t.Errorf("%s: model %q has nil capabilities", rel, id)
			}
		}
	}
	for id := range wantMissing {
		if !seenMissing[id] {
			t.Errorf("wantMissing lists %q, which was not missing from any built-in list", id)
		}
	}
}

func extractBuiltinModelIDs(t *testing.T, path string, raw []byte) []string {
	t.Helper()
	type item struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Slug string `json:"slug"`
	}
	pick := func(it item) string {
		if it.ID != "" {
			return it.ID
		}
		if it.Name != "" {
			return it.Name
		}
		return it.Slug
	}
	var wrapper struct {
		Models []item `json:"models"`
	}
	var ids []string
	seen := map[string]bool{}
	add := func(id string) {
		if id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if err := json.Unmarshal(raw, &wrapper); err == nil && len(wrapper.Models) > 0 {
		for _, it := range wrapper.Models {
			add(pick(it))
		}
		return ids
	}
	var byPlan map[string][]item
	if err := json.Unmarshal(raw, &byPlan); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	for _, list := range byPlan {
		for _, it := range list {
			add(pick(it))
		}
	}
	return ids
}

// TestCatalogueRefresh verifies daily refresh behavior against an httptest server:
// 200 replaces state/catalogue.json and updates lookup results, 304 with If-None-Match
// keeps the file, a 500 error or an empty body keeps the last good file, and a hanging
// server is bounded by the timeout (default 10 s).
func TestCatalogueRefresh(t *testing.T) {
	t.Parallel()

	var (
		mode       atomic.Int32
		ifNoneSeen atomic.Value
		hangWait   = make(chan struct{})
	)
	defer close(hangWait)

	const bodyV1 = `{
		"openai": {
			"models": {
				"gpt-refresh-test": {
					"limit": {"context": 400000, "input": 300000, "output": 64000},
					"modalities": {"input": ["text", "image"], "output": ["text"]},
					"tool_call": true,
					"reasoning": true,
					"reasoning_options": [{"type": "effort", "values": ["low", "medium", "high"]}],
					"structured_output": true
				}
			}
		}
	}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if v := r.Header.Get("If-None-Match"); v != "" {
			ifNoneSeen.Store(v)
		}
		switch mode.Load() {
		case 0: // 200 OK with ETag
			w.Header().Set("ETag", `"etag-v1"`)
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, bodyV1)
		case 1: // 304 Not Modified
			w.WriteHeader(http.StatusNotModified)
		case 2: // 500 Internal Server Error
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, "upstream down")
		case 3: // 200 OK with empty body
			w.WriteHeader(http.StatusOK)
		case 4: // 200 OK with empty JSON object (no models)
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{}`)
		case 5: // hang until request context expires
			select {
			case <-r.Context().Done():
			case <-hangWait:
			}
		}
	}))
	t.Cleanup(srv.Close)

	statePath := filepath.Join(t.TempDir(), "state", "catalogue.json")
	cat := New(Options{
		StatePath: statePath,
		URL:       srv.URL,
	})
	if cat.timeout != DefaultTimeout || DefaultTimeout != 10*time.Second {
		t.Fatalf("default timeout = %v (DefaultTimeout=%v), want 10s", cat.timeout, DefaultTimeout)
	}

	// 1. 200 replaces state/catalogue.json and updates lookup.
	mode.Store(0)
	if err := cat.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh (200): %v", err)
	}
	m, ok := cat.Lookup("gpt-refresh-test")
	if !ok {
		t.Fatalf("gpt-refresh-test missing after 200 refresh")
	}
	if m.ContextLength != 300000 || m.MaxCompletionTokens != 64000 {
		t.Errorf("limits = (%d, %d), want (300000, 64000)", m.ContextLength, m.MaxCompletionTokens)
	}
	if m.Capabilities == nil || !m.Capabilities.Tools || !m.Capabilities.StructuredOutput ||
		!m.Capabilities.Vision || !m.Capabilities.Reasoning ||
		!slices.Equal(m.Capabilities.ReasoningLevels, []string{"low", "medium", "high"}) {
		t.Errorf("capabilities = %+v", m.Capabilities)
	}
	savedAfter200, err := os.ReadFile(statePath)
	if err != nil || len(savedAfter200) == 0 {
		t.Fatalf("read %s after 200: len=%d err=%v", statePath, len(savedAfter200), err)
	}

	// 2. 304 sends If-None-Match and keeps the file and in-memory data.
	mode.Store(1)
	if err := cat.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh (304): %v", err)
	}
	if got, _ := ifNoneSeen.Load().(string); got != `"etag-v1"` {
		t.Errorf("If-None-Match = %q, want %q", got, `"etag-v1"`)
	}
	savedAfter304, err := os.ReadFile(statePath)
	if err != nil || string(savedAfter304) != string(savedAfter200) {
		t.Errorf("state file changed on 304")
	}
	if _, ok := cat.Lookup("gpt-refresh-test"); !ok {
		t.Errorf("gpt-refresh-test lost after 304")
	}

	// 3. 500 error keeps the last good file and in-memory data.
	mode.Store(2)
	if err := cat.Refresh(context.Background()); err == nil {
		t.Errorf("Refresh (500): expected error, got nil")
	}
	savedAfter500, err := os.ReadFile(statePath)
	if err != nil || string(savedAfter500) != string(savedAfter200) {
		t.Errorf("state file changed on 500")
	}
	if _, ok := cat.Lookup("gpt-refresh-test"); !ok {
		t.Errorf("gpt-refresh-test lost after 500")
	}

	// 4. Empty body and empty JSON object keep the last good file and in-memory data.
	for _, mCode := range []int32{3, 4} {
		mode.Store(mCode)
		if err := cat.Refresh(context.Background()); err == nil {
			t.Errorf("Refresh (mode %d): expected error on empty body, got nil", mCode)
		}
		savedAfterEmpty, err := os.ReadFile(statePath)
		if err != nil || string(savedAfterEmpty) != string(savedAfter200) {
			t.Errorf("state file changed on empty mode %d", mCode)
		}
		if _, ok := cat.Lookup("gpt-refresh-test"); !ok {
			t.Errorf("gpt-refresh-test lost after empty mode %d", mCode)
		}
	}

	// 5. Timeout limit cancels a hanging server and keeps the last good file.
	fastCat := New(Options{
		StatePath: statePath,
		URL:       srv.URL,
		Timeout:   50 * time.Millisecond,
	})
	mode.Store(5)
	if err := fastCat.Refresh(context.Background()); err == nil {
		t.Errorf("Refresh (timeout): expected error, got nil")
	}
	if _, ok := fastCat.Lookup("gpt-refresh-test"); !ok {
		t.Errorf("reloaded catalogue from statePath missing gpt-refresh-test after timeout")
	}
}

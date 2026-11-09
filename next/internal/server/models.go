package server

import (
	"cmp"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// Defaults an Anthropic model list shows for a model whose limits are not known, as today's core
// shows them.
const (
	defaultMaxInputTokens  = 200000
	defaultMaxOutputTokens = 64000
)

// serveModels adds the model lists to mux: GET /v1/models and /v1/models/{id} in OpenAI's shape,
// or Anthropic's for an Anthropic client; GET /v1beta/models and /v1beta/models/{name} in
// Gemini's. Each lists the models the accounts serve now, under every name a client can use.
func serveModels(mux *http.ServeMux, src Source) {
	list := func(google bool, write func(w http.ResponseWriter, r *http.Request, models []protocol.Model)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			g, release := src.Acquire()
			defer release()
			if !authorized(g.Config, r, google) {
				f := chatFormat
				if google {
					f = geminiFormat
				}
				f.writeError(w, &llm.Error{Code: llm.CodeAuth, Message: "missing or invalid API key", Status: http.StatusUnauthorized, ProviderCode: "invalid_api_key"})
				return
			}
			write(w, r, g.Router.Models(r.Context()))
		}
	}
	mux.HandleFunc("GET /v1/models", list(false, func(w http.ResponseWriter, r *http.Request, models []protocol.Model) {
		if anthropicClient(r) {
			writeList(w, http.StatusOK, anthropicList(models))
			return
		}
		data := make([]map[string]any, len(models))
		for i, m := range models {
			data[i] = openAIModel(m)
		}
		writeList(w, http.StatusOK, map[string]any{"object": "list", "data": data})
	}))
	mux.HandleFunc("GET /v1/models/{model...}", list(false, func(w http.ResponseWriter, r *http.Request, models []protocol.Model) {
		id := r.PathValue("model")
		var items []map[string]any
		if anthropicClient(r) {
			items = anthropicList(models)["data"].([]map[string]any)
		} else {
			for _, m := range models {
				items = append(items, openAIModel(m))
			}
		}
		for _, m := range items {
			if m["id"] == id {
				writeList(w, http.StatusOK, m)
				return
			}
		}
		writeList(w, http.StatusNotFound, map[string]any{"error": map[string]string{
			"message": "Model not found", "type": "invalid_request_error", "code": "model_not_found"}})
	}))
	mux.HandleFunc("GET /v1beta/models", list(true, func(w http.ResponseWriter, _ *http.Request, models []protocol.Model) {
		out := make([]map[string]any, len(models))
		for i, m := range models {
			out[i] = geminiModel(m, true)
		}
		writeList(w, http.StatusOK, map[string]any{"models": out})
	}))
	mux.HandleFunc("GET /v1beta/models/{name...}", list(true, func(w http.ResponseWriter, r *http.Request, models []protocol.Model) {
		// As in today's core: the name matches with or without "models/" before it, against
		// the model's resource name as it is held.
		name := r.PathValue("name")
		for _, m := range models {
			if n := cmp.Or(m.Name, m.ID); n == name || n == "models/"+name {
				writeList(w, http.StatusOK, geminiModel(m, false))
				return
			}
		}
		writeList(w, http.StatusNotFound, map[string]any{"error": map[string]string{"message": "Not Found", "type": "not_found"}})
	}))
}

// anthropicClient reports whether a model list request comes from an Anthropic client: it sends
// anthropic-version, or Claude Code's user agent.
func anthropicClient(r *http.Request) bool {
	return r.Header.Get("Anthropic-Version") != "" || strings.HasPrefix(r.Header.Get("User-Agent"), "claude-cli")
}

func openAIModel(m protocol.Model) map[string]any {
	out := map[string]any{"id": m.ID, "object": "model", "owned_by": m.OwnedBy}
	if m.Created > 0 {
		out["created"] = m.Created
	}
	return out
}

// anthropicList is Anthropic's model list: sorted by display name, then ID.
func anthropicList(models []protocol.Model) map[string]any {
	data := make([]map[string]any, 0, len(models))
	for _, m := range models {
		item := map[string]any{"id": m.ID, "object": "model", "owned_by": m.OwnedBy, "type": "model",
			"display_name":     cmp.Or(m.DisplayName, m.ID),
			"max_input_tokens": cmp.Or(m.ContextLength, defaultMaxInputTokens),
			"max_tokens":       cmp.Or(m.MaxCompletionTokens, defaultMaxOutputTokens)}
		if m.Created > 0 {
			item["created_at"] = time.Unix(m.Created, 0).UTC().Format(time.RFC3339)
		}
		data = append(data, item)
	}
	slices.SortStableFunc(data, func(a, b map[string]any) int {
		return cmp.Or(strings.Compare(a["display_name"].(string), b["display_name"].(string)), strings.Compare(a["id"].(string), b["id"].(string)))
	})
	first, last := "", ""
	if len(data) > 0 {
		first, last = data[0]["id"].(string), data[len(data)-1]["id"].(string)
	}
	return map[string]any{"data": data, "has_more": false, "first_id": first, "last_id": last}
}

// geminiModel is a model in Gemini's shape. A list fills in a display name, a description and
// the generation methods where the model has none, as today's core's list does; one model alone
// is shown as it is held.
func geminiModel(m protocol.Model, inList bool) map[string]any {
	name := cmp.Or(m.Name, m.ID)
	out := map[string]any{"name": name}
	if !strings.HasPrefix(name, "models/") {
		out["name"] = "models/" + name
	}
	set := func(k, v string) {
		if v != "" {
			out[k] = v
		}
	}
	set("version", m.Version)
	set("displayName", m.DisplayName)
	set("description", m.Description)
	if n := cmp.Or(m.InputTokenLimit, m.ContextLength); n > 0 {
		out["inputTokenLimit"] = n
	}
	if n := cmp.Or(m.OutputTokenLimit, m.MaxCompletionTokens); n > 0 {
		out["outputTokenLimit"] = n
	}
	for k, v := range map[string][]string{"supportedGenerationMethods": m.GenerationMethods,
		"supportedInputModalities": m.InputModalities, "supportedOutputModalities": m.OutputModalities} {
		if len(v) > 0 {
			out[k] = v
		}
	}
	if inList {
		if out["displayName"] == nil {
			out["displayName"] = name
		}
		if out["description"] == nil {
			out["description"] = name
		}
		if out["supportedGenerationMethods"] == nil {
			out["supportedGenerationMethods"] = []string{"generateContent"}
		}
	}
	return out
}

// writeList writes a model list reply, with the content type today's core gives it.
func writeList(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(b)
}

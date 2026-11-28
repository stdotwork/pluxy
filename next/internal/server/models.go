package server

import (
	"cmp"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/stdotwork/pluxy/next/internal/route"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

const (
	defaultMaxInputTokens  = 200000
	defaultMaxOutputTokens = 64000
)

func serveModels(mux *http.ServeMux, src Source) {
	list := func(google bool, write func(w http.ResponseWriter, r *http.Request, models []route.ServedModel)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			g, release := src.Acquire()
			defer release()
			if e := refused(g.Config, r, google); e != nil {
				f := chatFormat
				if google {
					f = geminiFormat
				}
				f.writeError(w, e)
				return
			}
			if !google && r.URL.Path == "/v1/models" && r.URL.Query().Has("client_version") {
				serveCodexModels(w, r, g)
				return
			}
			write(w, r, g.Router.ServedModels(r.Context()))
		}
	}
	toModels := func(served []route.ServedModel) []protocol.Model {
		out := make([]protocol.Model, len(served))
		for i, m := range served {
			out[i] = m.Model
		}
		return out
	}
	mux.HandleFunc("GET /v1/models", accessLog(list(false, func(w http.ResponseWriter, r *http.Request, models []route.ServedModel) {
		if anthropicClient(r) {
			writeList(w, http.StatusOK, anthropicList(toModels(models)))
			return
		}
		data := make([]map[string]any, len(models))
		for i, m := range models {
			data[i] = openAIModel(m)
		}
		writeList(w, http.StatusOK, map[string]any{"object": "list", "data": data})
	})))
	mux.HandleFunc("GET /v1/models/{model...}", accessLog(list(false, func(w http.ResponseWriter, r *http.Request, models []route.ServedModel) {
		id := r.PathValue("model")
		var items []map[string]any
		if anthropicClient(r) {
			items = anthropicList(toModels(models))["data"].([]map[string]any)
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
	})))
	mux.HandleFunc("GET /v1beta/models", accessLog(list(true, func(w http.ResponseWriter, _ *http.Request, models []route.ServedModel) {
		out := make([]map[string]any, len(models))
		for i, m := range models {
			out[i] = geminiModel(m.Model, true)
		}
		writeList(w, http.StatusOK, map[string]any{"models": out})
	})))
	mux.HandleFunc("GET /v1beta/models/{name...}", accessLog(list(true, func(w http.ResponseWriter, r *http.Request, models []route.ServedModel) {
		name := r.PathValue("name")
		for _, m := range models {
			if n := cmp.Or(m.Name, m.ID); n == name || n == "models/"+name {
				writeList(w, http.StatusOK, geminiModel(m.Model, false))
				return
			}
		}
		writeList(w, http.StatusNotFound, map[string]any{"error": map[string]string{"message": "Not Found", "type": "not_found"}})
	})))
}

func serveCodexModels(w http.ResponseWriter, r *http.Request, g *Generation) {
	var account *protocol.Account
	for _, a := range g.Router.Accounts() {
		if a.Extension == "pluxy/codex" {
			account = &protocol.Account{ID: a.ID}
			break
		}
	}
	if account == nil {
		writeList(w, http.StatusOK, map[string]any{"models": []any{}})
		return
	}
	if g.Endpoint == nil {
		chatFormat.writeError(w, &llm.Error{Code: llm.CodeNotFound, Status: http.StatusNotFound, Message: "this endpoint is not served"})
		return
	}
	models := make([]map[string]any, 0)
	for _, n := range g.Router.ModelNames(r.Context(), "pluxy/codex") {
		model := map[string]any{"name": n.Public, "upstream": n.Upstream}
		if n.DisplayName != "" {
			model["display_name"] = n.DisplayName
		}
		if n.NonCodex {
			model["non_codex"] = true
		}
		models = append(models, model)
	}
	body, _ := json.Marshal(map[string]any{"client_version": r.URL.Query().Get("client_version"), "models": models})
	req := llm.Request{Native: &llm.Native{Format: "codex.models", Body: body}}
	stream := g.Endpoint(r.Context(), "pluxy/codex", *account, req)
	writeEndpointReply(w, r, stream, "codex.models", chatFormat, nil, false)
}

func anthropicClient(r *http.Request) bool {
	return r.Header.Get("Anthropic-Version") != "" || strings.HasPrefix(r.Header.Get("User-Agent"), "claude-cli")
}

func openAIModel(m route.ServedModel) map[string]any {
	out := map[string]any{"id": m.ID, "object": "model", "owned_by": m.OwnedBy}
	if m.Created > 0 {
		out["created"] = m.Created
	}
	if m.ContextLength > 0 {
		out["context_length"] = m.ContextLength
	}
	if m.MaxOutputTokens > 0 {
		out["max_output_tokens"] = m.MaxOutputTokens
	}
	if m.Capabilities != nil {
		out["capabilities"] = m.Capabilities
	}
	if m.From != "" {
		out["from"] = m.From
	}
	return out
}

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

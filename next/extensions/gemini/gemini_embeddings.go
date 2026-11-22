package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/sdk"
)

type geminiEmbedItem struct {
	text                 string
	taskType             string
	title                string
	outputDimensionality *int
}

type vertexPredictEmbedInstance struct {
	Content  string `json:"content"`
	TaskType string `json:"task_type,omitempty"`
	Title    string `json:"title,omitempty"`
}

type vertexPredictEmbedRequest struct {
	Instances  []vertexPredictEmbedInstance `json:"instances"`
	Parameters *vertexEmbedParams           `json:"parameters,omitempty"`
}

// geminiEmbedContent serves Gemini's POST /v1beta/models/{model}:embedContent on a Gemini account.
func geminiEmbedContent(ctx context.Context, call sdk.Call, out *sdk.Stream) error {
	return geminiEmbedTo(ctx, call, out, llm.FormatGeminiEmbedContent)
}

// geminiBatchEmbedContents serves Gemini's POST /v1beta/models/{model}:batchEmbedContents on a
// Gemini account.
func geminiBatchEmbedContents(ctx context.Context, call sdk.Call, out *sdk.Stream) error {
	return geminiEmbedTo(ctx, call, out, llm.FormatGeminiBatchEmbedContents)
}

func geminiEmbedTo(ctx context.Context, call sdk.Call, out sender, format string) (err error) {
	if call.Request.Options.ThinkingSuffix != "" {
		return badEmbeddingRequest("a thinking suffix on the model name is not supported for embeddings")
	}
	if call.Request.Native == nil {
		return badEmbeddingRequest("the request body is missing")
	}
	var rawMap map[string]json.RawMessage
	if json.Unmarshal(call.Request.Native.Body, &rawMap) != nil || rawMap == nil {
		return badEmbeddingRequest("the request body is not a JSON object")
	}
	if string(rawMap["stream"]) == "true" || call.Request.Stream {
		return badEmbeddingRequest("embeddings do not support streaming")
	}
	s, err := readSettings(call.Account.Settings)
	if err != nil {
		return err
	}
	model := strings.TrimPrefix(call.Request.Model, "models/")
	isBatch := format == llm.FormatGeminiBatchEmbedContents

	method := ":embedContent"
	if s.Vertex != nil {
		if isVertexEmbedContentModel(model) {
			method = ":embedContent"
		} else {
			method = ":predict"
		}
	} else if isBatch {
		method = ":batchEmbedContents"
	}

	t, err := resolveMethod(ctx, s, call.Account.Credential, model, method)
	if err != nil {
		return err
	}
	send := &reporting{out: out, known: knownNames(call.Account)}
	if t.token != "" {
		send.redact = protocolRedactor(t.token)
		defer func() {
			var e *llm.Error
			if errors.As(err, &e) {
				err = send.redact.Event(llm.Event{Type: llm.EventError, Error: e}).Error
			}
		}()
	}

	// Gemini API: pass the client's body and the upstream's reply through unchanged, rewriting
	// the body's model only when an account alias changed it.
	if s.Vertex == nil {
		reqBody, err := geminiAPIPassthroughBody(call.Request.Native.Body, rawMap, model, isBatch)
		if err != nil {
			return err
		}
		raw, err := postEmbeddingRaw(ctx, t, model, reqBody)
		if err != nil {
			return err
		}
		if !json.Valid(raw) {
			return upstreamEmbeddingError("Gemini's embedding reply is not JSON")
		}
		usage := decodeGeminiEmbedUsage(raw)
		if err := send.StartBody(format, "application/json"); err != nil {
			return err
		}
		bw := newChunkedBodyWriter(send, embeddingBodyChunkSize)
		if _, err := bw.Write(raw); err != nil {
			return err
		}
		if err := bw.Close(); err != nil {
			return err
		}
		return send.Finish(llm.Finish{Reason: llm.FinishStop}, usage)
	}

	// Vertex AI: translate only what the model's Vertex endpoint needs.
	if isVertexEmbedContentModel(model) {
		if !isBatch {
			reqBody, err := buildVertexEmbedContentBody(rawMap)
			if err != nil {
				return err
			}
			raw, err := postEmbeddingRaw(ctx, t, model, reqBody)
			if err != nil {
				return err
			}
			if !json.Valid(raw) {
				return upstreamEmbeddingError("Vertex AI's embedding reply is not JSON")
			}
			usage := decodeGeminiEmbedUsage(raw)
			if err := send.StartBody(format, "application/json"); err != nil {
				return err
			}
			bw := newChunkedBodyWriter(send, embeddingBodyChunkSize)
			if _, err := bw.Write(raw); err != nil {
				return err
			}
			if err := bw.Close(); err != nil {
				return err
			}
			return send.Finish(llm.Finish{Reason: llm.FinishStop}, usage)
		}
		items, err := readGeminiBatchRawItems(rawMap)
		if err != nil {
			return err
		}
		vectors := make([][]float64, 0, len(items))
		var totalValues int
		var promptTokens int64
		var hasUsage bool
		for _, item := range items {
			reqBody, err := buildVertexEmbedContentBody(item)
			if err != nil {
				return err
			}
			raw, err := postEmbeddingRaw(ctx, t, model, reqBody)
			if err != nil {
				return err
			}
			vec, tokens, ok, err := decodeEmbedContentReply(raw, "Vertex AI")
			if err != nil {
				return err
			}
			totalValues += len(vec)
			if totalValues > maxEmbeddingsTotalValues {
				return upstreamEmbeddingError("Vertex AI returned more embedding values than supported")
			}
			vectors = append(vectors, vec)
			if ok {
				promptTokens += tokens
				hasUsage = true
			}
		}
		return writeGeminiEmbedReply(send, format, true, vectors, promptTokens, hasUsage)
	}

	// Vertex AI :predict models (gemini-embedding-001 at batch size 1; other models up to 250).
	var items []geminiEmbedItem
	if isBatch {
		rawItems, err := readGeminiBatchRawItems(rawMap)
		if err != nil {
			return err
		}
		items = make([]geminiEmbedItem, len(rawItems))
		for i, rawItem := range rawItems {
			it, err := parseGeminiEmbedItem(rawItem, fmt.Sprintf("requests[%d]", i))
			if err != nil {
				return err
			}
			items[i] = it
		}
	} else {
		it, err := parseGeminiEmbedItem(rawMap, "")
		if err != nil {
			return err
		}
		items = []geminiEmbedItem{it}
	}
	vectors, promptTokens, hasUsage, err := runVertexPredictGeminiItems(ctx, t, model, items)
	if err != nil {
		return err
	}
	return writeGeminiEmbedReply(send, format, isBatch, vectors, promptTokens, hasUsage)
}

// geminiAPIPassthroughBody returns raw unchanged when the body's model fields already match
// models/<model> (or are omitted), and updates them only when an account alias renamed the model.
func geminiAPIPassthroughBody(raw []byte, rawMap map[string]json.RawMessage, model string, isBatch bool) ([]byte, error) {
	want := "models/" + model
	if !isBatch {
		mRaw, ok := rawMap["model"]
		if !ok || string(mRaw) == "null" {
			return raw, nil
		}
		var cur string
		if json.Unmarshal(mRaw, &cur) != nil || cur == "" || cur == want || cur == model {
			return raw, nil
		}
		updated := make(map[string]json.RawMessage, len(rawMap))
		for k, v := range rawMap {
			updated[k] = v
		}
		updated["model"], _ = json.Marshal(want)
		return json.Marshal(updated)
	}
	reqsRaw, ok := rawMap["requests"]
	if !ok || string(reqsRaw) == "null" {
		return raw, nil
	}
	var reqs []map[string]json.RawMessage
	if json.Unmarshal(reqsRaw, &reqs) != nil {
		return raw, nil
	}
	changed := false
	for _, r := range reqs {
		if r == nil {
			continue
		}
		mRaw, ok := r["model"]
		if !ok || string(mRaw) == "null" {
			continue
		}
		var cur string
		if json.Unmarshal(mRaw, &cur) == nil && cur != "" && cur != want && cur != model {
			r["model"], _ = json.Marshal(want)
			changed = true
		}
	}
	if !changed {
		return raw, nil
	}
	updated := make(map[string]json.RawMessage, len(rawMap))
	for k, v := range rawMap {
		updated[k] = v
	}
	updated["requests"], _ = json.Marshal(reqs)
	return json.Marshal(updated)
}

func buildVertexEmbedContentBody(item map[string]json.RawMessage) ([]byte, error) {
	contentRaw, ok := item["content"]
	if !ok || len(contentRaw) == 0 || string(contentRaw) == "null" {
		return nil, badEmbeddingRequest("content is required")
	}
	var cfg map[string]json.RawMessage
	if rawCfg, ok := item["embedContentConfig"]; ok && len(rawCfg) > 0 && string(rawCfg) != "null" {
		if err := json.Unmarshal(rawCfg, &cfg); err != nil || cfg == nil {
			return nil, badEmbeddingRequest("embedContentConfig must be a JSON object")
		}
	}
	setCfg := func(dst string, srcs ...string) {
		if cfg != nil {
			if _, exists := cfg[dst]; exists {
				return
			}
		}
		for _, src := range srcs {
			if v, ok := item[src]; ok && len(v) > 0 && string(v) != "null" {
				if cfg == nil {
					cfg = map[string]json.RawMessage{}
				}
				cfg[dst] = v
				return
			}
		}
	}
	setCfg("taskType", "taskType", "task_type")
	setCfg("title", "title")
	setCfg("outputDimensionality", "outputDimensionality", "output_dimensionality")

	out := map[string]json.RawMessage{"content": contentRaw}
	if len(cfg) > 0 {
		b, err := json.Marshal(cfg)
		if err != nil {
			return nil, err
		}
		out["embedContentConfig"] = b
	}
	return json.Marshal(out)
}

func readGeminiBatchRawItems(rawMap map[string]json.RawMessage) ([]map[string]json.RawMessage, error) {
	reqsRaw, ok := rawMap["requests"]
	if !ok || len(reqsRaw) == 0 || string(reqsRaw) == "null" {
		return nil, badEmbeddingRequest("requests is required")
	}
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(reqsRaw, &items); err != nil {
		return nil, badEmbeddingRequest("requests must be an array of request objects")
	}
	if len(items) == 0 {
		return nil, badEmbeddingRequest("requests must not be empty")
	}
	if len(items) > maxEmbeddingsInputs {
		return nil, badEmbeddingRequest(fmt.Sprintf("requests has %d items, at most %d are supported", len(items), maxEmbeddingsInputs))
	}
	for i, it := range items {
		if it == nil {
			return nil, badEmbeddingRequest(fmt.Sprintf("requests[%d] must be a JSON object", i))
		}
	}
	return items, nil
}

func parseGeminiEmbedItem(item map[string]json.RawMessage, prefix string) (geminiEmbedItem, error) {
	field := func(name string) string {
		if prefix == "" {
			return name
		}
		return prefix + "." + name
	}
	var out geminiEmbedItem
	contentRaw, ok := item["content"]
	if !ok || len(contentRaw) == 0 || string(contentRaw) == "null" {
		return out, badEmbeddingRequest(field("content") + " is required")
	}
	var content struct {
		Parts []map[string]json.RawMessage `json:"parts"`
	}
	if err := json.Unmarshal(contentRaw, &content); err != nil {
		return out, badEmbeddingRequest(field("content") + " must be a JSON object with parts")
	}
	if len(content.Parts) == 0 {
		return out, badEmbeddingRequest(field("content.parts") + " must not be empty")
	}
	var sb strings.Builder
	for i, part := range content.Parts {
		if part == nil {
			return out, badEmbeddingRequest(fmt.Sprintf("%s[%d] must be a JSON object", field("content.parts"), i))
		}
		for k := range part {
			if k != "text" {
				return out, &llm.Error{
					Code:      llm.CodeUnsupported,
					Status:    http.StatusBadRequest,
					Scope:     llm.ScopeCredential,
					Retryable: true,
					Message:   fmt.Sprintf("%s[%d].%s: only text parts are supported on this embedding endpoint", field("content.parts"), i, k),
				}
			}
		}
		textRaw, ok := part["text"]
		if !ok || string(textRaw) == "null" {
			return out, badEmbeddingRequest(fmt.Sprintf("%s[%d].text is required", field("content.parts"), i))
		}
		var text string
		if err := json.Unmarshal(textRaw, &text); err != nil {
			return out, badEmbeddingRequest(fmt.Sprintf("%s[%d].text must be a string", field("content.parts"), i))
		}
		sb.WriteString(text)
	}
	out.text = sb.String()

	var cfg map[string]json.RawMessage
	if rawCfg, ok := item["embedContentConfig"]; ok && len(rawCfg) > 0 && string(rawCfg) != "null" {
		if err := json.Unmarshal(rawCfg, &cfg); err != nil || cfg == nil {
			return out, badEmbeddingRequest(field("embedContentConfig") + " must be a JSON object")
		}
	}
	pickRaw := func(names ...string) (json.RawMessage, bool) {
		for _, n := range names {
			if v, ok := item[n]; ok && len(v) > 0 && string(v) != "null" {
				return v, true
			}
		}
		for _, n := range names {
			if v, ok := cfg[n]; ok && len(v) > 0 && string(v) != "null" {
				return v, true
			}
		}
		return nil, false
	}
	if raw, ok := pickRaw("taskType", "task_type"); ok {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return out, badEmbeddingRequest(field("taskType") + " must be a string")
		}
		if s != "" && s != "TASK_TYPE_UNSPECIFIED" {
			out.taskType = s
		}
	}
	if raw, ok := pickRaw("title"); ok {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return out, badEmbeddingRequest(field("title") + " must be a string")
		}
		out.title = s
	}
	if raw, ok := pickRaw("outputDimensionality", "output_dimensionality"); ok {
		var dim int
		if err := json.Unmarshal(raw, &dim); err != nil || dim < 1 {
			return out, badEmbeddingRequest(field("outputDimensionality") + " must be a positive integer")
		}
		out.outputDimensionality = &dim
	}
	return out, nil
}

func sameOptInt(a, b *int) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func runVertexPredictGeminiItems(ctx context.Context, t target, model string, items []geminiEmbedItem) ([][]float64, int64, bool, error) {
	all := make([][]float64, 0, len(items))
	var totalValues int
	var promptTokens int64
	var hasUsage bool
	batchSize := vertexPredictBatchSize(model)

	for start := 0; start < len(items); {
		end := start + 1
		for end < len(items) && end-start < batchSize && sameOptInt(items[start].outputDimensionality, items[end].outputDimensionality) {
			end++
		}
		chunk := items[start:end]
		start = end

		instances := make([]vertexPredictEmbedInstance, len(chunk))
		for i, it := range chunk {
			instances[i] = vertexPredictEmbedInstance{
				Content:  it.text,
				TaskType: it.taskType,
				Title:    it.title,
			}
		}
		var params *vertexEmbedParams
		if chunk[0].outputDimensionality != nil {
			params = &vertexEmbedParams{OutputDimensionality: chunk[0].outputDimensionality}
		}
		raw, err := postEmbeddingJSON(ctx, t, model, vertexPredictEmbedRequest{Instances: instances, Parameters: params})
		if err != nil {
			return nil, 0, false, err
		}
		var resp struct {
			Predictions []struct {
				Embeddings *struct {
					Values     []float64 `json:"values"`
					Statistics *struct {
						TokenCount *float64 `json:"token_count"`
					} `json:"statistics"`
				} `json:"embeddings"`
			} `json:"predictions"`
		}
		if err := json.Unmarshal(raw, &resp); err != nil {
			return nil, 0, false, upstreamEmbeddingError("Vertex AI's embedding reply is not JSON")
		}
		if len(resp.Predictions) != len(chunk) {
			return nil, 0, false, upstreamEmbeddingError(fmt.Sprintf("Vertex AI returned %d embeddings for %d inputs", len(resp.Predictions), len(chunk)))
		}
		for _, pred := range resp.Predictions {
			if pred.Embeddings == nil || pred.Embeddings.Values == nil {
				return nil, 0, false, upstreamEmbeddingError("Vertex AI returned a prediction without embedding values")
			}
			totalValues += len(pred.Embeddings.Values)
			if totalValues > maxEmbeddingsTotalValues {
				return nil, 0, false, upstreamEmbeddingError("Vertex AI returned more embedding values than supported")
			}
			all = append(all, pred.Embeddings.Values)
			if pred.Embeddings.Statistics != nil && pred.Embeddings.Statistics.TokenCount != nil {
				promptTokens += int64(*pred.Embeddings.Statistics.TokenCount)
				hasUsage = true
			}
		}
	}
	return all, promptTokens, hasUsage, nil
}

func postEmbeddingRaw(ctx context.Context, t target, model string, body []byte) ([]byte, error) {
	resp, err := postBody(ctx, t, body, false, model)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		return nil, upstreamEmbeddingError("Gemini streamed an embedding reply")
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxEmbeddingsReply+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, upstreamEmbeddingError("reading upstream response: " + err.Error())
	}
	if len(raw) > maxEmbeddingsReply {
		return nil, upstreamEmbeddingError(fmt.Sprintf("the upstream's reply is larger than %d MiB", maxEmbeddingsReply>>20))
	}
	return raw, nil
}

func decodeGeminiEmbedUsage(raw []byte) *llm.Usage {
	var u struct {
		UsageMetadata *struct {
			PromptTokenCount *int64 `json:"promptTokenCount"`
		} `json:"usageMetadata"`
	}
	if json.Unmarshal(raw, &u) != nil || u.UsageMetadata == nil || u.UsageMetadata.PromptTokenCount == nil {
		return nil
	}
	n := *u.UsageMetadata.PromptTokenCount
	return &llm.Usage{InputTokens: &n}
}

func writeGeminiEmbedReply(send *reporting, format string, isBatch bool, vectors [][]float64, promptTokens int64, hasUsage bool) error {
	var usage *llm.Usage
	if hasUsage {
		n := promptTokens
		usage = &llm.Usage{InputTokens: &n}
	}
	if err := send.StartBody(format, "application/json"); err != nil {
		return err
	}
	bw := newChunkedBodyWriter(send, embeddingBodyChunkSize)
	if err := streamGeminiEmbedJSON(bw, isBatch, vectors, promptTokens, hasUsage); err != nil {
		return err
	}
	if err := bw.Close(); err != nil {
		return err
	}
	return send.Finish(llm.Finish{Reason: llm.FinishStop}, usage)
}

func streamGeminiEmbedJSON(w io.Writer, isBatch bool, vectors [][]float64, promptTokens int64, hasUsage bool) error {
	type contentEmbedding struct {
		Values []float64 `json:"values"`
	}
	if !isBatch {
		if _, err := io.WriteString(w, `{"embedding":`); err != nil {
			return err
		}
		entry, err := json.Marshal(contentEmbedding{Values: vectors[0]})
		if err != nil {
			return err
		}
		if _, err := w.Write(entry); err != nil {
			return err
		}
	} else {
		if _, err := io.WriteString(w, `{"embeddings":[`); err != nil {
			return err
		}
		for i, vec := range vectors {
			if i > 0 {
				if _, err := io.WriteString(w, ","); err != nil {
					return err
				}
			}
			entry, err := json.Marshal(contentEmbedding{Values: vec})
			if err != nil {
				return err
			}
			if _, err := w.Write(entry); err != nil {
				return err
			}
		}
		if _, err := io.WriteString(w, `]`); err != nil {
			return err
		}
	}
	if hasUsage {
		var buf bytes.Buffer
		fmt.Fprintf(&buf, `,"usageMetadata":{"promptTokenCount":%d}`, promptTokens)
		if _, err := w.Write(buf.Bytes()); err != nil {
			return err
		}
	}
	_, err := io.WriteString(w, "}")
	return err
}

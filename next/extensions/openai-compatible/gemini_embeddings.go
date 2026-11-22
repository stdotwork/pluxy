package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/sdk"
)

const (
	// maxEmbeddingsReply limits what is read of one upstream embeddings reply.
	maxEmbeddingsReply = 64 << 20
	// maxEmbeddingsTotalValues bounds the total embedding float values held in memory across all
	// upstream batches of one request (~64 MiB as float64; covers 2048 x 3072 values).
	maxEmbeddingsTotalValues = 8 << 20
	// maxEmbeddingsInputs is OpenAI's documented maximum number of inputs in one /v1/embeddings call.
	maxEmbeddingsInputs = 2048
)

// embeddingBodyChunkSize is the maximum byte size of each EventNative body piece sent to core,
// keeping every protocol frame well below protocol.MaxFrame (64 MiB).
var embeddingBodyChunkSize = 1 << 20

type geminiToOpenAIEmbedItem struct {
	text       string
	dimensions *int
}

type openAIEmbedRequest struct {
	Model      string `json:"model"`
	Input      any    `json:"input"`
	Dimensions *int   `json:"dimensions,omitempty"`
}

// geminiEmbeddings serves Gemini's :embedContent and :batchEmbedContents on an OpenAI-compatible
// account:
//   - wire_api "chat" or "responses": translates to POST {base_url}/embeddings and back.
//   - wire_api "gemini": passes through to POST {base_url}/models/{model}:embedContent or
//     :batchEmbedContents with x-goog-api-key.
//   - wire_api "messages": answers retryable CodeUnsupported on ScopeCredential.
func geminiEmbeddings(ctx context.Context, call sdk.Call, out *sdk.Stream, format string) error {
	return geminiEmbeddingsTo(ctx, call, out, format)
}

func geminiEmbeddingsTo(ctx context.Context, call sdk.Call, out bodySender, format string) error {
	s, wire, key, err := account(call)
	if err != nil {
		return err
	}
	if wire.format == llm.FormatGeminiGenerate {
		return geminiEmbeddingsPassthrough(ctx, call, s, wire, key, out, format)
	}
	if err := requireOpenAIWire(wire, "embeddings"); err != nil {
		return err
	}
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

	isBatch := format == llm.FormatGeminiBatchEmbedContents
	items, err := readGeminiItemsForOpenAI(rawMap, isBatch)
	if err != nil {
		return err
	}

	// The key goes as Chat's does on both Chat and Responses wires.
	chat := wireInfo{format: llm.FormatOpenAIChat}
	all := make([][]float64, 0, len(items))
	var totalValues int
	var promptTokens int64
	var hasUsage bool

	for start := 0; start < len(items); {
		end := start + 1
		for end < len(items) && end-start < maxEmbeddingsInputs && sameOptInt(items[start].dimensions, items[end].dimensions) {
			end++
		}
		chunk := items[start:end]
		start = end

		var input any
		if !isBatch && len(chunk) == 1 {
			input = chunk[0].text
		} else {
			texts := make([]string, len(chunk))
			for i, it := range chunk {
				texts[i] = it.text
			}
			input = texts
		}
		reqBody, err := json.Marshal(openAIEmbedRequest{
			Model:      call.Request.Model,
			Input:      input,
			Dimensions: chunk[0].dimensions,
		})
		if err != nil {
			return err
		}
		resp, err := postBody(ctx, call, s, chat, key, "/embeddings", reqBody)
		if err != nil {
			return noEndpoint(err, "embeddings", "/embeddings")
		}
		raw, err := readEmbeddingReply(ctx, resp)
		if err != nil {
			return err
		}
		vecs, tokens, ok, err := decodeOpenAIEmbeddingsReply(raw, len(chunk))
		if err != nil {
			return err
		}
		for _, v := range vecs {
			totalValues += len(v)
			if totalValues > maxEmbeddingsTotalValues {
				return upstreamEmbeddingError("upstream returned more embedding values than supported")
			}
			all = append(all, v)
		}
		if ok {
			promptTokens += tokens
			hasUsage = true
		}
	}

	var usage *llm.Usage
	if hasUsage {
		n := promptTokens
		usage = &llm.Usage{InputTokens: &n}
	}
	if err := out.StartBody(format, "application/json"); err != nil {
		return err
	}
	bw := newChunkedBodyWriter(out, embeddingBodyChunkSize)
	if err := streamGeminiEmbedJSON(bw, isBatch, all, promptTokens, hasUsage); err != nil {
		return err
	}
	if err := bw.Close(); err != nil {
		return err
	}
	return out.Finish(llm.Finish{Reason: llm.FinishStop}, usage)
}

func geminiEmbeddingsPassthrough(ctx context.Context, call sdk.Call, s settings, wire wireInfo, key string, out bodySender, format string) error {
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
	model := strings.TrimPrefix(call.Request.Model, "models/")
	isBatch := format == llm.FormatGeminiBatchEmbedContents
	method := ":embedContent"
	if isBatch {
		method = ":batchEmbedContents"
	}
	reqBody, err := geminiPassthroughBody(call.Request.Native.Body, rawMap, model, isBatch)
	if err != nil {
		return err
	}
	path := "/models/" + url.PathEscape(model) + method
	resp, err := postBody(ctx, call, s, wire, key, path, reqBody)
	if err != nil {
		return noEndpoint(err, "embeddings", path)
	}
	raw, err := readEmbeddingReply(ctx, resp)
	if err != nil {
		return err
	}
	if !json.Valid(raw) {
		return upstreamEmbeddingError("upstream embedding reply is not JSON")
	}
	var usage *llm.Usage
	var meta struct {
		UsageMetadata *struct {
			PromptTokenCount *int64 `json:"promptTokenCount"`
		} `json:"usageMetadata"`
	}
	if json.Unmarshal(raw, &meta) == nil && meta.UsageMetadata != nil && meta.UsageMetadata.PromptTokenCount != nil {
		n := *meta.UsageMetadata.PromptTokenCount
		usage = &llm.Usage{InputTokens: &n}
	}
	if err := out.StartBody(format, "application/json"); err != nil {
		return err
	}
	bw := newChunkedBodyWriter(out, embeddingBodyChunkSize)
	if _, err := bw.Write(raw); err != nil {
		return err
	}
	if err := bw.Close(); err != nil {
		return err
	}
	return out.Finish(llm.Finish{Reason: llm.FinishStop}, usage)
}

func readGeminiItemsForOpenAI(rawMap map[string]json.RawMessage, isBatch bool) ([]geminiToOpenAIEmbedItem, error) {
	if !isBatch {
		it, err := parseGeminiItemForOpenAI(rawMap, "")
		if err != nil {
			return nil, err
		}
		return []geminiToOpenAIEmbedItem{it}, nil
	}
	reqsRaw, ok := rawMap["requests"]
	if !ok || len(reqsRaw) == 0 || string(reqsRaw) == "null" {
		return nil, badEmbeddingRequest("requests is required")
	}
	var rawItems []map[string]json.RawMessage
	if err := json.Unmarshal(reqsRaw, &rawItems); err != nil {
		return nil, badEmbeddingRequest("requests must be an array of request objects")
	}
	if len(rawItems) == 0 {
		return nil, badEmbeddingRequest("requests must not be empty")
	}
	if len(rawItems) > maxEmbeddingsInputs {
		return nil, badEmbeddingRequest(fmt.Sprintf("requests has %d items, at most %d are supported", len(rawItems), maxEmbeddingsInputs))
	}
	items := make([]geminiToOpenAIEmbedItem, len(rawItems))
	for i, rawItem := range rawItems {
		if rawItem == nil {
			return nil, badEmbeddingRequest(fmt.Sprintf("requests[%d] must be a JSON object", i))
		}
		it, err := parseGeminiItemForOpenAI(rawItem, fmt.Sprintf("requests[%d]", i))
		if err != nil {
			return nil, err
		}
		items[i] = it
	}
	return items, nil
}

func parseGeminiItemForOpenAI(item map[string]json.RawMessage, prefix string) (geminiToOpenAIEmbedItem, error) {
	field := func(name string) string {
		if prefix == "" {
			return name
		}
		return prefix + "." + name
	}
	var out geminiToOpenAIEmbedItem
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
				return out, unsupportedEmbeddingFeature(fmt.Sprintf("%s[%d].%s: OpenAI embeddings only accept text parts", field("content.parts"), i, k))
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
			return out, unsupportedEmbeddingFeature(field("taskType") + ": OpenAI embeddings do not support taskType")
		}
	}
	if raw, ok := pickRaw("title"); ok {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return out, badEmbeddingRequest(field("title") + " must be a string")
		}
		if s != "" {
			return out, unsupportedEmbeddingFeature(field("title") + ": OpenAI embeddings do not support title")
		}
	}
	if raw, ok := pickRaw("outputDimensionality", "output_dimensionality"); ok {
		var dim int
		if err := json.Unmarshal(raw, &dim); err != nil || dim < 1 {
			return out, badEmbeddingRequest(field("outputDimensionality") + " must be a positive integer")
		}
		out.dimensions = &dim
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

func readEmbeddingReply(ctx context.Context, resp *http.Response) ([]byte, error) {
	defer resp.Body.Close()
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		return nil, upstreamEmbeddingError("upstream streamed an embedding reply")
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

func decodeOpenAIEmbeddingsReply(raw []byte, want int) ([][]float64, int64, bool, error) {
	var resp struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
		Usage *struct {
			PromptTokens *int64 `json:"prompt_tokens"`
			TotalTokens  *int64 `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, 0, false, upstreamEmbeddingError("upstream embedding reply is not JSON")
	}
	if len(resp.Data) != want {
		return nil, 0, false, upstreamEmbeddingError(fmt.Sprintf("upstream returned %d embeddings for %d inputs", len(resp.Data), want))
	}
	ordered := make([][]float64, want)
	seen := make([]bool, want)
	for _, entry := range resp.Data {
		if entry.Index < 0 || entry.Index >= want || seen[entry.Index] {
			return nil, 0, false, upstreamEmbeddingError(fmt.Sprintf("upstream returned invalid embedding index %d", entry.Index))
		}
		if entry.Embedding == nil {
			return nil, 0, false, upstreamEmbeddingError("upstream returned an embedding without values")
		}
		seen[entry.Index] = true
		ordered[entry.Index] = entry.Embedding
	}
	var promptTokens int64
	var hasUsage bool
	if resp.Usage != nil {
		if resp.Usage.PromptTokens != nil {
			promptTokens = *resp.Usage.PromptTokens
			hasUsage = true
		} else if resp.Usage.TotalTokens != nil {
			promptTokens = *resp.Usage.TotalTokens
			hasUsage = true
		}
	}
	return ordered, promptTokens, hasUsage, nil
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

// chunkedBodyWriter sends written bytes through bodySender.Body in pieces of at most chunkSize
// bytes, preserving UTF-8 character boundaries across piece splits.
type chunkedBodyWriter struct {
	out       bodySender
	chunkSize int
	buf       []byte
}

func newChunkedBodyWriter(out bodySender, chunkSize int) *chunkedBodyWriter {
	if chunkSize <= 0 {
		chunkSize = 1 << 20
	}
	return &chunkedBodyWriter{out: out, chunkSize: chunkSize}
}

func (w *chunkedBodyWriter) Write(p []byte) (int, error) {
	written := len(p)
	for len(p) > 0 {
		space := w.chunkSize - len(w.buf)
		if space <= 0 {
			if err := w.flush(false); err != nil {
				return 0, err
			}
			space = w.chunkSize - len(w.buf)
		}
		n := min(len(p), space)
		w.buf = append(w.buf, p[:n]...)
		p = p[n:]
		if len(w.buf) >= w.chunkSize {
			if err := w.flush(false); err != nil {
				return 0, err
			}
		}
	}
	return written, nil
}

func (w *chunkedBodyWriter) Close() error {
	return w.flush(true)
}

func (w *chunkedBodyWriter) flush(all bool) error {
	if len(w.buf) == 0 {
		return nil
	}
	cut := len(w.buf)
	if !all {
		for i := len(w.buf) - 1; i >= 0 && i >= len(w.buf)-3; i-- {
			if utf8.RuneStart(w.buf[i]) {
				if !utf8.FullRune(w.buf[i:]) {
					cut = i
				}
				break
			}
		}
	}
	if cut == 0 {
		if !all {
			return nil
		}
		cut = len(w.buf)
	}
	if err := w.out.Body(string(w.buf[:cut])); err != nil {
		return err
	}
	rem := copy(w.buf, w.buf[cut:])
	w.buf = w.buf[:rem]
	return nil
}

// geminiPassthroughBody returns raw unchanged when the body's model fields already match
// models/<model> (or are omitted), and updates them only when an account alias renamed the model.
func geminiPassthroughBody(raw []byte, rawMap map[string]json.RawMessage, model string, isBatch bool) ([]byte, error) {
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

func badEmbeddingRequest(message string) *llm.Error {
	return &llm.Error{Code: llm.CodeInvalidRequest, Status: http.StatusBadRequest, Scope: llm.ScopeRequest, Message: message}
}

func unsupportedEmbeddingFeature(message string) *llm.Error {
	return &llm.Error{Code: llm.CodeUnsupported, Status: http.StatusBadRequest, Scope: llm.ScopeCredential, Retryable: true, Message: message}
}

func upstreamEmbeddingError(message string) *llm.Error {
	return &llm.Error{Code: llm.CodeUpstream, Status: http.StatusBadGateway, Scope: llm.ScopeRequest, Message: message}
}

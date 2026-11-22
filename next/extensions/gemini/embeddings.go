package main

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/sdk"
)

// maxEmbeddingsReply limits what is read of one upstream embeddings reply.
const maxEmbeddingsReply = 64 << 20

// maxEmbeddingsTotalValues bounds the total embedding float values held in memory across all
// upstream batches of one request (~64 MiB as float64; covers 2048 x 3072 = 6,291,456 values).
const maxEmbeddingsTotalValues = 8 << 20

// embeddingBodyChunkSize is the maximum byte size of each EventNative body piece sent to core,
// keeping every protocol frame well below protocol.MaxFrame (64 MiB).
var embeddingBodyChunkSize = 1 << 20

// maxGeminiEmbedBatch is Google's per-request limit on Gemini API batchEmbedContents.
const maxGeminiEmbedBatch = 100

// maxVertexPredictBatch is Google's per-request limit on Vertex AI :predict text embeddings
// for models that accept batched instances (such as text-embedding-005).
const maxVertexPredictBatch = 250

// maxEmbeddingsInputs is OpenAI's documented maximum number of inputs in one /v1/embeddings call.
const maxEmbeddingsInputs = 2048

type embeddingsRequest struct {
	inputs         []string
	dimensions     *int
	encodingFormat string // "float" or "base64"
}

type geminiEmbedRequest struct {
	Model                string          `json:"model"`
	Content              geminiEmbedBody `json:"content"`
	OutputDimensionality *int            `json:"outputDimensionality,omitempty"`
}

type geminiEmbedBody struct {
	Parts []geminiEmbedPart `json:"parts"`
}

type geminiEmbedPart struct {
	Text string `json:"text"`
}

type geminiBatchEmbedRequest struct {
	Requests []geminiEmbedRequest `json:"requests"`
}

type vertexEmbedContentRequest struct {
	Content            geminiEmbedBody          `json:"content"`
	EmbedContentConfig *vertexEmbedContentAttrs `json:"embedContentConfig,omitempty"`
}

type vertexEmbedContentAttrs struct {
	OutputDimensionality *int `json:"outputDimensionality,omitempty"`
}

type vertexEmbedRequest struct {
	Instances  []vertexEmbedInstance `json:"instances"`
	Parameters *vertexEmbedParams    `json:"parameters,omitempty"`
}

type vertexEmbedInstance struct {
	Content string `json:"content"`
}

type vertexEmbedParams struct {
	OutputDimensionality *int `json:"outputDimensionality,omitempty"`
}

type openAIEmbeddingData struct {
	Object    string `json:"object"`
	Index     int    `json:"index"`
	Embedding any    `json:"embedding"`
}

type openAIEmbeddingUsage struct {
	PromptTokens int64 `json:"prompt_tokens"`
	TotalTokens  int64 `json:"total_tokens"`
}

// isVertexEmbedContentModel reports whether a Vertex AI model uses :embedContent rather than
// :predict (Google's Vertex AI multimodal embeddings docs use :embedContent for gemini-embedding-2).
func isVertexEmbedContentModel(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	return m == "gemini-embedding-2" || strings.HasPrefix(m, "gemini-embedding-2-")
}

// vertexPredictBatchSize returns the per-request instance limit for a Vertex AI :predict model.
// Google's Vertex AI text embeddings reference states: "For gemini-embedding-001, each request
// can only include a single input text." Other Vertex text embedding models accept up to 250.
func vertexPredictBatchSize(model string) int {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "gemini-embedding-001" || strings.HasPrefix(m, "gemini-embedding-001-") {
		return 1
	}
	return maxVertexPredictBatch
}

// embeddings serves OpenAI's POST /v1/embeddings on a Gemini account:
//   - Gemini API: :embedContent for one input, or :batchEmbedContents in batches of at most 100.
//   - Vertex AI: :embedContent (one input per request) for gemini-embedding-2*, :predict with one
//     input per request for gemini-embedding-001, and :predict in batches of up to 250 for other
//     text embedding models.
func embeddings(ctx context.Context, call sdk.Call, out *sdk.Stream) error {
	return embeddingsTo(ctx, call, out)
}

func embeddingsTo(ctx context.Context, call sdk.Call, out sender) (err error) {
	req, err := readEmbeddingsRequest(call.Request)
	if err != nil {
		return err
	}
	s, err := readSettings(call.Account.Settings)
	if err != nil {
		return err
	}
	model := strings.TrimPrefix(call.Request.Model, "models/")
	method := ":embedContent"
	if s.Vertex != nil {
		if isVertexEmbedContentModel(model) {
			method = ":embedContent"
		} else {
			method = ":predict"
		}
	} else if len(req.inputs) > 1 {
		method = ":batchEmbedContents"
	}
	t, err := resolveMethod(ctx, s, call.Account.Credential, model, method)
	if err != nil {
		return err
	}
	send := &reporting{out: out}
	if t.token != "" {
		// Core redacts what the credential holds; the fetched access token is redacted here too.
		send.redact = protocolRedactor(t.token)
		defer func() {
			var e *llm.Error
			if errors.As(err, &e) {
				err = send.redact.Event(llm.Event{Type: llm.EventError, Error: e}).Error
			}
		}()
	}

	var (
		vectors      [][]float64
		promptTokens int64
		hasUsage     bool
	)
	switch {
	case s.Vertex != nil && isVertexEmbedContentModel(model):
		vectors, promptTokens, hasUsage, err = runVertexEmbedContent(ctx, t, model, req)
	case s.Vertex != nil:
		vectors, promptTokens, hasUsage, err = runVertexPredictEmbeddings(ctx, t, model, req)
	case len(req.inputs) == 1:
		vectors, promptTokens, hasUsage, err = runGeminiSingleEmbedding(ctx, t, model, req)
	default:
		vectors, promptTokens, hasUsage, err = runGeminiBatchEmbeddings(ctx, t, model, req)
	}
	if err != nil {
		return err
	}

	replyModel := call.Request.Model
	if replyModel == "" {
		replyModel = model
	}
	var usage *llm.Usage
	if hasUsage {
		tokens := promptTokens
		usage = &llm.Usage{InputTokens: &tokens}
	}
	if err := send.StartBody(llm.FormatOpenAIEmbeddings, "application/json"); err != nil {
		return err
	}
	bw := newChunkedBodyWriter(send, embeddingBodyChunkSize)
	if err := writeEmbeddingsReply(bw, replyModel, vectors, req.encodingFormat, promptTokens); err != nil {
		return err
	}
	if err := bw.Close(); err != nil {
		return err
	}
	return send.Finish(llm.Finish{Reason: llm.FinishStop}, usage)
}

// writeEmbeddingsReply streams the OpenAI list response into w one embedding entry at a time so
// large replies (up to 2048 x 3072 floats) are never serialized into one contiguous buffer.
func writeEmbeddingsReply(w io.Writer, model string, vectors [][]float64, encodingFormat string, promptTokens int64) error {
	if _, err := io.WriteString(w, `{"object":"list","data":[`); err != nil {
		return err
	}
	for i, values := range vectors {
		if i > 0 {
			if _, err := io.WriteString(w, ","); err != nil {
				return err
			}
		}
		var encoded any = values
		if encodingFormat == "base64" {
			encoded = encodeFloat32Base64(values)
		}
		entry, err := json.Marshal(openAIEmbeddingData{
			Object:    "embedding",
			Index:     i,
			Embedding: encoded,
		})
		if err != nil {
			return err
		}
		if _, err := w.Write(entry); err != nil {
			return err
		}
	}
	modelJSON, err := json.Marshal(model)
	if err != nil {
		return err
	}
	usageJSON, err := json.Marshal(openAIEmbeddingUsage{
		PromptTokens: promptTokens,
		TotalTokens:  promptTokens,
	})
	if err != nil {
		return err
	}
	if _, err := io.WriteString(w, `],"model":`); err != nil {
		return err
	}
	if _, err := w.Write(modelJSON); err != nil {
		return err
	}
	if _, err := io.WriteString(w, `,"usage":`); err != nil {
		return err
	}
	if _, err := w.Write(usageJSON); err != nil {
		return err
	}
	_, err = io.WriteString(w, "}")
	return err
}

// chunkedBodyWriter sends written bytes through reporting.Body in pieces of at most chunkSize
// bytes, preserving UTF-8 character boundaries across piece splits.
type chunkedBodyWriter struct {
	send      *reporting
	chunkSize int
	buf       []byte
}

func newChunkedBodyWriter(send *reporting, chunkSize int) *chunkedBodyWriter {
	if chunkSize <= 0 {
		chunkSize = 1 << 20
	}
	return &chunkedBodyWriter{send: send, chunkSize: chunkSize}
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
		// Hold back an incomplete trailing UTF-8 rune (at most 3 bytes), as sdk.BodyWriter does.
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
	if err := w.send.Body(string(w.buf[:cut])); err != nil {
		return err
	}
	rem := copy(w.buf, w.buf[cut:])
	w.buf = w.buf[:rem]
	return nil
}

func runGeminiSingleEmbedding(ctx context.Context, t target, model string, req embeddingsRequest) ([][]float64, int64, bool, error) {
	payload := geminiEmbedRequest{
		Model:                "models/" + model,
		Content:              geminiEmbedBody{Parts: []geminiEmbedPart{{Text: req.inputs[0]}}},
		OutputDimensionality: req.dimensions,
	}
	raw, err := postEmbeddingJSON(ctx, t, model, payload)
	if err != nil {
		return nil, 0, false, err
	}
	vec, promptTokens, hasUsage, err := decodeEmbedContentReply(raw, "Gemini")
	if err != nil {
		return nil, 0, false, err
	}
	if len(vec) > maxEmbeddingsTotalValues {
		return nil, 0, false, upstreamEmbeddingError("Gemini returned more embedding values than supported")
	}
	return [][]float64{vec}, promptTokens, hasUsage, nil
}

func runGeminiBatchEmbeddings(ctx context.Context, t target, model string, req embeddingsRequest) ([][]float64, int64, bool, error) {
	all := make([][]float64, 0, len(req.inputs))
	var totalValues int
	var promptTokens int64
	var hasUsage bool
	modelName := "models/" + model

	for start := 0; start < len(req.inputs); start += maxGeminiEmbedBatch {
		end := min(start+maxGeminiEmbedBatch, len(req.inputs))
		chunk := req.inputs[start:end]
		reqs := make([]geminiEmbedRequest, len(chunk))
		for i, text := range chunk {
			reqs[i] = geminiEmbedRequest{
				Model:                modelName,
				Content:              geminiEmbedBody{Parts: []geminiEmbedPart{{Text: text}}},
				OutputDimensionality: req.dimensions,
			}
		}
		raw, err := postEmbeddingJSON(ctx, t, model, geminiBatchEmbedRequest{Requests: reqs})
		if err != nil {
			return nil, 0, false, err
		}
		var resp struct {
			Embeddings []struct {
				Values []float64 `json:"values"`
			} `json:"embeddings"`
			UsageMetadata *struct {
				PromptTokenCount *int64 `json:"promptTokenCount"`
			} `json:"usageMetadata"`
		}
		if err := json.Unmarshal(raw, &resp); err != nil {
			return nil, 0, false, upstreamEmbeddingError("Gemini's batch embedding reply is not JSON")
		}
		if len(resp.Embeddings) != len(chunk) {
			return nil, 0, false, upstreamEmbeddingError(fmt.Sprintf("Gemini returned %d embeddings for %d inputs", len(resp.Embeddings), len(chunk)))
		}
		for _, emb := range resp.Embeddings {
			if emb.Values == nil {
				return nil, 0, false, upstreamEmbeddingError("Gemini returned an embedding without values")
			}
			totalValues += len(emb.Values)
			if totalValues > maxEmbeddingsTotalValues {
				return nil, 0, false, upstreamEmbeddingError("Gemini returned more embedding values than supported")
			}
			all = append(all, emb.Values)
		}
		if resp.UsageMetadata != nil && resp.UsageMetadata.PromptTokenCount != nil {
			promptTokens += *resp.UsageMetadata.PromptTokenCount
			hasUsage = true
		}
	}
	return all, promptTokens, hasUsage, nil
}

func runVertexEmbedContent(ctx context.Context, t target, model string, req embeddingsRequest) ([][]float64, int64, bool, error) {
	all := make([][]float64, 0, len(req.inputs))
	var totalValues int
	var promptTokens int64
	var hasUsage bool

	var cfg *vertexEmbedContentAttrs
	if req.dimensions != nil {
		cfg = &vertexEmbedContentAttrs{OutputDimensionality: req.dimensions}
	}

	for _, text := range req.inputs {
		payload := vertexEmbedContentRequest{
			Content:            geminiEmbedBody{Parts: []geminiEmbedPart{{Text: text}}},
			EmbedContentConfig: cfg,
		}
		raw, err := postEmbeddingJSON(ctx, t, model, payload)
		if err != nil {
			return nil, 0, false, err
		}
		vec, tokens, ok, err := decodeEmbedContentReply(raw, "Vertex AI")
		if err != nil {
			return nil, 0, false, err
		}
		totalValues += len(vec)
		if totalValues > maxEmbeddingsTotalValues {
			return nil, 0, false, upstreamEmbeddingError("Vertex AI returned more embedding values than supported")
		}
		all = append(all, vec)
		if ok {
			promptTokens += tokens
			hasUsage = true
		}
	}
	return all, promptTokens, hasUsage, nil
}

func runVertexPredictEmbeddings(ctx context.Context, t target, model string, req embeddingsRequest) ([][]float64, int64, bool, error) {
	all := make([][]float64, 0, len(req.inputs))
	var totalValues int
	var promptTokens int64
	var hasUsage bool

	var params *vertexEmbedParams
	if req.dimensions != nil {
		params = &vertexEmbedParams{OutputDimensionality: req.dimensions}
	}
	batchSize := vertexPredictBatchSize(model)

	for start := 0; start < len(req.inputs); start += batchSize {
		end := min(start+batchSize, len(req.inputs))
		chunk := req.inputs[start:end]
		instances := make([]vertexEmbedInstance, len(chunk))
		for i, text := range chunk {
			instances[i] = vertexEmbedInstance{Content: text}
		}
		raw, err := postEmbeddingJSON(ctx, t, model, vertexEmbedRequest{Instances: instances, Parameters: params})
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

func decodeEmbedContentReply(raw []byte, vendor string) ([]float64, int64, bool, error) {
	var resp struct {
		Embedding *struct {
			Values []float64 `json:"values"`
		} `json:"embedding"`
		UsageMetadata *struct {
			PromptTokenCount *int64 `json:"promptTokenCount"`
		} `json:"usageMetadata"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, 0, false, upstreamEmbeddingError(vendor + "'s embedding reply is not JSON")
	}
	if resp.Embedding == nil || resp.Embedding.Values == nil {
		return nil, 0, false, upstreamEmbeddingError(vendor + " returned no embedding values")
	}
	var promptTokens int64
	var hasUsage bool
	if resp.UsageMetadata != nil && resp.UsageMetadata.PromptTokenCount != nil {
		promptTokens = *resp.UsageMetadata.PromptTokenCount
		hasUsage = true
	}
	return resp.Embedding.Values, promptTokens, hasUsage, nil
}

func postEmbeddingJSON(ctx context.Context, t target, model string, payload any) ([]byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
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

func upstreamEmbeddingError(message string) *llm.Error {
	return &llm.Error{Code: llm.CodeUpstream, Status: http.StatusBadGateway, Scope: llm.ScopeRequest, Message: message}
}

// readEmbeddingsRequest validates an OpenAI /v1/embeddings body and extracts what Gemini takes.
func readEmbeddingsRequest(r llm.Request) (embeddingsRequest, error) {
	var out embeddingsRequest
	if r.Options.ThinkingSuffix != "" {
		return out, badEmbeddingRequest("a thinking suffix on the model name is not supported for embeddings")
	}
	if r.Native == nil {
		return out, badEmbeddingRequest("the request body is missing")
	}
	var body map[string]json.RawMessage
	if json.Unmarshal(r.Native.Body, &body) != nil || body == nil {
		return out, badEmbeddingRequest("the request body is not a JSON object")
	}
	if string(body["stream"]) == "true" || r.Stream {
		return out, badEmbeddingRequest("embeddings do not support streaming")
	}
	inputs, err := readEmbeddingInputs(body["input"])
	if err != nil {
		return out, err
	}
	if len(inputs) > maxEmbeddingsInputs {
		return out, badEmbeddingRequest(fmt.Sprintf("input has %d items, at most %d are supported", len(inputs), maxEmbeddingsInputs))
	}
	out.inputs = inputs

	if raw, ok := body["dimensions"]; ok && string(raw) != "null" {
		var dim int
		if err := json.Unmarshal(raw, &dim); err != nil || dim < 1 {
			return out, badEmbeddingRequest("dimensions must be a positive integer")
		}
		out.dimensions = &dim
	}

	out.encodingFormat = "float"
	if raw, ok := body["encoding_format"]; ok && string(raw) != "null" {
		var format string
		if err := json.Unmarshal(raw, &format); err != nil {
			return out, badEmbeddingRequest(`encoding_format must be "float" or "base64"`)
		}
		switch format {
		case "float", "base64":
			out.encodingFormat = format
		default:
			return out, badEmbeddingRequest(fmt.Sprintf("encoding_format %q is not supported; use \"float\" or \"base64\"", format))
		}
	}
	return out, nil
}

// readEmbeddingInputs parses OpenAI's input field (a string or list of strings) and refuses
// token-ID arrays with an error naming the input field.
func readEmbeddingInputs(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, badEmbeddingRequest("input is required")
	}
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		return []string{single}, nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, badEmbeddingRequest("input must be a string or an array of strings")
	}
	if len(items) == 0 {
		return nil, badEmbeddingRequest("input must not be empty")
	}
	if isTokenArray(items) {
		return nil, unsupportedEmbeddingInput("input: Gemini embeddings do not accept token-ID arrays; pass text strings instead")
	}
	out := make([]string, len(items))
	for i, item := range items {
		var s string
		if err := json.Unmarshal(item, &s); err != nil {
			return nil, badEmbeddingRequest("input must be a string or an array of strings")
		}
		out[i] = s
	}
	return out, nil
}

// isTokenArray reports whether items is OpenAI's integer token-ID array or array of non-empty
// integer token-ID arrays. Mixed or malformed arrays return false so they stay invalid requests.
func isTokenArray(items []json.RawMessage) bool {
	allInt := true
	for _, item := range items {
		var n int64
		if json.Unmarshal(item, &n) != nil {
			allInt = false
			break
		}
	}
	if allInt {
		return true
	}
	for _, item := range items {
		var nested []int64
		if json.Unmarshal(item, &nested) != nil || len(nested) == 0 {
			return false
		}
	}
	return true
}

// encodeFloat32Base64 encodes an embedding vector as IEEE 754 little-endian float32 bytes in
// standard base64, matching OpenAI's base64 encoding_format.
func encodeFloat32Base64(values []float64) string {
	buf := make([]byte, len(values)*4)
	for i, v := range values {
		binary.LittleEndian.PutUint32(buf[i*4:(i+1)*4], math.Float32bits(float32(v)))
	}
	return base64.StdEncoding.EncodeToString(buf)
}

func badEmbeddingRequest(message string) *llm.Error {
	return &llm.Error{Code: llm.CodeInvalidRequest, Status: http.StatusBadRequest, Scope: llm.ScopeRequest, Message: message}
}

func unsupportedEmbeddingInput(message string) *llm.Error {
	return &llm.Error{Code: llm.CodeUnsupported, Status: http.StatusBadRequest, Scope: llm.ScopeCredential, Retryable: true, Message: message}
}

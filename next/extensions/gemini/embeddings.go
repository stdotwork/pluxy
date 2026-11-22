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

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/sdk"
)

// maxEmbeddingsReply limits what is read of one upstream embeddings reply.
const maxEmbeddingsReply = 64 << 20

// maxGeminiEmbedBatch is Google's per-request limit on batchEmbedContents.
const maxGeminiEmbedBatch = 100

// maxVertexEmbedBatch is Vertex AI's per-request limit on :predict text embeddings.
const maxVertexEmbedBatch = 250

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

type openAIEmbeddingResponse struct {
	Object string                `json:"object"`
	Data   []openAIEmbeddingData `json:"data"`
	Model  string                `json:"model"`
	Usage  openAIEmbeddingUsage  `json:"usage"`
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

// embeddings serves OpenAI's POST /v1/embeddings on a Gemini account:
//   - Gemini API: :embedContent for one input, or :batchEmbedContents in batches of at most 100.
//   - Vertex AI: :predict in batches of at most 250.
func embeddings(ctx context.Context, call sdk.Call, out *sdk.Stream) (err error) {
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
		method = ":predict"
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
	if s.Vertex != nil {
		vectors, promptTokens, hasUsage, err = runVertexEmbeddings(ctx, t, model, req)
	} else if len(req.inputs) == 1 {
		vectors, promptTokens, hasUsage, err = runGeminiSingleEmbedding(ctx, t, model, req)
	} else {
		vectors, promptTokens, hasUsage, err = runGeminiBatchEmbeddings(ctx, t, model, req)
	}
	if err != nil {
		return err
	}

	data := make([]openAIEmbeddingData, len(vectors))
	for i, values := range vectors {
		var encoded any = values
		if req.encodingFormat == "base64" {
			encoded = encodeFloat32Base64(values)
		}
		data[i] = openAIEmbeddingData{
			Object:    "embedding",
			Index:     i,
			Embedding: encoded,
		}
	}
	replyModel := call.Request.Model
	if replyModel == "" {
		replyModel = model
	}
	reply, err := json.Marshal(openAIEmbeddingResponse{
		Object: "list",
		Data:   data,
		Model:  replyModel,
		Usage: openAIEmbeddingUsage{
			PromptTokens: promptTokens,
			TotalTokens:  promptTokens,
		},
	})
	if err != nil {
		return err
	}
	var usage *llm.Usage
	if hasUsage {
		tokens := promptTokens
		usage = &llm.Usage{InputTokens: &tokens}
	}
	if err := send.StartBody(llm.FormatOpenAIEmbeddings, "application/json"); err != nil {
		return err
	}
	if err := send.Body(string(reply)); err != nil {
		return err
	}
	return send.Finish(llm.Finish{Reason: llm.FinishStop}, usage)
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
	var resp struct {
		Embedding *struct {
			Values []float64 `json:"values"`
		} `json:"embedding"`
		UsageMetadata *struct {
			PromptTokenCount *int64 `json:"promptTokenCount"`
		} `json:"usageMetadata"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, 0, false, &llm.Error{Code: llm.CodeUpstream, Status: http.StatusBadGateway, Scope: llm.ScopeEndpoint, Message: "Gemini's embedding reply is not JSON"}
	}
	if resp.Embedding == nil || resp.Embedding.Values == nil {
		return nil, 0, false, &llm.Error{Code: llm.CodeUpstream, Status: http.StatusBadGateway, Scope: llm.ScopeEndpoint, Message: "Gemini returned no embedding values"}
	}
	var promptTokens int64
	var hasUsage bool
	if resp.UsageMetadata != nil && resp.UsageMetadata.PromptTokenCount != nil {
		promptTokens = *resp.UsageMetadata.PromptTokenCount
		hasUsage = true
	}
	return [][]float64{resp.Embedding.Values}, promptTokens, hasUsage, nil
}

func runGeminiBatchEmbeddings(ctx context.Context, t target, model string, req embeddingsRequest) ([][]float64, int64, bool, error) {
	all := make([][]float64, 0, len(req.inputs))
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
			return nil, 0, false, &llm.Error{Code: llm.CodeUpstream, Status: http.StatusBadGateway, Scope: llm.ScopeEndpoint, Message: "Gemini's batch embedding reply is not JSON"}
		}
		if len(resp.Embeddings) != len(chunk) {
			return nil, 0, false, &llm.Error{
				Code:    llm.CodeUpstream,
				Status:  http.StatusBadGateway,
				Scope:   llm.ScopeEndpoint,
				Message: fmt.Sprintf("Gemini returned %d embeddings for %d inputs", len(resp.Embeddings), len(chunk)),
			}
		}
		for _, emb := range resp.Embeddings {
			if emb.Values == nil {
				return nil, 0, false, &llm.Error{Code: llm.CodeUpstream, Status: http.StatusBadGateway, Scope: llm.ScopeEndpoint, Message: "Gemini returned an embedding without values"}
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

func runVertexEmbeddings(ctx context.Context, t target, model string, req embeddingsRequest) ([][]float64, int64, bool, error) {
	all := make([][]float64, 0, len(req.inputs))
	var promptTokens int64
	var hasUsage bool

	var params *vertexEmbedParams
	if req.dimensions != nil {
		params = &vertexEmbedParams{OutputDimensionality: req.dimensions}
	}

	for start := 0; start < len(req.inputs); start += maxVertexEmbedBatch {
		end := min(start+maxVertexEmbedBatch, len(req.inputs))
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
			return nil, 0, false, &llm.Error{Code: llm.CodeUpstream, Status: http.StatusBadGateway, Scope: llm.ScopeEndpoint, Message: "Vertex AI's embedding reply is not JSON"}
		}
		if len(resp.Predictions) != len(chunk) {
			return nil, 0, false, &llm.Error{
				Code:    llm.CodeUpstream,
				Status:  http.StatusBadGateway,
				Scope:   llm.ScopeEndpoint,
				Message: fmt.Sprintf("Vertex AI returned %d embeddings for %d inputs", len(resp.Predictions), len(chunk)),
			}
		}
		for _, pred := range resp.Predictions {
			if pred.Embeddings == nil || pred.Embeddings.Values == nil {
				return nil, 0, false, &llm.Error{Code: llm.CodeUpstream, Status: http.StatusBadGateway, Scope: llm.ScopeEndpoint, Message: "Vertex AI returned a prediction without embedding values"}
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
		return nil, &llm.Error{Code: llm.CodeUpstream, Status: http.StatusBadGateway, Scope: llm.ScopeEndpoint, Message: "Gemini streamed an embedding reply"}
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxEmbeddingsReply+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &llm.Error{Code: llm.CodeUpstream, Scope: llm.ScopeEndpoint, Message: "reading upstream response: " + err.Error()}
	}
	if len(raw) > maxEmbeddingsReply {
		return nil, &llm.Error{Code: llm.CodeUpstream, Scope: llm.ScopeEndpoint, Message: fmt.Sprintf("the upstream's reply is larger than %d MiB", maxEmbeddingsReply>>20)}
	}
	return raw, nil
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
		return nil, badEmbeddingRequest("input: Gemini embeddings do not accept token-ID arrays; pass text strings instead")
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

// isTokenArray reports whether items looks like OpenAI's integer token-ID array or array of
// integer token-ID arrays.
func isTokenArray(items []json.RawMessage) bool {
	var n int64
	if json.Unmarshal(items[0], &n) == nil {
		return true
	}
	var nested []int64
	return json.Unmarshal(items[0], &nested) == nil
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

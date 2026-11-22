package main

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/conformance"
	"github.com/stdotwork/pluxy/next/extensions/gemini/geminitest"
	"github.com/stdotwork/pluxy/next/internal/host"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

// embeddingTestExtension builds the extension and returns a Gemini API account pointed at srv.
func embeddingTestExtension(t *testing.T) (*host.Extension, *geminitest.Server, protocol.Account) {
	t.Helper()
	srv := geminitest.New(t)
	ext, err := host.Start(conformance.BuildExtension(t, "pluxy-extension.json", "."), host.Options{Log: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ext.Stop)
	settings, _ := json.Marshal(map[string]any{"base_url": srv.URL})
	return ext, srv, protocol.Account{
		ID:         "gemini-embed",
		Settings:   settings,
		Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: srv.IssueKey()},
	}
}

// embeddingEvents runs one OpenAI Embeddings endpoint call and returns its events.
func embeddingEvents(t *testing.T, ext *host.Extension, acct protocol.Account, req llm.Request) []llm.Event {
	t.Helper()
	if req.Model == "" {
		req.Model = "gemini-embedding-001"
	}
	s := ext.Endpoint(context.Background(), acct, req)
	defer s.Close()
	var events []llm.Event
	for {
		ev := s.Next()
		events = append(events, ev)
		if ev.Terminal() {
			return events
		}
	}
}

type openAIEmbeddingsReply struct {
	Object string `json:"object"`
	Model  string `json:"model"`
	Data   []struct {
		Object    string          `json:"object"`
		Index     int             `json:"index"`
		Embedding json.RawMessage `json:"embedding"`
	} `json:"data"`
	Usage struct {
		PromptTokens int64 `json:"prompt_tokens"`
		TotalTokens  int64 `json:"total_tokens"`
	} `json:"usage"`
}

// embeddingsReply unmarshals a successful Embeddings API endpoint reply.
func embeddingsReply(t *testing.T, events []llm.Event) openAIEmbeddingsReply {
	t.Helper()
	if len(events) < 3 || events[0].Type != llm.EventResponseStart || events[len(events)-1].Type != llm.EventFinish {
		t.Fatalf("events = %+v, want response.start, body and finish", events)
	}
	if events[0].Response == nil || events[0].Response.Format != llm.FormatOpenAIEmbeddings {
		t.Fatalf("response.start = %+v", events[0].Response)
	}
	var start llm.EndpointReply
	if err := json.Unmarshal(events[0].Response.Raw, &start); err != nil || start.ContentType != "application/json" {
		t.Fatalf("endpoint reply = %+v, err %v", start, err)
	}
	var body string
	for _, ev := range events[1 : len(events)-1] {
		if ev.Type != llm.EventNative {
			t.Fatalf("event = %+v, want a native body piece", ev)
		}
		var part string
		if err := json.Unmarshal(ev.Raw, &part); err != nil {
			t.Fatal(err)
		}
		body += part
	}
	var reply openAIEmbeddingsReply
	if err := json.Unmarshal([]byte(body), &reply); err != nil {
		t.Fatalf("reply %q is not JSON: %v", body, err)
	}
	return reply
}

// A single string input calls :embedContent with model, text part and outputDimensionality, and
// returns an OpenAI list response with index 0 and usage from usageMetadata.
func TestGeminiEmbeddingsSingle(t *testing.T) {
	ext, srv, acct := embeddingTestExtension(t)
	srv.Generate = func(w http.ResponseWriter, r *http.Request, req geminitest.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"embedding":{"values":[0.25,-0.5,1.0]},"usageMetadata":{"promptTokenCount":6}}`)
	}

	events := embeddingEvents(t, ext, acct, llm.Request{
		Model: "gemini-embedding-001",
		Native: &llm.Native{
			Format: llm.FormatOpenAIEmbeddings,
			Body:   json.RawMessage(`{"model":"gemini-embedding-001","input":"hello world","dimensions":3}`),
		},
	})
	reply := embeddingsReply(t, events)
	if reply.Object != "list" || reply.Model != "gemini-embedding-001" {
		t.Fatalf("reply header = %+v", reply)
	}
	if len(reply.Data) != 1 || reply.Data[0].Object != "embedding" || reply.Data[0].Index != 0 {
		t.Fatalf("reply data = %+v", reply.Data)
	}
	var vec []float64
	if err := json.Unmarshal(reply.Data[0].Embedding, &vec); err != nil {
		t.Fatalf("embedding = %s: %v", reply.Data[0].Embedding, err)
	}
	if !reflect.DeepEqual(vec, []float64{0.25, -0.5, 1.0}) {
		t.Errorf("vec = %v, want [0.25 -0.5 1]", vec)
	}
	if reply.Usage.PromptTokens != 6 || reply.Usage.TotalTokens != 6 {
		t.Errorf("reply usage = %+v, want 6/6", reply.Usage)
	}
	finish := events[len(events)-1]
	if finish.Usage == nil || finish.Usage.InputTokens == nil || *finish.Usage.InputTokens != 6 {
		t.Errorf("finish usage = %+v, want InputTokens=6", finish.Usage)
	}

	reqs := srv.Requests()
	if len(reqs) != 1 {
		t.Fatalf("upstream got %d requests, want 1", len(reqs))
	}
	if reqs[0].Method != "embedContent" || reqs[0].Model != "gemini-embedding-001" {
		t.Fatalf("upstream request = %s:%s", reqs[0].Model, reqs[0].Method)
	}
	var sent struct {
		Model   string `json:"model"`
		Content struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"content"`
		OutputDimensionality *int `json:"outputDimensionality"`
	}
	if err := json.Unmarshal(reqs[0].Body, &sent); err != nil {
		t.Fatal(err)
	}
	if sent.Model != "models/gemini-embedding-001" || len(sent.Content.Parts) != 1 || sent.Content.Parts[0].Text != "hello world" {
		t.Errorf("sent = %+v", sent)
	}
	if sent.OutputDimensionality == nil || *sent.OutputDimensionality != 3 {
		t.Errorf("outputDimensionality = %v, want 3", sent.OutputDimensionality)
	}
}

// A list of strings calls :batchEmbedContents and preserves exact input order and index values.
func TestGeminiEmbeddingsBatch(t *testing.T) {
	ext, srv, acct := embeddingTestExtension(t)
	srv.Generate = func(w http.ResponseWriter, r *http.Request, req geminitest.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"embeddings":[{"values":[1.0,2.0]},{"values":[3.0,4.0]},{"values":[5.0,6.0]}],"usageMetadata":{"promptTokenCount":9}}`)
	}

	events := embeddingEvents(t, ext, acct, llm.Request{
		Model: "gemini-embedding-001",
		Native: &llm.Native{
			Format: llm.FormatOpenAIEmbeddings,
			Body:   json.RawMessage(`{"model":"gemini-embedding-001","input":["first","second","third"],"dimensions":2,"encoding_format":"float"}`),
		},
	})
	reply := embeddingsReply(t, events)
	if len(reply.Data) != 3 {
		t.Fatalf("len(data) = %d, want 3", len(reply.Data))
	}
	wantVecs := [][]float64{{1, 2}, {3, 4}, {5, 6}}
	for i, want := range wantVecs {
		if reply.Data[i].Object != "embedding" || reply.Data[i].Index != i {
			t.Errorf("data[%d] = %+v, want index %d", i, reply.Data[i], i)
		}
		var got []float64
		if err := json.Unmarshal(reply.Data[i].Embedding, &got); err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("data[%d].embedding = %v (err %v), want %v", i, got, err, want)
		}
	}
	if reply.Usage.PromptTokens != 9 || reply.Usage.TotalTokens != 9 {
		t.Errorf("usage = %+v, want 9/9", reply.Usage)
	}

	reqs := srv.Requests()
	if len(reqs) != 1 || reqs[0].Method != "batchEmbedContents" {
		t.Fatalf("upstream requests = %+v, want one batchEmbedContents", reqs)
	}
	var sent struct {
		Requests []struct {
			Model   string `json:"model"`
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
			OutputDimensionality *int `json:"outputDimensionality"`
		} `json:"requests"`
	}
	if err := json.Unmarshal(reqs[0].Body, &sent); err != nil {
		t.Fatal(err)
	}
	if len(sent.Requests) != 3 {
		t.Fatalf("sent %d requests, want 3", len(sent.Requests))
	}
	for i, text := range []string{"first", "second", "third"} {
		r := sent.Requests[i]
		if r.Model != "models/gemini-embedding-001" || len(r.Content.Parts) != 1 || r.Content.Parts[0].Text != text ||
			r.OutputDimensionality == nil || *r.OutputDimensionality != 2 {
			t.Errorf("requests[%d] = %+v", i, r)
		}
	}
}

// encoding_format "base64" returns IEEE 754 little-endian float32 vectors encoded as base64 strings,
// never silently returning float arrays.
func TestGeminiEmbeddingsBase64(t *testing.T) {
	ext, srv, acct := embeddingTestExtension(t)
	srv.Generate = func(w http.ResponseWriter, r *http.Request, req geminitest.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"embedding":{"values":[0.5,-1.25,2.0]}}`)
	}

	events := embeddingEvents(t, ext, acct, llm.Request{
		Model: "gemini-embedding-001",
		Native: &llm.Native{
			Format: llm.FormatOpenAIEmbeddings,
			Body:   json.RawMessage(`{"model":"gemini-embedding-001","input":"encode me","encoding_format":"base64"}`),
		},
	})
	reply := embeddingsReply(t, events)
	if len(reply.Data) != 1 {
		t.Fatalf("len(data) = %d, want 1", len(reply.Data))
	}
	var b64 string
	if err := json.Unmarshal(reply.Data[0].Embedding, &b64); err != nil {
		t.Fatalf("embedding = %s, want a base64 JSON string: %v", reply.Data[0].Embedding, err)
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatalf("invalid base64 %q: %v", b64, err)
	}
	if len(raw) != 12 {
		t.Fatalf("decoded length = %d bytes, want 12 (3 float32s)", len(raw))
	}
	got := make([]float32, 3)
	for i := range got {
		got[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[i*4 : (i+1)*4]))
	}
	want := []float32{0.5, -1.25, 2.0}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("decoded float32s = %v, want %v", got, want)
	}
	// Upstream omitted usageMetadata: OpenAI reply still has 0/0, finish.Usage stays nil.
	if reply.Usage.PromptTokens != 0 || reply.Usage.TotalTokens != 0 {
		t.Errorf("reply usage = %+v, want 0/0", reply.Usage)
	}
	if finish := events[len(events)-1]; finish.Usage != nil {
		t.Errorf("finish.Usage = %+v, want nil when upstream reports no usage", finish.Usage)
	}
}

// More than 100 inputs are split across sequential :batchEmbedContents calls of at most 100 items
// each, preserving order, index and total usage.
func TestGeminiEmbeddingsBatchLimit100(t *testing.T) {
	ext, srv, acct := embeddingTestExtension(t)
	srv.Generate = func(w http.ResponseWriter, r *http.Request, req geminitest.Request) {
		var body struct {
			Requests []struct {
				Content struct {
					Parts []struct {
						Text string `json:"text"`
					} `json:"parts"`
				} `json:"content"`
			} `json:"requests"`
		}
		if err := json.Unmarshal(req.Body, &body); err != nil {
			t.Errorf("decode batch body: %v", err)
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		if len(body.Requests) > 100 {
			t.Errorf("batch had %d requests, exceeds 100 limit", len(body.Requests))
			http.Error(w, "too many", http.StatusBadRequest)
			return
		}
		type emb struct {
			Values []float64 `json:"values"`
		}
		out := make([]emb, len(body.Requests))
		for i, item := range body.Requests {
			var idx float64
			_, _ = fmt.Sscanf(item.Content.Parts[0].Text, "item-%f", &idx)
			out[i] = emb{Values: []float64{idx}}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"embeddings":    out,
			"usageMetadata": map[string]int{"promptTokenCount": len(body.Requests) * 2},
		})
	}

	inputs := make([]string, 105)
	for i := range inputs {
		inputs[i] = fmt.Sprintf("item-%d", i)
	}
	body, _ := json.Marshal(map[string]any{
		"model": "gemini-embedding-001",
		"input": inputs,
	})
	events := embeddingEvents(t, ext, acct, llm.Request{
		Model:  "gemini-embedding-001",
		Native: &llm.Native{Format: llm.FormatOpenAIEmbeddings, Body: body},
	})
	reply := embeddingsReply(t, events)
	if len(reply.Data) != 105 {
		t.Fatalf("len(data) = %d, want 105", len(reply.Data))
	}
	for i := range 105 {
		if reply.Data[i].Index != i {
			t.Fatalf("data[%d].index = %d, want %d", i, reply.Data[i].Index, i)
		}
		var vec []float64
		if err := json.Unmarshal(reply.Data[i].Embedding, &vec); err != nil || len(vec) != 1 || vec[0] != float64(i) {
			t.Fatalf("data[%d].embedding = %s, want [%d]", i, reply.Data[i].Embedding, i)
		}
	}
	if reply.Usage.PromptTokens != 210 || reply.Usage.TotalTokens != 210 {
		t.Errorf("usage = %+v, want 210/210", reply.Usage)
	}
	finish := events[len(events)-1]
	if finish.Usage == nil || finish.Usage.InputTokens == nil || *finish.Usage.InputTokens != 210 {
		t.Errorf("finish usage = %+v, want InputTokens=210", finish.Usage)
	}

	reqs := srv.Requests()
	if len(reqs) != 2 {
		t.Fatalf("upstream got %d requests, want 2 (100 + 5)", len(reqs))
	}
	if reqs[0].Method != "batchEmbedContents" || reqs[1].Method != "batchEmbedContents" {
		t.Errorf("methods = %s, %s, want batchEmbedContents", reqs[0].Method, reqs[1].Method)
	}
}

// Vertex AI accounts route by model per Google's Vertex AI docs:
//   - gemini-embedding-001 uses :predict with one input per request;
//   - text-embedding-005 batches up to 250 inputs per :predict request;
//   - gemini-embedding-2 uses :embedContent (with embedContentConfig.outputDimensionality), one input per request.
func TestGeminiEmbeddingsVertex(t *testing.T) {
	srv := geminitest.New(t)
	ext, err := host.Start(conformance.BuildExtension(t, "pluxy-extension.json", "."), host.Options{Log: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ext.Stop)

	settings, _ := json.Marshal(map[string]any{
		"base_url": srv.URL,
		"vertex":   map[string]any{"project": "embed-proj", "location": "us-central1"},
	})
	acct := protocol.Account{
		ID:         "vertex-embed",
		Settings:   settings,
		Credential: &protocol.Credential{Kind: CredentialServiceAccount, Data: srv.ServiceAccount("embed-proj", true)},
	}

	t.Run("gemini-embedding-001 single input per predict", func(t *testing.T) {
		before := len(srv.Requests())
		srv.Generate = func(w http.ResponseWriter, r *http.Request, req geminitest.Request) {
			var body struct {
				Instances []struct {
					Content string `json:"content"`
				} `json:"instances"`
			}
			if err := json.Unmarshal(req.Body, &body); err != nil {
				t.Errorf("decode predict body: %v", err)
				http.Error(w, "bad body", http.StatusBadRequest)
				return
			}
			if len(body.Instances) != 1 {
				t.Errorf("gemini-embedding-001 got %d instances in one :predict call, want 1", len(body.Instances))
				http.Error(w, "at most 1 instance", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			if body.Instances[0].Content == "alpha" {
				io.WriteString(w, `{"predictions":[{"embeddings":{"values":[0.1,0.2],"statistics":{"truncated":false,"token_count":4}}}]}`)
				return
			}
			io.WriteString(w, `{"predictions":[{"embeddings":{"values":[0.3,0.4],"statistics":{"truncated":false,"token_count":7}}}]}`)
		}

		events := embeddingEvents(t, ext, acct, llm.Request{
			Model: "gemini-embedding-001",
			Native: &llm.Native{
				Format: llm.FormatOpenAIEmbeddings,
				Body:   json.RawMessage(`{"model":"gemini-embedding-001","input":["alpha","beta"],"dimensions":2}`),
			},
		})
		reply := embeddingsReply(t, events)
		if len(reply.Data) != 2 || reply.Data[0].Index != 0 || reply.Data[1].Index != 1 {
			t.Fatalf("reply data = %+v", reply.Data)
		}
		if reply.Usage.PromptTokens != 11 || reply.Usage.TotalTokens != 11 {
			t.Errorf("usage = %+v, want 11/11", reply.Usage)
		}
		reqs := srv.Requests()[before:]
		if len(reqs) != 2 {
			t.Fatalf("upstream got %d requests, want 2 (one per input)", len(reqs))
		}
		for i, wantText := range []string{"alpha", "beta"} {
			if reqs[i].Kind != "vertex" || reqs[i].Method != "predict" {
				t.Fatalf("reqs[%d] = %s:%s, want vertex:predict", i, reqs[i].Kind, reqs[i].Method)
			}
			var sent struct {
				Instances []struct {
					Content string `json:"content"`
				} `json:"instances"`
				Parameters *struct {
					OutputDimensionality int `json:"outputDimensionality"`
				} `json:"parameters"`
			}
			if err := json.Unmarshal(reqs[i].Body, &sent); err != nil {
				t.Fatal(err)
			}
			if len(sent.Instances) != 1 || sent.Instances[0].Content != wantText {
				t.Errorf("reqs[%d] instances = %+v, want [%s]", i, sent.Instances, wantText)
			}
			if sent.Parameters == nil || sent.Parameters.OutputDimensionality != 2 {
				t.Errorf("reqs[%d] parameters = %+v, want outputDimensionality 2", i, sent.Parameters)
			}
		}
	})

	t.Run("text-embedding-005 batches on predict", func(t *testing.T) {
		before := len(srv.Requests())
		srv.Generate = func(w http.ResponseWriter, r *http.Request, req geminitest.Request) {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"predictions":[`+
				`{"embeddings":{"values":[0.1,0.2],"statistics":{"truncated":false,"token_count":4}}},`+
				`{"embeddings":{"values":[0.3,0.4],"statistics":{"truncated":false,"token_count":7}}}]}`)
		}

		events := embeddingEvents(t, ext, acct, llm.Request{
			Model: "text-embedding-005",
			Native: &llm.Native{
				Format: llm.FormatOpenAIEmbeddings,
				Body:   json.RawMessage(`{"model":"text-embedding-005","input":["alpha","beta"],"dimensions":2}`),
			},
		})
		reply := embeddingsReply(t, events)
		if len(reply.Data) != 2 || reply.Usage.PromptTokens != 11 {
			t.Fatalf("reply = %+v", reply)
		}
		reqs := srv.Requests()[before:]
		if len(reqs) != 1 || reqs[0].Kind != "vertex" || reqs[0].Method != "predict" {
			t.Fatalf("upstream requests = %+v, want one vertex :predict", reqs)
		}
	})

	t.Run("gemini-embedding-2 uses embedContent on Vertex", func(t *testing.T) {
		before := len(srv.Requests())
		srv.Generate = func(w http.ResponseWriter, r *http.Request, req geminitest.Request) {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"embedding":{"values":[0.5,0.25]},"usageMetadata":{"promptTokenCount":5}}`)
		}

		events := embeddingEvents(t, ext, acct, llm.Request{
			Model: "gemini-embedding-2",
			Native: &llm.Native{
				Format: llm.FormatOpenAIEmbeddings,
				Body:   json.RawMessage(`{"model":"gemini-embedding-2","input":["first","second"],"dimensions":2}`),
			},
		})
		reply := embeddingsReply(t, events)
		if len(reply.Data) != 2 || reply.Usage.PromptTokens != 10 {
			t.Fatalf("reply = %+v", reply)
		}
		reqs := srv.Requests()[before:]
		if len(reqs) != 2 {
			t.Fatalf("upstream got %d requests, want 2", len(reqs))
		}
		for i, wantText := range []string{"first", "second"} {
			if reqs[i].Kind != "vertex" || reqs[i].Method != "embedContent" {
				t.Fatalf("reqs[%d] = %s:%s, want vertex:embedContent", i, reqs[i].Kind, reqs[i].Method)
			}
			var sent struct {
				Content struct {
					Parts []struct {
						Text string `json:"text"`
					} `json:"parts"`
				} `json:"content"`
				EmbedContentConfig *struct {
					OutputDimensionality int `json:"outputDimensionality"`
				} `json:"embedContentConfig"`
			}
			if err := json.Unmarshal(reqs[i].Body, &sent); err != nil {
				t.Fatal(err)
			}
			if len(sent.Content.Parts) != 1 || sent.Content.Parts[0].Text != wantText {
				t.Errorf("reqs[%d] content = %+v, want %q", i, sent.Content, wantText)
			}
			if sent.EmbedContentConfig == nil || sent.EmbedContentConfig.OutputDimensionality != 2 {
				t.Errorf("reqs[%d] embedContentConfig = %+v, want outputDimensionality 2", i, sent.EmbedContentConfig)
			}
		}
	})
}

// Token-ID array inputs are refused with retryable credential-scoped CodeUnsupported so routing
// can move on to another provider; requests invalid for every account (empty, missing or malformed
// input, bad dimensions, unsupported encoding_format, streaming, thinking suffix) are refused with
// CodeInvalidRequest on ScopeRequest before calling the upstream.
func TestGeminiEmbeddingsRefusals(t *testing.T) {
	ext, srv, acct := embeddingTestExtension(t)
	srv.Generate = func(w http.ResponseWriter, r *http.Request, req geminitest.Request) {
		t.Errorf("refused call reached the upstream: %+v", req)
		http.Error(w, "unexpected", http.StatusInternalServerError)
	}
	for _, tc := range []struct {
		name          string
		req           llm.Request
		wantCode      string
		wantScope     string
		wantRetryable bool
		wantSub       string
	}{
		{
			name: "token array input",
			req: llm.Request{
				Native: &llm.Native{Format: llm.FormatOpenAIEmbeddings, Body: json.RawMessage(`{"model":"gemini-embedding-001","input":[1212,318,257]}`)},
			},
			wantCode:      llm.CodeUnsupported,
			wantScope:     llm.ScopeCredential,
			wantRetryable: true,
			wantSub:       "input",
		},
		{
			name: "nested token array input",
			req: llm.Request{
				Native: &llm.Native{Format: llm.FormatOpenAIEmbeddings, Body: json.RawMessage(`{"model":"gemini-embedding-001","input":[[1212,318],[42]]}`)},
			},
			wantCode:      llm.CodeUnsupported,
			wantScope:     llm.ScopeCredential,
			wantRetryable: true,
			wantSub:       "input",
		},
		{
			name: "mixed token and string input",
			req: llm.Request{
				Native: &llm.Native{Format: llm.FormatOpenAIEmbeddings, Body: json.RawMessage(`{"model":"gemini-embedding-001","input":[1212,"bad"]}`)},
			},
			wantCode:  llm.CodeInvalidRequest,
			wantScope: llm.ScopeRequest,
			wantSub:   "input",
		},
		{
			name: "empty nested token array input",
			req: llm.Request{
				Native: &llm.Native{Format: llm.FormatOpenAIEmbeddings, Body: json.RawMessage(`{"model":"gemini-embedding-001","input":[[]]}`)},
			},
			wantCode:  llm.CodeInvalidRequest,
			wantScope: llm.ScopeRequest,
			wantSub:   "input",
		},
		{
			name: "empty input array",
			req: llm.Request{
				Native: &llm.Native{Format: llm.FormatOpenAIEmbeddings, Body: json.RawMessage(`{"model":"gemini-embedding-001","input":[]}`)},
			},
			wantCode:  llm.CodeInvalidRequest,
			wantScope: llm.ScopeRequest,
			wantSub:   "input",
		},
		{
			name: "missing input",
			req: llm.Request{
				Native: &llm.Native{Format: llm.FormatOpenAIEmbeddings, Body: json.RawMessage(`{"model":"gemini-embedding-001"}`)},
			},
			wantCode:  llm.CodeInvalidRequest,
			wantScope: llm.ScopeRequest,
			wantSub:   "input",
		},
		{
			name: "non-string element in input",
			req: llm.Request{
				Native: &llm.Native{Format: llm.FormatOpenAIEmbeddings, Body: json.RawMessage(`{"model":"gemini-embedding-001","input":["ok",{"text":"no"}]}`)},
			},
			wantCode:  llm.CodeInvalidRequest,
			wantScope: llm.ScopeRequest,
			wantSub:   "input",
		},
		{
			name: "zero dimensions",
			req: llm.Request{
				Native: &llm.Native{Format: llm.FormatOpenAIEmbeddings, Body: json.RawMessage(`{"model":"gemini-embedding-001","input":"hi","dimensions":0}`)},
			},
			wantCode:  llm.CodeInvalidRequest,
			wantScope: llm.ScopeRequest,
			wantSub:   "dimensions",
		},
		{
			name: "non-integer dimensions",
			req: llm.Request{
				Native: &llm.Native{Format: llm.FormatOpenAIEmbeddings, Body: json.RawMessage(`{"model":"gemini-embedding-001","input":"hi","dimensions":"768"}`)},
			},
			wantCode:  llm.CodeInvalidRequest,
			wantScope: llm.ScopeRequest,
			wantSub:   "dimensions",
		},
		{
			name: "unsupported encoding_format",
			req: llm.Request{
				Native: &llm.Native{Format: llm.FormatOpenAIEmbeddings, Body: json.RawMessage(`{"model":"gemini-embedding-001","input":"hi","encoding_format":"int8"}`)},
			},
			wantCode:  llm.CodeInvalidRequest,
			wantScope: llm.ScopeRequest,
			wantSub:   "encoding_format",
		},
		{
			name: "stream",
			req: llm.Request{
				Stream: true,
				Native: &llm.Native{Format: llm.FormatOpenAIEmbeddings, Body: json.RawMessage(`{"model":"gemini-embedding-001","input":"hi","stream":true}`)},
			},
			wantCode:  llm.CodeInvalidRequest,
			wantScope: llm.ScopeRequest,
			wantSub:   "stream",
		},
		{
			name: "thinking suffix",
			req: llm.Request{
				Options: llm.Options{ThinkingSuffix: "high"},
				Native:  &llm.Native{Format: llm.FormatOpenAIEmbeddings, Body: json.RawMessage(`{"model":"gemini-embedding-001","input":"hi"}`)},
			},
			wantCode:  llm.CodeInvalidRequest,
			wantScope: llm.ScopeRequest,
			wantSub:   "thinking",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			events := embeddingEvents(t, ext, acct, tc.req)
			if len(events) != 1 || events[0].Type != llm.EventError || events[0].Error == nil {
				t.Fatalf("events = %+v, want one error", events)
			}
			e := events[0].Error
			if e.Code != tc.wantCode || e.Status != http.StatusBadRequest || e.Scope != tc.wantScope || e.Retryable != tc.wantRetryable {
				t.Errorf("error = %+v, want code=%s status=400 scope=%s retryable=%v", e, tc.wantCode, tc.wantScope, tc.wantRetryable)
			}
			if !strings.Contains(strings.ToLower(e.Message), tc.wantSub) {
				t.Errorf("message = %q, want substring %q", e.Message, tc.wantSub)
			}
		})
	}
	if reqs := srv.Requests(); len(reqs) != 0 {
		t.Fatalf("refused calls reached the upstream: %+v", reqs)
	}
}

// Malformed, short, streamed, or broken upstream replies fail this request only (ScopeRequest)
// rather than ScopeEndpoint, so the account is not cooled down for later requests.
func TestGeminiEmbeddingsUpstreamErrorScope(t *testing.T) {
	ext, srv, acct := embeddingTestExtension(t)
	for _, tc := range []struct {
		name   string
		body   string
		handle func(w http.ResponseWriter, r *http.Request, req geminitest.Request)
	}{
		{
			name: "count mismatch",
			body: `{"model":"gemini-embedding-001","input":["a","b"]}`,
			handle: func(w http.ResponseWriter, r *http.Request, req geminitest.Request) {
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"embeddings":[{"values":[0.1]}]}`)
			},
		},
		{
			name: "malformed json",
			body: `{"model":"gemini-embedding-001","input":"a"}`,
			handle: func(w http.ResponseWriter, r *http.Request, req geminitest.Request) {
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{not json`)
			},
		},
		{
			name: "streamed reply",
			body: `{"model":"gemini-embedding-001","input":"a"}`,
			handle: func(w http.ResponseWriter, r *http.Request, req geminitest.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, "data: {}\n\n")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv.Generate = tc.handle
			events := embeddingEvents(t, ext, acct, llm.Request{
				Model:  "gemini-embedding-001",
				Native: &llm.Native{Format: llm.FormatOpenAIEmbeddings, Body: json.RawMessage(tc.body)},
			})
			if len(events) != 1 || events[0].Type != llm.EventError || events[0].Error == nil {
				t.Fatalf("events = %+v, want one error", events)
			}
			if e := events[0].Error; e.Code != llm.CodeUpstream || e.Scope != llm.ScopeRequest {
				t.Errorf("error = %+v, want CodeUpstream on ScopeRequest", e)
			}
		})
	}
}

type recordSender struct {
	events []llm.Event
}

func (r *recordSender) Send(ev llm.Event) error {
	r.events = append(r.events, ev)
	return nil
}

// A large embeddings reply is split into multiple EventNative body pieces bounded by
// embeddingBodyChunkSize rather than sent as one oversized protocol frame.
func TestGeminiEmbeddingsReplyChunking(t *testing.T) {
	srv := geminitest.New(t)
	srv.Generate = func(w http.ResponseWriter, r *http.Request, req geminitest.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"embeddings":[{"values":[0.125,0.25,0.5,1.0]},{"values":[1.5,2.5,3.5,4.5]},{"values":[5.5,6.5,7.5,8.5]}],"usageMetadata":{"promptTokenCount":9}}`)
	}
	prevChunk := embeddingBodyChunkSize
	embeddingBodyChunkSize = 64
	t.Cleanup(func() { embeddingBodyChunkSize = prevChunk })

	settings, _ := json.Marshal(map[string]any{"base_url": srv.URL})
	call := sdk.Call{
		Account: protocol.Account{
			ID:         "gemini-chunk",
			Settings:   settings,
			Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: srv.IssueKey()},
		},
		Request: llm.Request{
			Model: "gemini-embedding-001",
			Native: &llm.Native{
				Format: llm.FormatOpenAIEmbeddings,
				Body:   json.RawMessage(`{"model":"gemini-embedding-001","input":["one","two","three"]}`),
			},
		},
	}
	rec := &recordSender{}
	if err := embeddingsTo(context.Background(), call, rec); err != nil {
		t.Fatalf("embeddingsTo: %v", err)
	}
	var nativeCount int
	var full strings.Builder
	for _, ev := range rec.events {
		if ev.Type != llm.EventNative {
			continue
		}
		nativeCount++
		var piece string
		if err := json.Unmarshal(ev.Raw, &piece); err != nil {
			t.Fatal(err)
		}
		if len(piece) > embeddingBodyChunkSize {
			t.Errorf("piece %d length = %d, exceeds chunk limit %d", nativeCount, len(piece), embeddingBodyChunkSize)
		}
		full.WriteString(piece)
	}
	if nativeCount < 2 {
		t.Fatalf("native events = %d, want multiple pieces when reply exceeds chunk size", nativeCount)
	}
	var reply openAIEmbeddingsReply
	if err := json.Unmarshal([]byte(full.String()), &reply); err != nil {
		t.Fatalf("joined reply %q is not valid JSON: %v", full.String(), err)
	}
	if len(reply.Data) != 3 || reply.Usage.PromptTokens != 9 {
		t.Errorf("reply = %+v", reply)
	}
}

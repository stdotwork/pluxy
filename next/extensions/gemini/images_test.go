package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/conformance"
	"github.com/stdotwork/pluxy/next/extensions/gemini/geminitest"
	"github.com/stdotwork/pluxy/next/internal/host"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

func imageTestExtension(t *testing.T) (*host.Extension, *geminitest.Server, protocol.Account) {
	t.Helper()
	srv := geminitest.New(t)
	ext, err := host.Start(conformance.BuildExtension(t, "pluxy-extension.json", "."), host.Options{Log: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ext.Stop)
	settings, _ := json.Marshal(map[string]any{"base_url": srv.URL})
	return ext, srv, protocol.Account{ID: "gemini-image", Settings: settings,
		Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: srv.IssueKey()}}
}

func imageEvents(t *testing.T, ext *host.Extension, acct protocol.Account, format, body string) []llm.Event {
	t.Helper()
	req := llm.Request{Model: "gemini-3.1-flash-image", Native: &llm.Native{Format: format, Body: json.RawMessage(body)}}
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

func imagesReply(t *testing.T, events []llm.Event) struct {
	Created int64 `json:"created"`
	Data    []struct {
		B64JSON string `json:"b64_json"`
	} `json:"data"`
} {
	t.Helper()
	if len(events) < 3 || events[0].Type != llm.EventResponseStart || events[len(events)-1].Type != llm.EventFinish {
		t.Fatalf("events = %+v, want response.start, body and finish", events)
	}
	if events[0].Response == nil || events[0].Response.Format != llm.FormatOpenAIImagesGenerations {
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
	var reply struct {
		Created int64 `json:"created"`
		Data    []struct {
			B64JSON string `json:"b64_json"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &reply); err != nil {
		t.Fatalf("reply %q is not JSON: %v", body, err)
	}
	return reply
}

func TestGeminiImagesGeneration(t *testing.T) {
	ext, srv, acct := imageTestExtension(t)
	srv.Generate = func(w http.ResponseWriter, r *http.Request, req geminitest.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"candidates":[`+
			`{"content":{"role":"model","parts":[{"text":"Here are two."},{"inlineData":{"mimeType":"image/png","data":"aW1hZ2UtMQ=="}}]},"finishReason":"STOP","index":0},`+
			`{"content":{"role":"model","parts":[{"inlineData":{"mimeType":"image/jpeg","data":"aW1hZ2UtMg=="}}]},"finishReason":"STOP","index":1}],`+
			`"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":7,"totalTokenCount":18},"modelVersion":"gemini-test"}`)
	}

	events := imageEvents(t, ext, acct, llm.FormatOpenAIImagesGenerations,
		`{"model":"gemini-3.1-flash-image","prompt":"draw a cat","size":"1024x1536","n":1,"response_format":"b64_json"}`)
	reply := imagesReply(t, events)
	if reply.Created <= 0 || !reflect.DeepEqual(reply.Data, []struct {
		B64JSON string `json:"b64_json"`
	}{{B64JSON: "aW1hZ2UtMQ=="}, {B64JSON: "aW1hZ2UtMg=="}}) {
		t.Fatalf("Images reply = %+v, want two b64_json entries and a created time", reply)
	}
	if finish := events[len(events)-1].Finish; finish == nil || finish.Reason != llm.FinishStop {
		t.Errorf("finish = %+v, want stop", finish)
	} else if usage := events[len(events)-1].Usage; usage == nil || usage.InputTokens == nil || *usage.InputTokens != 11 || usage.OutputTokens == nil || *usage.OutputTokens != 7 {
		t.Errorf("usage = %+v, want 11 input and 7 output tokens", usage)
	}

	requests := srv.Requests()
	if len(requests) != 1 {
		t.Fatalf("upstream got %d requests, want 1", len(requests))
	}
	up := requests[0]
	if up.Method != "generateContent" || up.Stream || up.Model != "gemini-3.1-flash-image" {
		t.Errorf("upstream request = %+v, want one non-streamed generateContent call", up)
	}
	var sent struct {
		Contents []struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"contents"`
		GenerationConfig struct {
			ResponseModalities []string `json:"responseModalities"`
			ImageConfig        *struct {
				AspectRatio string `json:"aspectRatio"`
			} `json:"imageConfig"`
		} `json:"generationConfig"`
	}
	if err := json.Unmarshal(up.Body, &sent); err != nil {
		t.Fatal(err)
	}
	if len(sent.Contents) != 1 || len(sent.Contents[0].Parts) != 1 || sent.Contents[0].Parts[0].Text != "draw a cat" {
		t.Errorf("Gemini contents = %+v, want the request prompt", sent.Contents)
	}
	if !reflect.DeepEqual(sent.GenerationConfig.ResponseModalities, []string{"IMAGE"}) {
		t.Errorf("responseModalities = %v, want IMAGE", sent.GenerationConfig.ResponseModalities)
	}
	if sent.GenerationConfig.ImageConfig == nil || sent.GenerationConfig.ImageConfig.AspectRatio != "2:3" {
		t.Errorf("imageConfig = %+v, want aspectRatio 2:3", sent.GenerationConfig.ImageConfig)
	}
}

func TestGeminiImagesSize(t *testing.T) {
	ext, srv, acct := imageTestExtension(t)
	srv.Generate = func(w http.ResponseWriter, r *http.Request, req geminitest.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"aW1hZ2U="}}]},"finishReason":"STOP","index":0}]}`)
	}
	for _, tc := range []struct{ size, ratio string }{
		{"1024x1024", "1:1"},
		{"1536x1024", "3:2"},
		{"1024x1536", "2:3"},
		{"1792x1024", "16:9"},
		{"1024x1792", "9:16"},
		{"auto", ""},
		{"", ""},
	} {
		t.Run(tc.size, func(t *testing.T) {
			before := len(srv.Requests())
			body := `{"model":"gemini-3.1-flash-image","prompt":"draw a cat"}`
			if tc.size != "" {
				body = `{"model":"gemini-3.1-flash-image","prompt":"draw a cat","size":"` + tc.size + `"}`
			}
			events := imageEvents(t, ext, acct, llm.FormatOpenAIImagesGenerations, body)
			reply := imagesReply(t, events)
			if len(reply.Data) != 1 || reply.Data[0].B64JSON != "aW1hZ2U=" {
				t.Fatalf("Images reply = %+v", reply)
			}
			requests := srv.Requests()
			if len(requests) != before+1 {
				t.Fatalf("upstream got %d requests, want %d", len(requests), before+1)
			}
			var sent struct {
				GenerationConfig struct {
					ImageConfig *struct {
						AspectRatio string `json:"aspectRatio"`
					} `json:"imageConfig"`
				} `json:"generationConfig"`
			}
			if err := json.Unmarshal(requests[before].Body, &sent); err != nil {
				t.Fatal(err)
			}
			if tc.ratio == "" {
				if sent.GenerationConfig.ImageConfig != nil {
					t.Errorf("imageConfig = %+v, want it left to Gemini", sent.GenerationConfig.ImageConfig)
				}
			} else if sent.GenerationConfig.ImageConfig == nil || sent.GenerationConfig.ImageConfig.AspectRatio != tc.ratio {
				t.Errorf("imageConfig = %+v, want aspectRatio %s", sent.GenerationConfig.ImageConfig, tc.ratio)
			}
		})
	}
}

func TestGeminiImagesRefusals(t *testing.T) {
	ext, srv, acct := imageTestExtension(t)
	srv.Generate = func(w http.ResponseWriter, r *http.Request, req geminitest.Request) {
		t.Errorf("refused call reached the upstream: %+v", req)
		http.Error(w, "unexpected", http.StatusInternalServerError)
	}
	for _, tc := range []struct {
		name, format, body, code string
	}{
		{"n above one", llm.FormatOpenAIImagesGenerations, `{"model":"gemini-3.1-flash-image","prompt":"draw a cat","n":2}`, llm.CodeInvalidRequest},
		{"stream", llm.FormatOpenAIImagesGenerations, `{"model":"gemini-3.1-flash-image","prompt":"draw a cat","stream":true}`, llm.CodeInvalidRequest},
		{"bad size", llm.FormatOpenAIImagesGenerations, `{"model":"gemini-3.1-flash-image","prompt":"draw a cat","size":"large"}`, llm.CodeInvalidRequest},
		{"edits", llm.FormatOpenAIImagesEdits, `{"model":"gemini-3.1-flash-image","prompt":"draw a cat","images":[]}`, llm.CodeUnsupported},
	} {
		t.Run(tc.name, func(t *testing.T) {
			events := imageEvents(t, ext, acct, tc.format, tc.body)
			if len(events) != 1 || events[0].Type != llm.EventError || events[0].Error == nil || events[0].Error.Code != tc.code {
				t.Fatalf("events = %+v, want one %s error", events, tc.code)
			}
			if tc.code == llm.CodeUnsupported && (!events[0].Error.Retryable || events[0].Error.Scope != llm.ScopeCredential) {
				t.Errorf("edits error = %+v, want a retryable credential refusal", events[0].Error)
			}
		})
	}
	if requests := srv.Requests(); len(requests) != 0 {
		t.Fatalf("refused calls reached the upstream: %+v", requests)
	}
}

func TestGeminiImagesReplySizeLimit(t *testing.T) {
	ext, srv, acct := imageTestExtension(t)
	srv.Generate = func(w http.ResponseWriter, r *http.Request, req geminitest.Request) {
		w.Header().Set("Content-Type", "application/json")
		pad := bytes.Repeat([]byte{' '}, 1<<20)
		for written := 0; written < maxImagesReply; written += len(pad) {
			if _, err := w.Write(pad); err != nil {
				return
			}
		}
		_, _ = io.WriteString(w, `{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"aW1hZ2U="}}]},"finishReason":"STOP","index":0}]}`)
	}

	events := imageEvents(t, ext, acct, llm.FormatOpenAIImagesGenerations,
		`{"model":"gemini-3.1-flash-image","prompt":"draw a cat"}`)
	if len(events) != 1 || events[0].Type != llm.EventError || events[0].Error == nil {
		t.Fatalf("events = %+v, want one upstream error for reply over %d MiB", events, maxImagesReply>>20)
	}
	if e := events[0].Error; e.Code != llm.CodeUpstream || e.Scope != llm.ScopeEndpoint || !strings.Contains(e.Message, "larger than") {
		t.Fatalf("error = %+v, want CodeUpstream on ScopeEndpoint naming the reply-size limit", e)
	}
}

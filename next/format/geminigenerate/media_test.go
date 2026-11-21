package geminigenerate

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/format/anthropicmessages"
	"github.com/stdotwork/pluxy/next/format/openaichat"
	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/llm"
)

// contentsOf returns the parts GenerateContent parts of a body, one entry per content.
func contentsOf(t *testing.T, body []byte) []struct {
	Role  string            `json:"role"`
	Parts []json.RawMessage `json:"parts"`
} {
	t.Helper()
	var got struct {
		Contents []struct {
			Role  string            `json:"role"`
			Parts []json.RawMessage `json:"parts"`
		} `json:"contents"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("body %s: %v", body, err)
	}
	return got.Contents
}

// A URL in a request from another format reaches Gemini as fileData only when it is a file URI
// Gemini documents: a Files API URI or a Cloud Storage URI.
func TestEncodeMediaURL(t *testing.T) {
	for _, c := range []struct {
		name string
		body string
		want string
	}{
		{
			name: "Chat image, Files API URI",
			body: `{"model":"gemini-x","messages":[{"role":"user","content":[{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"https://generativelanguage.googleapis.com/v1beta/files/abc-123"}}]}]}`,
			want: `{"fileData":{"fileUri":"https://generativelanguage.googleapis.com/v1beta/files/abc-123"}}`,
		},
		{
			name: "Chat image, Cloud Storage URI",
			body: `{"model":"gemini-x","messages":[{"role":"user","content":[{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"gs://bucket/cat.png"}}]}]}`,
			want: `{"fileData":{"fileUri":"gs://bucket/cat.png"}}`,
		},
	} {
		req, err := openaichat.ParseRequest([]byte(c.body))
		if err != nil {
			t.Fatal(err)
		}
		body, losses, err := EncodeRequest(req)
		if err != nil {
			t.Fatal(err)
		}
		contents := contentsOf(t, body)
		if len(contents) != 1 || len(contents[0].Parts) != 2 {
			t.Fatalf("%s: contents = %s, want one part", c.name, body)
		}
		sameJSON(t, c.name, contents[0].Parts[1], c.want)
		if len(losses) != 0 {
			t.Errorf("%s: losses = %+v, want none", c.name, losses)
		}
	}

	// A Responses file URL, and a Messages document URL, map the same way.
	resp, err := openairesponses.ParseRequest([]byte(`{"model":"gemini-x","input":[{"type":"message","role":"user","content":[
		{"type":"input_text","text":"look"},{"type":"input_file","file_url":"https://generativelanguage.googleapis.com/v1beta/files/doc-1"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	body, losses, err := EncodeRequest(resp)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"fileData":{"fileUri":"https://generativelanguage.googleapis.com/v1beta/files/doc-1"}`) || len(losses) != 0 {
		t.Errorf("Responses file URL: body %s, losses %+v", body, losses)
	}
	msgs, err := anthropicmessages.ParseRequest([]byte(`{"model":"gemini-x","max_tokens":9,"messages":[{"role":"user","content":[
		{"type":"text","text":"look"},{"type":"document","source":{"type":"url","url":"gs://bucket/report.pdf"}}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	body, losses, err = EncodeRequest(msgs)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"fileData":{"fileUri":"gs://bucket/report.pdf"}`) || len(losses) != 0 {
		t.Errorf("Messages document URL: body %s, losses %+v", body, losses)
	}
}

// A URL Gemini does not document is dropped with a loss, never fetched: pluxy does not follow a
// client's URL.
func TestEncodeUnsupportedMediaURLDropped(t *testing.T) {
	req, err := openaichat.ParseRequest([]byte(`{"model":"gemini-x","messages":[{"role":"user","content":[
		{"type":"text","text":"look"},
		{"type":"image_url","image_url":{"url":"https://example.com/cat.png"}}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	body, losses, err := EncodeRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "example.com") {
		t.Errorf("an undocumented URL reached Gemini: %s", body)
	}
	if len(losses) != 1 || losses[0].Kind != llm.LossDropped || losses[0].Path != "messages[0].content[1]" {
		t.Fatalf("losses = %+v, want the image dropped at messages[0].content[1]", losses)
	}
	if tr := (&llm.Translation{Losses: losses}); !tr.Dropped() {
		t.Error("Translation.Dropped() = false, want true so the reject policy refuses the request")
	}

	msgs, err := anthropicmessages.ParseRequest([]byte(`{"model":"gemini-x","max_tokens":9,"messages":[{"role":"user","content":[
		{"type":"text","text":"look"},{"type":"document","source":{"type":"url","url":"https://example.com/report.pdf"}}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	body, losses, err = EncodeRequest(msgs)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "example.com") || len(losses) == 0 || losses[0].Kind != llm.LossDropped {
		t.Errorf("document URL: body %s, losses %+v; want it dropped", body, losses)
	}
}

// A GenerateContent request is passed through unchanged elsewhere, but when its own parts are
// rebuilt the request's file URI is written back as the client sent it.
func TestEncodeGeminiFileURLKept(t *testing.T) {
	req, err := ParseRequest("gemini-x", false, []byte(`{"contents":[{"role":"user","parts":[
		{"fileData":{"fileUri":"https://example.com/cat.png","mimeType":"image/png"}}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	body, losses, err := EncodeRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"fileData":{"fileUri":"https://example.com/cat.png","mimeType":"image/png"}`) {
		t.Errorf("body %s: want the client's own fileData written back", body)
	}
	if len(losses) != 0 {
		t.Errorf("losses = %+v, want none", losses)
	}
}

// An inline image is sent only when its data is base64 and its media type is one Gemini
// documents for images.
func TestEncodeInlineImageValidation(t *testing.T) {
	for _, c := range []struct {
		name string
		url  string
		want string // the inlineData media type sent, or "" when it is dropped
	}{
		{"png", "data:image/png;base64,aGVsbG8=", "image/png"},
		{"jpeg", "data:image/jpeg;base64,aGVsbG8=", "image/jpeg"},
		{"webp", "data:image/webp;base64,aGVsbG8=", "image/webp"},
		{"invalid base64", "data:image/png;base64,not base64", ""},
		{"empty data", "data:image/png;base64,", ""},
		{"unsupported media type", "data:image/tiff;base64,aGVsbG8=", ""},
		{"not a data URL", "https://example.com/cat.png", ""},
	} {
		req, err := openaichat.ParseRequest([]byte(`{"model":"gemini-x","messages":[{"role":"user","content":[
			{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"` + c.url + `"}}]}]}`))
		if err != nil {
			t.Fatal(err)
		}
		body, losses, err := EncodeRequest(req)
		if err != nil {
			t.Fatal(err)
		}
		if c.want == "" {
			if strings.Contains(string(body), "inlineData") {
				t.Errorf("%s: body %s carries the image", c.name, body)
			}
			if len(losses) != 1 || losses[0].Kind != llm.LossDropped {
				t.Errorf("%s: losses = %+v, want one drop", c.name, losses)
			}
			continue
		}
		if !strings.Contains(string(body), `"inlineData":{`) || !strings.Contains(string(body), `"mimeType":"`+c.want+`"`) {
			t.Errorf("%s: body %s, want inlineData %s", c.name, body, c.want)
		}
		if len(losses) != 0 {
			t.Errorf("%s: losses = %+v, want none", c.name, losses)
		}
	}
}

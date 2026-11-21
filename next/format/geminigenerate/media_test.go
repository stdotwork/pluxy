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
		{"type":"text","text":"look"},{"type":"document","source":{"type":"url","url":"https://generativelanguage.googleapis.com/v1beta/files/report-1"}}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	body, losses, err = EncodeRequest(msgs)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"fileData":{"fileUri":"https://generativelanguage.googleapis.com/v1beta/files/report-1"}`) || len(losses) != 0 {
		t.Errorf("Messages document URL: body %s, losses %+v", body, losses)
	}
}

// A Cloud Storage URI from a translated request is dropped with a loss: Vertex would read the
// object with the account's own permissions, which a client key must not reach through pluxy.
func TestEncodeCloudStorageURIDropped(t *testing.T) {
	chat, err := openaichat.ParseRequest([]byte(`{"model":"gemini-x","messages":[{"role":"user","content":[
		{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"gs://bucket/cat.png"}}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	body, losses, err := EncodeRequest(chat)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "gs://") || len(losses) != 1 || losses[0].Kind != llm.LossDropped {
		t.Errorf("Chat image: body %s, losses %+v; want the URI dropped", body, losses)
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
	if strings.Contains(string(body), "gs://") || len(losses) != 1 || losses[0].Kind != llm.LossDropped {
		t.Errorf("Messages document: body %s, losses %+v; want the URI dropped", body, losses)
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
		{"upper-case media type", "data:IMAGE/PNG;base64,aGVsbG8=", "image/png"},
		{"media type parameters", "data:image/png;charset=utf-8;base64,aGVsbG8=", "image/png"},
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

// A Messages plain-text document has no GenerateContent form: its text is carried in a text
// part, delimited and named after the document, and reported as an approximated loss.
func TestEncodeTextDocument(t *testing.T) {
	req, err := anthropicmessages.ParseRequest([]byte(`{"model":"gemini-x","max_tokens":9,"messages":[{"role":"user","content":[
		{"type":"text","text":"summarize"},
		{"type":"document","source":{"type":"text","media_type":"text/plain","data":"Line one\nLine two"},"title":"notes.txt"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	body, losses, err := EncodeRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	want := "[Document: notes.txt]\nLine one\nLine two\n[End of document: notes.txt]"
	if !strings.Contains(string(body), `"text":"`+strings.ReplaceAll(want, "\n", `\n`)+`"`) {
		t.Errorf("body %s, want the document's text carried", body)
	}
	if len(losses) != 1 || losses[0].Kind != llm.LossApproximated || losses[0].Path != "messages[0].content[1]" {
		t.Errorf("losses = %+v, want one approximation at messages[0].content[1]", losses)
	}
}

// transImageRequest is a user turn with text and one image translated from another format (no
// origin, so not the target's own).
func transImageRequest(url string) llm.Request {
	return llm.Request{Model: "m", Items: []llm.Item{{
		Type:    llm.ItemTypeMessage,
		Role:    llm.RoleUser,
		Content: []llm.Part{{Type: llm.PartText, Text: "look"}, {Type: llm.PartImage, URL: url}},
	}}}
}

// A part the client sent to GenerateContent itself is what Gemini accepts: the encoder does not
// apply the image list or the base64 check to it, only to parts translated from another format.
func TestEncodeSameFormatImageNotValidated(t *testing.T) {
	same := transImageRequest("data:image/tiff;base64,aGVsbG8=")
	same.Items[0].Content[1].Origin = &llm.Origin{Format: llm.FormatGeminiGenerate, Path: "messages[0].content[1]"}
	body, losses, err := EncodeRequest(same)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"mimeType":"image/tiff"`) {
		t.Errorf("same-format body %s, want the client's own image written back", body)
	}
	if len(losses) != 0 {
		t.Errorf("same-format losses = %+v, want none", losses)
	}

	translated := transImageRequest("data:image/tiff;base64,aGVsbG8=")
	body, losses, err = EncodeRequest(translated)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "image/tiff") || len(losses) != 1 || losses[0].Kind != llm.LossDropped {
		t.Errorf("translated body %s, losses %+v; want the image dropped", body, losses)
	}
}

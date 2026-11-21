package openaichat

import (
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

func imageRequest(url, fileID string) llm.Request {
	p := llm.Part{Type: llm.PartImage, URL: url}
	if url == "" {
		p.FileID = fileID
	}
	return llm.Request{Model: "m", Items: []llm.Item{{
		Type:    llm.ItemTypeMessage,
		Role:    llm.RoleUser,
		Content: []llm.Part{{Type: llm.PartText, Text: "look"}, p},
	}}}
}

// Chat Completions has no image-by-file-id form: the file part takes a file id but only a PDF,
// so an image by file id is dropped with a loss, never silently.
func TestEncodeImageByFileIDDropped(t *testing.T) {
	body, losses, err := EncodeRequest(imageRequest("", "file_1"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "image_url") || strings.Contains(string(body), "file_1") {
		t.Errorf("body %s carries the image", body)
	}
	if len(losses) != 1 || losses[0].Kind != llm.LossDropped || !strings.Contains(losses[0].Detail, "file id") {
		t.Fatalf("losses = %+v, want the image dropped with a loss naming the file id", losses)
	}
	if !(&llm.Translation{Losses: losses}).Dropped() {
		t.Error("Translation.Dropped() = false, want true so the reject policy refuses the request")
	}
}

// An inline image is sent only when its data is base64 and its media type is one OpenAI
// documents for image input.
func TestEncodeInlineImageValidation(t *testing.T) {
	for _, c := range []struct {
		name string
		url  string
		keep bool
	}{
		{"png", "data:image/png;base64,aGVsbG8=", true},
		{"jpeg", "data:image/jpeg;base64,aGVsbG8=", true},
		{"gif", "data:image/gif;base64,aGVsbG8=", true},
		{"webp", "data:image/webp;base64,aGVsbG8=", true},
		{"bmp", "data:image/bmp;base64,aGVsbG8=", true},
		{"https URL", "https://example.com/cat.png", true},
		{"invalid base64", "data:image/png;base64,not base64", false},
		{"empty data", "data:image/png;base64,", false},
		{"not an image type", "data:text/plain;base64,aGVsbG8=", false},
		{"upper-case scheme and marker", "DATA:image/png;BASE64,aGVsbG8=", true},
		{"upper-case scheme, bad base64", "DATA:image/png;base64,%%%", false},
	} {
		body, losses, err := EncodeRequest(imageRequest(c.url, ""))
		if err != nil {
			t.Fatal(err)
		}
		if c.keep {
			if !strings.Contains(string(body), `"image_url":{"url":"`+c.url+`"`) {
				t.Errorf("%s: body %s, want the image kept", c.name, body)
			}
			if len(losses) != 0 {
				t.Errorf("%s: losses = %+v, want none", c.name, losses)
			}
			continue
		}
		if strings.Contains(string(body), "image_url") {
			t.Errorf("%s: body %s carries the image", c.name, body)
		}
		if len(losses) != 1 || losses[0].Kind != llm.LossDropped {
			t.Errorf("%s: losses = %+v, want one drop", c.name, losses)
		}
	}
}

// A Messages plain-text document (as anthropicmessages.ParseRequest builds it) has no Chat
// Completions form: its text is carried in a text part, delimited and named after the document,
// and reported as an approximated loss.
func TestEncodeTextDocument(t *testing.T) {
	doc := []byte(`{"type":"document","source":{"type":"text","media_type":"text/plain","data":"Line one\nLine two"},"title":"notes.txt"}`)
	req := llm.Request{Model: "m", Items: []llm.Item{{
		Type: llm.ItemTypeMessage, Role: llm.RoleUser,
		Content: []llm.Part{
			{Type: llm.PartText, Text: "summarize"},
			{Type: llm.PartNative, Format: llm.FormatAnthropicMessages, Data: doc,
				Origin: &llm.Origin{Format: llm.FormatAnthropicMessages, Path: "messages[0].content[1]"}},
		},
	}}}
	body, losses, err := EncodeRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	want := "[Document: notes.txt]\nLine one\nLine two\n[End of document: notes.txt]"
	if !strings.Contains(string(body), `"type":"text","text":"`+strings.ReplaceAll(want, "\n", `\n`)+`"`) {
		t.Errorf("body %s, want the document's text carried", body)
	}
	if len(losses) != 1 || losses[0].Kind != llm.LossApproximated || losses[0].Path != "messages[0].content[1]" {
		t.Errorf("losses = %+v, want one approximation at messages[0].content[1]", losses)
	}
}

// Base64 wrapped with CR or LF is still base64, and the data is forwarded exactly as the client
// sent it.
func TestEncodeWrappedBase64Forwarded(t *testing.T) {
	url := "data:image/png;base64,TW\r\nFu\r\n"
	body, losses, err := EncodeRequest(imageRequest(url, ""))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), strings.ReplaceAll(strings.ReplaceAll(url, "\r", `\r`), "\n", `\n`)) {
		t.Errorf("body %s, want the wrapped data forwarded unchanged", body)
	}
	if len(losses) != 0 {
		t.Errorf("losses = %+v, want none", losses)
	}
}

// Only a Messages document block is a Messages text document: a native part of another format
// that happens to hold the same JSON is not carried as its text.
func TestEncodeForeignNativeTextDocumentDropped(t *testing.T) {
	doc := []byte(`{"type":"document","source":{"type":"text","media_type":"text/plain","data":"Line one\nLine two"},"title":"notes.txt"}`)
	req := llm.Request{Model: "m", Items: []llm.Item{{
		Type: llm.ItemTypeMessage, Role: llm.RoleUser,
		Content: []llm.Part{
			{Type: llm.PartText, Text: "summarize"},
			{Type: llm.PartNative, Format: llm.FormatOpenAIResponses, Data: doc,
				Origin: &llm.Origin{Format: llm.FormatOpenAIResponses, Path: "input[0].content[1]"}},
		},
	}}}
	body, losses, err := EncodeRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "Line one") || len(losses) != 1 || losses[0].Kind != llm.LossDropped {
		t.Errorf("body %s, losses %+v; want the foreign part dropped", body, losses)
	}
}

// A translated file part is checked too: a data URL that is not base64 is not sent, and a part
// with nothing in it never becomes an empty file block.
func TestEncodeTranslatedFileValidation(t *testing.T) {
	file := func(p llm.Part) llm.Request {
		return llm.Request{Model: "m", Items: []llm.Item{{
			Type: llm.ItemTypeMessage, Role: llm.RoleUser,
			Content: []llm.Part{{Type: llm.PartText, Text: "look"}, p},
		}}}
	}
	for _, c := range []struct {
		name string
		part llm.Part
		keep bool
	}{
		{"invalid base64", llm.Part{Type: llm.PartFile, FileData: "data:application/pdf;base64,%%%"}, false},
		{"not a base64 data URL", llm.Part{Type: llm.PartFile, FileData: "data:application/pdf,raw"}, false},
		{"valid data URL", llm.Part{Type: llm.PartFile, FileData: "data:application/pdf;base64,JVBERi0="}, true},
		{"file id", llm.Part{Type: llm.PartFile, FileID: "file_1"}, true},
		{"empty", llm.Part{Type: llm.PartFile}, false},
	} {
		body, losses, err := EncodeRequest(file(c.part))
		if err != nil {
			t.Fatal(err)
		}
		if c.keep {
			if !strings.Contains(string(body), `"type":"file"`) {
				t.Errorf("%s: body %s, want the file kept", c.name, body)
			}
			if len(losses) != 0 {
				t.Errorf("%s: losses = %+v, want none", c.name, losses)
			}
			continue
		}
		if strings.Contains(string(body), `"type":"file"`) || len(losses) != 1 || losses[0].Kind != llm.LossDropped {
			t.Errorf("%s: body %s, losses %+v; want the file dropped", c.name, body, losses)
		}
	}
}

// A Messages document source of a text type other than text/plain is not a plain-text document:
// it falls through to the ordinary dropped loss.
func TestEncodeNonPlainTextDocumentDropped(t *testing.T) {
	doc := []byte(`{"type":"document","source":{"type":"text","media_type":"text/html","data":"<p>Hello</p>"}}`)
	req := llm.Request{Model: "m", Items: []llm.Item{{
		Type: llm.ItemTypeMessage, Role: llm.RoleUser,
		Content: []llm.Part{
			{Type: llm.PartText, Text: "summarize"},
			{Type: llm.PartNative, Format: llm.FormatAnthropicMessages, Data: doc,
				Origin: &llm.Origin{Format: llm.FormatAnthropicMessages, Path: "messages[0].content[1]"}},
		},
	}}}
	body, losses, err := EncodeRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "Hello") || len(losses) != 1 || losses[0].Kind != llm.LossDropped {
		t.Errorf("body %s, losses %+v; want the document dropped", body, losses)
	}
}

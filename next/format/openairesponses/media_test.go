package openairesponses

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

// Responses takes an image by file id, and a file by URL; both are its own forms.
func TestEncodeImageByFileID(t *testing.T) {
	body, losses, err := EncodeRequest(imageRequest("", "file_1"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"file_id":"file_1"`) || !strings.Contains(string(body), `"type":"input_image"`) {
		t.Errorf("body %s, want the file id kept", body)
	}
	if len(losses) != 0 {
		t.Errorf("losses = %+v, want none", losses)
	}
}

func TestEncodeFileURL(t *testing.T) {
	req := llm.Request{Model: "m", Items: []llm.Item{{
		Type: llm.ItemTypeMessage, Role: llm.RoleUser,
		Content: []llm.Part{{Type: llm.PartText, Text: "look"}, {Type: llm.PartFile, URL: "https://example.com/report.pdf"}},
	}}}
	body, losses, err := EncodeRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"file_url":"https://example.com/report.pdf"`) || !strings.Contains(string(body), `"type":"input_file"`) {
		t.Errorf("body %s, want the file URL kept", body)
	}
	if len(losses) != 0 {
		t.Errorf("losses = %+v, want none", losses)
	}
}

// These encoders also serve OpenAI-compatible vendors, so any image media type is kept: an
// inline image is sent only when its data is base64 and its media type starts with image/.
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
			if !strings.Contains(string(body), `"image_url":"`+c.url+`"`) {
				t.Errorf("%s: body %s, want the image kept", c.name, body)
			}
			if len(losses) != 0 {
				t.Errorf("%s: losses = %+v, want none", c.name, losses)
			}
			continue
		}
		if strings.Contains(string(body), "input_image") {
			t.Errorf("%s: body %s carries the image", c.name, body)
		}
		if len(losses) != 1 || losses[0].Kind != llm.LossDropped {
			t.Errorf("%s: losses = %+v, want one drop", c.name, losses)
		}
	}
}

// A Messages plain-text document (as anthropicmessages.ParseRequest builds it) has no Responses
// form: its text is carried in a text part, delimited, and reported as an approximated loss.
func TestEncodeTextDocument(t *testing.T) {
	doc := []byte(`{"type":"document","source":{"type":"text","media_type":"text/plain","data":"Line one\nLine two"}}`)
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
	want := "[Document]\nLine one\nLine two\n[End of document]"
	if !strings.Contains(string(body), `"text":"`+strings.ReplaceAll(want, "\n", `\n`)+`"`) {
		t.Errorf("body %s, want the document's text carried", body)
	}
	if len(losses) != 1 || losses[0].Kind != llm.LossApproximated || losses[0].Path != "messages[0].content[1]" {
		t.Errorf("losses = %+v, want one approximation at messages[0].content[1]", losses)
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
			{Type: llm.PartNative, Format: llm.FormatOpenAIChat, Data: doc,
				Origin: &llm.Origin{Format: llm.FormatOpenAIChat, Path: "messages[0].content[1]"}},
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

// A translated file part is checked too: a data URL that is not base64 is not sent, and an empty
// part does not become an empty input_file.
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
		{"empty", llm.Part{Type: llm.PartFile}, false},
		{"valid data URL", llm.Part{Type: llm.PartFile, FileData: "data:application/pdf;base64,JVBERi0="}, true},
		{"file id", llm.Part{Type: llm.PartFile, FileID: "file_1"}, true},
		{"file URL", llm.Part{Type: llm.PartFile, URL: "https://example.com/r.pdf"}, true},
	} {
		body, losses, err := EncodeRequest(file(c.part))
		if err != nil {
			t.Fatal(err)
		}
		if c.keep {
			if !strings.Contains(string(body), `"type":"input_file"`) {
				t.Errorf("%s: body %s, want the file kept", c.name, body)
			}
			if len(losses) != 0 {
				t.Errorf("%s: losses = %+v, want none", c.name, losses)
			}
			continue
		}
		if strings.Contains(string(body), `"type":"input_file"`) || len(losses) != 1 || losses[0].Kind != llm.LossDropped {
			t.Errorf("%s: body %s, losses %+v; want the file dropped", c.name, body, losses)
		}
	}
}

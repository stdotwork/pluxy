package anthropicmessages

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

// Messages takes an image by URL and by file id, and a PDF by URL, as its own source forms.
func TestEncodeImageSources(t *testing.T) {
	for _, c := range []struct {
		name   string
		req    llm.Request
		inBody string
	}{
		{"image URL", imageRequest("https://example.com/cat.png", ""), `"type":"url","url":"https://example.com/cat.png"`},
		{"image file id", imageRequest("", "file_1"), `"file_id":"file_1","type":"file"`},
		{"file URL", llm.Request{Model: "m", Items: []llm.Item{{Type: llm.ItemTypeMessage, Role: llm.RoleUser,
			Content: []llm.Part{{Type: llm.PartText, Text: "look"}, {Type: llm.PartFile, URL: "https://example.com/report.pdf"}}}}},
			`"type":"url","url":"https://example.com/report.pdf"`},
		{"file id", llm.Request{Model: "m", Items: []llm.Item{{Type: llm.ItemTypeMessage, Role: llm.RoleUser,
			Content: []llm.Part{{Type: llm.PartText, Text: "look"}, {Type: llm.PartFile, FileID: "file_2"}}}}},
			`"file_id":"file_2","type":"file"`},
	} {
		body, losses, err := EncodeRequest(c.req)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), c.inBody) {
			t.Errorf("%s: body %s, want %s", c.name, body, c.inBody)
		}
		if len(losses) != 0 {
			t.Errorf("%s: losses = %+v, want none", c.name, losses)
		}
	}
}

// An inline image is sent only when its data is base64 and its media type is one Claude
// documents for image input.
func TestEncodeInlineImageValidation(t *testing.T) {
	for _, c := range []struct {
		name      string
		url       string
		mediaType string
		keep      bool
	}{
		{"png", "data:image/png;base64,aGVsbG8=", "image/png", true},
		{"jpeg", "data:image/jpeg;base64,aGVsbG8=", "image/jpeg", true},
		{"gif", "data:image/gif;base64,aGVsbG8=", "image/gif", true},
		{"webp", "data:image/webp;base64,aGVsbG8=", "image/webp", true},
		{"upper-case media type", "data:IMAGE/PNG;base64,aGVsbG8=", "image/png", true},
		{"media type parameters", "data:image/png;charset=utf-8;base64,aGVsbG8=", "image/png", true},
		{"upper-case scheme and marker", "DATA:image/png;BASE64,aGVsbG8=", "image/png", true},
		{"upper-case scheme, bad base64", "DATA:image/png;base64,%%%", "image/png", false},
		{"invalid base64", "data:image/png;base64,not base64", "image/png", false},
		{"empty data", "data:image/png;base64,", "image/png", false},
		{"unsupported media type", "data:image/tiff;base64,aGVsbG8=", "image/tiff", false},
	} {
		body, losses, err := EncodeRequest(imageRequest(c.url, ""))
		if err != nil {
			t.Fatal(err)
		}
		if c.keep {
			if !strings.Contains(string(body), `"data":"aGVsbG8="`) || !strings.Contains(string(body), `"media_type":"`+c.mediaType+`"`) {
				t.Errorf("%s: body %s, want the image kept", c.name, body)
			}
			if len(losses) != 0 {
				t.Errorf("%s: losses = %+v, want none", c.name, losses)
			}
			continue
		}
		if strings.Contains(string(body), "image") {
			t.Errorf("%s: body %s carries the image", c.name, body)
		}
		if len(losses) != 1 || losses[0].Kind != llm.LossDropped {
			t.Errorf("%s: losses = %+v, want one drop", c.name, losses)
		}
	}
}

// A Messages plain-text document is a Messages block: it is written back as the client sent it.
func TestEncodeTextDocumentKept(t *testing.T) {
	req, err := ParseRequest([]byte(`{"model":"m","max_tokens":9,"messages":[{"role":"user","content":[
		{"type":"text","text":"summarize"},
		{"type":"document","source":{"type":"text","media_type":"text/plain","data":"Line one\nLine two"},"title":"notes.txt"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	body, losses, err := EncodeRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"type":"document","source":{"type":"text","media_type":"text/plain","data":"Line one\nLine two"},"title":"notes.txt"`) {
		t.Errorf("body %s, want the document written back", body)
	}
	if len(losses) != 0 {
		t.Errorf("losses = %+v, want none", losses)
	}
}

// A part the client sent to Messages itself is what Messages accepts: the encoder does not
// apply the image list or the base64 check to it, only to parts translated from another format.
func TestEncodeSameFormatImageNotValidated(t *testing.T) {
	same := imageRequest("data:image/tiff;base64,aGVsbG8=", "")
	same.Items[0].Content[1].Origin = &llm.Origin{Format: llm.FormatAnthropicMessages, Path: "messages[0].content[1]"}
	body, losses, err := EncodeRequest(same)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"media_type":"image/tiff"`) {
		t.Errorf("same-format body %s, want the client's own image written back", body)
	}
	if len(losses) != 0 {
		t.Errorf("same-format losses = %+v, want none", losses)
	}

	translated := imageRequest("data:image/tiff;base64,aGVsbG8=", "")
	body, losses, err = EncodeRequest(translated)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "image/tiff") || len(losses) != 1 || losses[0].Kind != llm.LossDropped {
		t.Errorf("translated body %s, losses %+v; want the image dropped", body, losses)
	}
}

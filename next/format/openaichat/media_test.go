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

func TestEncodeTranslatedFileDataForms(t *testing.T) {
	file := func(data string) llm.Request {
		return llm.Request{Model: "m", Items: []llm.Item{{
			Type: llm.ItemTypeMessage, Role: llm.RoleUser,
			Content: []llm.Part{{Type: llm.PartText, Text: "look"}, {Type: llm.PartFile, FileData: data}},
		}}}
	}
	for _, c := range []struct {
		name string
		data string
		keep bool
	}{
		{"data URL", "data:application/pdf;base64,JVBERi0=", true},
		{"raw base64", "JVBERi0=", true},
		{"URL", "https://example.com/r.pdf", true},
		{"garbage", "%%%", false},
		{"not base64 data URL", "data:application/pdf,raw", false},
	} {
		body, losses, err := EncodeRequest(file(c.data))
		if err != nil {
			t.Fatal(err)
		}
		if c.keep {
			if !strings.Contains(string(body), `"type":"file"`) || len(losses) != 0 {
				t.Errorf("%s: body %s, losses %+v; want the file kept", c.name, body, losses)
			}
			continue
		}
		if strings.Contains(string(body), `"type":"file"`) || len(losses) != 1 || losses[0].Kind != llm.LossDropped {
			t.Errorf("%s: body %s, losses %+v; want the file dropped", c.name, body, losses)
		}
	}
}

func TestEncodeAudioDataURLCase(t *testing.T) {
	req, err := ParseRequest([]byte(`{"model":"m","messages":[{"role":"user","content":[
		{"type":"text","text":"transcribe"},
		{"type":"file","file":{"file_data":"DATA:audio/wav;BASE64,SUQzBA=="}}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	body, losses, err := EncodeRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"input_audio":{"data":"SUQzBA==","format":"wav"}`) {
		t.Errorf("body %s, want input_audio", body)
	}
	if len(losses) != 0 {
		t.Errorf("losses = %+v, want none", losses)
	}
}

func TestEncodeAudioMediaTypeCase(t *testing.T) {
	encode := func(fileData string) string {
		t.Helper()
		req, err := ParseRequest([]byte(`{"model":"m","messages":[{"role":"user","content":[
			{"type":"text","text":"transcribe"},
			{"type":"file","file":{"file_data":"` + fileData + `"}}]}]}`))
		if err != nil {
			t.Fatalf("ParseRequest(%q): %v", fileData, err)
		}
		body, losses, err := EncodeRequest(req)
		if err != nil {
			t.Fatalf("EncodeRequest(%q): %v", fileData, err)
		}
		if len(losses) != 0 {
			t.Fatalf("EncodeRequest(%q): losses = %+v, want none", fileData, losses)
		}
		return string(body)
	}
	for _, c := range []struct {
		lower string
		mixed string
	}{
		{"data:audio/wav;base64,SUQzBA==", "data:AUDIO/WAV;base64,SUQzBA=="},
		{"data:audio/mpeg;base64,SUQzBA==", "data:Audio/MPEG;base64,SUQzBA=="},
		{"data:audio/ogg;base64,SUQzBA==", "DATA:AUDIO/OGG;BASE64,SUQzBA=="},
	} {
		want := encode(c.lower)
		got := encode(c.mixed)
		if got != want {
			t.Errorf("file_data %q:\n got  %s\n want %s", c.mixed, got, want)
		}
	}
}

// Package responsesinput reads the content of Responses input items: a message's parts and a tool
// output's. It is shared by the Responses parser and by the code that sends a Responses client's
// custom tool outputs to other formats (format/internal/clienttools).
package responsesinput

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/stdotwork/pluxy/next/format/internal/audioinput"
	"github.com/stdotwork/pluxy/next/format/internal/rawjson"
	"github.com/stdotwork/pluxy/next/llm"
)

const format = llm.FormatOpenAIResponses

// ToolOutput reads a tool output item's output field into it: a string, or a list of parts whose
// text is joined in Output and which are kept in Content when they hold more than text. It reports
// whether it could read the field.
func ToolOutput(it *llm.Item, raw json.RawMessage, path string) bool {
	if json.Unmarshal(raw, &it.Output) == nil {
		return true
	}
	parts, text, ok := OutputParts(raw, path+".output")
	if !ok {
		return false
	}
	// A list of text parts with nothing else in them is the result's text; any other list is
	// carried as parts too.
	it.Output = text
	if !PlainTexts(parts) {
		it.Content = parts
	}
	return true
}

// OutputParts reads a function_call_output's list of parts, with its text parts joined.
func OutputParts(raw json.RawMessage, path string) ([]llm.Part, string, bool) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.TrimSpace(raw)[0] != '[' {
		return nil, "", false
	}
	parts, _, err := Content(raw, path)
	if err != nil {
		return nil, "", false
	}
	var text strings.Builder
	for _, p := range parts {
		if p.Type == llm.PartText {
			text.WriteString(p.Text)
		}
	}
	return parts, text.String(), true
}

// PlainTexts reports whether every part is text with no fields of its own beyond its text.
func PlainTexts(parts []llm.Part) bool {
	for _, p := range parts {
		if p.Type != llm.PartText || (p.Origin != nil && len(p.Origin.Raw) > 0) {
			return false
		}
	}
	return true
}

// Content reads message content: a string (textContent true) or a list of parts.
func Content(raw json.RawMessage, path string) (parts []llm.Part, textContent bool, err error) {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, false, nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return []llm.Part{{Type: llm.PartText, Text: s}}, true, nil
	}
	var rawParts []json.RawMessage
	if err := json.Unmarshal(raw, &rawParts); err != nil {
		return nil, false, fmt.Errorf("content must be a string or a list of parts")
	}
	for i, rp := range rawParts {
		pp := fmt.Sprintf("%s[%d]", path, i)
		m := rawjson.Object(rp)
		var p struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			ImageURL string `json:"image_url"`
			Detail   string `json:"detail"`
			FileID   string `json:"file_id"`
			FileData string `json:"file_data"`
			FileURL  string `json:"file_url"`
			Filename string `json:"filename"`
			// input_audio: the flat spelling, or data and format inside input_audio.
			Data   string `json:"data"`
			Format string `json:"format"`
			Audio  *struct {
				Data   string `json:"data"`
				Format string `json:"format"`
			} `json:"input_audio"`
		}
		if m == nil || json.Unmarshal(rp, &p) != nil {
			return nil, false, fmt.Errorf("content[%d] must be an object", i)
		}
		switch p.Type {
		case "input_text", "output_text":
			modelled := []string{"type", "text"}
			// Empty annotations and logprobs on a replayed output say nothing.
			for _, k := range []string{"annotations", "logprobs"} {
				if v, ok := m[k]; ok && (bytes.Equal(v, []byte("[]")) || bytes.Equal(v, []byte("null"))) {
					modelled = append(modelled, k)
				}
			}
			parts = append(parts, llm.Part{Type: llm.PartText, Text: p.Text, Origin: rawjson.Origin(format, pp, m, modelled, nil)})
		case "input_image":
			parts = append(parts, llm.Part{Type: llm.PartImage, URL: p.ImageURL, Detail: p.Detail, FileID: p.FileID,
				Origin: rawjson.Origin(format, pp, m, []string{"type", "image_url", "detail", "file_id"}, nil)})
		case "input_file":
			parts = append(parts, llm.Part{Type: llm.PartFile, FileID: p.FileID, FileData: p.FileData, URL: p.FileURL, Filename: p.Filename,
				Origin: rawjson.Origin(format, pp, m, []string{"type", "file_id", "file_data", "file_url", "filename"}, nil)})
		case "input_audio":
			data, audioFormat := p.Data, p.Format
			if p.Audio != nil {
				if data == "" {
					data = p.Audio.Data
				}
				if audioFormat == "" {
					audioFormat = p.Audio.Format
				}
			}
			if data == "" {
				// No bytes to carry; kept whole so a Responses target sends it as it came.
				parts = append(parts, llm.Part{Type: llm.PartNative, Format: format, Data: append(json.RawMessage(nil), rp...),
					Origin: &llm.Origin{Format: format, Path: pp}})
				break
			}
			// OpenAI's InputAudio nests data and format inside input_audio, which is how the
			// encoder writes it; the flat spelling today's clients also send is read too. The
			// part's other fields are not kept.
			parts = append(parts, llm.Part{Type: llm.PartFile, FileData: audioinput.DataURL(audioinput.MIME(audioFormat), data),
				Origin: rawjson.Origin(format, pp, m, []string{"type", "data", "format", "input_audio"}, nil)})
		default:
			parts = append(parts, llm.Part{Type: llm.PartNative, Format: format, Data: append(json.RawMessage(nil), rp...),
				Origin: &llm.Origin{Format: format, Path: pp}})
		}
	}
	return parts, false, nil
}

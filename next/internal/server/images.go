package server

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"

	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/llm"
)

// defaultImageModel is the model of an Images API request that names none, as in today's core.
const defaultImageModel = "gpt-image-2"

// imagesGenerationsEndpoint is OpenAI's Images API generation. A request without a prompt is
// refused before it is routed, as today's core refuses it.
var imagesGenerationsEndpoint = endpoint{
	format:       llm.FormatOpenAIImagesGenerations,
	headers:      openairesponses.Headers,
	defaultModel: defaultImageModel,
	check:        func(body map[string]json.RawMessage, _ bool) *llm.Error { return needPrompt(body) },
}

// imagesEditsEndpoint is OpenAI's Images API edit, in JSON or as multipart/form-data, which needs
// a prompt and, as a form, an image file.
var imagesEditsEndpoint = endpoint{
	format:       llm.FormatOpenAIImagesEdits,
	headers:      openairesponses.Headers,
	form:         true,
	defaultModel: defaultImageModel,
	check: func(body map[string]json.RawMessage, fromForm bool) *llm.Error {
		if e := needPrompt(body); e != nil {
			return e
		}
		var images []struct {
			ImageURL string `json:"image_url"`
		}
		_ = json.Unmarshal(body["images"], &images)
		if fromForm && len(images) == 0 {
			return badImagesRequest("image is required")
		}
		return nil
	},
}

func needPrompt(body map[string]json.RawMessage) *llm.Error {
	var prompt string
	_ = json.Unmarshal(body["prompt"], &prompt)
	if strings.TrimSpace(prompt) == "" {
		return badImagesRequest("prompt is required")
	}
	return nil
}

func badImagesRequest(what string) *llm.Error {
	return &llm.Error{Code: llm.CodeInvalidRequest, Status: http.StatusBadRequest, Scope: llm.ScopeRequest, Message: "Invalid request: " + what}
}

// mediaType returns a Content-Type's media type in lower case, without parameters.
func mediaType(contentType string) string {
	mt, _, _ := strings.Cut(contentType, ";")
	return strings.ToLower(strings.TrimSpace(mt))
}

// formJSON reads a multipart/form-data endpoint body into its JSON form. For an Images API edit,
// as today's core sends it to Codex, each field is a member (n, output_compression and
// partial_images as numbers, stream as a boolean, a repeated field as a list, mask[file_id] and
// mask[image_url] inside mask), the image files (image[], else image) are appended to images as
// data URLs, and the first mask file becomes mask.image_url. An audio file named file becomes
// {filename, content_type, data}, where data is the file's bytes in base64.
func formJSON(contentType string, body []byte) (json.RawMessage, *llm.Error) {
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil || params["boundary"] == "" {
		return nil, badImagesRequest("the multipart body has no boundary")
	}
	// The body is in memory already (at most MaxRequestBody), so the form is too.
	form, err := multipart.NewReader(bytes.NewReader(body), params["boundary"]).ReadForm(MaxRequestBody)
	if err != nil {
		return nil, badImagesRequest(err.Error())
	}
	defer func() { _ = form.RemoveAll() }()
	out := map[string]any{}
	mask := map[string]any{}
	for key, values := range form.Value {
		key = strings.TrimSpace(key)
		if key == "" || len(values) == 0 {
			continue
		}
		vals := make([]any, len(values))
		for i, v := range values {
			vals[i] = formValue(key, v)
		}
		var v any = vals
		if len(vals) == 1 {
			v = vals[0]
		}
		switch key {
		case "mask[file_id]":
			mask["file_id"] = v
		case "mask[image_url]":
			mask["image_url"] = v
		default:
			out[key] = v
		}
	}
	if files := form.File["mask"]; len(files) > 0 {
		u, err := dataURL(files[0])
		if err != nil {
			return nil, badImagesRequest(err.Error())
		}
		mask["image_url"] = u
	}
	if len(mask) > 0 {
		out["mask"] = mask
	}
	if files := form.File["file"]; len(files) > 0 {
		vals := make([]any, len(files))
		for i, fh := range files {
			v, err := formFileValue(fh)
			if err != nil {
				return nil, badImagesRequest(err.Error())
			}
			vals[i] = v
		}
		if len(vals) == 1 {
			out["file"] = vals[0]
		} else {
			out["file"] = vals
		}
	}
	files := form.File["image[]"]
	if len(files) == 0 {
		files = form.File["image"]
	}
	if len(files) > 0 {
		var images []any
		if prev, ok := out["images"].([]any); ok {
			images = prev
		} else if prev, ok := out["images"]; ok {
			images = []any{prev}
		}
		for _, fh := range files {
			u, err := dataURL(fh)
			if err != nil {
				return nil, badImagesRequest(err.Error())
			}
			images = append(images, map[string]string{"image_url": u})
		}
		out["images"] = images
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, badImagesRequest(err.Error())
	}
	return b, nil
}

// formValue is a form field's value in JSON: a number for the fields that are numbers, a boolean
// for stream, a string otherwise.
func formValue(key, v string) any {
	v = strings.TrimSpace(v)
	switch key {
	case "n", "output_compression", "partial_images":
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	case "stream":
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return v
}

// dataURL returns an uploaded file as a data URL.
func dataURL(fh *multipart.FileHeader) (string, error) {
	data, mt, err := upload(fh)
	if err != nil {
		return "", err
	}
	return "data:" + mt + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

// formFile is an uploaded file in a form's JSON form.
type formFile struct {
	Filename    string `json:"filename,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	Data        string `json:"data"`
}

// formFileValue returns an uploaded file in the JSON form of an audio request.
func formFileValue(fh *multipart.FileHeader) (formFile, error) {
	data, mt, err := upload(fh)
	if err != nil {
		return formFile{}, err
	}
	return formFile{Filename: fh.Filename, ContentType: mt, Data: base64.StdEncoding.EncodeToString(data)}, nil
}

// upload reads an uploaded file and returns its bytes and media type.
func upload(fh *multipart.FileHeader) ([]byte, string, error) {
	f, err := fh.Open()
	if err != nil {
		return nil, "", fmt.Errorf("reading the upload %q: %w", fh.Filename, err)
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, "", fmt.Errorf("reading the upload %q: %w", fh.Filename, err)
	}
	mt := strings.TrimSpace(fh.Header.Get("Content-Type"))
	if mt == "" {
		mt = http.DetectContentType(data)
	}
	return data, mt, nil
}

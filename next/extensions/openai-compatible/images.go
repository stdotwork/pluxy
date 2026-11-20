package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/sdk"
)

// images sends OpenAI's Images API through an OpenAI-compatible account. A generation goes as
// JSON. An edit whose images reached core as multipart is rebuilt as multipart with the same
// fields and file bytes, because core gives the extension the form's JSON form (the files are
// data URLs). An edit that names an image by URL or file ID stays JSON. The upstream's reply
// body and content type come back unchanged.
func images(ctx context.Context, call sdk.Call, out *sdk.Stream) error {
	s, wire, key, err := account(call)
	if err != nil {
		return err
	}
	if err := requireOpenAIWire(wire, "images"); err != nil {
		return err
	}
	path := "/images/generations"
	if call.Request.Native != nil && call.Request.Native.Format == llm.FormatOpenAIImagesEdits {
		path = "/images/edits"
	}
	if call.Request.Native == nil {
		return &llm.Error{Code: llm.CodeInvalidRequest, Message: "the Images API request has no native body", Scope: llm.ScopeRequest}
	}
	body, contentType, err := imageBody(call)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(s.BaseURL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return &llm.Error{Code: llm.CodeInvalidRequest, Message: "bad base_url: " + err.Error(), Scope: llm.ScopeCredential}
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer "+key)
	if call.Request.Stream {
		req.Header.Set("Accept", "text/event-stream")
	} else {
		req.Header.Set("Accept", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &llm.Error{Code: llm.CodeUnavailable, Message: "upstream unreachable: " + err.Error(), Scope: llm.ScopeEndpoint}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return upstreamError(resp, call.Request.Model)
	}
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/json"
		if call.Request.Stream {
			ct = "text/event-stream"
		}
	}
	if err := out.StartBody(call.Request.Native.Format, ct); err != nil {
		return err
	}
	bw := out.BodyWriter()
	if _, err := io.Copy(bw, resp.Body); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &llm.Error{Code: llm.CodeUpstream, Message: "reading upstream response: " + err.Error(), Scope: llm.ScopeEndpoint}
	}
	if err := bw.Close(); err != nil {
		return err
	}
	return out.Finish(llm.Finish{Reason: llm.FinishStop}, nil)
}

// imageBody returns the body and content type to send upstream. Generations and JSON edits keep
// their JSON body, changing only the model when routing renamed it. A multipart edit is rebuilt
// as multipart from core's JSON form: scalar and repeated fields keep their names and values, and
// data-URL images and mask become file parts with their original bytes and media type.
func imageBody(call sdk.Call) ([]byte, string, error) {
	var body map[string]json.RawMessage
	if err := json.Unmarshal(call.Request.Native.Body, &body); err != nil || body == nil {
		return nil, "", &llm.Error{Code: llm.CodeInvalidRequest, Message: "the Images API request body is not a JSON object", Scope: llm.ScopeRequest}
	}
	if call.Request.Native.Format == llm.FormatOpenAIImagesEdits {
		if multipartBody, ok, err := buildImageEdit(body, call.Request.Model); err != nil {
			return nil, "", err
		} else if ok {
			return multipartBody.body, multipartBody.contentType, nil
		}
	}
	out, changed, err := withImageModel(call.Request.Native.Body, body, call.Request.Model)
	if err != nil {
		return nil, "", err
	}
	if changed {
		return out, "application/json", nil
	}
	return call.Request.Native.Body, "application/json", nil
}

// multipartImage is a multipart Images API edit body and the content type naming its boundary.
type multipartImage struct {
	body        []byte
	contentType string
}

// buildImageEdit returns a multipart body when every image reference is a data URL, such as the
// files core lifted out of a multipart request. It returns ok false for a JSON edit naming images
// by URL or file ID, which is sent as JSON.
func buildImageEdit(body map[string]json.RawMessage, model string) (multipartImage, bool, error) {
	images, err := imageParts(body["images"])
	if err != nil {
		return multipartImage{}, false, err
	}
	if len(images) == 0 {
		return multipartImage{}, false, nil
	}
	for _, image := range images {
		if _, _, ok := parseDataURL(image.url); !ok {
			return multipartImage{}, false, nil
		}
	}
	var mask *imagePart
	if raw := body["mask"]; len(raw) > 0 && string(raw) != "null" {
		var m struct {
			FileID   string `json:"file_id"`
			ImageURL string `json:"image_url"`
		}
		if err := json.Unmarshal(raw, &m); err != nil {
			return multipartImage{}, false, &llm.Error{Code: llm.CodeInvalidRequest, Message: "the Images API mask is not an object", Scope: llm.ScopeRequest}
		}
		if m.ImageURL != "" {
			if _, _, ok := parseDataURL(m.ImageURL); !ok {
				return multipartImage{}, false, nil
			}
			mask = &imagePart{url: m.ImageURL}
		} else if m.FileID != "" {
			return multipartImage{}, false, nil
		}
	}

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	if model != "" {
		if err := writer.WriteField("model", model); err != nil {
			return multipartImage{}, false, err
		}
	}
	for name, raw := range body {
		switch name {
		case "model", "images", "mask":
			continue
		}
		if err := writeImageFormField(writer, name, raw); err != nil {
			return multipartImage{}, false, err
		}
	}
	imageField := "image"
	if len(images) > 1 {
		imageField = "image[]"
	}
	for i, image := range images {
		mediaType, data, _ := parseDataURL(image.url)
		if err := writeImageFile(writer, imageField, fmt.Sprintf("image-%d%s", i+1, extensionForMediaType(mediaType)), mediaType, data); err != nil {
			return multipartImage{}, false, err
		}
	}
	if mask != nil {
		mediaType, data, _ := parseDataURL(mask.url)
		if err := writeImageFile(writer, "mask", "mask"+extensionForMediaType(mediaType), mediaType, data); err != nil {
			return multipartImage{}, false, err
		}
	}
	if err := writer.Close(); err != nil {
		return multipartImage{}, false, err
	}
	return multipartImage{body: buf.Bytes(), contentType: writer.FormDataContentType()}, true, nil
}

type imagePart struct {
	url string
}

func imageParts(raw json.RawMessage) ([]imagePart, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var values []struct {
		FileID   string `json:"file_id"`
		ImageURL string `json:"image_url"`
	}
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, &llm.Error{Code: llm.CodeInvalidRequest, Message: "the Images API images are not a list", Scope: llm.ScopeRequest}
	}
	out := make([]imagePart, 0, len(values))
	for _, v := range values {
		switch {
		case v.ImageURL != "":
			out = append(out, imagePart{url: v.ImageURL})
		case v.FileID != "":
			out = append(out, imagePart{})
		}
	}
	return out, nil
}

// writeImageFormField writes a JSON value as the form fields that carry it: scalar values once,
// arrays as repeated fields. A nested object is not a form field, so it is sent as JSON text.
func writeImageFormField(writer *multipart.Writer, name string, raw json.RawMessage) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var values []json.RawMessage
	if err := json.Unmarshal(raw, &values); err == nil {
		for _, value := range values {
			if err := writeImageFormValue(writer, name, value); err != nil {
				return err
			}
		}
		return nil
	}
	return writeImageFormValue(writer, name, raw)
}

func writeImageFormValue(writer *multipart.Writer, name string, raw json.RawMessage) error {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return writer.WriteField(name, s)
	}
	return writer.WriteField(name, string(raw))
}

func writeImageFile(writer *multipart.Writer, field, filename, mediaType string, data []byte) error {
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", multipart.FileContentDisposition(field, filename))
	if mediaType == "" {
		mediaType = "application/octet-stream"
	}
	h.Set("Content-Type", mediaType)
	part, err := writer.CreatePart(h)
	if err != nil {
		return err
	}
	_, err = part.Write(data)
	return err
}

func parseDataURL(s string) (mediaType string, data []byte, ok bool) {
	rest, found := strings.CutPrefix(s, "data:")
	if !found {
		return "", nil, false
	}
	meta, payload, found := strings.Cut(rest, ",")
	if !found {
		return "", nil, false
	}
	parts := strings.Split(meta, ";")
	mediaType = "application/octet-stream"
	if parts[0] != "" {
		mediaType = parts[0]
	}
	base64Encoded := false
	for _, p := range parts[1:] {
		if strings.EqualFold(strings.TrimSpace(p), "base64") {
			base64Encoded = true
		}
	}
	if !base64Encoded {
		return "", nil, false
	}
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return "", nil, false
	}
	return mediaType, data, true
}

func extensionForMediaType(mediaType string) string {
	exts, err := mime.ExtensionsByType(mediaType)
	if err == nil && len(exts) > 0 {
		return exts[0]
	}
	return ""
}

// withImageModel returns body with model in place, or changed false when it already names the
// model routing chose. The original bytes are kept in that case.
func withImageModel(original []byte, body map[string]json.RawMessage, model string) ([]byte, bool, error) {
	if model == "" {
		return original, false, nil
	}
	var current string
	_ = json.Unmarshal(body["model"], &current)
	if current == model {
		return original, false, nil
	}
	raw, err := json.Marshal(model)
	if err != nil {
		return nil, false, err
	}
	body["model"] = raw
	out, err := json.Marshal(body)
	if err != nil {
		return nil, false, err
	}
	return out, true, nil
}

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

func imageCall(format, model, body string) sdk.Call {
	return sdk.Call{
		Request: llm.Request{
			Model: model,
			Native: &llm.Native{
				Format: format,
				Body:   json.RawMessage(body),
			},
		},
	}
}

func TestImageBodyRebuildsMultipartEdit(t *testing.T) {
	call := imageCall(llm.FormatOpenAIImagesEdits, "upstream-image", `{
		"model":"client-alias",
		"prompt":"add a hat",
		"n":2,
		"output_format":"png",
		"images":[
			{"image_url":"data:image/png;base64,UE5HMQ=="},
			{"image_url":"data:image/webp;base64,V0VCUDI="}
		],
		"mask":{"image_url":"data:image/png;base64,TUFTSw=="}
	}`)
	body, contentType, err := imageBody(call)
	if err != nil {
		t.Fatalf("imageBody: %v", err)
	}
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil || mediaType != "multipart/form-data" || params["boundary"] == "" {
		t.Fatalf("Content-Type = %q: %v", contentType, err)
	}
	form, err := multipart.NewReader(bytes.NewReader(body), params["boundary"]).ReadForm(1 << 20)
	if err != nil {
		t.Fatalf("reading rebuilt form: %v", err)
	}
	defer form.RemoveAll()
	for field, want := range map[string]string{"model": "upstream-image", "prompt": "add a hat", "n": "2", "output_format": "png"} {
		if got := form.Value[field]; len(got) != 1 || got[0] != want {
			t.Errorf("field %s = %v, want [%s]", field, got, want)
		}
	}
	images := form.File["image[]"]
	if len(images) != 2 {
		t.Fatalf("image files = %d, want 2", len(images))
	}
	for i, want := range []string{"PNG1", "WEBP2"} {
		f, err := images[i].Open()
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(f)
		f.Close()
		if string(data) != want {
			t.Errorf("image %d = %q, want %q", i, data, want)
		}
	}
	masks := form.File["mask"]
	if len(masks) != 1 {
		t.Fatalf("mask files = %d, want 1", len(masks))
	}
	mask, err := masks[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	maskData, _ := io.ReadAll(mask)
	mask.Close()
	if string(maskData) != "MASK" {
		t.Errorf("mask = %q, want MASK", maskData)
	}
}

func TestImageBodyRebuildsMultipartEditDataURLCase(t *testing.T) {
	call := imageCall(llm.FormatOpenAIImagesEdits, "upstream-image", `{
		"model":"client-alias",
		"prompt":"add a hat",
		"images":[
			{"image_url":"DATA:image/png;base64,UE5HMQ=="},
			{"image_url":"Data:image/webp;base64,V0VCUDI="}
		],
		"mask":{"image_url":"DaTa:image/png;base64,TUFTSw=="}
	}`)
	body, contentType, err := imageBody(call)
	if err != nil {
		t.Fatalf("imageBody: %v", err)
	}
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil || mediaType != "multipart/form-data" || params["boundary"] == "" {
		t.Fatalf("Content-Type = %q, want multipart/form-data: %v", contentType, err)
	}
	form, err := multipart.NewReader(bytes.NewReader(body), params["boundary"]).ReadForm(1 << 20)
	if err != nil {
		t.Fatalf("reading rebuilt form: %v", err)
	}
	defer form.RemoveAll()
	for field, want := range map[string]string{"model": "upstream-image", "prompt": "add a hat"} {
		if got := form.Value[field]; len(got) != 1 || got[0] != want {
			t.Errorf("field %s = %v, want [%s]", field, got, want)
		}
	}
	images := form.File["image[]"]
	if len(images) != 2 {
		t.Fatalf("image files = %d, want 2", len(images))
	}
	for i, want := range []string{"PNG1", "WEBP2"} {
		f, err := images[i].Open()
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(f)
		f.Close()
		if string(data) != want {
			t.Errorf("image %d = %q, want %q", i, data, want)
		}
	}
	masks := form.File["mask"]
	if len(masks) != 1 {
		t.Fatalf("mask files = %d, want 1", len(masks))
	}
	mask, err := masks[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	maskData, _ := io.ReadAll(mask)
	mask.Close()
	if string(maskData) != "MASK" {
		t.Errorf("mask = %q, want MASK", maskData)
	}
}

func TestImageBodyJSONEditKeepsURL(t *testing.T) {
	call := imageCall(llm.FormatOpenAIImagesEdits, "upstream-image", `{"model":"client-alias","prompt":"p","images":[{"image_url":"https://example.com/a.png"}]}`)
	body, contentType, err := imageBody(call)
	if err != nil {
		t.Fatalf("imageBody: %v", err)
	}
	if contentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", contentType)
	}
	var sent struct {
		Model  string `json:"model"`
		Images []struct {
			ImageURL string `json:"image_url"`
		} `json:"images"`
	}
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatalf("body %s: %v", body, err)
	}
	if sent.Model != "upstream-image" || len(sent.Images) != 1 || sent.Images[0].ImageURL != "https://example.com/a.png" {
		t.Errorf("body = %s, want upstream-image and the original image URL", body)
	}
}

func TestImageBodyGenerationKeepsBytes(t *testing.T) {
	const body = `{"model":"image-model","prompt":"a cat","x_vendor":{"keep":true}}`
	call := imageCall(llm.FormatOpenAIImagesGenerations, "image-model", body)
	got, contentType, err := imageBody(call)
	if err != nil {
		t.Fatalf("imageBody: %v", err)
	}
	if contentType != "application/json" || string(got) != body {
		t.Errorf("body = %q (%s), want unchanged %q as application/json", got, contentType, body)
	}
}

func TestImagesRefusesWiresWithoutImages(t *testing.T) {
	formats := []struct {
		name   string
		format string
		body   string
	}{
		{"generations", llm.FormatOpenAIImagesGenerations, `{"model":"image-model","prompt":"a cat"}`},
		{"edits", llm.FormatOpenAIImagesEdits, `{"model":"image-model","prompt":"add a hat","images":[{"image_url":"https://example.com/a.png"}]}`},
	}
	for _, wire := range []string{wireMessages, wireGemini} {
		for _, format := range formats {
			t.Run(wire+"/"+format.name, func(t *testing.T) {
				srv, recorded := newCompactUpstream(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
					w.WriteHeader(http.StatusInternalServerError)
				})
				call := imageCall(format.format, "image-model", format.body)
				call.Account = protocol.Account{
					ID:         "compat-images",
					Settings:   json.RawMessage(`{"base_url":"` + srv.URL + `/v1","wire_api":"` + wire + `"}`),
					Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "sk-test"},
				}

				err := images(context.Background(), call, nil)
				var e *llm.Error
				if !errors.As(err, &e) || e.Code != llm.CodeUnsupported || e.Scope != llm.ScopeCredential || !e.Retryable {
					t.Fatalf("err = %+v; want retryable CodeUnsupported on ScopeCredential", err)
				}
				if want := formatNames[wires[wire].format]; !strings.Contains(e.Message, want) {
					t.Errorf("error message = %q, want it to name %q", e.Message, want)
				}
				if got := recorded(); len(got) != 0 {
					t.Errorf("upstream got %d requests, want 0", len(got))
				}
			})
		}
	}
}

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
	"sort"
	"strings"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/sdk"
)

// audio serves OpenAI's speech, transcription and translation endpoints through an account.
// Speech sends the client's JSON body unchanged. Transcriptions and translations rebuild the
// multipart form from the JSON form core read. The upstream reply keeps its body and Content-Type;
// a non-SSE speech reply travels as base64 pieces, as llm.FormatOpenAIAudioSpeech describes.
func audio(ctx context.Context, call sdk.Call, out *sdk.Stream) error {
	if call.Request.Native == nil {
		return &llm.Error{Code: llm.CodeInvalidRequest, Message: "the request has no endpoint body", Scope: llm.ScopeRequest}
	}
	s, wire, key, err := account(call)
	if err != nil {
		return err
	}
	format := call.Request.Native.Format
	var (
		path        string
		requestBody []byte
		contentType string
	)
	switch format {
	case llm.FormatOpenAIAudioSpeech:
		path, requestBody, contentType = "/audio/speech", call.Request.Native.Body, "application/json"
	case llm.FormatOpenAIAudioTranscriptions:
		path = "/audio/transcriptions"
		requestBody, contentType, err = audioMultipart(call.Request.Native.Body)
	case llm.FormatOpenAIAudioTranslations:
		path = "/audio/translations"
		requestBody, contentType, err = audioMultipart(call.Request.Native.Body)
	default:
		return &llm.Error{Code: llm.CodeUnsupported, Scope: llm.ScopeCredential, Retryable: true,
			Message: fmt.Sprintf("the OpenAI-compatible extension does not serve %q", format)}
	}
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(s.BaseURL, "/")+path, bytes.NewReader(requestBody))
	if err != nil {
		return &llm.Error{Code: llm.CodeInvalidRequest, Message: "bad base_url: " + err.Error(), Scope: llm.ScopeCredential}
	}
	h := req.Header
	h.Set("Content-Type", contentType)
	if call.Request.Stream {
		h.Set("Accept", "text/event-stream")
	}
	setKey(req, wire, key, call.Request.Native)
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return requestFailed(ctx, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return audioEndpointStatusError(resp, call.Request.Model)
	}
	replyType := resp.Header.Get("Content-Type")
	if replyType == "" {
		replyType = "application/octet-stream"
		if format != llm.FormatOpenAIAudioSpeech {
			replyType = "application/json"
		}
	}
	if err := out.StartBody(format, replyType); err != nil {
		return err
	}
	bw := out.BodyWriter()
	if format == llm.FormatOpenAIAudioSpeech && !strings.HasPrefix(replyType, "text/event-stream") {
		enc := base64.NewEncoder(base64.StdEncoding, bw)
		if _, err := io.Copy(enc, resp.Body); err != nil {
			_ = enc.Close()
			_ = bw.Close()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return &llm.Error{Code: llm.CodeUpstream, Message: "reading upstream response: " + err.Error(), Scope: llm.ScopeEndpoint}
		}
		if err := enc.Close(); err != nil {
			return err
		}
	} else if _, err := io.Copy(bw, resp.Body); err != nil {
		_ = bw.Close()
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

// audioEndpointStatusError is upstreamError for an audio endpoint: a 404 that does not name a
// missing model means the upstream has no such endpoint, not that the account lacks the model.
func audioEndpointStatusError(resp *http.Response, model string) *llm.Error {
	e := upstreamError(resp, model)
	if e.Code == llm.CodeNotFound && !e.Retryable {
		e.Scope = llm.ScopeRequest
	}
	return e
}

// audioMultipart rebuilds a transcription or translation's multipart body from the JSON form core
// read. Repeated fields become repeated form fields, the file keeps its filename and media type,
// and every other value keeps its text form.
func audioMultipart(raw json.RawMessage) ([]byte, string, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var fields map[string]any
	if err := dec.Decode(&fields); err != nil || fields == nil {
		return nil, "", &llm.Error{Code: llm.CodeInvalidRequest, Message: "the audio request form is not a JSON object", Scope: llm.ScopeRequest}
	}
	var file struct {
		Filename    string `json:"filename"`
		ContentType string `json:"content_type"`
		Data        string `json:"data"`
	}
	if err := json.Unmarshal(rawField(fields, "file"), &file); err != nil || file.Data == "" {
		return nil, "", &llm.Error{Code: llm.CodeInvalidRequest, Message: "the audio request needs its file", Scope: llm.ScopeRequest}
	}
	data, err := base64.StdEncoding.DecodeString(file.Data)
	if err != nil {
		return nil, "", &llm.Error{Code: llm.CodeInvalidRequest, Message: "the audio request's file is not base64", Scope: llm.ScopeRequest}
	}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	keys := make([]string, 0, len(fields))
	for key := range fields {
		if key != "file" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		if err := writeAudioField(mw, key, fields[key]); err != nil {
			return nil, "", err
		}
	}
	header := make(textproto.MIMEHeader)
	disposition := mime.FormatMediaType("form-data", map[string]string{"name": "file", "filename": file.Filename})
	if disposition == "" {
		return nil, "", &llm.Error{Code: llm.CodeInvalidRequest, Message: "the audio request's file name is invalid", Scope: llm.ScopeRequest}
	}
	header.Set("Content-Disposition", disposition)
	if file.ContentType != "" {
		header.Set("Content-Type", file.ContentType)
	}
	part, err := mw.CreatePart(header)
	if err != nil {
		return nil, "", &llm.Error{Code: llm.CodeInternal, Message: "building the audio request: " + err.Error()}
	}
	if _, err := part.Write(data); err != nil {
		return nil, "", &llm.Error{Code: llm.CodeInternal, Message: "building the audio request: " + err.Error()}
	}
	if err := mw.Close(); err != nil {
		return nil, "", &llm.Error{Code: llm.CodeInternal, Message: "building the audio request: " + err.Error()}
	}
	return body.Bytes(), mw.FormDataContentType(), nil
}

func rawField(m map[string]any, key string) json.RawMessage {
	b, _ := json.Marshal(m[key])
	return b
}

func writeAudioField(mw *multipart.Writer, key string, value any) error {
	if values, ok := value.([]any); ok {
		for _, v := range values {
			if err := writeAudioField(mw, key, v); err != nil {
				return err
			}
		}
		return nil
	}
	if value == nil {
		return nil
	}
	var text string
	switch v := value.(type) {
	case string:
		text = v
	case json.Number:
		text = v.String()
	case bool:
		text = fmt.Sprint(v)
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return &llm.Error{Code: llm.CodeInvalidRequest, Message: fmt.Sprintf("the audio request field %q is not a form value", key), Scope: llm.ScopeRequest}
		}
		text = string(b)
	}
	if err := mw.WriteField(key, text); err != nil {
		return &llm.Error{Code: llm.CodeInternal, Message: "building the audio request: " + err.Error()}
	}
	return nil
}

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/stdotwork/pluxy/next/format/geminigenerate"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

// maxImagesReply limits what is read of a non-streamed GenerateContent reply.
const maxImagesReply = 64 << 20

// images serves OpenAI Images API generations through one non-streamed GenerateContent call.
// Gemini's imageConfig has no image count, so n above one is refused; the documented response
// may also hold fewer images than a prompt asks for.
func images(ctx context.Context, call sdk.Call, out *sdk.Stream) (err error) {
	req, err := readImagesRequest(call.Request)
	if err != nil {
		return err
	}
	s, err := readSettings(call.Account.Settings)
	if err != nil {
		return err
	}
	t, err := resolve(ctx, s, call.Account.Credential, call.Request.Model, false)
	if err != nil {
		return err
	}
	send := &reporting{Stream: out}
	if t.token != "" {
		// Core redacts what the credential holds; the fetched access token is redacted here too.
		send.redact = protocolRedactor(t.token)
		defer func() {
			if e, ok := err.(*llm.Error); ok {
				err = send.redact.Event(llm.Event{Type: llm.EventError, Error: e}).Error
			}
		}()
	}
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	resp, err := postBody(ctx, t, body, false, call.Request.Model)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		return &llm.Error{Code: llm.CodeUpstream, Scope: llm.ScopeEndpoint, Message: "Gemini streamed an image generation reply"}
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxImagesReply+1))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &llm.Error{Code: llm.CodeUpstream, Scope: llm.ScopeEndpoint, Message: "reading upstream response: " + err.Error()}
	}
	if len(raw) > maxImagesReply {
		return &llm.Error{Code: llm.CodeUpstream, Scope: llm.ScopeEndpoint, Message: fmt.Sprintf("the upstream's reply is larger than %d MiB", maxImagesReply>>20)}
	}
	events, err := geminigenerate.DecodeResponse(raw)
	if err != nil {
		return err
	}
	var usage *llm.Usage
	finishReason := ""
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type == llm.EventFinish {
			usage = events[i].Usage
			if events[i].Finish != nil {
				finishReason = events[i].Finish.RawReason
			}
			break
		}
	}
	data, err := imagesData(raw)
	if err != nil {
		return err
	}
	if len(data) == 0 {
		msg := "Gemini returned no image"
		if finishReason != "" {
			msg += " (finish reason " + finishReason + ")"
		}
		return &llm.Error{Code: llm.CodeUpstream, Status: http.StatusBadGateway, Scope: llm.ScopeEndpoint, Message: msg}
	}
	reply, err := json.Marshal(struct {
		Created int64 `json:"created"`
		Data    []struct {
			B64JSON string `json:"b64_json"`
		} `json:"data"`
	}{Created: time.Now().Unix(), Data: data})
	if err != nil {
		return err
	}
	if err := send.StartBody(llm.FormatOpenAIImagesGenerations, "application/json"); err != nil {
		return err
	}
	if err := send.Body(string(reply)); err != nil {
		return err
	}
	return send.Finish(llm.Finish{Reason: llm.FinishStop}, usage)
}

// protocolRedactor returns a reporting redactor for a Vertex access token fetched during the
// call, which core does not know to remove from the extension's reply.
func protocolRedactor(token string) *protocol.Redactor {
	return protocol.NewRedactor(&protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: token})
}

// readImagesRequest maps an Images API generation body to Gemini's GenerateContent body.
func readImagesRequest(r llm.Request) (struct {
	Contents []struct {
		Parts []struct {
			Text string `json:"text"`
		} `json:"parts"`
	} `json:"contents"`
	GenerationConfig struct {
		ResponseModalities []string `json:"responseModalities"`
		ImageConfig        *struct {
			AspectRatio string `json:"aspectRatio"`
		} `json:"imageConfig,omitempty"`
	} `json:"generationConfig"`
}, error) {
	var out struct {
		Contents []struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"contents"`
		GenerationConfig struct {
			ResponseModalities []string `json:"responseModalities"`
			ImageConfig        *struct {
				AspectRatio string `json:"aspectRatio"`
			} `json:"imageConfig,omitempty"`
		} `json:"generationConfig"`
	}
	if r.Native == nil {
		return out, badImageRequest("the request body is missing")
	}
	var body map[string]json.RawMessage
	if json.Unmarshal(r.Native.Body, &body) != nil || body == nil {
		return out, badImageRequest("the request body is not a JSON object")
	}
	var prompt string
	if err := json.Unmarshal(body["prompt"], &prompt); err != nil || strings.TrimSpace(prompt) == "" {
		return out, badImageRequest("prompt is required")
	}
	n := 1
	if raw, ok := body["n"]; ok && string(raw) != "null" {
		if err := json.Unmarshal(raw, &n); err != nil {
			return out, badImageRequest("n must be an integer")
		}
	}
	if n < 1 {
		return out, badImageRequest("n must be at least 1")
	}
	if n > 1 {
		return out, badImageRequest("Gemini image generation supports n=1; send separate requests for more images")
	}
	if string(body["stream"]) == "true" || r.Stream {
		return out, badImageRequest("Gemini image generation does not support streaming")
	}
	var size string
	if raw, ok := body["size"]; ok && string(raw) != "null" {
		if err := json.Unmarshal(raw, &size); err != nil {
			return out, badImageRequest("size must be a string")
		}
	}
	ratio, err := geminiImageAspectRatio(size)
	if err != nil {
		return out, err
	}
	out.Contents = append(out.Contents, struct {
		Parts []struct {
			Text string `json:"text"`
		} `json:"parts"`
	}{Parts: []struct {
		Text string `json:"text"`
	}{{Text: prompt}}})
	out.GenerationConfig.ResponseModalities = []string{"IMAGE"}
	if ratio != "" {
		out.GenerationConfig.ImageConfig = &struct {
			AspectRatio string `json:"aspectRatio"`
		}{AspectRatio: ratio}
	}
	return out, nil
}

func badImageRequest(message string) *llm.Error {
	return &llm.Error{Code: llm.CodeInvalidRequest, Status: http.StatusBadRequest, Scope: llm.ScopeRequest, Message: message}
}

// geminiImageAspectRatio maps an OpenAI image size to one of Gemini's documented ratios. auto and
// an empty size leave the choice to Gemini; any other value must be WIDTHxHEIGHT.
func geminiImageAspectRatio(size string) (string, error) {
	size = strings.TrimSpace(size)
	if size == "" || strings.EqualFold(size, "auto") {
		return "", nil
	}
	widthText, heightText, ok := strings.Cut(strings.ToLower(size), "x")
	if !ok {
		return "", badImageRequest("size must be auto or WIDTHxHEIGHT")
	}
	width, err := strconv.Atoi(widthText)
	if err != nil || width <= 0 {
		return "", badImageRequest("size must be auto or WIDTHxHEIGHT")
	}
	height, err := strconv.Atoi(heightText)
	if err != nil || height <= 0 {
		return "", badImageRequest("size must be auto or WIDTHxHEIGHT")
	}
	want := float64(width) / float64(height)
	best := ""
	bestDistance := math.Inf(1)
	for _, ratio := range []struct {
		name  string
		value float64
	}{
		{"1:1", 1}, {"1:4", 1.0 / 4}, {"4:1", 4}, {"1:8", 1.0 / 8}, {"8:1", 8},
		{"2:3", 2.0 / 3}, {"3:2", 3.0 / 2}, {"3:4", 3.0 / 4}, {"4:3", 4.0 / 3},
		{"4:5", 4.0 / 5}, {"5:4", 5.0 / 4}, {"9:16", 9.0 / 16}, {"16:9", 16.0 / 9},
		{"21:9", 21.0 / 9},
	} {
		distance := math.Abs(math.Log(ratio.value / want))
		if distance < bestDistance {
			best, bestDistance = ratio.name, distance
		}
	}
	return best, nil
}

// imagesData returns every inlineData image in a GenerateContent reply as Images API data entries.
func imagesData(raw []byte) ([]struct {
	B64JSON string `json:"b64_json"`
}, error) {
	var reply struct {
		Candidates []struct {
			Content *struct {
				Parts []struct {
					InlineData *struct {
						Data string `json:"data"`
					} `json:"inlineData"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil {
		return nil, &llm.Error{Code: llm.CodeUpstream, Status: http.StatusBadGateway, Scope: llm.ScopeEndpoint, Message: "Gemini's image reply is not JSON"}
	}
	var data []struct {
		B64JSON string `json:"b64_json"`
	}
	for _, candidate := range reply.Candidates {
		if candidate.Content == nil {
			continue
		}
		for _, part := range candidate.Content.Parts {
			if part.InlineData != nil && part.InlineData.Data != "" {
				data = append(data, struct {
					B64JSON string `json:"b64_json"`
				}{B64JSON: part.InlineData.Data})
			}
		}
	}
	return data, nil
}

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/sdk"
)

func endpointCall(ctx context.Context, call sdk.Call, out *sdk.Stream) error {
	format := ""
	if call.Request.Native != nil {
		format = call.Request.Native.Format
	}
	switch format {
	case llm.FormatOpenAIResponsesCompact:
		return compact(ctx, call, out)
	case llm.FormatOpenAIResponsesInputTokens:
		return inputTokens(ctx, call, out)
	case llm.FormatOpenAIEmbeddings:
		return embeddings(ctx, call, out)
	case llm.FormatGeminiEmbedContent, llm.FormatGeminiBatchEmbedContents:
		return geminiEmbeddings(ctx, call, out, format)
	case llm.FormatOpenAIImagesGenerations, llm.FormatOpenAIImagesEdits:
		return images(ctx, call, out)
	case llm.FormatOpenAIAudioSpeech, llm.FormatOpenAIAudioTranscriptions, llm.FormatOpenAIAudioTranslations:
		return audio(ctx, call, out)
	default:
		return &llm.Error{Code: llm.CodeUnsupported, Scope: llm.ScopeCredential, Retryable: true,
			Message: fmt.Sprintf("the OpenAI-compatible extension does not serve %q", format)}
	}
}

func requireOpenAIWire(wire wireInfo, endpoint string) error {
	switch wire.format {
	case llm.FormatOpenAIChat, llm.FormatOpenAIResponses:
		return nil
	default:
		return &llm.Error{
			Code:      llm.CodeUnsupported,
			Scope:     llm.ScopeCredential,
			Retryable: true,
			Message:   "this account's upstream speaks " + formatNames[wire.format] + ", which does not serve " + endpoint,
		}
	}
}

func embeddings(ctx context.Context, call sdk.Call, out *sdk.Stream) error {
	s, wire, key, err := account(call)
	if err != nil {
		return err
	}
	if err := requireOpenAIWire(wire, "embeddings"); err != nil {
		return err
	}
	chat := wireInfo{format: llm.FormatOpenAIChat}
	reqBody, err := embeddingsBody(call)
	if err != nil {
		return err
	}
	resp, err := postBody(ctx, call, s, chat, key, "/embeddings", reqBody)
	if err != nil {
		return embeddingNotFound(err, call.Request.Model, "/embeddings")
	}
	defer resp.Body.Close()

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/json"
	}
	if err := out.StartBody(llm.FormatOpenAIEmbeddings, contentType); err != nil {
		return err
	}
	body := out.BodyWriter()
	if _, err := io.Copy(body, resp.Body); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &llm.Error{Code: llm.CodeUpstream, Message: "reading upstream response: " + err.Error(), Scope: llm.ScopeEndpoint}
	}
	if err := body.Close(); err != nil {
		return err
	}
	return out.Finish(llm.Finish{Reason: llm.FinishStop}, nil)
}

func embeddingsBody(call sdk.Call) ([]byte, error) {
	var body map[string]json.RawMessage
	if err := json.Unmarshal(call.Request.Native.Body, &body); err != nil || body == nil {
		return nil, &llm.Error{Code: llm.CodeInvalidRequest, Message: "the Embeddings API request body is not a JSON object", Scope: llm.ScopeRequest}
	}
	out, _, err := withModel(call.Request.Native.Body, body, call.Request.Model)
	return out, err
}

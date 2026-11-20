package main

import (
	"context"
	"fmt"
	"io"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/sdk"
)

// endpointCall serves the provider endpoints this extension supports.
func endpointCall(ctx context.Context, call sdk.Call, out *sdk.Stream) error {
	format := ""
	if call.Request.Native != nil {
		format = call.Request.Native.Format
	}
	switch format {
	case llm.FormatOpenAIResponsesCompact:
		return compact(ctx, call, out)
	case llm.FormatOpenAIEmbeddings:
		return embeddings(ctx, call, out)
	case llm.FormatOpenAIImagesGenerations, llm.FormatOpenAIImagesEdits:
		return images(ctx, call, out)
	default:
		return &llm.Error{Code: llm.CodeUnsupported, Scope: llm.ScopeCredential, Retryable: true,
			Message: fmt.Sprintf("the OpenAI-compatible extension does not serve %q", format)}
	}
}

// requireOpenAIWire refuses an endpoint the upstream's Chat or Responses wire cannot serve. Its
// error carries credential scope so core tries the request on another account.
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

// embeddings passes an Embeddings API request through to {base_url}/embeddings on a Chat or
// Responses account, with its key sent as for Chat, and returns the upstream's reply unchanged.
// Messages and Gemini accounts answer unsupported so core tries another account.
func embeddings(ctx context.Context, call sdk.Call, out *sdk.Stream) error {
	s, wire, key, err := account(call)
	if err != nil {
		return err
	}
	if err := requireOpenAIWire(wire, "embeddings"); err != nil {
		return err
	}
	// The key goes as Chat's does on both wires that serve embeddings.
	chat := wireInfo{format: llm.FormatOpenAIChat}
	resp, err := postBody(ctx, call, s, chat, key, "/embeddings", call.Request.Native.Body)
	if err != nil {
		return err
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

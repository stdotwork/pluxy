package host

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/sdk/payload"
)

const (
	payloadMinor     = 14
	payloadTurnMinor = 15
)

type payloadClientKey struct{}

type payloadClient struct {
	model   string
	headers http.Header
}

func WithPayloadClient(ctx context.Context, model string, headers http.Header) context.Context {
	return context.WithValue(ctx, payloadClientKey{}, payloadClient{model: model, headers: headers})
}

func clientHeaders(ctx context.Context) http.Header {
	c, _ := ctx.Value(payloadClientKey{}).(payloadClient)
	return c.headers
}

func Channel(id string) string {
	if id == "pluxy/openai-compatible" {
		return "openai"
	}
	return id[strings.LastIndexByte(id, '/')+1:]
}

func FromProtocol(format string) string {
	switch format {
	case llm.FormatOpenAIChat:
		return "openai"
	case llm.FormatOpenAIResponses, llm.FormatOpenAIResponsesCompact:
		return "openai-response"
	case llm.FormatAnthropicMessages:
		return "claude"
	case llm.FormatGeminiGenerate:
		return "gemini"
	case llm.FormatGeminiInteractions:
		return "interactions"
	case llm.FormatCodexAlphaSearch:
		return "codex-alpha-search"
	case llm.FormatOpenAIImagesGenerations, llm.FormatOpenAIImagesEdits:
		return "openai-image"
	}
	return ""
}

func (e *Extension) payload(ctx context.Context, req llm.Request, minor int) (*payload.Rules, *llm.Error) {
	if e.opts.Payload == nil {
		return nil, nil
	}
	rules := e.opts.Payload()
	if rules == nil {
		return nil, nil
	}
	client, _ := ctx.Value(payloadClientKey{}).(payloadClient)
	requested := client.model
	if s := req.Options.ThinkingSuffix; requested != "" && s != "" {
		requested += "(" + s + ")"
	}
	in := payload.Input{Model: req.Model, RequestedModel: requested, Protocol: Channel(e.manifest.ID), Headers: client.headers}
	if req.Native != nil {
		in.FromProtocol = FromProtocol(req.Native.Format)
	}
	selected := payload.Select(rules, in)
	if selected != nil && e.info.ProtocolMinor < minor {
		return nil, &llm.Error{Code: llm.CodeUnsupported, Scope: llm.ScopeCredential, Retryable: true,
			Message: fmt.Sprintf("extension %s implements protocol 0.%d, which cannot apply payload rules here (0.%d adds them); rules apply to this request, so it is not sent to the account",
				e.manifest.ID, e.info.ProtocolMinor, minor)}
	}
	return selected, nil
}

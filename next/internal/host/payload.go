package host

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/sdk/payload"
)

// payloadMinor is the protocol minor version that adds payload rules, and payloadTurnMinor the
// one that has a session turn apply them to its upstream message.
const (
	payloadMinor     = 14
	payloadTurnMinor = 15
)

type payloadClientKey struct{}

// payloadClient is what payload rules match of the client's request besides the call.
type payloadClient struct {
	model   string
	headers http.Header
}

// WithPayloadClient returns ctx carrying the client's request for payload rules: the model it
// asked for (before an alias or prefix is resolved, without a thinking suffix) and its headers.
// Rules match the model of each call made with ctx and also this one, and their header
// conditions match these headers, which never leave core.
func WithPayloadClient(ctx context.Context, model string, headers http.Header) context.Context {
	return context.WithValue(ctx, payloadClientKey{}, payloadClient{model: model, headers: headers})
}

// Channel is the payload rules' protocol for an extension: the last part of its package ID
// (pluxy/codex is codex), and openai for pluxy/openai-compatible, as today's core calls its
// OpenAI-compatible providers.
func Channel(id string) string {
	if id == "pluxy/openai-compatible" {
		return "openai"
	}
	return id[strings.LastIndexByte(id, '/')+1:]
}

// FromProtocol is the payload rules' from-protocol for a request in a native format: today's
// core's name for the client API it came in on; "" for none.
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

// payload returns the payload rules that apply to a call with req on account: the config's
// (Options.Payload) that match the call's model, the client's (WithPayloadClient), the
// extension's channel and the client's format and headers, each reduced to its body conditions
// (payload.Select). It refuses the call when rules apply and the extension implements a protocol
// older than minor, which would send the request without them.
func (e *Extension) payload(ctx context.Context, account string, req llm.Request, minor int) (*payload.Rules, *llm.Error) {
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
			Message: fmt.Sprintf("extension %s implements protocol 0.%d, which cannot apply payload rules here (0.%d adds them); rules apply to this request, so it is not sent to account %q",
				e.manifest.ID, e.info.ProtocolMinor, minor, account)}
	}
	return selected, nil
}

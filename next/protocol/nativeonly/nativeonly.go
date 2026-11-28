package nativeonly

import (
	"fmt"
	"unicode/utf8"

	"github.com/stdotwork/pluxy/next/format/anthropicmessages"
	"github.com/stdotwork/pluxy/next/format/geminigenerate"
	"github.com/stdotwork/pluxy/next/format/openaichat"
	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/llm"
)

// Gemini's model and whether it streams are in the request path, not the body, so its parser takes
// them from the request.
var nativeParsers = map[string]func(req llm.Request) (llm.Request, error){
	llm.FormatOpenAIChat:        func(req llm.Request) (llm.Request, error) { return openaichat.ParseRequest(req.Native.Body) },
	llm.FormatOpenAIResponses:   func(req llm.Request) (llm.Request, error) { return openairesponses.ParseRequest(req.Native.Body) },
	llm.FormatAnthropicMessages: func(req llm.Request) (llm.Request, error) { return anthropicmessages.ParseRequest(req.Native.Body) },
	llm.FormatGeminiGenerate: func(req llm.Request) (llm.Request, error) {
		return geminigenerate.ParseRequest(req.Model, req.Stream, req.Native.Body)
	},
}

func Can(req llm.Request) bool {
	n := req.Native
	return n != nil && nativeParsers[n.Format] != nil && n.IsParsed() && utf8.Valid(n.Body)
}

func Strip(req llm.Request) llm.Request {
	req.Items, req.Tools, req.Origin = nil, nil, nil
	return req
}

func Parse(req *llm.Request) error {
	if req.Native == nil {
		return fmt.Errorf("a native-only request has no native body")
	}
	parse := nativeParsers[req.Native.Format]
	if parse == nil {
		return fmt.Errorf("a native-only request has native format %q, which has no parser", req.Native.Format)
	}
	parsed, err := parse(*req)
	if err != nil {
		return err
	}
	req.Items, req.Tools, req.Origin = parsed.Items, parsed.Tools, parsed.Origin
	return nil
}

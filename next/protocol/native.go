package protocol

import (
	"fmt"

	"github.com/stdotwork/pluxy/next/format/anthropicmessages"
	"github.com/stdotwork/pluxy/next/format/geminigenerate"
	"github.com/stdotwork/pluxy/next/format/openaichat"
	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/llm"
)

// NativeOnlyMinor is the minor version that adds native-only generate calls (Call.NativeOnly).
const NativeOnlyMinor = 24

// nativeParsers are the parsers of the native formats a native-only call may carry: the ones
// core reads a generate request's body with. Gemini's model and whether it streams are in the
// request path, not the body, so its parser takes them from the request.
var nativeParsers = map[string]func(req llm.Request) (llm.Request, error){
	llm.FormatOpenAIChat:        func(req llm.Request) (llm.Request, error) { return openaichat.ParseRequest(req.Native.Body) },
	llm.FormatOpenAIResponses:   func(req llm.Request) (llm.Request, error) { return openairesponses.ParseRequest(req.Native.Body) },
	llm.FormatAnthropicMessages: func(req llm.Request) (llm.Request, error) { return anthropicmessages.ParseRequest(req.Native.Body) },
	llm.FormatGeminiGenerate: func(req llm.Request) (llm.Request, error) {
		return geminigenerate.ParseRequest(req.Model, req.Stream, req.Native.Body)
	},
}

// CanBeNativeOnly reports whether a generate call may carry req native-only: it has a native
// body in one of the native formats, and its items, tools and origin are what that format's
// parser makes of the body (llm.Native.Parsed is the body). Core checks this before it leaves
// them out.
func CanBeNativeOnly(req llm.Request) bool {
	n := req.Native
	return n != nil && nativeParsers[n.Format] != nil && n.IsParsed()
}

// NativeOnly returns req as a native-only call carries it: without its items, tools and origin,
// which ParseNative restores from its native body.
func NativeOnly(req llm.Request) llm.Request {
	req.Items, req.Tools, req.Origin = nil, nil, nil
	return req
}

// ParseNative restores the items, tools and origin of a request that came in a native-only call
// (Call.NativeOnly) by parsing its native body, with the same parser core read the client's body
// with. Everything else in req stays as the call carried it: its model, options and drop policy
// are core's (core may have renamed the model or taken a thinking suffix off it), not the
// body's.
func ParseNative(req *llm.Request) error {
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

// Package nativeonly holds the rule of native-only generate calls (protocol minor version 27,
// protocol.Call.NativeOnly): when core may leave a request's items, tools and origin out of a
// call, and how an extension restores them from the request's native body. It is apart from
// package protocol because it uses the format parsers, whose tests use package protocol.
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

// Can reports whether a generate call may carry req native-only: it has a native
// body in one of the native formats, its items, tools and origin are what that format's parser
// makes of the body (llm.Native.Parsed is the body), and the body is valid UTF-8. A frame
// carries the parsed request's text as UTF-8, with each invalid byte replaced, so from a body
// that is not, the extension's own parse could keep bytes the full call would have replaced.
// Core checks this before it leaves them out.
func Can(req llm.Request) bool {
	n := req.Native
	return n != nil && nativeParsers[n.Format] != nil && n.IsParsed() && utf8.Valid(n.Body)
}

// Strip returns req as a native-only call carries it: without its items, tools and origin,
// which Parse restores from its native body.
func Strip(req llm.Request) llm.Request {
	req.Items, req.Tools, req.Origin = nil, nil, nil
	return req
}

// Parse restores the items, tools and origin of a request that came in a native-only call
// (protocol.Call.NativeOnly) by parsing its native body, with the same parser core read the client's body
// with. Everything else in req stays as the call carried it: its model, options and drop policy
// are core's (core may have renamed the model or taken a thinking suffix off it), not the
// body's.
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

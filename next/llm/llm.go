// Package llm is pluxy's internal model of a model request and its streamed reply.
//
// A request is an ordered list of typed items; a reply is a stream of events that open, fill and
// close output items and end in exactly one terminal event. The shapes follow the prompt and
// stream-part types of Vercel's AI SDK Language Model Specification and the items and streaming
// events of Open Responses. Client formats and provider extensions translate to and from this
// model; the extension protocol carries it on the wire (see ../protocol/README.md).
package llm

import "encoding/json"

// Request is a model request in pluxy's internal form.
type Request struct {
	Model   string  `json:"model"`
	Stream  bool    `json:"stream"`
	Items   []Item  `json:"items"`
	Tools   []Tool  `json:"tools,omitempty"`
	Options Options `json:"options"`
	// Native is the client's original body, when core received one. An extension whose upstream
	// speaks Native.Format sends Native.Body unchanged instead of rebuilding it from Items.
	Native *Native `json:"native,omitempty"`
}

// Native is a body kept in its original format.
type Native struct {
	Format string          `json:"format"`
	Body   json.RawMessage `json:"body"`
}

// Native formats.
const (
	FormatOpenAIChat = "openai.chat"
)

// Item types.
const (
	ItemTypeMessage    = "message"
	ItemTypeToolCall   = "tool_call"
	ItemTypeToolResult = "tool_result"
)

// Message roles.
const (
	RoleSystem    = "system"
	RoleDeveloper = "developer"
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

// Item is one entry in a conversation. Type selects which fields apply.
type Item struct {
	Type string `json:"type"`

	// message
	Role    string `json:"role,omitempty"`
	Content []Part `json:"content,omitempty"`

	// tool_call and tool_result
	CallID string `json:"call_id,omitempty"`
	// tool_call
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	// tool_result
	Output string `json:"output,omitempty"`
}

// Part types.
const (
	PartText   = "text"
	PartImage  = "image"
	PartNative = "native"
)

// Part is one piece of message content. Type selects which fields apply.
type Part struct {
	Type string `json:"type"`
	// text
	Text string `json:"text,omitempty"`
	// image
	URL    string `json:"url,omitempty"`
	Detail string `json:"detail,omitempty"`
	// native: a part core could not interpret, kept as the client sent it.
	Format string          `json:"format,omitempty"`
	Data   json.RawMessage `json:"data,omitempty"`
}

// Tool is a function tool the model may call.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// Options are generation options. Nil means not set.
type Options struct {
	MaxOutputTokens *int     `json:"max_output_tokens,omitempty"`
	Temperature     *float64 `json:"temperature,omitempty"`
	TopP            *float64 `json:"top_p,omitempty"`
	Stop            []string `json:"stop,omitempty"`
}

// Event types.
const (
	EventResponseStart  = "response.start"
	EventItemStart      = "item.start"
	EventTextDelta      = "text.delta"
	EventReasoningDelta = "reasoning.delta"
	EventToolInputDelta = "tool_input.delta"
	EventItemEnd        = "item.end"
	EventNative         = "native"
	EventFinish         = "finish"
	EventError          = "error"
)

// Event is one step of a streamed reply. Type selects which fields apply.
type Event struct {
	Type string `json:"type"`

	// item.start, the deltas and item.end
	Index int `json:"index"`
	// text.delta, reasoning.delta, tool_input.delta
	Delta string `json:"delta,omitempty"`

	Response *ResponseInfo `json:"response,omitempty"` // response.start
	Item     *ItemStart    `json:"item,omitempty"`     // item.start
	Finish   *Finish       `json:"finish,omitempty"`   // finish
	Usage    *Usage        `json:"usage,omitempty"`    // finish
	Error    *Error        `json:"error,omitempty"`    // error

	// Raw is the upstream data the event came from, for diagnostics. In a native event it is the
	// event's content: upstream data the model has no place for, such as OpenAI choices after
	// the first, in the reply's format (ResponseInfo.Format). A client format that matches writes
	// it back; others leave it out.
	Raw json.RawMessage `json:"raw,omitempty"`
}

// Terminal reports whether the event ends the stream.
func (e Event) Terminal() bool { return e.Type == EventFinish || e.Type == EventError }

// ResponseInfo describes the reply as a whole.
type ResponseInfo struct {
	ID string `json:"id"`
	// Model is the model that actually served the request.
	Model   string `json:"model"`
	Created int64  `json:"created,omitempty"`
	// Format is the upstream format the reply came from. The upstream data kept in the reply
	// (Raw fields, native events, usage raw) is in this format.
	Format string `json:"format,omitempty"`
	// Raw holds the upstream's reply-level fields that have no place in the model, as one JSON
	// object; for OpenAI Chat Completions, fields such as system_fingerprint.
	Raw json.RawMessage `json:"raw,omitempty"`
}

// Output item kinds.
const (
	ItemMessage   = "message"
	ItemReasoning = "reasoning"
	ItemToolCall  = "tool_call"
)

// ItemStart describes an output item when it opens.
type ItemStart struct {
	Kind   string `json:"kind"`
	CallID string `json:"call_id,omitempty"`
	Name   string `json:"name,omitempty"`
}

// Finish reasons.
const (
	FinishStop          = "stop"
	FinishLength        = "length"
	FinishToolCalls     = "tool_calls"
	FinishContentFilter = "content_filter"
	FinishOther         = "other"
)

// Finish says why generation ended. RawReason keeps the upstream's own value.
type Finish struct {
	Reason    string `json:"reason"`
	RawReason string `json:"raw_reason,omitempty"`
}

// Usage counts tokens. A nil field was not reported, which is different from zero.
type Usage struct {
	InputTokens       *int64          `json:"input_tokens,omitempty"`
	OutputTokens      *int64          `json:"output_tokens,omitempty"`
	CachedInputTokens *int64          `json:"cached_input_tokens,omitempty"`
	ReasoningTokens   *int64          `json:"reasoning_tokens,omitempty"`
	Raw               json.RawMessage `json:"raw,omitempty"`
}

// Error codes.
const (
	CodeInvalidRequest = "invalid_request"
	CodeAuth           = "auth"
	CodePermission     = "permission"
	CodeNotFound       = "not_found"
	CodeRateLimit      = "rate_limit"
	CodeQuota          = "quota"
	CodeUnsupported    = "unsupported"
	CodeUpstream       = "upstream"
	CodeUnavailable    = "unavailable"
	CodeCancelled      = "cancelled"
	CodeInternal       = "internal"

	// Produced by core, not by extensions.
	CodeExtensionCrashed     = "extension_crashed"
	CodeExtensionProtocol    = "extension_protocol"
	CodeExtensionUnavailable = "extension_unavailable"
)

// Error scopes: what a failure is about.
const (
	ScopeRequest    = "request"
	ScopeCredential = "credential"
	ScopeModel      = "model"
	ScopeEndpoint   = "endpoint"
)

// Error is a structured failure.
type Error struct {
	Code         string `json:"code"`
	Message      string `json:"message"`
	Status       int    `json:"status,omitempty"`
	ProviderCode string `json:"provider_code,omitempty"`
	// Retryable is true only when the upstream certainly did not run the request.
	Retryable    bool            `json:"retryable,omitempty"`
	Scope        string          `json:"scope,omitempty"`
	RetryAfterMS int64           `json:"retry_after_ms,omitempty"`
	Raw          json.RawMessage `json:"raw,omitempty"`
	// Usage is what the upstream reported using before the failure, when it reported any: a
	// reply that fails partway may still have cost tokens.
	Usage *Usage `json:"usage,omitempty"`
}

// RawJSON returns upstream bytes for a Raw field, which must hold valid JSON: b itself when it is
// valid JSON, otherwise b as a JSON string. An event carrying it can then always be encoded.
func RawJSON(b []byte) json.RawMessage {
	if json.Valid(b) {
		return append(json.RawMessage(nil), b...)
	}
	s, _ := json.Marshal(string(b))
	return s
}

func (e *Error) Error() string {
	if e.Message == "" {
		return e.Code
	}
	return e.Code + ": " + e.Message
}

// Int64 returns a pointer to v, for Usage fields.
func Int64(v int64) *int64 { return &v }

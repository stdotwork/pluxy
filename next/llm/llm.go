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
	// Origin holds the request's top-level fields that have no place in the model.
	Origin *Origin `json:"origin,omitempty"`
	// DropPolicy says what an extension does when building its upstream request would drop part
	// of this request: DropReport (the default) sends it and reports the losses, DropReject
	// refuses it with an unsupported error.
	DropPolicy string `json:"drop_policy,omitempty"`
}

// Drop policies.
const (
	DropReport = "report"
	DropReject = "reject"
)

// Origin records where a request, item, part or tool came from: its format, its path in the
// source body (for example messages[2]) and, in Raw, its fields that have no place in the model,
// as one JSON object in Format. An encoder for Format writes Raw back, so those fields survive;
// an encoder for another format reports each of them as dropped.
type Origin struct {
	Format string          `json:"format"`
	Path   string          `json:"path,omitempty"`
	Raw    json.RawMessage `json:"raw,omitempty"`
}

// Loss kinds.
const (
	// LossApproximated: the meaning was carried over in a different form.
	LossApproximated = "approximated"
	// LossDropped: the target format has no place for it, so it was left out.
	LossDropped = "dropped"
)

// Loss is one thing a translation could not carry over exactly. Path names it in the source
// body (for example input[1].content[0] or metadata).
type Loss struct {
	Kind   string `json:"kind"`
	Path   string `json:"path"`
	Detail string `json:"detail,omitempty"`
}

// MaxLosses is how many losses a translation lists in full. A stream can produce losses without
// end (one per upstream field or content type), so past MaxLosses one more entry says that more
// were left out, and nothing after it is listed.
const MaxLosses = 100

// AddLoss appends l to losses. The loss that would be the one past MaxLosses gets one entry in its
// place, of its kind, that says more were left out; the list does not grow after that.
func AddLoss(losses []Loss, l Loss) []Loss {
	switch {
	case len(losses) < MaxLosses:
		return append(losses, l)
	case len(losses) == MaxLosses:
		return append(losses, Loss{Kind: l.Kind, Path: "...", Detail: "more losses not listed"})
	}
	return losses
}

// Translation reports how an upstream request was built from the client's request. When
// Passthrough is true the client's body was sent unchanged and nothing was lost; otherwise the
// body was rebuilt from the model in format To, and Losses lists what did not carry over.
// Everything not listed was preserved.
type Translation struct {
	From        string `json:"from"`
	To          string `json:"to"`
	Passthrough bool   `json:"passthrough,omitempty"`
	Losses      []Loss `json:"losses,omitempty"`
}

// Dropped reports whether any loss is a drop.
func (t *Translation) Dropped() bool {
	if t == nil {
		return false
	}
	for _, l := range t.Losses {
		if l.Kind == LossDropped {
			return true
		}
	}
	return false
}

// Native is a body kept in its original format.
type Native struct {
	Format string          `json:"format"`
	Body   json.RawMessage `json:"body"`
	// Headers are the client's request headers that belong to Format and say how to read Body,
	// keyed by lower-case name: for Anthropic Messages, anthropic-version and anthropic-beta; for
	// OpenAI Responses, the Codex CLI's session and turn headers. An extension that passes Body
	// through sends those its upstream reads. For Anthropic Messages they also hold the headers
	// the client names itself with (anthropicmessages.ClientHeaders), which an extension may send
	// with a body it passes through.
	Headers map[string]string `json:"headers,omitempty"`
}

// Native formats.
const (
	FormatOpenAIChat        = "openai.chat"
	FormatOpenAIResponses   = "openai.responses"
	FormatAnthropicMessages = "anthropic.messages"
	// FormatGeminiGenerate is Gemini's GenerateContent API. Its model and whether it streams
	// are in the request path, not the body: Request.Model and Request.Stream carry them.
	FormatGeminiGenerate = "gemini.generate_content"
)

// Endpoint formats: the Native.Format of an endpoint call (protocol minor version 7), a client
// request to a provider endpoint beyond generation. The reply's body is the endpoint's own.
const (
	// FormatOpenAIResponsesCompact is OpenAI's Responses compaction, POST /v1/responses/compact:
	// a Responses request body, answered with one JSON object.
	FormatOpenAIResponsesCompact = "openai.responses.compact"
	// FormatCodexAlphaSearch is Codex's search endpoint, POST /v1/alpha/search: a search request
	// in Codex's own shape, which may name no model, answered with one JSON object.
	FormatCodexAlphaSearch = "codex.alpha_search"
	// FormatGeminiInteractions is Gemini's Interactions API, POST /v1beta/interactions: an
	// Interactions request in Google's own shape, which names a model or an agent; the reply is
	// one JSON object, or server-sent events when it asks for "stream": true.
	FormatGeminiInteractions = "gemini.interactions"
	// FormatOpenAIImagesGenerations is OpenAI's Images API generation, POST
	// /v1/images/generations: a JSON body, answered with one JSON object, or server-sent events
	// when it asks for a stream.
	FormatOpenAIImagesGenerations = "openai.images.generations"
	// FormatOpenAIImagesEdits is OpenAI's Images API edit, POST /v1/images/edits, as for
	// generations. A multipart/form-data edit arrives in its JSON form: each field a member (n,
	// output_compression and partial_images as numbers, stream as a boolean, mask[file_id] and
	// mask[image_url] as mask.file_id and mask.image_url) and each file a data URL, the images
	// (image[] or image) appended to images[].image_url and the mask as mask.image_url.
	FormatOpenAIImagesEdits = "openai.images.edits"
)

// EndpointReply is the Raw of an endpoint call's response.start: how to send the reply's body,
// which follows in native events, each a JSON string holding the next piece of the body.
type EndpointReply struct {
	// ContentType is the body's media type, such as application/json or text/event-stream.
	ContentType string `json:"content_type"`
}

// Item types.
const (
	ItemTypeMessage    = "message"
	ItemTypeToolCall   = "tool_call"
	ItemTypeToolResult = "tool_result"
	ItemTypeReasoning  = "reasoning"
	// ItemTypeNative is an item core could not interpret; Origin.Raw holds it whole.
	ItemTypeNative = "native"
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
	// ID is the item's own identifier in its source format, when it had one (Responses item
	// ids). It names the item; it is not content.
	ID string `json:"id,omitempty"`

	// message
	Role    string `json:"role,omitempty"`
	Content []Part `json:"content,omitempty"`
	// TextContent is true when the source gave the content as a plain string rather than a list
	// of parts; an encoder that has both forms keeps the source's.
	TextContent bool `json:"text_content,omitempty"`

	// tool_call and tool_result
	CallID string `json:"call_id,omitempty"`
	// tool_call
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	// Custom marks a call to a custom (freeform) tool: Arguments is the tool's raw input, not
	// JSON.
	Custom bool `json:"custom,omitempty"`
	// tool_result
	Output string `json:"output,omitempty"`
	// reasoning: the reasoning text or summary the client replayed. Opaque reasoning data, such
	// as an encrypted blob, stays in Origin.Raw.
	Text string `json:"text,omitempty"`

	Origin *Origin `json:"origin,omitempty"`
}

// Part types.
const (
	PartText   = "text"
	PartImage  = "image"
	PartFile   = "file"
	PartNative = "native"
)

// Part is one piece of message content. Type selects which fields apply.
type Part struct {
	Type string `json:"type"`
	// text
	Text string `json:"text,omitempty"`
	// image and file
	URL    string `json:"url,omitempty"`
	Detail string `json:"detail,omitempty"` // image
	// file: one of FileID (a file stored with the provider), FileData (inline, as a data: URL
	// or base64) or URL.
	FileID   string `json:"file_id,omitempty"`
	FileData string `json:"file_data,omitempty"`
	Filename string `json:"filename,omitempty"`
	// native: a part core could not interpret, kept as the client sent it.
	Format string          `json:"format,omitempty"`
	Data   json.RawMessage `json:"data,omitempty"`

	Origin *Origin `json:"origin,omitempty"`
}

// Tool types.
const (
	ToolFunction = "function"
	// ToolNative is a tool core could not interpret, such as a provider-hosted tool;
	// Origin.Raw holds it whole.
	ToolNative = "native"
)

// Tool is a tool the model may call. An empty Type means function.
type Tool struct {
	Type        string          `json:"type,omitempty"`
	Name        string          `json:"name,omitempty"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	// Strict asks the provider to make arguments follow Parameters exactly.
	Strict *bool   `json:"strict,omitempty"`
	Origin *Origin `json:"origin,omitempty"`
}

// Options are generation options. Nil or empty means not set.
type Options struct {
	MaxOutputTokens   *int            `json:"max_output_tokens,omitempty"`
	Temperature       *float64        `json:"temperature,omitempty"`
	TopP              *float64        `json:"top_p,omitempty"`
	Stop              []string        `json:"stop,omitempty"`
	ToolChoice        *ToolChoice     `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool           `json:"parallel_tool_calls,omitempty"`
	ResponseFormat    *ResponseFormat `json:"response_format,omitempty"`
	// ReasoningEffort is the reasoning effort the client asked for, as it named it (for
	// example low, medium or high).
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	// ThinkingSuffix is the thinking setting the client wrote after the model name, as in
	// gpt-5(high): a level, none or auto, or a token budget (see SplitThinkingSuffix). Core has
	// taken it off Model and off the model in Native.Body. It wins over ReasoningEffort and over
	// any reasoning or thinking settings in Native.Body, also in a body sent unchanged.
	ThinkingSuffix string `json:"thinking_suffix,omitempty"`
}

// Tool choice modes.
const (
	ToolChoiceAuto     = "auto"
	ToolChoiceNone     = "none"
	ToolChoiceRequired = "required"
	ToolChoiceTool     = "tool" // the function named in Name
)

// ToolChoice says whether and which tool the model must call.
type ToolChoice struct {
	Mode string `json:"mode"`
	Name string `json:"name,omitempty"`
}

// Response format types.
const (
	ResponseFormatText       = "text"
	ResponseFormatJSONObject = "json_object"
	ResponseFormatJSONSchema = "json_schema"
)

// ResponseFormat constrains the reply's text: plain text, any JSON object, or JSON following
// Schema.
type ResponseFormat struct {
	Type        string          `json:"type"`
	Name        string          `json:"name,omitempty"`
	Description string          `json:"description,omitempty"`
	Schema      json.RawMessage `json:"schema,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
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
	// Opaque, on the item.end of a reasoning item, is the provider's opaque record of that
	// reasoning, which the provider needs back to continue from it (an Anthropic thinking
	// signature, a Responses reasoning item's encrypted_content). A client format with a place
	// for it (a Responses reasoning item's encrypted_content, a thinking block's signature)
	// carries it, so the client can replay it.
	Opaque *Opaque `json:"opaque,omitempty"`
	Finish *Finish `json:"finish,omitempty"` // finish
	Usage  *Usage  `json:"usage,omitempty"`  // finish
	Error  *Error  `json:"error,omitempty"`  // error

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
	// Translation reports how the extension built the upstream request from the client's.
	Translation *Translation `json:"translation,omitempty"`
}

// Output item kinds.
const (
	ItemMessage   = "message"
	ItemReasoning = "reasoning"
	ItemToolCall  = "tool_call"
)

// OpaqueDevin is the Opaque.Format of a Devin reasoning signature, which has no native format.
// A Messages client carries it in the thinking block's signature and a streamed Gemini reply in a
// thought part's thoughtSignature, as today's core does; both give it back on the next turn.
const OpaqueDevin = "devin"

// Opaque is reasoning state only its provider can read, with the format it came from.
type Opaque struct {
	Format string `json:"format"`
	Data   string `json:"data"`
}

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
// InputTokens is every input token, cached or not; CachedInputTokens (read from a cache) and
// CacheWriteInputTokens (written to one) are the parts of it the provider reports separately.
type Usage struct {
	InputTokens           *int64          `json:"input_tokens,omitempty"`
	OutputTokens          *int64          `json:"output_tokens,omitempty"`
	CachedInputTokens     *int64          `json:"cached_input_tokens,omitempty"`
	CacheWriteInputTokens *int64          `json:"cache_write_input_tokens,omitempty"`
	ReasoningTokens       *int64          `json:"reasoning_tokens,omitempty"`
	Raw                   json.RawMessage `json:"raw,omitempty"`
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
	// QuotaWindowMinutes is, for a quota error, the length in minutes of the usage window that
	// is used up, when the upstream said (protocol 0.6).
	QuotaWindowMinutes int64 `json:"quota_window_minutes,omitempty"`
	// UsageLimit, set only by core, marks the final usage-limit reply: every account for the
	// request is out of quota until a reset the upstream named (routing's usage-limit quota
	// response mode). It never travels over the protocol.
	UsageLimit *UsageLimit `json:"-"`
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

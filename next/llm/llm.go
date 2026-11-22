// Package llm is pluxy's internal model of a model request and its streamed reply.
//
// A request is an ordered list of typed items; a reply is a stream of events that open, fill and
// close output items and end in exactly one terminal event. The shapes follow the prompt and
// stream-part types of Vercel's AI SDK Language Model Specification and the items and streaming
// events of Open Responses. Client formats and provider extensions translate to and from this
// model; the extension protocol carries it on the wire (see ../protocol/README.md).
package llm

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"
)

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
	// Caller stands for the client key the request came with (protocol minor version 8): the
	// same for every request with that key to one extension, different for another key or
	// another extension, and not derivable from the key. Empty when the client sent no key. An
	// extension that keeps state from replies across requests keys it by Caller, so one client
	// never gets another's.
	Caller string `json:"caller,omitempty"`
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
// Passthrough is true the client's body went upstream rather than a body rebuilt from the model
// in format To; the extension may still have fitted it to its endpoint, and Losses then lists
// what that changed or dropped. Otherwise the body was rebuilt, and Losses lists what did not
// carry over. Everything not listed was preserved.
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

// DroppedPaths returns the paths of the losses that are drops, in order.
func (t *Translation) DroppedPaths() []string {
	if t == nil {
		return nil
	}
	var paths []string
	for _, l := range t.Losses {
		if l.Kind == LossDropped {
			paths = append(paths, l.Path)
		}
	}
	return paths
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
	// Parsed is core's own record, never sent: the body the request's Items, Tools and Origin
	// were parsed from, by Format's parser. While it is still Body (IsParsed), an extension that
	// parses Body gets the same Items, Tools and Origin, so core can leave them out of the call
	// (protocol minor version 27). Code that changes Body leaves Parsed alone, so the two differ
	// and core sends them, unless the change cannot change what the parser makes of them.
	Parsed json.RawMessage `json:"-"`
}

// IsParsed reports whether n's Body is the body its request's Items, Tools and Origin were parsed
// from (Parsed).
func (n *Native) IsParsed() bool {
	return n != nil && len(n.Body) > 0 && bytes.Equal(n.Parsed, n.Body)
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
	// FormatOpenAIResponsesInputTokens is OpenAI's Responses input token count, POST
	// /v1/responses/input_tokens: a Responses request body, answered with
	// {"object":"response.input_tokens","input_tokens":N}.
	FormatOpenAIResponsesInputTokens = "openai.responses.input_tokens"
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
	// FormatOpenAIAudioSpeech is OpenAI's speech endpoint, POST /v1/audio/speech: a JSON
	// request answered with binary audio under the upstream's Content-Type, or server-sent events
	// when it asks for "stream_format": "sse". The binary reply travels as base64 body pieces.
	FormatOpenAIAudioSpeech = "openai.audio.speech"
	// FormatOpenAIAudioTranscriptions is OpenAI's transcription endpoint, POST
	// /v1/audio/transcriptions. A multipart form arrives in its JSON form: each field a member,
	// the uploaded file as {filename, content_type, data} with data base64, and a repeated field
	// as a list. The reply is the upstream's, one JSON or text body or server-sent events.
	FormatOpenAIAudioTranscriptions = "openai.audio.transcriptions"
	// FormatOpenAIAudioTranslations is OpenAI's translation endpoint, POST
	// /v1/audio/translations, with the same request and reply form as transcriptions.
	FormatOpenAIAudioTranslations = "openai.audio.translations"
	// FormatOpenAIEmbeddings is OpenAI's Embeddings API, POST /v1/embeddings: a JSON body,
	// answered with one JSON object.
	FormatOpenAIEmbeddings = "openai.embeddings"
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
	Role string `json:"role,omitempty"`
	// Content is a message's parts. On a tool_result (protocol minor 25) it is the result's parts
	// when the result holds more than text: images, files or parts core could not interpret, in
	// the order the client gave them. Output then still holds the result's text parts joined, for
	// an encoder or extension that can send only text.
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
	// IsError marks a tool result that reports the tool failed (protocol minor 25).
	IsError bool `json:"is_error,omitempty"`
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
	// ToolWebSearch is a provider-hosted web search tool (protocol minor version 29). WebSearch
	// holds the settings more than one format can say; Origin holds the client's original tool.
	ToolWebSearch = "web_search"
	// ToolCodeExecution is a provider-hosted code execution tool (protocol minor version 30).
	// Origin holds the client's original tool.
	ToolCodeExecution = "code_execution"
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
	Strict *bool `json:"strict,omitempty"`
	// WebSearch holds the settings of a ToolWebSearch tool (protocol minor version 29).
	WebSearch *WebSearchTool `json:"web_search,omitempty"`
	Origin    *Origin        `json:"origin,omitempty"`
}

// IsFunction reports whether t is a function tool (Type is empty or ToolFunction).
func (t Tool) IsFunction() bool {
	return t.Type == "" || t.Type == ToolFunction
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
	// ServiceTier is the service tier the client asked for (protocol minor version 28): auto,
	// default, flex, priority, scale or ultrafast (the ServiceTier constants), or another value
	// the client named, as it wrote it. A format that names a tier otherwise is read into these
	// (Messages' standard_only is default, its speed fast and Gemini's priority are priority,
	// Gemini's standard is default); an OpenAI format's fast stays fast, and
	// CanonicalServiceTier makes it priority. Empty: the client named none, and the upstream's
	// own default applies. A paid tier is sent only when the client asked for it or its
	// equivalent; a tier a format cannot say is dropped with a loss, never moved to another.
	ServiceTier string `json:"service_tier,omitempty"`
	// Verbosity is how long an answer the client asked for (protocol minor version 28): low,
	// medium or high, or another value it named, as it wrote it. Empty: the client named none.
	// Chat Completions and Responses have it; another format drops it with a loss.
	Verbosity string `json:"verbosity,omitempty"`
}

// Tool choice modes.
const (
	ToolChoiceAuto     = "auto"
	ToolChoiceNone     = "none"
	ToolChoiceRequired = "required"
	ToolChoiceTool     = "tool" // the function named in Name
	// ToolChoiceCodeExecution forces the provider-hosted code execution tool (protocol minor
	// version 30).
	ToolChoiceCodeExecution = "code_execution"
)

// ToolChoice says whether and which tool the model must call.
type ToolChoice struct {
	Mode string `json:"mode"`
	Name string `json:"name,omitempty"`
	// Allowed is the set of tools the model may choose from, when the client named a set instead
	// of one tool (OpenAI's allowed_tools, Gemini's allowedFunctionNames). Mode is then auto or
	// required, and Name is empty. A format that has no allowed list carries the narrowing by
	// defining only these tools. An extension below protocol minor 0.26 cannot read it: core
	// sends the choice without the list and cuts the request's tools to the function tools it
	// names, so the narrowing still holds, and refuses the request as invalid when it names none
	// of them (see internal/host).
	Allowed []string `json:"allowed,omitempty"`
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
	CodeAccountClosed  = "account_closed"
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
	// CodeExtensionTimeout: the extension sent core nothing at all on a call for longer than
	// it promised to (as long as the account's timeouts let the upstream stay quiet, plus a minute).
	CodeExtensionTimeout = "extension_timeout"
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
	// Dropped is set only on a refusal under the reject drop policy: the paths of the request's
	// parts the upstream's format cannot carry (protocol 0.16). Core tells such a refusal from an
	// account that cannot serve the call by it.
	Dropped []string `json:"dropped,omitempty"`
	// UsageLimit, set only by core, marks the final usage-limit reply: every account for the
	// request is out of quota until a reset the upstream named (routing's usage-limit quota
	// response mode). It never travels over the protocol.
	UsageLimit *UsageLimit `json:"-"`
	// Explain, set only by core, says what routing tried for the request and which accounts it
	// left out, for the `pluxy` member of the error a client gets. It never travels over the
	// protocol.
	Explain *Explain `json:"-"`
}

// Explain is the `pluxy` member of a client's error: what routing did for the request. It holds
// codes and times, never the words of an upstream's reply.
type Explain struct {
	// Trace is the request's trace ID, as the X-CPA-TRACE-ID header has it.
	Trace string `json:"trace,omitempty"`
	// Tried are the attempts the request made, in order.
	Tried []Tried `json:"tried"`
	// LeftOut are the accounts that serve the request's model and were not tried, and why.
	LeftOut []LeftOut `json:"left_out"`
}

// Tried is one attempt: the account (its public ID), its provider, the model sent upstream and
// how it ended, by the code of the error (for example rate_limit or quota).
type Tried struct {
	Account  string `json:"account"`
	Provider string `json:"provider"`
	Model    string `json:"model,omitempty"`
	Result   string `json:"result"`
}

// LeftOut is an account that serves the request's model and was not tried. State is the
// account's health (ok, cooling, exhausted, needs_login, banned or disabled), busy while the
// account was at its concurrency limit, or skipped when the request's limits ended it before the
// account's turn. Until is when the block that holds it ends, when known.
type LeftOut struct {
	Account  string     `json:"account"`
	Provider string     `json:"provider,omitempty"`
	Model    string     `json:"model,omitempty"`
	State    string     `json:"state"`
	Until    *time.Time `json:"until,omitempty"`
}

// Pluxy adds the error's `pluxy` member to the error object o of a client format, when core set
// one.
func (e *Error) Pluxy(o map[string]any) {
	if e.Explain != nil {
		o["pluxy"] = e.Explain
	}
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

// UpstreamTimeoutPhrase is in the message of a failure that an SDK upstream timeout caused
// ("upstream response header timeout after 1m0s"). The protocol carries such a failure as an
// ordinary unavailable error, so core finds it by this phrase.
const UpstreamTimeoutPhrase = " timeout after "

// IsTimeout reports whether e is a timeout: core's extension_timeout, or an upstream failure
// whose message names an SDK upstream timeout.
func (e *Error) IsTimeout() bool {
	if e == nil {
		return false
	}
	switch e.Code {
	case CodeExtensionTimeout:
		return true
	case CodeUnavailable, CodeUpstream:
		return strings.Contains(e.Message, UpstreamTimeoutPhrase)
	}
	return false
}

func (e *Error) Error() string {
	if e.Message == "" {
		return e.Code
	}
	return e.Code + ": " + e.Message
}

// Int64 returns a pointer to v, for Usage fields.
func Int64(v int64) *int64 { return &v }

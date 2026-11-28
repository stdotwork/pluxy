package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/stdotwork/pluxy/next/format/sse"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

const defaultAPIRevision = "2026-05-20"

const maxInteractionsReply = 64 << 20

func endpoint(ctx context.Context, call sdk.Call, out *sdk.Stream) (err error) {
	ctx = withAccountID(ctx, call.Account.ID)
	format := ""
	if call.Request.Native != nil {
		format = call.Request.Native.Format
	}
	switch format {
	case llm.FormatOpenAIEmbeddings:
		return embeddings(ctx, call, out)
	case llm.FormatGeminiEmbedContent:
		return geminiEmbedContent(ctx, call, out)
	case llm.FormatGeminiBatchEmbedContents:
		return geminiBatchEmbedContents(ctx, call, out)
	case llm.FormatOpenAIImagesGenerations:
		return images(ctx, call, out)
	case llm.FormatOpenAIAudioSpeech:
		return audioSpeech(ctx, call, out)
	case llm.FormatOpenAIAudioTranscriptions:
		return audioTranscriptions(ctx, call, out)
	case llm.FormatOpenAIResponsesCompact:
		return compact(ctx, call, out)
	case llm.FormatGeminiInteractions:
	default:
		return &llm.Error{Code: llm.CodeUnsupported, Scope: llm.ScopeCredential, Retryable: true,
			Message: fmt.Sprintf("the Gemini extension does not serve %q", format)}
	}
	s, err := readSettings(call.Account.Settings)
	if err != nil {
		return err
	}
	if !s.Interactions {
		return &llm.Error{Code: llm.CodeUnsupported, Scope: llm.ScopeCredential, Retryable: true,
			Message: "this account does not serve the Interactions API (its interactions setting is off)"}
	}
	req := call.Request
	if req.Options.ThinkingSuffix != "" {
		return &llm.Error{Code: llm.CodeInvalidRequest, Scope: llm.ScopeRequest,
			Message: "a thinking suffix on the model name is not supported for the Interactions API; set generation_config.thinking_level instead"}
	}
	body, stream, err := interactionsBody(req.Native.Body, req.Model)
	if err != nil {
		return err
	}
	t, err := resolveInteractions(ctx, s, call.Account.Credential, stream)
	if err != nil {
		return err
	}
	send := &reporting{out: out, known: knownNames(call.Account)}
	if t.token != "" {
		send.redact = protocol.NewRedactor(&protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: t.token})
		defer func() {
			if e, ok := err.(*llm.Error); ok {
				err = send.redact.Event(llm.Event{Type: llm.EventError, Error: e}).Error
			}
		}()
	}
	revision := defaultAPIRevision
	if v := strings.TrimSpace(req.Native.Headers["api-revision"]); v != "" {
		revision = v
	}
	authorize := t.authorize
	t.authorize = func(h http.Header) {
		authorize(h)
		h.Set("Api-Revision", revision)
	}
	resp, err := postBody(ctx, t, body, false, req.Model)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		return relayInteractions(ctx, resp.Body, send)
	}
	reply, err := io.ReadAll(io.LimitReader(resp.Body, maxInteractionsReply+1))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &llm.Error{Code: llm.CodeUpstream, Message: "reading upstream response: " + err.Error(), Scope: llm.ScopeEndpoint}
	}
	if len(reply) > maxInteractionsReply {
		return &llm.Error{Code: llm.CodeUpstream, Message: fmt.Sprintf("the upstream's reply is larger than %d MiB", maxInteractionsReply>>20), Scope: llm.ScopeEndpoint}
	}
	if err := send.StartBody(llm.FormatGeminiInteractions, "application/json"); err != nil {
		return err
	}
	if err := send.Body(string(maskReply(reply, "", send.known))); err != nil {
		return err
	}
	var r struct {
		Usage json.RawMessage `json:"usage"`
	}
	_ = json.Unmarshal(reply, &r)
	return send.Finish(llm.Finish{Reason: llm.FinishStop}, interactionsUsage(r.Usage))
}

func (r *reporting) StartBody(format, contentType string) error {
	raw, err := json.Marshal(llm.EndpointReply{ContentType: contentType})
	if err != nil {
		return err
	}
	return r.Send(llm.Event{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{Format: format, Raw: raw}})
}

func (r *reporting) Body(chunk string) error {
	raw, err := json.Marshal(chunk)
	if err != nil {
		return err
	}
	return r.Send(llm.Event{Type: llm.EventNative, Raw: raw})
}

func (r *reporting) Finish(f llm.Finish, usage *llm.Usage) error {
	return r.Send(llm.Event{Type: llm.EventFinish, Finish: &f, Usage: usage})
}

func resolveInteractions(ctx context.Context, s settings, cred *protocol.Credential, stream bool) (target, error) {
	if s.Vertex == nil {
		if cred == nil || cred.Kind != protocol.CredentialAPIKey || cred.APIKey == "" {
			return target{}, &llm.Error{Code: llm.CodeAuth, Message: "the account has no API key", Scope: llm.ScopeCredential}
		}
		key := cred.APIKey
		return target{url: s.BaseURL + "/v1beta/interactions", authorize: func(h http.Header) { h.Set("X-Goog-Api-Key", key) }}, nil
	}
	query := ""
	if stream {
		query = "?alt=sse"
	}
	switch {
	case cred != nil && cred.Kind == protocol.CredentialAPIKey && cred.APIKey != "":
		base := s.BaseURL
		if base == "" {
			base = vertexHost("global")
		}
		u := base + "/v1beta1/interactions"
		if p := s.Vertex.Project; p != "" {
			u = fmt.Sprintf("%s/v1beta1/projects/%s/locations/global/interactions", base, url.PathEscape(p))
		}
		key := cred.APIKey
		return target{url: u + query, authorize: func(h http.Header) { h.Set("X-Goog-Api-Key", key) }}, nil
	case cred != nil && cred.Kind == CredentialServiceAccount:
		sa, err := readServiceAccount(cred.Data)
		if err != nil {
			return target{}, err
		}
		project := s.Vertex.Project
		if project == "" {
			project = sa.ProjectID
		}
		if project == "" {
			return target{}, &llm.Error{Code: llm.CodeInvalidRequest, Message: "the account needs vertex.project: its service account key names no project_id", Scope: llm.ScopeCredential}
		}
		base := s.BaseURL
		if base == "" {
			base = vertexHost("global")
		}
		token, err := tokens.get(ctx, sa)
		if err != nil {
			return target{}, err
		}
		u := fmt.Sprintf("%s/v1beta1/projects/%s/locations/global/interactions%s", base, url.PathEscape(project), query)
		return target{url: u, authorize: func(h http.Header) { h.Set("Authorization", "Bearer "+token) }, token: token, sa: sa}, nil
	}
	return target{}, &llm.Error{Code: llm.CodeAuth, Message: "the Vertex account has neither an API key nor a service account key", Scope: llm.ScopeCredential}
}

func interactionsBody(raw json.RawMessage, model string) ([]byte, bool, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		return nil, false, &llm.Error{Code: llm.CodeInvalidRequest, Message: "the request body is not a JSON object", Scope: llm.ScopeRequest}
	}
	if _, ok := m["model"]; ok && model != "" {
		m["model"], _ = json.Marshal(model)
	}
	if in, ok := m["input"]; ok {
		if clean, changed := sanitizeInput(in); changed {
			m["input"] = clean
		}
	}
	body, err := json.Marshal(m)
	if err != nil {
		return nil, false, err
	}
	return body, string(m["stream"]) == "true", nil
}

// sanitizeInput removes from an Interactions input (a list of steps) what the upstream refuses:
// a function_call step without an id takes its call_id as its id, and loses call_id; every
// other step, and every content part of a step that is not a thought, loses its id. A thought
// step keeps only its signature (the first of signature, thought_signature and thoughtSignature
// that is not blank, at signature), losing id, content, text and the other two spellings; one
// with no signature is left out.
func sanitizeInput(in json.RawMessage) (json.RawMessage, bool) {
	var steps []map[string]json.RawMessage
	if json.Unmarshal(in, &steps) != nil {
		return in, false
	}
	changed := false
	out := steps[:0]
	for _, st := range steps {
		if st == nil {
			out = append(out, st)
			continue
		}
		var typ string
		_ = json.Unmarshal(st["type"], &typ)
		if typ == "thought" {
			sig := ""
			for _, k := range []string{"signature", "thought_signature", "thoughtSignature"} {
				var v string
				if json.Unmarshal(st[k], &v) == nil && strings.TrimSpace(v) != "" {
					sig = strings.TrimSpace(v)
					break
				}
			}
			if sig == "" {
				changed = true
				continue
			}
			if b, _ := json.Marshal(sig); string(b) != string(st["signature"]) {
				st["signature"] = b
				changed = true
			}
			for _, k := range []string{"id", "content", "text", "thought_signature", "thoughtSignature"} {
				if _, ok := st[k]; ok {
					delete(st, k)
					changed = true
				}
			}
			out = append(out, st)
			continue
		}
		if typ == "function_call" {
			if c, ok := st["call_id"]; ok {
				if _, has := st["id"]; !has {
					st["id"] = c
				}
				delete(st, "call_id")
				changed = true
			}
		} else if _, ok := st["id"]; ok {
			delete(st, "id")
			changed = true
		}
		if parts, ok := cleanParts(st["content"]); ok {
			st["content"] = parts
			changed = true
		}
		out = append(out, st)
	}
	if !changed {
		return in, false
	}
	b, err := json.Marshal(out)
	if err != nil {
		return in, false
	}
	return b, true
}

func cleanParts(raw json.RawMessage) (json.RawMessage, bool) {
	var parts []map[string]json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &parts) != nil {
		return raw, false
	}
	changed := false
	for _, p := range parts {
		if _, ok := p["id"]; ok {
			delete(p, "id")
			changed = true
		}
	}
	if !changed {
		return raw, false
	}
	b, err := json.Marshal(parts)
	if err != nil {
		return raw, false
	}
	return b, true
}

func relayInteractions(ctx context.Context, body io.Reader, out *reporting) error {
	if err := out.StartBody(llm.FormatGeminiInteractions, "text/event-stream"); err != nil {
		return err
	}
	r := sse.NewReader(body)
	var usage *llm.Usage
	for {
		e, err := r.Next()
		if err == io.EOF {
			return out.Finish(llm.Finish{Reason: llm.FinishStop}, usage)
		}
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return &llm.Error{Code: llm.CodeUpstream, Message: "upstream stream broke: " + err.Error(), Scope: llm.ScopeEndpoint, Usage: usage}
		}
		var frame strings.Builder
		if e.Name != "" {
			frame.WriteString("event: " + e.Name + "\n")
		}
		if e.ID != "" {
			frame.WriteString("id: " + e.ID + "\n")
		}
		data := string(maskReply([]byte(e.Data), e.Name, out.known))
		for _, line := range strings.Split(data, "\n") {
			frame.WriteString("data: " + line + "\n")
		}
		frame.WriteString("\n")
		if err := out.Body(frame.String()); err != nil {
			return err
		}
		if u := streamUsage(e.Data); u != nil {
			usage = u
		}
	}
}

func streamUsage(data string) *llm.Usage {
	var ev struct {
		Interaction struct {
			Usage json.RawMessage `json:"usage"`
		} `json:"interaction"`
		Usage json.RawMessage `json:"usage"`
	}
	if json.Unmarshal([]byte(data), &ev) != nil {
		return nil
	}
	if u := interactionsUsage(ev.Interaction.Usage); u != nil {
		return u
	}
	return interactionsUsage(ev.Usage)
}

func interactionsUsage(raw json.RawMessage) *llm.Usage {
	var u map[string]json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &u) != nil || u == nil {
		return nil
	}
	count := func(names ...string) *int64 {
		for _, n := range names {
			var v int64
			if json.Unmarshal(u[n], &v) == nil && u[n] != nil {
				return &v
			}
		}
		return nil
	}
	out := &llm.Usage{
		InputTokens:       count("total_input_tokens", "input_tokens"),
		OutputTokens:      count("total_output_tokens", "output_tokens"),
		CachedInputTokens: count("total_cached_tokens", "cached_tokens"),
		ReasoningTokens:   count("total_thought_tokens", "reasoning_tokens"),
		Raw:               raw,
	}
	if out.InputTokens == nil && out.OutputTokens == nil {
		return nil
	}
	return out
}

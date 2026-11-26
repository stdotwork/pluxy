package main

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/conformance"
	"github.com/stdotwork/pluxy/next/extensions/codex/codextest"
	"github.com/stdotwork/pluxy/next/internal/host"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// Tests for how a request is shaped for the Codex endpoint: the fields it does not take, the
// smallest output budget, the prompt cache key, the reasoning levels of a model, the image tool,
// and the account settings that change them.

func settingsFrom(t *testing.T, js string) settings {
	t.Helper()
	s, err := readSettings(json.RawMessage(js))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// responsesReq is a Responses client's request for model with body.
func responsesReq(model, body string, headers map[string]string) llm.Request {
	return llm.Request{Model: model, Native: &llm.Native{Format: llm.FormatOpenAIResponses, Body: json.RawMessage(body), Headers: headers}}
}

// messagesReq is a Messages client's request: the IR a user turn and the output budget, and the
// body and headers as the client sent them.
func messagesReq(model, body string, maxTokens *int, headers map[string]string) llm.Request {
	return llm.Request{
		Model:   model,
		Items:   []llm.Item{{Type: llm.ItemTypeMessage, Role: llm.RoleUser, Content: []llm.Part{{Type: llm.PartText, Text: "hi"}}}},
		Options: llm.Options{MaxOutputTokens: maxTokens},
		Native:  &llm.Native{Format: llm.FormatAnthropicMessages, Body: json.RawMessage(body), Headers: headers},
	}
}

func shapeWith(t *testing.T, req llm.Request, s settings, a account) (*upstreamRequest, map[string]json.RawMessage) {
	t.Helper()
	ur, err := buildRequest(req, "acct", s, a, false)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(ur.body, &m); err != nil {
		t.Fatal(err)
	}
	return ur, m
}

func field(m map[string]json.RawMessage, k string) string {
	if v, ok := m[k]; ok {
		return string(v)
	}
	return "<absent>"
}

func effortOf(t *testing.T, m map[string]json.RawMessage) string {
	t.Helper()
	var r struct {
		Effort string `json:"effort"`
	}
	if err := json.Unmarshal(m["reasoning"], &r); err != nil {
		return "<none>"
	}
	return r.Effort
}

// Codex's own request has no metadata field, so the field a client sets on its request is not
// sent, whatever the client's format.
func TestMetadataIsNotSent(t *testing.T) {
	_, m := shapeWith(t, responsesReq("gpt-test", `{"model":"gpt-test","input":"hi","metadata":{"user_id":"u","k":"v"}}`, nil), settings{}, account{})
	if got := field(m, "metadata"); got != "<absent>" {
		t.Errorf("Responses client: metadata = %s, want it removed", got)
	}
	_, m = shapeWith(t, messagesReq("gpt-test", `{"model":"gpt-test","max_tokens":100,"metadata":{"user_id":"u"}}`, nil, nil), settings{}, account{})
	if got := field(m, "metadata"); got != "<absent>" {
		t.Errorf("Messages client: metadata = %s, want it absent", got)
	}
}

// The Codex endpoint refuses an integer output budget under 16; a smaller one is raised to 16.
// Budgets that are missing, zero, negative or not whole numbers are left to the upstream to judge.
func TestSmallOutputBudgetRaised(t *testing.T) {
	for _, c := range []struct{ name, in, want string }{
		{"1", `1`, `16`},
		{"15", `15`, `16`},
		{"16", `16`, `16`},
		{"17", `17`, `17`},
		{"4096", `4096`, `4096`},
		{"0", `0`, `0`},
		{"negative", `-3`, `-3`},
		{"fraction", `1.5`, `1.5`},
		{"null", `null`, `null`},
	} {
		t.Run("max_output_tokens "+c.name, func(t *testing.T) {
			_, m := shapeWith(t, responsesReq("gpt-test", `{"model":"gpt-test","input":"hi","max_output_tokens":`+c.in+`}`, nil), settings{}, account{})
			if got := field(m, "max_output_tokens"); got != c.want {
				t.Errorf("max_output_tokens %s became %s, want %s", c.in, got, c.want)
			}
		})
	}
	// The budget a Chat client names as max_completion_tokens or max_tokens is copied, then raised.
	for _, k := range []string{"max_tokens", "max_completion_tokens"} {
		_, m := shapeWith(t, responsesReq("gpt-test", `{"model":"gpt-test","input":"hi","`+k+`":1}`, nil), settings{}, account{})
		if got := field(m, "max_output_tokens"); got != "16" {
			t.Errorf("%s 1 became max_output_tokens %s, want 16", k, got)
		}
	}
	// A translated request: Claude Code's model-switch probe asks for one token.
	one := 1
	_, m := shapeWith(t, messagesReq("gpt-test", `{"model":"gpt-test","max_tokens":1}`, &one, nil), settings{}, account{})
	if got := field(m, "max_output_tokens"); got != "16" {
		t.Errorf("Messages max_tokens 1 became max_output_tokens %s, want 16", got)
	}
	// An API key account and a SIWC account take the same minimum.
	_, m = shapeWith(t, responsesReq("gpt-test", `{"model":"gpt-test","input":"hi","max_output_tokens":1}`, nil), settings{}, account{siwc: true})
	if got := field(m, "max_output_tokens"); got != "16" {
		t.Errorf("SIWC account: max_output_tokens %s, want 16", got)
	}
}

const (
	ccSession = "11111111-2222-3333-4444-555555555555"
	ccOther   = "99999999-2222-3333-4444-555555555555"
)

func cacheKeyOf(t *testing.T, req llm.Request, accountID string) (key string, ur *upstreamRequest) {
	t.Helper()
	ur, err := buildRequest(req, accountID, settings{}, account{}, false)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(ur.body, &m); err != nil {
		t.Fatal(err)
	}
	if v, ok := m["prompt_cache_key"]; ok {
		_ = json.Unmarshal(v, &key)
	}
	return key, ur
}

func ccRequest(model, body string, headers map[string]string) llm.Request {
	one := 100
	return messagesReq(model, body, &one, headers)
}

// A Claude Code request's prompt cache key follows its session: the same for every turn of the
// session and agent whatever the first turn said, and different for another session, agent,
// model or account. The session comes from the X-Claude-Code-Session-Id header, or else from
// metadata.user_id in either of the forms Claude Code writes it.
func TestPromptCacheKeyFollowsClaudeCodeSession(t *testing.T) {
	body := func(extra string) string {
		return `{"model":"gpt-test","max_tokens":100` + extra + `,"messages":[{"role":"user","content":"hello"}]}`
	}
	hdr := func(session, agent string) map[string]string {
		h := map[string]string{"x-claude-code-session-id": session}
		if agent != "" {
			h["x-claude-code-agent-id"] = agent
		}
		return h
	}
	k1, ur := cacheKeyOf(t, ccRequest("gpt-test", body(""), hdr(ccSession, "")), "a")
	if k1 == "" {
		t.Fatal("no prompt_cache_key for a Claude Code request with a session header")
	}
	if ur.sessionID != k1 {
		t.Errorf("session ID %q, want the key %q (it is also the Session-Id header)", ur.sessionID, k1)
	}
	// Another first message, same session: same key.
	other := strings.Replace(body(""), "hello", "something else entirely", 1)
	if k, _ := cacheKeyOf(t, ccRequest("gpt-test", other, hdr(ccSession, "")), "a"); k != k1 {
		t.Errorf("same session, another first message: key %q, want %q", k, k1)
	}
	for _, c := range []struct {
		name  string
		req   llm.Request
		acct  string
		equal bool
	}{
		{"another session", ccRequest("gpt-test", body(""), hdr(ccOther, "")), "a", false},
		{"a subagent of the session", ccRequest("gpt-test", body(""), hdr(ccSession, "agent-7")), "a", false},
		{"the main agent named", ccRequest("gpt-test", body(""), hdr(ccSession, "main")), "a", true},
		{"another model", ccRequest("gpt-test-2", body(""), hdr(ccSession, "")), "a", false},
		{"another account", ccRequest("gpt-test", body(""), hdr(ccSession, "")), "b", false},
		{"user_id in the old form", ccRequest("gpt-test", body(`,"metadata":{"user_id":"user_abc123_account_`+ccOther+`_session_`+ccSession+`"}`), nil), "a", true},
		{"user_id as JSON", ccRequest("gpt-test", body(`,"metadata":{"user_id":"{\"device_id\":\"d\",\"session_id\":\"`+ccSession+`\"}"}`), nil), "a", true},
		{"header over user_id", ccRequest("gpt-test", body(`,"metadata":{"user_id":"user_abc_account_x_session_`+ccOther+`"}`), hdr(ccSession, "")), "a", true},
	} {
		k, _ := cacheKeyOf(t, c.req, c.acct)
		if k == "" || (k == k1) != c.equal {
			t.Errorf("%s: key %q, first key %q; want equal %v", c.name, k, k1, c.equal)
		}
	}
	// A client's own prompt_cache_key wins over the session.
	req := responsesReq("gpt-test", `{"model":"gpt-test","input":"hi","prompt_cache_key":"mine"}`, hdr(ccSession, ""))
	if k, _ := cacheKeyOf(t, req, "a"); k != "mine" {
		t.Errorf("client key: got %q, want mine", k)
	}
	// With no session the key still comes from the conversation's start (unchanged).
	if k, _ := cacheKeyOf(t, ccRequest("gpt-test", body(""), nil), "a"); k == "" || k == k1 {
		t.Errorf("no session: key %q, want one derived from the conversation (not %q)", k, k1)
	}
}

// When a Claude Code request also carries the client's own Session-Id header, promptCacheKey
// defers to it instead of deriving a conflicting prompt_cache_key from the Claude session.
func TestPromptCacheKeyDefersToSessionIDHeader(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		headers map[string]string
	}{
		{
			name:    "session header",
			body:    `{"model":"gpt-test","max_tokens":100,"messages":[{"role":"user","content":"hello"}]}`,
			headers: map[string]string{"x-claude-code-session-id": ccSession, "session-id": "client-sess"},
		},
		{
			name:    "metadata user_id",
			body:    `{"model":"gpt-test","max_tokens":100,"metadata":{"user_id":"user_abc_account_x_session_` + ccSession + `"},"messages":[{"role":"user","content":"hello"}]}`,
			headers: map[string]string{"session-id": "client-sess"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := ccRequest("gpt-test", tc.body, tc.headers)
			key, ur := cacheKeyOf(t, req, "a")
			if key != "" || ur.sessionID != "" {
				t.Errorf("prompt_cache_key = %q, ur.sessionID = %q; want both empty when Session-Id is set", key, ur.sessionID)
			}
			h := http.Header{}
			setHeaders(h, req, ur, settings{}, account{})
			if got := h.Get("Session-Id"); got != "client-sess" {
				t.Errorf("wire Session-Id = %q, want client-sess", got)
			}
		})
	}
}

// catalogLevels returns a catalog model's reasoning levels, in the catalog's order.
func catalogLevels(t *testing.T, slug string) []string {
	t.Helper()
	var levels []struct {
		Effort string `json:"effort"`
	}
	if err := json.Unmarshal(clientTemplates()[slug]["supported_reasoning_levels"], &levels); err != nil {
		t.Fatalf("%s: %v", slug, err)
	}
	var out []string
	for _, l := range levels {
		out = append(out, l.Effort)
	}
	return out
}

// modelWithout returns a catalog model that does not take level.
func modelWithout(t *testing.T, level string) string {
	t.Helper()
	for slug := range clientTemplates() {
		if levels := catalogLevels(t, slug); len(levels) > 0 && !slices.Contains(levels, level) {
			return slug
		}
	}
	t.Fatalf("no catalog model lacks %q", level)
	return ""
}

// A reasoning effort the model does not take is clamped to a level it does, and the change is
// reported as a loss. An effort the model takes, a model the catalog does not list and an effort
// the extension does not know are left as they are.
func TestEffortClampedToModelLevels(t *testing.T) {
	noUltra := modelWithout(t, "ultra")
	body := func(model, effort string) string {
		return `{"model":"` + model + `","input":"hi","reasoning":{"effort":"` + effort + `"}}`
	}
	for _, c := range []struct {
		model, in, want string
		loss            bool
	}{
		{noUltra, "ultra", "max", true},
		{noUltra, "ULTRA", "max", true},
		{noUltra, "max", "max", false},
		{noUltra, "medium", "medium", false},
		{noUltra, "minimal", "low", true},
		{noUltra, "none", "low", true},
		{"not-in-the-catalog", "ultra", "ultra", false},
		{noUltra, "auto", "auto", false},
	} {
		ur, m := shapeWith(t, responsesReq(c.model, body(c.model, c.in), nil), settings{}, account{})
		if got := effortOf(t, m); got != c.want {
			t.Errorf("%s effort %s became %s, want %s", c.model, c.in, got, c.want)
		}
		var found *llm.Loss
		for i := range ur.tr.Losses {
			if ur.tr.Losses[i].Path == "reasoning.effort" {
				found = &ur.tr.Losses[i]
			}
		}
		if (found != nil) != c.loss {
			t.Errorf("%s effort %s: losses %+v, want a reasoning.effort loss: %v", c.model, c.in, ur.tr.Losses, c.loss)
		}
		if found != nil && (found.Kind != llm.LossApproximated || !strings.Contains(found.Detail, c.in) || !strings.Contains(found.Detail, c.want)) {
			t.Errorf("loss %+v: want kind approximated naming %s and %s", *found, c.in, c.want)
		}
	}
	// A thinking suffix's effort and a Messages client's output_config effort are clamped too.
	req := responsesReq(noUltra, body(noUltra, "low"), nil)
	req.Options.ThinkingSuffix = "ultra"
	if _, m := shapeWith(t, req, settings{}, account{}); effortOf(t, m) != "max" {
		t.Errorf("thinking suffix ultra: effort %s, want max", effortOf(t, m))
	}
	_, m := shapeWith(t, messagesReq(noUltra, `{"model":"x","max_tokens":9,"output_config":{"effort":"ultra"}}`, nil, nil), settings{}, account{})
	if got := effortOf(t, m); got != "max" {
		t.Errorf("Messages output_config effort ultra: %s, want max", got)
	}
}

// image_generation is offered to every model unless the account lists the model (an exact name or
// a pattern with * and ?) in image_generation_off_models.
func TestImageToolOffPerModel(t *testing.T) {
	has := func(m map[string]json.RawMessage) bool { return slices.Contains(toolTypes(t, m), "image_generation") }
	body := func(model string) string { return `{"model":"` + model + `","input":"hi"}` }
	s := settingsFrom(t, `{"image_generation_off_models":["gpt-6-luna","gpt-5.*"]}`)
	for model, want := range map[string]bool{"gpt-6-luna": false, "gpt-5.6-terra": false, "GPT-6-LUNA": false, "gpt-6.1-sol": true} {
		_, m := shapeWith(t, responsesReq(model, body(model), nil), s, account{})
		if has(m) != want {
			t.Errorf("%s: image_generation offered %v, want %v", model, has(m), want)
		}
	}
	if _, m := shapeWith(t, responsesReq("gpt-6-luna", body("gpt-6-luna"), nil), settings{}, account{}); !has(m) {
		t.Error("no setting: image_generation not offered")
	}
	// The client's own image_generation tool stays.
	own := `{"model":"gpt-6-luna","input":"hi","tools":[{"type":"image_generation","output_format":"webp"}]}`
	if _, m := shapeWith(t, responsesReq("gpt-6-luna", own, nil), s, account{}); !strings.Contains(string(m["tools"]), "webp") {
		t.Errorf("the client's own image tool was removed: %s", m["tools"])
	}
	if _, err := readSettings(json.RawMessage(`{"image_generation_off_models":["["]}`)); err == nil {
		t.Error("a malformed pattern was accepted")
	}
}

// codex_client_version sets the version in the User-Agent the extension sends in place of the
// client's; unset, the default stays.
func TestCodexClientVersionSetting(t *testing.T) {
	ua := func(s settings) string {
		h := http.Header{}
		setIdentity(h, nil, s, account{})
		return h.Get("User-Agent")
	}
	if got := ua(settings{}); !strings.HasPrefix(got, "codex-tui/0.159.0 (") || !strings.HasSuffix(got, "(codex-tui; 0.159.0)") {
		t.Errorf("default User-Agent %q", got)
	}
	got := ua(settingsFrom(t, `{"codex_client_version":"0.201.3-beta.1"}`))
	if !strings.HasPrefix(got, "codex-tui/0.201.3-beta.1 (") || !strings.HasSuffix(got, "(codex-tui; 0.201.3-beta.1)") || strings.Contains(got, "0.159.0") {
		t.Errorf("configured User-Agent %q", got)
	}
	for _, bad := range []string{`"0.1 2"`, `"1.0\r\nX: y"`, `"a/b"`, `" "`} {
		if _, err := readSettings(json.RawMessage(`{"codex_client_version":` + bad + `}`)); err == nil {
			t.Errorf("codex_client_version %s accepted", bad)
		}
	}
}

// prompt_cache_retention is removed from the request unless the account's
// prompt_cache_retention_passthrough is on; a SIWC account never sends it.
func TestPromptCacheRetentionSetting(t *testing.T) {
	body := `{"model":"gpt-test","input":"hi","prompt_cache_retention":"24h"}`
	_, m := shapeWith(t, responsesReq("gpt-test", body, nil), settings{}, account{})
	if got := field(m, "prompt_cache_retention"); got != "<absent>" {
		t.Errorf("default: prompt_cache_retention = %s, want it removed", got)
	}
	on := settingsFrom(t, `{"prompt_cache_retention_passthrough":true}`)
	_, m = shapeWith(t, responsesReq("gpt-test", body, nil), on, account{})
	if got := field(m, "prompt_cache_retention"); got != `"24h"` {
		t.Errorf("passthrough on: prompt_cache_retention = %s, want \"24h\"", got)
	}
	_, m = shapeWith(t, responsesReq("gpt-test", body, nil), on, account{siwc: true})
	if got := field(m, "prompt_cache_retention"); got != "<absent>" {
		t.Errorf("SIWC account: prompt_cache_retention = %s, want it removed", got)
	}
}

// Requests go through the real extension program to a fake Codex endpoint. A Claude Code
// Messages request with a one-token budget arrives with 16, and a prompt cache key that follows its
// session; a Responses client's metadata is gone; and the configured client version is in the
// User-Agent.
func TestShapingThroughTheExtension(t *testing.T) {
	manifest := conformance.BuildExtension(t, "pluxy-extension.json", ".")
	srv := codextest.New(t)
	settingsJSON, _ := json.Marshal(map[string]any{
		"base_url": srv.URL + "/backend-api/codex", "auth_base_url": srv.URL, "callback_listen": false,
		"codex_client_version": "0.201.3",
	})
	account := protocol.Account{ID: "codex", Settings: settingsJSON, Credential: subscription(srv.IssueToken("text"), "")}
	ext, err := host.Start(manifest, host.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ext.Stop)

	// send makes one call and returns the request the upstream received.
	send := func(req llm.Request) codextest.Request {
		t.Helper()
		before := len(srv.Requests())
		s := ext.Generate(context.Background(), account, req)
		defer s.Close()
		for {
			ev := s.Next()
			if ev.Type == llm.EventError {
				t.Fatalf("the call failed: %+v", ev.Error)
			}
			if ev.Terminal() {
				break
			}
		}
		all := srv.Requests()
		if len(all) != before+1 || !strings.HasSuffix(all[before].Path, "/responses") {
			t.Fatalf("%d requests reached the upstream after %d, want one more to /responses: %+v", len(all), before, all)
		}
		return all[before]
	}

	one := 1
	body := `{"model":"gpt-6.1-sol","max_tokens":1,"metadata":{"user_id":"user_x_account_y_session_` + ccSession + `"},"messages":[{"role":"user","content":"hi"}]}`
	r := send(messagesReq("gpt-6.1-sol", body, &one, map[string]string{"x-claude-code-session-id": ccSession}))
	if v, ok := r.Body["max_output_tokens"].(float64); !ok || v != 16 {
		t.Errorf("Messages: max_output_tokens = %v, want 16", r.Body["max_output_tokens"])
	}
	if v, ok := r.Body["metadata"]; ok {
		t.Errorf("Messages: metadata reached the upstream: %v", v)
	}
	key, _ := r.Body["prompt_cache_key"].(string)
	want, _ := cacheKeyOf(t, messagesReq("gpt-6.1-sol", body, &one, map[string]string{"x-claude-code-session-id": ccSession}), "codex")
	if key == "" || key != want || r.Header.Get("Session-Id") != key {
		t.Errorf("Messages: prompt_cache_key %q, Session-Id %q; want the session's key %q in both", key, r.Header.Get("Session-Id"), want)
	}
	if ua := r.Header.Get("User-Agent"); !strings.HasPrefix(ua, "codex-tui/0.201.3 (") {
		t.Errorf("Messages: User-Agent %q, want the configured client version", ua)
	}

	r = send(responsesReq("gpt-6.1-sol", `{"model":"gpt-6.1-sol","input":"hi","max_output_tokens":1,"metadata":{"k":"v"}}`, nil))
	if v, ok := r.Body["max_output_tokens"].(float64); !ok || v != 16 {
		t.Errorf("Responses: max_output_tokens = %v, want 16", r.Body["max_output_tokens"])
	}
	if v, ok := r.Body["metadata"]; ok {
		t.Errorf("Responses: metadata reached the upstream: %v", v)
	}
}

package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/stdotwork/pluxy/next/extensions/gemini/geminitest"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

// resolveFor returns where an Interactions request goes for an account with settings and cred.
func resolveFor(t *testing.T, settings string, cred *protocol.Credential, stream bool) target {
	t.Helper()
	s, err := readSettings(json.RawMessage(settings))
	if err != nil {
		t.Fatal(err)
	}
	tg, err := resolveInteractions(context.Background(), s, cred, stream)
	if err != nil {
		t.Fatal(err)
	}
	return tg
}

// sameJSON fails the test unless got holds the same JSON as want; object key order does not count.
func sameJSON(t *testing.T, got []byte, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("got is not JSON: %s: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("bad expected literal %s: %v", want, err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestResolveInteractionsGeminiAPIKey(t *testing.T) {
	key := &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "geminitest-key-interactions"}
	// A trailing slash on base_url is trimmed; the stream flag adds no query for the Gemini API.
	const settings = `{"base_url":"https://gemini.example.test/","interactions":true}`
	for _, stream := range []bool{false, true} {
		tg := resolveFor(t, settings, key, stream)
		if want := "https://gemini.example.test/v1beta/interactions"; tg.url != want {
			t.Errorf("stream %v: url %s, want %s", stream, tg.url, want)
		}
		h := http.Header{}
		tg.authorize(h)
		if got := h.Get("X-Goog-Api-Key"); got != key.APIKey {
			t.Errorf("stream %v: X-Goog-Api-Key = %q, want the account's key", stream, got)
		}
		if tg.token != "" || h.Get("Authorization") != "" {
			t.Errorf("stream %v: an API key request carries a bearer token", stream)
		}
	}
}

func TestResolveInteractionsVertexExpressKey(t *testing.T) {
	key := &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "geminitest-key-express"}
	const settings = `{"base_url":"https://express.example.test","vertex":{},"interactions":true}`
	for _, c := range []struct {
		stream bool
		want   string
	}{
		{false, "https://express.example.test/v1beta1/interactions"},
		{true, "https://express.example.test/v1beta1/interactions?alt=sse"},
	} {
		tg := resolveFor(t, settings, key, c.stream)
		if tg.url != c.want {
			t.Errorf("stream %v: url %s, want %s", c.stream, tg.url, c.want)
		}
		h := http.Header{}
		tg.authorize(h)
		if got := h.Get("X-Goog-Api-Key"); got != key.APIKey {
			t.Errorf("stream %v: X-Goog-Api-Key = %q, want the account's key", c.stream, got)
		}
	}
}

func TestResolveInteractionsVertexExpressKeyProject(t *testing.T) {
	key := &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "geminitest-key-express"}
	const settings = `{"base_url":"https://express.example.test","vertex":{"project":"proj-1"},"interactions":true}`
	for _, c := range []struct {
		stream bool
		want   string
	}{
		{false, "https://express.example.test/v1beta1/projects/proj-1/locations/global/interactions"},
		{true, "https://express.example.test/v1beta1/projects/proj-1/locations/global/interactions?alt=sse"},
	} {
		if tg := resolveFor(t, settings, key, c.stream); tg.url != c.want {
			t.Errorf("stream %v: url %s, want %s", c.stream, tg.url, c.want)
		}
	}
}

// With no base_url, Vertex express requests go to the default Vertex host; only the URL is checked,
// no request is sent.
func TestResolveInteractionsDefaultHost(t *testing.T) {
	key := &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "geminitest-key-default"}
	const settings = `{"vertex":{},"interactions":true}`
	if tg := resolveFor(t, settings, key, false); tg.url != "https://aiplatform.googleapis.com/v1beta1/interactions" {
		t.Errorf("url %s, want the default Vertex host", tg.url)
	}
	if tg := resolveFor(t, settings, key, true); tg.url != "https://aiplatform.googleapis.com/v1beta1/interactions?alt=sse" {
		t.Errorf("stream url %s, want the default Vertex host with alt=sse", tg.url)
	}
}

// A service account's Interactions request is authorised with a bearer token from the token
// endpoint (the fake Google's), and its location stays global even when settings name another.
func TestResolveInteractionsServiceAccount(t *testing.T) {
	tokens = &tokenCache{entries: map[string]*tokenEntry{}}
	srv := geminitest.New(t)
	cred := &protocol.Credential{Kind: CredentialServiceAccount, Data: srv.ServiceAccount("proj-1", true)}
	settings := `{"base_url":"` + srv.URL + `","vertex":{"location":"us-central1","project":"proj-1"}}`
	for _, c := range []struct {
		stream bool
		want   string
	}{
		{false, srv.URL + "/v1beta1/projects/proj-1/locations/global/interactions"},
		{true, srv.URL + "/v1beta1/projects/proj-1/locations/global/interactions?alt=sse"},
	} {
		tg := resolveFor(t, settings, cred, c.stream)
		if tg.url != c.want {
			t.Errorf("stream %v: url %s, want %s", c.stream, tg.url, c.want)
		}
		h := http.Header{}
		tg.authorize(h)
		if tg.token == "" || h.Get("Authorization") != "Bearer "+tg.token {
			t.Errorf("stream %v: Authorization = %q, want the fetched bearer token", c.stream, h.Get("Authorization"))
		}
	}
	// Both calls share one cached token: the token endpoint issued one.
	if n := srv.Grants(); n != 1 {
		t.Errorf("%d access tokens fetched, want 1", n)
	}

	// Without vertex.project the key's project_id is used.
	noProject := `{"base_url":"` + srv.URL + `","vertex":{"location":"us-central1"}}`
	if tg := resolveFor(t, noProject, cred, false); tg.url != srv.URL+"/v1beta1/projects/proj-1/locations/global/interactions" {
		t.Errorf("url %s, want the key's project_id in the path", tg.url)
	}

	// Without base_url the default Vertex host is used; no request goes there, only the token fetch.
	defaultHost := `{"vertex":{"project":"proj-1"}}`
	if tg := resolveFor(t, defaultHost, cred, false); tg.url != "https://aiplatform.googleapis.com/v1beta1/projects/proj-1/locations/global/interactions" {
		t.Errorf("url %s, want the default Vertex host", tg.url)
	}
}

func TestSanitizeInput(t *testing.T) {
	for _, c := range []struct {
		name    string
		in      string
		want    string
		changed bool
	}{
		{"function_call without id takes call_id as id",
			`[{"type":"function_call","call_id":"call_9","name":"lookup"}]`,
			`[{"type":"function_call","id":"call_9","name":"lookup"}]`, true},
		{"function_call with both keeps its id and loses call_id",
			`[{"type":"function_call","id":"fc_1","call_id":"call_9","name":"lookup"}]`,
			`[{"type":"function_call","id":"fc_1","name":"lookup"}]`, true},
		{"other step loses id, and its content parts lose ids",
			`[{"type":"user_input","id":"u_1","content":[{"type":"text","id":"p_1","text":"hi"}]}]`,
			`[{"type":"user_input","content":[{"type":"text","text":"hi"}]}]`, true},
		{"thought with no signature is dropped",
			`[{"type":"thought","id":"t_1","content":[{"type":"text","text":"x"}]},{"type":"user_input","content":[{"type":"text","text":"hi"}]}]`,
			`[{"type":"user_input","content":[{"type":"text","text":"hi"}]}]`, true},
		{"thought with a blank signature is dropped",
			`[{"type":"thought","signature":"   "}]`, `[]`, true},
		{"thought_signature moves to signature and the other fields go",
			`[{"type":"thought","id":"t_2","content":[{"type":"text","text":"x"}],"text":"x","thought_signature":"SIG_A"}]`,
			`[{"type":"thought","signature":"SIG_A"}]`, true},
		{"thoughtSignature moves to signature",
			`[{"type":"thought","id":"t_3","text":"y","thoughtSignature":"SIG_B"}]`,
			`[{"type":"thought","signature":"SIG_B"}]`, true},
		{"signature is kept and the other fields go",
			`[{"type":"thought","id":"t_4","signature":"SIG_C","content":[{"type":"text","text":"z"}]}]`,
			`[{"type":"thought","signature":"SIG_C"}]`, true},
		{"the first non-blank spelling of the signature wins",
			`[{"type":"thought","signature":"  ","thought_signature":"SIG_D","thoughtSignature":"SIG_E"}]`,
			`[{"type":"thought","signature":"SIG_D"}]`, true},
		{"a signed thought is kept whatever its signature looks like",
			`[{"type":"thought","signature":"not-issued-by-gemini"}]`,
			`[{"type":"thought","signature":"not-issued-by-gemini"}]`, false},
		{"an input with nothing to remove is unchanged",
			`[{"type":"user_input","content":[{"type":"text","text":"hi"}]}]`,
			`[{"type":"user_input","content":[{"type":"text","text":"hi"}]}]`, false},
		{"a string input is unchanged",
			`"hello"`, `"hello"`, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			out, changed := sanitizeInput(json.RawMessage(c.in))
			if changed != c.changed {
				t.Errorf("changed = %v, want %v", changed, c.changed)
			}
			sameJSON(t, out, c.want)
		})
	}
}

// A string input is returned byte for byte as it came.
func TestSanitizeInputStringBytesUnchanged(t *testing.T) {
	in := json.RawMessage(`"hello"`)
	if out, changed := sanitizeInput(in); changed || string(out) != `"hello"` {
		t.Errorf("sanitizeInput(%s) = %s, %v; want it unchanged", in, out, changed)
	}
}

func TestInteractionsBody(t *testing.T) {
	body, stream, err := interactionsBody(json.RawMessage(`{"model":"client-name","input":[{"type":"function_call","call_id":"call_9","name":"lookup"}],"stream":true}`), "gemini-real")
	if err != nil {
		t.Fatal(err)
	}
	if !stream {
		t.Error(`"stream": true reported as no stream`)
	}
	sameJSON(t, body, `{"model":"gemini-real","input":[{"type":"function_call","id":"call_9","name":"lookup"}],"stream":true}`)

	// An agent body names no model, so none is added.
	body, stream, err = interactionsBody(json.RawMessage(`{"agent":"deep-research","input":"hi"}`), "gemini-x")
	if err != nil {
		t.Fatal(err)
	}
	if stream {
		t.Error("a body without a stream flag reported as a stream")
	}
	sameJSON(t, body, `{"agent":"deep-research","input":"hi"}`)

	// Only the boolean true asks for a stream.
	for _, raw := range []string{`{"model":"m","input":"hi","stream":false}`, `{"model":"m","input":"hi","stream":"true"}`} {
		if _, stream, err := interactionsBody(json.RawMessage(raw), "gemini-x"); err != nil || stream {
			t.Errorf("%s: stream = %v, err = %v; want no stream", raw, stream, err)
		}
	}
}

func want64(t *testing.T, name string, got *int64, want int64) {
	t.Helper()
	if got == nil {
		t.Errorf("%s is nil, want %d", name, want)
	} else if *got != want {
		t.Errorf("%s = %d, want %d", name, *got, want)
	}
}

func TestInteractionsUsage(t *testing.T) {
	long := interactionsUsage(json.RawMessage(`{"total_input_tokens":11,"total_output_tokens":7,"total_cached_tokens":3,"total_thought_tokens":5}`))
	short := interactionsUsage(json.RawMessage(`{"input_tokens":11,"output_tokens":7,"cached_tokens":3,"reasoning_tokens":5}`))
	for name, u := range map[string]*llm.Usage{"total_ names": long, "short names": short} {
		if u == nil {
			t.Errorf("%s: usage is nil", name)
			continue
		}
		want64(t, name+" input", u.InputTokens, 11)
		want64(t, name+" output", u.OutputTokens, 7)
		want64(t, name+" cached", u.CachedInputTokens, 3)
		want64(t, name+" reasoning", u.ReasoningTokens, 5)
	}
	// Either an input or an output count is enough for a usage.
	if u := interactionsUsage(json.RawMessage(`{"total_input_tokens":4}`)); u == nil {
		t.Error("input count alone: usage is nil")
	} else if u.OutputTokens != nil {
		t.Errorf("output = %d, want nil when the upstream named none", *u.OutputTokens)
	}
	for _, raw := range []string{``, `null`, `{}`, `{"total_cached_tokens":3}`} {
		if u := interactionsUsage(json.RawMessage(raw)); u != nil {
			t.Errorf("interactionsUsage(%q) = %+v, want nil", raw, u)
		}
	}
}

func TestStreamUsage(t *testing.T) {
	complete := `{"event_type":"interaction.complete","interaction":{"id":"i_1","status":"completed","usage":{"total_input_tokens":11,"total_output_tokens":7}}}`
	if u := streamUsage(complete); u == nil {
		t.Error("interaction.usage: usage is nil")
	} else {
		want64(t, "interaction.usage input", u.InputTokens, 11)
		want64(t, "interaction.usage output", u.OutputTokens, 7)
	}

	top := `{"event_type":"usage","usage":{"input_tokens":2,"output_tokens":1}}`
	if u := streamUsage(top); u == nil {
		t.Error("top-level usage: usage is nil")
	} else {
		want64(t, "top-level usage input", u.InputTokens, 2)
		want64(t, "top-level usage output", u.OutputTokens, 1)
	}

	// Where an event carries both, the interaction's usage is the one read.
	both := `{"interaction":{"usage":{"total_input_tokens":11}},"usage":{"total_input_tokens":99}}`
	if u := streamUsage(both); u == nil {
		t.Error("both: usage is nil")
	} else {
		want64(t, "both input", u.InputTokens, 11)
	}

	for _, data := range []string{`{"event_type":"interaction.start","interaction":{"id":"i_1"}}`, `{"event_type":"step.delta","delta":{"text":"hi"}}`, `not json`} {
		if u := streamUsage(data); u != nil {
			t.Errorf("streamUsage(%s) = %+v, want nil", data, u)
		}
	}
}

func endpointCall(settings, format, body string) sdk.Call {
	return sdk.Call{
		Account: protocol.Account{ID: "i", Settings: json.RawMessage(settings)},
		Request: llm.Request{Model: "gemini-x", Native: &llm.Native{Format: format, Body: json.RawMessage(body)}},
	}
}

// refusedCall runs an endpoint call that must be refused before anything is sent, and returns its error.
func refusedCall(t *testing.T, call sdk.Call) *llm.Error {
	t.Helper()
	err := endpoint(context.Background(), call, nil)
	var e *llm.Error
	if !errors.As(err, &e) {
		t.Fatalf("err = %v, want an *llm.Error", err)
	}
	return e
}

func TestEndpointRefusals(t *testing.T) {
	var hits atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.NotFound(w, r)
	}))
	defer ts.Close()
	const body = `{"model":"gemini-x","input":"hi"}`
	off := `{"base_url":"` + ts.URL + `"}`
	on := `{"base_url":"` + ts.URL + `","interactions":true}`

	t.Run("account without interactions", func(t *testing.T) {
		e := refusedCall(t, endpointCall(off, llm.FormatGeminiInteractions, body))
		if e.Code != llm.CodeUnsupported || e.Scope != llm.ScopeCredential || !e.Retryable {
			t.Errorf("error = %+v, want unsupported, scope credential, retryable", e)
		}
	})

	t.Run("format other than gemini.interactions", func(t *testing.T) {
		e := refusedCall(t, endpointCall(on, llm.FormatGeminiGenerate, body))
		if e.Code != llm.CodeUnsupported || e.Scope != llm.ScopeCredential || !e.Retryable {
			t.Errorf("error = %+v, want unsupported, scope credential, retryable", e)
		}
	})

	t.Run("no native body at all", func(t *testing.T) {
		call := sdk.Call{Account: protocol.Account{ID: "i", Settings: json.RawMessage(on)}, Request: llm.Request{Model: "gemini-x"}}
		e := refusedCall(t, call)
		if e.Code != llm.CodeUnsupported || e.Scope != llm.ScopeCredential || !e.Retryable {
			t.Errorf("error = %+v, want unsupported, scope credential, retryable", e)
		}
	})

	t.Run("thinking suffix", func(t *testing.T) {
		call := endpointCall(on, llm.FormatGeminiInteractions, body)
		call.Request.Options.ThinkingSuffix = "high"
		e := refusedCall(t, call)
		if e.Code != llm.CodeInvalidRequest || e.Scope != llm.ScopeRequest {
			t.Errorf("error = %+v, want invalid_request with scope request", e)
		}
	})

	if n := hits.Load(); n != 0 {
		t.Errorf("the refused calls reached the upstream %d times", n)
	}
}

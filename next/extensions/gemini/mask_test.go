package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

const maskedProject = "owner-proj-81"

func failWith(err error) sdk.Handler {
	return func(context.Context, sdk.Call, *sdk.Stream) error { return err }
}

// An error a client-facing handler returns loses the account's own project and identity, wherever
// it is named, and keeps its classification.
func TestMaskedErrorsLoseTheAccountsProject(t *testing.T) {
	sa := json.RawMessage(`{"type":"service_account","project_id":"` + maskedProject + `","client_email":"svc@` + maskedProject + `.iam.gserviceaccount.com"}`)
	call := sdk.Call{Account: protocol.Account{
		Settings:   json.RawMessage(`{"vertex":{"project":"settings-proj-9","location":"global"}}`),
		Credential: &protocol.Credential{Kind: CredentialServiceAccount, Data: sa},
	}}
	e := &llm.Error{Code: llm.CodeRateLimit, Scope: llm.ScopeModel, Status: 429, ProviderCode: "RESOURCE_EXHAUSTED", Retryable: true, RetryAfterMS: 34000,
		Message: `quota of settings-proj-9 used by svc@owner-proj-81.iam.gserviceaccount.com`,
		Raw:     json.RawMessage(`{"error":{"code":429,"message":"x","status":"RESOURCE_EXHAUSTED","details":[{"@type":"t","metadata":{"project":"owner-proj-81"},"retryDelay":"34s"}]}}`)}
	got := masked(failWith(e))(context.Background(), call, nil)
	var le *llm.Error
	if !errors.As(got, &le) {
		t.Fatalf("got %v, want an *llm.Error", got)
	}
	for _, name := range []string{"settings-proj-9", maskedProject, "svc@"} {
		if strings.Contains(le.Message, name) || strings.Contains(string(le.Raw), name) {
			t.Errorf("%s survives: %q %s", name, le.Message, le.Raw)
		}
	}
	if le.Code != e.Code || le.Scope != e.Scope || le.Status != e.Status || le.ProviderCode != e.ProviderCode || !le.Retryable || le.RetryAfterMS != e.RetryAfterMS {
		t.Errorf("classification changed: %+v", le)
	}
	if !strings.Contains(string(le.Raw), `"retryDelay":"34s"`) {
		t.Errorf("raw lost the retry delay: %s", le.Raw)
	}
}

// An error that is not an *llm.Error becomes one only when its text names the project (a failed
// request's URL does); otherwise it is returned as it is.
func TestMaskedOtherErrors(t *testing.T) {
	call := sdk.Call{Account: protocol.Account{Settings: json.RawMessage(`{"vertex":{"project":"settings-proj-9"}}`)}}
	got := masked(failWith(errors.New(`Post "https://aiplatform.googleapis.com/v1/projects/settings-proj-9/locations/global": EOF`)))(context.Background(), call, nil)
	var le *llm.Error
	if !errors.As(got, &le) || strings.Contains(le.Message, "settings-proj-9") || le.Code != llm.CodeInternal {
		t.Errorf("got %#v, want an internal error without the project", got)
	}
	plain := errors.New("boom")
	if got := masked(failWith(plain))(context.Background(), call, nil); got != plain {
		t.Errorf("got %v, want the error itself", got)
	}
	if got := masked(failWith(nil))(context.Background(), call, nil); got != nil {
		t.Errorf("got %v, want nil", got)
	}
}

// An upstream that is down is "unavailable" with the request's URL in its message; the Vertex
// project in that URL does not reach the client.
func TestMaskedUnreachableVertexNamesNoProject(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()
	call := sdk.Call{
		Account: protocol.Account{ID: "v", Settings: json.RawMessage(`{"base_url":"` + url + `","vertex":{"project":"vertex-proj-3","location":"us-central1"}}`),
			Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "k"}},
		Request: llm.Request{Model: "gemini-x", Native: nil},
	}
	err := masked(func(ctx context.Context, c sdk.Call, out *sdk.Stream) error {
		_, err := postBody(ctx, target{url: url + "/v1/projects/vertex-proj-3/locations/us-central1/x", authorize: func(http.Header) {}}, []byte(`{}`), false, "gemini-x")
		return err
	})(context.Background(), call, nil)
	var le *llm.Error
	if !errors.As(err, &le) || le.Code != llm.CodeUnavailable {
		t.Fatalf("err = %v, want unavailable", err)
	}
	if strings.Contains(le.Message, "vertex-proj-3") {
		t.Errorf("message names the project: %q", le.Message)
	}
}

// The owner's own calls are not wrapped: the account check shows the project the API must be
// enabled in.
func TestCheckKeepsTheProject(t *testing.T) {
	const reply = `{"error":{"code":403,"message":"Generative Language API has not been used in project 738291456021 before or it is disabled.","status":"PERMISSION_DENIED",` +
		`"details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"SERVICE_DISABLED","metadata":{"containerInfo":"738291456021"}}]}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, reply)
	}))
	defer srv.Close()
	_, err := runCheck(json.RawMessage(`{"base_url":"`+srv.URL+`"}`), &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "k"})
	e := checkError(t, err)
	if !strings.Contains(e.Message, "738291456021") || !strings.Contains(string(e.Raw), "738291456021") {
		t.Errorf("the account check lost the project: %q %s", e.Message, e.Raw)
	}
}

// Only an error is masked in what an Interactions client gets as the upstream sent it: a model's
// text that mentions a project path stays, while a top-level object or string "error" member and
// an event named "error" are masked.
func TestMaskReply(t *testing.T) {
	text := []byte(`{"event_type":"content.delta","delta":{"text":"see projects/123/locations/x"}}`)
	if got := maskReply(text, "", nil); &got[0] != &text[0] {
		t.Errorf("an event with no error changed: %s", got)
	}
	e := []byte(`{"error":{"message":"The consumer 'projects/123' is suspended."}}`)
	if got := maskReply(e, "", nil); strings.Contains(string(got), "123") {
		t.Errorf("got %s", got)
	}
	strErr := []byte(`{"error":"projects/123"}`)
	if got := maskReply(strErr, "", nil); string(got) != `{"error":"projects/[redacted]"}` {
		t.Errorf("got %s", got)
	}
	if got := maskReply([]byte(`{"message":"projects/123"}`), "error", nil); strings.Contains(string(got), "123") {
		t.Errorf("an event named error was not masked: %s", got)
	}
}

// An Interactions reply or event with an "error" member one object level down (as a failed
// interaction has) masks only that member; one with no matching error member is the same bytes,
// whatever its text says.
func TestMaskReplyNestedError(t *testing.T) {
	nested := []byte(`{"event_type":"interaction.complete","interaction":{"id":"v1_x","status":"failed","error":{"code":403,"message":"The consumer 'projects/123' is suspended."}}}`)
	got := maskReply(nested, "", nil)
	if strings.Contains(string(got), "123") || !strings.Contains(string(got), `"status":"failed"`) || !json.Valid(got) {
		t.Errorf("got %s", got)
	}
	for _, same := range []string{
		`{"outputs":[{"type":"text","text":"an error in projects/123: project 'abc' failed"}]}`,
		`{"outputs":[{"type":"text","text":"the \"error\" word"}],"error":null}`,
	} {
		b := []byte(same)
		if got := maskReply(b, "", nil); &got[0] != &b[0] {
			t.Errorf("%s changed to %s", same, got)
		}
	}
}

// In an Interactions reply, only the value of an "error" member (object or string) at the top
// level or one object level down — never inside an array — is masked; model text and tool output
// outside that value keep their exact bytes. An event named "error" is still masked whole.
func TestMaskReplyOnlyErrors(t *testing.T) {
	t.Run("completed reply with tool error object stays byte-identical", func(t *testing.T) {
		in := []byte(`{"id":"v1_x","status":"completed","steps":[{"type":"text","text":"The project lives at projects/acme-prod-4821/models/x"},{"type":"function_result","result":{"error":{"code":"tool_failed","message":"projects/999"}}}]}`)
		if got := maskReply(in, "", []string{"acme-prod-4821"}); string(got) != string(in) || &got[0] != &in[0] {
			t.Errorf("completed reply changed:\n got  %s\n want %s", got, in)
		}
	})
	t.Run("top-level error object leaves sibling text untouched", func(t *testing.T) {
		in := []byte(`{"id":"v1_x","outputs":[{"type":"text","text":"see projects/999"}],"error":{"code":403,"message":"The consumer 'projects/123' is suspended."}}`)
		want := `{"id":"v1_x","outputs":[{"type":"text","text":"see projects/999"}],"error":{"code":403,"message":"The consumer 'projects/[redacted]' is suspended."}}`
		if got := maskReply(in, "", nil); string(got) != want {
			t.Errorf("got  %s\nwant %s", got, want)
		}
	})
	t.Run("one level down error object leaves outer fields untouched", func(t *testing.T) {
		in := []byte(`{"event_type":"interaction.complete","note":"projects/999","interaction":{"id":"v1_x","status":"failed","text":"projects/888","error":{"code":403,"message":"The consumer 'projects/123' is suspended."}}}`)
		want := `{"event_type":"interaction.complete","note":"projects/999","interaction":{"id":"v1_x","status":"failed","text":"projects/888","error":{"code":403,"message":"The consumer 'projects/[redacted]' is suspended."}}}`
		if got := maskReply(in, "", nil); string(got) != want {
			t.Errorf("got  %s\nwant %s", got, want)
		}
	})
	t.Run("string error at top level and one level down", func(t *testing.T) {
		in := []byte(`{"error":"The consumer 'projects/123' is suspended.","interaction":{"status":"failed","error":"project 456 failed","text":"projects/999"}}`)
		want := `{"error":"The consumer 'projects/[redacted]' is suspended.","interaction":{"status":"failed","error":"project [redacted] failed","text":"projects/999"}}`
		if got := maskReply(in, "", nil); string(got) != want {
			t.Errorf("got  %s\nwant %s", got, want)
		}
	})
	t.Run("failed interaction masks error at any depth and inside arrays", func(t *testing.T) {
		in := []byte(`{"status":"failed","outputs":[{"type":"x","detail":{"error":{"message":"project 987654 is off"}}}]}`)
		want := `{"status":"failed","outputs":[{"type":"x","detail":{"error":{"message":"project [redacted] is off"}}}]}`
		if got := maskReply(in, "", nil); string(got) != want {
			t.Errorf("got  %s\nwant %s", got, want)
		}
		inNested := []byte(`{"event_type":"interaction.complete","interaction":{"id":"v1_x","status":"failed","outputs":[{"type":"text","text":"keep model text projects/keep"},{"type":"tool_result","error":{"message":"projects/123"}}]}}`)
		wantNested := `{"event_type":"interaction.complete","interaction":{"id":"v1_x","status":"failed","outputs":[{"type":"text","text":"keep model text projects/keep"},{"type":"tool_result","error":{"message":"projects/[redacted]"}}]}}`
		if got := maskReply(inNested, "", nil); string(got) != wantNested {
			t.Errorf("got  %s\nwant %s", got, wantNested)
		}
	})
	t.Run("two object levels down or inside array stays byte-identical", func(t *testing.T) {
		for _, raw := range []string{
			`{"a":{"b":{"error":{"message":"projects/123"}}}}`,
			`[{"error":{"message":"projects/123"}}]`,
			`{"outputs":[{"type":"x","detail":{"error":{"message":"project 987654 is off"}}}]}`,
			`{"error":null}`,
			`{"error":["projects/123"]}`,
		} {
			b := []byte(raw)
			if got := maskReply(b, "", nil); string(got) != raw || &got[0] != &b[0] {
				t.Errorf("%s changed to %s", raw, got)
			}
		}
	})
	t.Run("event named error is still masked whole", func(t *testing.T) {
		in := []byte(`{"message":"projects/123","extra":"projects/456"}`)
		want := `{"message":"projects/[redacted]","extra":"projects/[redacted]"}`
		if got := maskReply(in, "error", nil); string(got) != want {
			t.Errorf("got  %s\nwant %s", got, want)
		}
	})
}

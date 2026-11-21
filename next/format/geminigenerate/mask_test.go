package geminigenerate

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

func TestMarkerIsProtocolRedacted(t *testing.T) {
	if Redacted != protocol.Redacted {
		t.Fatalf("Redacted = %q, want protocol.Redacted %q", Redacted, protocol.Redacted)
	}
}

// Google's documented CONSUMER_SUSPENDED reply names the project in the message and in the
// details' metadata; both go, and everything else stays.
func TestMaskConsumerSuspended(t *testing.T) {
	raw := `{"error":{"code":403,"message":"The consumer 'projects/123' is suspended.","status":"PERMISSION_DENIED",` +
		`"details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"CONSUMER_SUSPENDED","domain":"googleapis.com",` +
		`"metadata":{"consumer":"projects/123","service":"generativelanguage.googleapis.com"}}]}}`
	e := &llm.Error{Code: llm.CodeAccountClosed, Status: 403, ProviderCode: "PERMISSION_DENIED",
		Message: "The consumer 'projects/123' is suspended.", Raw: json.RawMessage(raw)}
	got := MaskProject(e)
	if strings.Contains(got.Message, "123") || strings.Contains(string(got.Raw), "123") {
		t.Fatalf("project still named: %q %s", got.Message, got.Raw)
	}
	if got.Message != "The consumer 'projects/[redacted]' is suspended." {
		t.Errorf("message = %q", got.Message)
	}
	for _, keep := range []string{`"@type":"type.googleapis.com/google.rpc.ErrorInfo"`, `"reason":"CONSUMER_SUSPENDED"`,
		`"domain":"googleapis.com"`, `"service":"generativelanguage.googleapis.com"`, `"code":403`} {
		if !strings.Contains(string(got.Raw), keep) {
			t.Errorf("raw lost %s: %s", keep, got.Raw)
		}
	}
	if got.Code != e.Code || got.Status != e.Status || got.ProviderCode != e.ProviderCode {
		t.Errorf("classification changed: %+v", got)
	}
	if e.Message != "The consumer 'projects/123' is suspended." || string(e.Raw) != raw {
		t.Errorf("the caller's error was changed: %q %s", e.Message, e.Raw)
	}
}

// SERVICE_DISABLED: a bare project number in containerInfo, the activation URL, a console URL in
// Help, and the message.
func TestMaskServiceDisabled(t *testing.T) {
	raw := `{"error":{"code":403,"message":"Generative Language API has not been used in project 987654321 before or it is disabled. Enable it by visiting https://console.developers.google.com/apis/api/generativelanguage.googleapis.com/overview?project=987654321 then retry.","status":"PERMISSION_DENIED",` +
		`"details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"SERVICE_DISABLED","domain":"googleapis.com",` +
		`"metadata":{"containerInfo":"987654321","service":"generativelanguage.googleapis.com","consumer":"projects/987654321","activationUrl":"https://console.developers.google.com/apis/api/generativelanguage.googleapis.com/overview?project=987654321"}},` +
		`{"@type":"type.googleapis.com/google.rpc.LocalizedMessage","locale":"en-US","message":"Generative Language API has not been used in project 987654321 before or it is disabled."},` +
		`{"@type":"type.googleapis.com/google.rpc.Help","links":[{"description":"Google developers console API activation","url":"https://console.developers.google.com/apis/api/generativelanguage.googleapis.com/overview?project=987654321"}]}]}}`
	e := &llm.Error{Code: llm.CodePermission, Message: "Generative Language API has not been used in project 987654321 before or it is disabled.", Raw: json.RawMessage(raw)}
	got := MaskProject(e)
	if strings.Contains(got.Message, "987654321") || strings.Contains(string(got.Raw), "987654321") {
		t.Fatalf("project still named: %q %s", got.Message, got.Raw)
	}
	for _, keep := range []string{"SERVICE_DISABLED", "generativelanguage.googleapis.com", "https://console.developers.google.com/apis/api/generativelanguage.googleapis.com/overview?project=[redacted]", `"locale":"en-US"`} {
		if !strings.Contains(string(got.Raw), keep) {
			t.Errorf("raw lost %s: %s", keep, got.Raw)
		}
	}
}

// A 429 keeps RetryInfo and the quota facts; only the subject's project goes.
func TestMaskQuotaFailureKeepsTheQuota(t *testing.T) {
	raw := `{"error":{"code":429,"message":"You exceeded your current quota.","status":"RESOURCE_EXHAUSTED","details":[` +
		`{"@type":"type.googleapis.com/google.rpc.QuotaFailure","violations":[{"quotaMetric":"generativelanguage.googleapis.com/generate_content_free_tier_requests",` +
		`"quotaId":"GenerateRequestsPerDayPerProjectPerModel-FreeTier","quotaDimensions":{"location":"global","model":"gemini-2.0-flash"},"quotaValue":"50","subject":"project:my-proj-7"}]},` +
		`{"@type":"type.googleapis.com/google.rpc.Help","links":[{"description":"Learn more","url":"https://ai.google.dev/gemini-api/docs/rate-limits"}]},` +
		`{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"34s"}]}}`
	got := MaskProject(&llm.Error{Code: llm.CodeRateLimit, Message: "You exceeded your current quota.", Raw: json.RawMessage(raw)})
	want := strings.Replace(raw, `"subject":"project:my-proj-7"`, `"subject":"project:[redacted]"`, 1)
	if string(got.Raw) != want {
		t.Errorf("raw = %s\nwant  %s", got.Raw, want)
	}
}

func TestMaskResourceInfoAndAccounts(t *testing.T) {
	raw := `{"error":{"code":403,"message":"denied for user:alice@example.com and serviceAccount:svc@my-proj-7.iam.gserviceaccount.com","status":"PERMISSION_DENIED","details":[` +
		`{"@type":"type.googleapis.com/google.rpc.ResourceInfo","resource_type":"project","resource_name":"my-proj-7","owner":"user:alice@example.com"},` +
		`{"@type":"type.googleapis.com/google.rpc.ResourceInfo","resource_type":"model","resource_name":"projects/55/locations/global/publishers/google/models/gemini-x","owner":"project:55"}]}}`
	got := MaskProject(&llm.Error{Code: llm.CodePermission, Message: "denied for user:alice@example.com", Raw: json.RawMessage(raw)})
	for _, gone := range []string{"alice@example.com", "my-proj-7", "projects/55", "project:55", "svc@"} {
		if strings.Contains(string(got.Raw), gone) || strings.Contains(got.Message, gone) {
			t.Errorf("%q survives: %q %s", gone, got.Message, got.Raw)
		}
	}
	if !strings.Contains(string(got.Raw), "/locations/global/publishers/google/models/gemini-x") {
		t.Errorf("the rest of the resource path was cut: %s", got.Raw)
	}
	if !strings.Contains(string(got.Raw), `"resource_type":"model"`) {
		t.Errorf("resource_type was changed: %s", got.Raw)
	}
}

// The identifiers the caller knows go as whole tokens: ID 12 leaves 123 and a model name alone.
func TestMaskKnownIdentifiersWholeTokens(t *testing.T) {
	msg := "model gemini-2.5-flash in my-proj: 12 and 123 and 12.5 and version 2.5 and xmy-proj and my-proj-2 and (my-proj)."
	got := MaskProject(&llm.Error{Code: llm.CodeUpstream, Message: msg}, "12", "my-proj", "2")
	want := "model gemini-2.5-flash in [redacted]: [redacted] and 123 and 12.5 and version 2.5 and xmy-proj and my-proj-2 and ([redacted])."
	if got.Message != want {
		t.Errorf("message = %q\nwant      %q", got.Message, want)
	}
}

// A known email is replaced whole, and the project in a service account's address goes too.
func TestMaskKnownEmail(t *testing.T) {
	got := MaskProject(&llm.Error{Code: llm.CodeUpstream, Message: "bad token for svc@owner-proj.iam.gserviceaccount.com in owner-proj"}, "svc@owner-proj.iam.gserviceaccount.com")
	if got.Message != "bad token for [redacted] in [redacted]" {
		t.Errorf("message = %q", got.Message)
	}
}

// Numbers keep their exact digits and the keys and layout stay as sent; a body with nothing to
// mask is the same bytes.
func TestMaskKeepsNumbersAndBytes(t *testing.T) {
	raw := `{ "error" : {"code":429,"message":"slow down","status":"RESOURCE_EXHAUSTED","details":[{"@type":"x","big":12345678901234567890,"f":1.50e3,"quotaValue":"123"}]} }`
	e := &llm.Error{Code: llm.CodeRateLimit, Message: "Your project has been denied access. GenerateRequestsPerDayPerProjectPerModel", Raw: json.RawMessage(raw)}
	if got := MaskProject(e); got != e || string(got.Raw) != raw {
		t.Errorf("an error with nothing to mask changed: %+v", got)
	}
	got := MaskProject(&llm.Error{Raw: json.RawMessage(strings.Replace(raw, `"slow down"`, `"project 77 is slow & <b>"`, 1))}, "77")
	if !strings.Contains(string(got.Raw), `12345678901234567890`) || !strings.Contains(string(got.Raw), `1.50e3`) ||
		!strings.Contains(string(got.Raw), `"message":"project [redacted] is slow & <b>"`) || !strings.Contains(string(got.Raw), `"quotaValue":"123"`) {
		t.Errorf("raw = %s", got.Raw)
	}
	if !json.Valid(got.Raw) {
		t.Errorf("raw is not JSON: %s", got.Raw)
	}
}

// Google's OpenAI-compatible API sends its error in a one-element array.
func TestMaskArrayForm(t *testing.T) {
	raw := `[{"error":{"code":403,"message":"The consumer 'projects/123' is suspended.","status":"PERMISSION_DENIED","details":[{"metadata":{"consumer":"projects/123"}}]}}]`
	got := MaskProject(&llm.Error{Code: llm.CodeAccountClosed, Message: "m", Raw: json.RawMessage(raw)})
	if strings.Contains(string(got.Raw), "123") || !json.Valid(got.Raw) || !strings.HasPrefix(string(got.Raw), "[{") {
		t.Errorf("raw = %s", got.Raw)
	}
}

// Text patterns: each form of naming a project, and the words around them, which stay.
func TestMaskPatterns(t *testing.T) {
	for in, want := range map[string]string{
		"consumer 'projects/123' x":              "consumer 'projects/[redacted]' x",
		"in projects/my-proj/locations/global/x": "in projects/[redacted]/locations/global/x",
		"subject project:abc-123.":               "subject project:[redacted].",
		"see ?project=my-proj&x=1":               "see ?project=[redacted]&x=1",
		"not used in project 4242 before":        "not used in project [redacted] before",
		"project '4242' is gone":                 "project '[redacted]' is gone",
		`project "my-proj" is gone`:              `project "[redacted]" is gone`,
		"owner user:a.b@example.com, sa serviceAccount:x@p.iam.gserviceaccount.com.": "owner user:[redacted], sa serviceAccount:[redacted].",
		"Your project has been denied access.":                                       "Your project has been denied access.",
		"project 'projects/9' is gone":                                               "project '[redacted]' is gone",
		"Post \"https://aiplatform.googleapis.com/v1/projects/p-1/locations/global/publishers/google/models/g:generateContent\": EOF": "Post \"https://aiplatform.googleapis.com/v1/projects/[redacted]/locations/global/publishers/google/models/g:generateContent\": EOF",
		"projects/[redacted] stays": "projects/[redacted] stays",
	} {
		if got := MaskProject(&llm.Error{Message: in}).Message; got != want {
			t.Errorf("%q\n got  %q\n want %q", in, got, want)
		}
	}
}

// A pattern found in one place masks the same bare identifier everywhere else in the error.
func TestMaskLearnsFromPatterns(t *testing.T) {
	got := MaskProject(&llm.Error{Message: "quota of project 5550001 used", Raw: json.RawMessage(`{"error":{"message":"owner 5550001 is over","status":"RESOURCE_EXHAUSTED"}}`)})
	if strings.Contains(string(got.Raw), "5550001") || strings.Contains(got.Message, "5550001") {
		t.Errorf("%q %s", got.Message, got.Raw)
	}
}

func TestMaskBody(t *testing.T) {
	in := []byte(`{"event_type":"error","error":{"code":403,"message":"The consumer 'projects/123' is suspended."}}`)
	got := MaskBody(in)
	if strings.Contains(string(got), "123") || !json.Valid(got) {
		t.Errorf("got %s", got)
	}
	same := []byte(`{"error":{"message":"fine"}}`)
	if out := MaskBody(same); &out[0] != &same[0] {
		t.Errorf("an unchanged body was copied")
	}
	if out := MaskBody([]byte("not json")); string(out) != "not json" {
		t.Errorf("got %q", out)
	}
}

func TestIsGoogleErrorShape(t *testing.T) {
	for raw, want := range map[string]bool{
		`{"error":{"code":403,"message":"m","status":"PERMISSION_DENIED"}}`:    true,
		`[{"error":{"code":429,"message":"m","status":"RESOURCE_EXHAUSTED"}}]`: true,
		`{"error":{"code":"403","message":"m","status":"X"}}`:                  false,
		`{"error":{"code":403,"message":"m"}}`:                                 false,
		`{"error":{"message":"m","type":"invalid_request_error","code":"x"}}`:  false,
		`not json`: false,
	} {
		if got := IsGoogleErrorShape([]byte(raw)); got != want {
			t.Errorf("%s: got %v, want %v", raw, got, want)
		}
	}
}

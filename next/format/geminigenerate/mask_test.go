package geminigenerate

import (
	"encoding/json"
	"fmt"
	"math/rand"
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
	want := strings.Replace(raw, `"subject":"project:my-proj-7"`, `"subject":"[redacted]"`, 1)
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
		`{"code":403,"message":"m","status":"PERMISSION_DENIED"}`:              true,
		`not json`: false,
	} {
		if got := IsGoogleErrorShape([]byte(raw)); got != want {
			t.Errorf("%s: got %v, want %v", raw, got, want)
		}
	}
}

// ErrorInfo's own example puts the project in metadata.resource.
func TestMaskErrorInfoResource(t *testing.T) {
	raw := `{"error":{"code":403,"message":"m","status":"PERMISSION_DENIED","details":[{"reason":"API_DISABLED","domain":"googleapis.com","metadata":{"resource":"projects/123","service":"pubsub.googleapis.com"}}]}}`
	got := MaskProject(&llm.Error{Message: "m", Raw: json.RawMessage(raw)})
	want := strings.Replace(raw, "projects/123", "projects/[redacted]", 1)
	if string(got.Raw) != want {
		t.Errorf("raw = %s\nwant  %s", got.Raw, want)
	}
}

// More ways Google's text names a project, an organization, a folder, a billing account or a
// person; the words around them stay.
func TestMaskMorePatterns(t *testing.T) {
	for in, want := range map[string]string{
		"Permission denied on resource project my-proj-1.":              "Permission denied on resource project [redacted].",
		"Permission denied on resource project 4242 now":                "Permission denied on resource project [redacted] now",
		"resource project denied the request":                           "resource project denied the request",
		"the resource project has been denied access":                   "the resource project has been denied access",
		"Your project has been denied access.":                          "Your project has been denied access.",
		"ids project_number:4242 and project_id:my-proj-3 ok":           "ids project_number:[redacted] and project_id:[redacted] ok",
		"in organizations/555000/x and folders/666000":                  "in organizations/[redacted]/x and folders/[redacted]",
		"billingAccounts/0123-ABCD-4567 is closed":                      "billingAccounts/[redacted] is closed",
		"organization:555000 folder:666000 billingAccount:0123-ABCD":    "organization:[redacted] folder:[redacted] billingAccount:[redacted]",
		"see projects/acme%2Dprod%2D4821/locations":                     "see projects/[redacted]/locations",
		"in project:acme%2Dprod%2D4821 here":                            "in project:[redacted] here",
		"see ?project=acme%2Dprod%2D4821&x=1":                           "see ?project=[redacted]&x=1",
		"see ?project_id=acme%2Dprod%2D4821&x=1":                        "see ?project_id=[redacted]&x=1",
		"owner group:team@example.com is gone":                          "owner group:[redacted] is gone",
		"Caller foo@my-proj-2.iam.gserviceaccount.com lacks permission": "Caller [redacted] lacks permission",
		"default 123-compute@developer.gserviceaccount.com":             "default [redacted]",
	} {
		if got := MaskProject(&llm.Error{Message: in}).Message; got != want {
			t.Errorf("%q\n got  %q\n want %q", in, got, want)
		}
	}
}

// The project of any service account address is learned and masked wherever else it stands.
func TestMaskLearnsProjectOfAnyServiceAccount(t *testing.T) {
	got := MaskProject(&llm.Error{Message: "Caller foo@my-proj-2.iam.gserviceaccount.com in my-proj-2 lacks permission"})
	if got.Message != "Caller [redacted] in [redacted] lacks permission" {
		t.Errorf("message = %q", got.Message)
	}
}

// A QuotaFailure's subject and a ResourceInfo's owner go whole, whatever they say; the other
// details' subjects, and the quota facts, stay.
func TestMaskWholeSubjectAndOwner(t *testing.T) {
	raw := `{"error":{"code":429,"message":"m","status":"RESOURCE_EXHAUSTED","details":[` +
		`{"@type":"type.googleapis.com/google.rpc.QuotaFailure","violations":[` +
		`{"subject":"clientip:203.0.113.7","quotaMetric":"m/x","quotaId":"Id","quotaValue":"9","quotaDimensions":{"region":"us"}},` +
		`{"subject":"organization:555000"},{"subject":"folder:666000"},{"subject":"something else entirely"}]},` +
		`{"@type":"type.googleapis.com/google.rpc.ResourceInfo","resource_type":"x","resource_name":"organizations/555000/x","owner":"group:team@example.com"},` +
		`{"@type":"type.googleapis.com/google.rpc.ResourceInfo","owner":"billingAccounts/0123-ABCD"},` +
		`{"@type":"type.googleapis.com/google.rpc.ResourceInfo","owner":"plain words"},` +
		`{"@type":"type.googleapis.com/google.rpc.PreconditionFailure","violations":[{"type":"TOS","subject":"google.com/cloud","description":"Terms of service not accepted"}]}]}}`
	got := string(MaskProject(&llm.Error{Message: "m", Raw: json.RawMessage(raw)}).Raw)
	for _, gone := range []string{"203.0.113.7", "555000", "666000", "something else entirely", "team@example.com", "0123-ABCD", "plain words"} {
		if strings.Contains(got, gone) {
			t.Errorf("%q survives: %s", gone, got)
		}
	}
	for _, keep := range []string{`"subject":"google.com/cloud"`, `"quotaMetric":"m/x"`, `"quotaId":"Id"`, `"quotaValue":"9"`, `"quotaDimensions":{"region":"us"}`, `"resource_name":"organizations/[redacted]/x"`, `"type":"TOS"`} {
		if !strings.Contains(got, keep) {
			t.Errorf("lost %s: %s", keep, got)
		}
	}
	if n := strings.Count(got, `"subject":"[redacted]"`); n != 4 {
		t.Errorf("%d subjects masked, want 4: %s", n, got)
	}
}

// Empty subject or owner values mask nothing else (nor turn themselves into [redacted]).
func TestMaskEmptySubjectAndOwner(t *testing.T) {
	for name, raw := range map[string]string{
		"empty subject": `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"m","details":[{"@type":"type.googleapis.com/google.rpc.QuotaFailure","violations":[{"subject":"","quotaId":"Q"}]},{"@type":"type.googleapis.com/google.rpc.LocalizedMessage","locale":"","message":""}]}}`,
		"empty owner":   `{"error":{"code":403,"status":"PERMISSION_DENIED","message":"m","details":[{"@type":"type.googleapis.com/google.rpc.ResourceInfo","owner":"","description":""}]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			e := &llm.Error{Code: llm.CodeRateLimit, Message: "", Raw: json.RawMessage(raw)}
			if got := MaskProject(e); got != e || string(got.Raw) != raw {
				t.Errorf("empty value masked something:\n got  %q %s\n want %q %s", got.Message, got.Raw, e.Message, raw)
			}
		})
	}
}

// A whole value (QuotaFailure.violations[].subject or ResourceInfo.owner) is masked only in its
// own field, not where another field or detail uses the same plain word.
func TestMaskWholeOnlyInOwnField(t *testing.T) {
	raw := `{"error":{"code":429,"message":"global quota exceeded","status":"RESOURCE_EXHAUSTED","details":[` +
		`{"@type":"type.googleapis.com/google.rpc.QuotaFailure","violations":[{"subject":"global","quotaDimensions":{"location":"global"}}]},` +
		`{"@type":"type.googleapis.com/google.rpc.ResourceInfo","resource_type":"global","owner":"global"},` +
		`{"@type":"type.googleapis.com/google.rpc.PreconditionFailure","violations":[{"type":"TOS","subject":"global"}]}]}}`
	e := &llm.Error{Code: llm.CodeRateLimit, Message: "global quota exceeded", Raw: json.RawMessage(raw)}
	got := MaskProject(e)
	if got.Message != "global quota exceeded" {
		t.Errorf("message changed: %q", got.Message)
	}
	want := `{"error":{"code":429,"message":"global quota exceeded","status":"RESOURCE_EXHAUSTED","details":[` +
		`{"@type":"type.googleapis.com/google.rpc.QuotaFailure","violations":[{"subject":"[redacted]","quotaDimensions":{"location":"global"}}]},` +
		`{"@type":"type.googleapis.com/google.rpc.ResourceInfo","resource_type":"global","owner":"[redacted]"},` +
		`{"@type":"type.googleapis.com/google.rpc.PreconditionFailure","violations":[{"type":"TOS","subject":"global"}]}]}}`
	if string(got.Raw) != want {
		t.Errorf("raw =\n%s\nwant\n%s", got.Raw, want)
	}
}

// A subject or owner that looks like an identifier (contains :, / or @) is also masked where the
// message or another string repeats it, as a whole token; a plain word is not.
func TestMaskSubjectAndOwnerIdentifierInMessage(t *testing.T) {
	raw := `{"error":{"code":429,"message":"Quota exceeded for clientip:203.0.113.7 (not clientip:203.0.113.70), owner acme/team-7 and bob@corp.example in global.","status":"RESOURCE_EXHAUSTED","details":[` +
		`{"@type":"type.googleapis.com/google.rpc.QuotaFailure","violations":[{"subject":"clientip:203.0.113.7"},{"subject":"global"}]},` +
		`{"@type":"type.googleapis.com/google.rpc.ResourceInfo","owner":"acme/team-7"},` +
		`{"@type":"type.googleapis.com/google.rpc.ResourceInfo","owner":"bob@corp.example"},` +
		`{"@type":"type.googleapis.com/google.rpc.LocalizedMessage","locale":"en-US","message":"Blocked clientip:203.0.113.7 and acme/team-70 and xbob@corp.example."}]}}`
	msg := "Quota exceeded for clientip:203.0.113.7 (not clientip:203.0.113.70), owner acme/team-7 and bob@corp.example in global."
	got := MaskProject(&llm.Error{Code: llm.CodeRateLimit, Message: msg, Raw: json.RawMessage(raw)})
	wantMsg := "Quota exceeded for [redacted] (not clientip:203.0.113.70), owner [redacted] and [redacted] in global."
	if got.Message != wantMsg {
		t.Errorf("message =\n%q\nwant\n%q", got.Message, wantMsg)
	}
	for _, gone := range []string{"203.0.113.7)", "clientip:203.0.113.7 ", "owner acme/team-7 ", "bob@corp.example in"} {
		if strings.Contains(string(got.Raw), gone) {
			t.Errorf("identifier survived in raw (%q): %s", gone, got.Raw)
		}
	}
	for _, keep := range []string{"clientip:203.0.113.70", "acme/team-70", "xbob@corp.example", "in global."} {
		if !strings.Contains(string(got.Raw), keep) {
			t.Errorf("non-matching token %q was altered in raw: %s", keep, got.Raw)
		}
	}
	if !strings.Contains(string(got.Raw), `"Blocked [redacted] and acme/team-70 and xbob@corp.example."`) {
		t.Errorf("LocalizedMessage not masked as expected: %s", got.Raw)
	}
}

// "resource project <id>" with an all-letter ID (6..30 chars) followed by end of text or
// punctuation is masked, while ordinary prose stays and digit/hyphen forms still work.
func TestMaskResourceProjectAllLetterID(t *testing.T) {
	max30 := strings.Repeat("a", 30)
	over31 := strings.Repeat("a", 31)
	for in, want := range map[string]string{
		"Permission denied on resource project myproject.":                           "Permission denied on resource project [redacted].",
		"Permission denied on resource project myproject\n":                          "Permission denied on resource project [redacted]\n",
		"Permission denied on resource project myproject\r\n":                        "Permission denied on resource project [redacted]\r\n",
		"Permission denied on resource project myproject ":                           "Permission denied on resource project [redacted] ",
		"Permission denied on resource project myproject (or it may not exist).":     "Permission denied on resource project [redacted] (or it may not exist).",
		"Permission denied on resource project myproject":                            "Permission denied on resource project [redacted]",
		"Permission denied on resource project myproject, check billing":             "Permission denied on resource project [redacted], check billing",
		"Permission denied on resource project myproject; retry":                     "Permission denied on resource project [redacted]; retry",
		"Permission denied on resource project myproject: check":                     "Permission denied on resource project [redacted]: check",
		"Permission denied on (resource project myproject)":                          "Permission denied on (resource project [redacted])",
		"Permission denied on 'resource project myproject'":                          "Permission denied on 'resource project [redacted]'",
		`Permission denied on "resource project myproject"`:                          `Permission denied on "resource project [redacted]"`,
		"Permission denied on `resource project myproject`":                          "Permission denied on `resource project [redacted]`",
		"Permission denied on resource project myproject. Project myproject is off.": "Permission denied on resource project [redacted]. Project [redacted] is off.",
		"Permission denied on resource project abcdef.":                              "Permission denied on resource project [redacted].",
		"Permission denied on resource project abcde.":                               "Permission denied on resource project abcde.",
		"Permission denied on resource project " + max30 + ".":                       "Permission denied on resource project [redacted].",
		"Permission denied on resource project " + over31 + ".":                      "Permission denied on resource project " + over31 + ".",
		"resource project has been denied":                                           "resource project has been denied",
		"resource project denied the request":                                        "resource project denied the request",
		"Permission denied on resource project my-proj-1 now":                        "Permission denied on resource project [redacted] now",
		"Permission denied on resource project 4242 now":                             "Permission denied on resource project [redacted] now",
	} {
		if got := MaskProject(&llm.Error{Message: in}).Message; got != want {
			t.Errorf("%q\n got  %q\n want %q", in, got, want)
		}
	}
}

// A subject or owner that is a bare project ID (looksLikeProjectID) is masked in its own field
// and added to the token list so repetitions in the message are masked; global stays elsewhere.
func TestMaskBareProjectIDInOwnerRepeatedInMessage(t *testing.T) {
	raw := `{"error":{"code":403,"message":"The consumer acme-prod-4821 is suspended.","details":[` +
		`{"@type":"type.googleapis.com/google.rpc.ResourceInfo","owner":"acme-prod-4821"},` +
		`{"@type":"type.googleapis.com/google.rpc.QuotaFailure","violations":[{"subject":"global"}]}` +
		`]}}`
	e := &llm.Error{Code: llm.CodePermission, Message: "The consumer acme-prod-4821 is suspended in global.", Raw: json.RawMessage(raw)}
	got := MaskProject(e)
	wantMsg := "The consumer [redacted] is suspended in global."
	if got.Message != wantMsg {
		t.Errorf("got message %q, want %q", got.Message, wantMsg)
	}
	wantRaw := `{"error":{"code":403,"message":"The consumer [redacted] is suspended.","details":[` +
		`{"@type":"type.googleapis.com/google.rpc.ResourceInfo","owner":"[redacted]"},` +
		`{"@type":"type.googleapis.com/google.rpc.QuotaFailure","violations":[{"subject":"[redacted]"}]}` +
		`]}}`
	if string(got.Raw) != wantRaw {
		t.Errorf("got raw:\n%s\nwant:\n%s", got.Raw, wantRaw)
	}
}

// Structured-field scanning stops at maxFieldDepth; the pattern pass still looks at every string
// at any depth, so a name in a pattern form is masked there.
func TestMaskDepthCap(t *testing.T) {
	wrap := func(depth int, inner string) string {
		return strings.Repeat(`{"a":`, depth) + inner + strings.Repeat(`}`, depth)
	}
	inner := `{"@type":"type.googleapis.com/google.rpc.ResourceInfo","owner":"plain words","resource_name":"projects/deep-proj-42/x"}`

	shallow := wrap(4, inner)
	gotShallow := string(MaskBody([]byte(shallow)))
	if strings.Contains(gotShallow, "plain words") || strings.Contains(gotShallow, "deep-proj-42") {
		t.Errorf("shallow error not masked: %s", gotShallow)
	}

	deep := wrap(100, inner)
	gotDeep := string(MaskBody([]byte(deep)))
	if strings.Contains(gotDeep, "deep-proj-42") {
		t.Errorf("pattern pass missed deep project name: %s", gotDeep)
	}
	if !strings.Contains(gotDeep, `"owner":"plain words"`) {
		t.Errorf("structured-field scan descended past depth cap: %s", gotDeep)
	}
}

func makeDeepError(depth, size int) []byte {
	var b strings.Builder
	b.Grow(size + depth*6 + 128)
	for i := 0; i < depth; i++ {
		b.WriteString(`{"a":`)
	}
	b.WriteString(`{"message":"projects/deep-proj-42","pad":"`)
	pad := size - b.Len() - depth - 4
	if pad < 0 {
		pad = 0
	}
	b.WriteString(strings.Repeat("x", pad))
	b.WriteString(`"}`)
	for i := 0; i < depth; i++ {
		b.WriteByte('}')
	}
	return []byte(b.String())
}

func BenchmarkMaskDeepNesting(b *testing.B) {
	for _, depth := range []int{100, 1000, 4000} {
		raw := makeDeepError(depth, 1<<20)
		b.Run(depthLabel(depth), func(b *testing.B) {
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = MaskBody(raw)
			}
		})
	}
}

func depthLabel(d int) string {
	switch d {
	case 100:
		return "depth100"
	case 1000:
		return "depth1000"
	case 4000:
		return "depth4000"
	default:
		return "depth"
	}
}

// A project scoped to a domain is one name: the domain and the colon go with it.
func TestMaskDomainScopedProject(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"see projects/example.com:acme-prod-4821/locations", "see projects/[redacted]/locations"},
		{"see ?project=example.com:acme-prod-4821&x=1", "see ?project=[redacted]&x=1"},
		{"the project:example.com:acme-prod-4821 is off", "the project:[redacted] is off"},
		{"folders/example.com:acme-prod-4821.", "folders/[redacted]."},
		// Both halves go where the body names them again.
		{"projects/example.com:acme-prod-4821 and acme-prod-4821 and example.com", "projects/[redacted] and [redacted] and [redacted]"},
		// Without a dot before the colon the colon ends the name, as before.
		{"POST projects/acme-prod-4821:setIamPolicy", "POST projects/[redacted]:setIamPolicy"},
		{"projects/12345: denied", "projects/[redacted]: denied"},
	} {
		if got := string(MaskBody([]byte(tc.in))); got != tc.want {
			t.Errorf("MaskBody(%q)\n got  %q\n want %q", tc.in, got, tc.want)
		}
	}
	got := string(MaskBody([]byte(`{"error":{"message":"projects/example.com:acme-prod-4821 is off","details":[{"metadata":{"consumer":"projects/example.com:acme-prod-4821"}}]}}`)))
	if strings.Contains(got, "acme-prod-4821") || strings.Contains(got, "example.com") {
		t.Errorf("domain-scoped project named in %s", got)
	}
}

// project= takes a value with a dot at its start or two dots in a row, as it did before names
// were read as dotted words; a dot that ends a sentence stays outside the name.
func TestMaskProjectQueryOddDots(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"see ?project=.acme-prod-4821&x=1", "see ?project=[redacted]&x=1"},
		{"see ?project=acme..prod-4821&x=1", "see ?project=[redacted]&x=1"},
		{"see ?project=acme-prod-4821.", "see ?project=[redacted]."},
		{"see ?projectId=a.b-c%2Fd&x=1", "see ?projectId=[redacted]&x=1"},
	} {
		if got := string(MaskBody([]byte(tc.in))); got != tc.want {
			t.Errorf("MaskBody(%q)\n got  %q\n want %q", tc.in, got, tc.want)
		}
	}
}

// countWork returns the work f makes the masking code do: the bytes its JSON scanners pass over
// and the steps of its name matcher. It stands in for a clock, which a slow or busy machine
// makes useless as a bound.
func countWork(f func()) int {
	var n int
	onScan = func(k int) { n += k }
	defer func() { onScan = nil }()
	f()
	return n
}

// What masking a body costs is a few passes over the body, whatever the depth of its nesting.
// Scanning each level's whole subtree again would multiply the work by the depth.
func TestMaskWorkDoesNotGrowWithDepth(t *testing.T) {
	const bound = 8 // passes over the body; masking measures about 5
	for _, depth := range []int{100, 4000} {
		raw := makeDeepError(depth, 1<<20)
		if w := countWork(func() { MaskBody(raw) }); w > bound*len(raw) {
			t.Errorf("MaskBody at depth %d: work %d is %.1f passes over %d bytes, want at most %d", depth, w, float64(w)/float64(len(raw)), len(raw), bound)
		}
		in := []byte(`{"status":"failed","nested":` + strings.Repeat(`[{"a":`, depth) +
			`{"error":{"message":"projects/deep-99"}}` + strings.Repeat(`}]`, depth) + `}`)
		if w := countWork(func() { MaskReply(in, "") }); w > bound*len(in) {
			t.Errorf("MaskReply at depth %d: work %d is %.1f passes over %d bytes, want at most %d", depth, w, float64(w)/float64(len(in)), len(in), bound)
		}
	}
}

// What masking a body costs does not grow with the number of names it holds: each string is
// matched against all names in one pass, so a body of N names costs N times a name, not N times N.
func TestMaskWorkDoesNotGrowWithNames(t *testing.T) {
	const bound = 8
	for _, n := range []int{500, 8000} {
		var b strings.Builder
		b.WriteString(`{"error":{"code":403,"message":"x","details":[`)
		for i := 0; i < n; i++ {
			if i > 0 {
				b.WriteByte(',')
			}
			// Each name is learned from a structured field and again named in plain text.
			fmt.Fprintf(&b, `{"consumer":"projects/p-%06d"},"p-%06d is off","projects/q-%06d"`, i, i, i)
		}
		b.WriteString(`]}}`)
		body := []byte(b.String())
		var out []byte
		w := countWork(func() { out = MaskBody(body) })
		if w > bound*len(body) {
			t.Errorf("%d names: work %d is %.1f passes over %d bytes, want at most %d", n, w, float64(w)/float64(len(body)), len(body), bound)
		}
		if strings.Contains(string(out), "p-000001") || strings.Contains(string(out), "q-000001") {
			t.Errorf("%d names: a name is left in the body", n)
		}
	}
}

// maskOneByOne is the plain way to replace names: each in turn, longest first, in the text the
// ones before it left. The matcher must give the same text.
func maskOneByOne(s string, list []string) string {
	for _, id := range list {
		var b strings.Builder
		i := 0
		for {
			k := strings.Index(s[i:], id)
			if k < 0 {
				break
			}
			k += i
			end := k + len(id)
			if standalone(s, nil, k, end) {
				b.WriteString(s[i:k])
				b.WriteString(Redacted)
				i = end
			} else {
				b.WriteString(s[i : k+1])
				i = k + 1
			}
		}
		b.WriteString(s[i:])
		s = b.String()
	}
	return s
}

func TestNameMatcherMatchesNameByName(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	const alphabet = "ab1-_. /:"
	word := func(max int) string {
		n := 1 + r.Intn(max)
		b := make([]byte, n)
		for i := range b {
			b[i] = alphabet[r.Intn(len(alphabet))]
		}
		return string(b)
	}
	for i := 0; i < 20000; i++ {
		d := newNames(nil)
		for n := 1 + r.Intn(6); n > 0; n-- {
			d.add(word(4))
		}
		if len(d.list) == 0 {
			continue
		}
		s := word(24)
		sortedList := append([]string(nil), d.list...)
		// the order maskString uses: longest first, then as learned
		for a := 1; a < len(sortedList); a++ {
			for b := a; b > 0 && len(sortedList[b]) > len(sortedList[b-1]); b-- {
				sortedList[b], sortedList[b-1] = sortedList[b-1], sortedList[b]
			}
		}
		want := maskOneByOne(s, sortedList)
		if got := d.maskString(s); got != want {
			t.Fatalf("names %q in %q\n got  %q\n want %q", sortedList, s, got, want)
		}
	}
}

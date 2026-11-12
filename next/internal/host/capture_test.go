package host

import (
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/protocol"
)

func captureRedactor(secret string) *protocol.Redactor {
	return protocol.NewRedactor(&protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: secret})
}

func redactedRequestBody(r *protocol.Redactor, b protocol.CapturedBody) protocol.CapturedBody {
	d := redactDiagnostic(r, protocol.Diagnostic{Upstream: []protocol.UpstreamExchange{
		{Request: protocol.UpstreamRequest{Method: "POST", URL: "https://upstream.example.test/v1", CapturedBody: b}}}})
	return d.Upstream[0].Request.CapturedBody
}

// A body the extension captured whole is still whole once a credential in it is replaced by the
// shorter "[redacted]": nothing in it was cut, so nothing may be said to be.
func TestRedactDiagnosticBodyThatFits(t *testing.T) {
	const secret = "sk-fake-credential-value-0123456789abcdef"
	body := "token=" + secret + " visible"
	got := redactedRequestBody(captureRedactor(secret), protocol.CapturedBody{Body: body, BodySize: int64(len(body))})
	if got.Body != "token=[redacted] visible" || got.BodyEnd != "" {
		t.Errorf("redacted body = %+v", got)
	}
	if got.BodySize != int64(len(got.Body)) {
		t.Errorf("BodySize = %d for a body of %d bytes that was not cut", got.BodySize, len(got.Body))
	}
}

// A body the extension cut keeps the count of what was cut between its pieces, whatever
// redaction does to the length of the pieces.
func TestRedactDiagnosticBodyThatWasCut(t *testing.T) {
	const secret = "sk-fake-credential-value-0123456789abcdef"
	head, end := "start "+secret+" middle", "tail "+secret+" end"
	const cutOut = 5000
	got := redactedRequestBody(captureRedactor(secret), protocol.CapturedBody{Body: head, BodyEnd: end,
		BodySize: int64(len(head) + len(end) + cutOut)})
	if got.Body != "start [redacted] middle" || got.BodyEnd != "tail [redacted] end" {
		t.Fatalf("redacted body = %+v", got)
	}
	if left := got.BodySize - int64(len(got.Body)+len(got.BodyEnd)); left != cutOut {
		t.Errorf("%d bytes left out between the pieces, want %d", left, cutOut)
	}
}

// A secret cut in two by the cut of a body is not left in part beside the cut, however it is
// punctuated: the redaction cannot recognise a part of it.
func TestRedactDiagnosticSecretAtTheCut(t *testing.T) {
	const secret = "alpha!bravo!charlie"
	const cutOut = 1000
	// The cut falls inside the secret: the head ends with its start, the tail begins with its end.
	head, end := `{"padding":"`+strings.Repeat("x", 50)+`","k":"alpha!bra`, `vo!charlie","z":1}`
	got := redactedRequestBody(captureRedactor(secret), protocol.CapturedBody{Body: head, BodyEnd: end,
		BodySize: int64(len(head) + len(end) + cutOut)})
	if strings.Contains(got.Body, "alpha") || strings.Contains(got.BodyEnd, "charlie") || strings.Contains(got.BodyEnd, "vo!") {
		t.Errorf("a part of the secret is left beside the cut: %q ... %q", got.Body, got.BodyEnd)
	}
	if !strings.HasPrefix(got.Body, `{"padding":"xxx`) || !strings.HasSuffix(got.BodyEnd, `","z":1}`) {
		t.Errorf("more than the secret's parts was removed: %q ... %q", got.Body, got.BodyEnd)
	}
	// What was removed counts as left out: 9 bytes of "alpha!bra" and 10 of "vo!charlie".
	if left := got.BodySize - int64(len(got.Body)+len(got.BodyEnd)); left != cutOut+9+10 {
		t.Errorf("%d bytes left out, want %d", left, cutOut+9+10)
	}
	// Text that merely ends like the start of the secret in fewer than 4 bytes is kept.
	got = redactedRequestBody(captureRedactor(secret), protocol.CapturedBody{Body: "ends with alp", BodyEnd: "ie starts", BodySize: 5000})
	if got.Body != "ends with alp" || got.BodyEnd != "ie starts" {
		t.Errorf("a short run was removed: %+v", got)
	}
}

func TestRedactDiagnosticKeepsHeadersLeftOut(t *testing.T) {
	d := redactDiagnostic(captureRedactor("sk-fake-credential-value-0123456789abcdef"), protocol.Diagnostic{Upstream: []protocol.UpstreamExchange{{
		Request:  protocol.UpstreamRequest{Method: "GET", URL: "https://upstream.example.test/", HeadersLeftOut: 3},
		Response: &protocol.UpstreamResponse{Status: 200, HeadersLeftOut: 7}}}})
	if x := d.Upstream[0]; x.Request.HeadersLeftOut != 3 || x.Response.HeadersLeftOut != 7 {
		t.Errorf("headers left out: request %d, response %d, want 3 and 7", x.Request.HeadersLeftOut, x.Response.HeadersLeftOut)
	}
}

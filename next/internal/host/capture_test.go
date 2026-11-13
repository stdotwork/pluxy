package host

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stdotwork/pluxy/next/internal/configdoc"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
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

// A body sent as a start and an end with nothing between them (body_size no more than the two
// together, or absent) is one text: the request log writes the two joined. A secret cut between
// them is removed as from the whole body.
func TestRedactDiagnosticSecretBetweenAdjacentPieces(t *testing.T) {
	const secret = "sk-fake-credential-value-0123456789abcdef"
	head, end := "start sk-fake-credential", "-value-0123456789abcdef end"
	for _, size := range []int64{int64(len(head) + len(end)), 0} {
		got := redactedRequestBody(captureRedactor(secret), protocol.CapturedBody{Body: head, BodyEnd: end, BodySize: size})
		if joined := got.Body + got.BodyEnd; joined != "start [redacted] end" {
			t.Errorf("body_size %d: joined body %q (%q + %q), want %q", size, joined, got.Body, got.BodyEnd, "start [redacted] end")
		}
		if got.BodySize != int64(len(got.Body)+len(got.BodyEnd)) {
			t.Errorf("body_size %d: BodySize %d for %d bytes with nothing left out", size, got.BodySize, len(got.Body)+len(got.BodyEnd))
		}
	}
}

// A body whose start and end lie side by side is redacted as one text also where the start makes
// the redaction give up and replace it whole: the rest of a secret cut between them is removed,
// also when it is the secret's base64 and the body ends with it.
func TestRedactDiagnosticAdjacentPiecesAfterCollapse(t *testing.T) {
	const secret = "sk-body-key-0123456789"
	key64 := base64.RawStdEncoding.EncodeToString([]byte(secret))
	collapsing := "model said " + strings.Repeat("\\", 16) + "%25252525 ; upstream: invalid key "
	for _, c := range []struct{ head, end, rest string }{
		{collapsing + "sk-", "body-key-0123456789 rejected", "body-key-0123456789"},
		{collapsing + key64[:1], key64[1:], key64[1:]},
	} {
		got := redactedRequestBody(captureRedactor(secret), protocol.CapturedBody{Body: c.head, BodyEnd: c.end})
		if joined := got.Body + got.BodyEnd; strings.Contains(joined, c.rest) || !strings.HasSuffix(joined, strings.TrimPrefix(c.end, c.rest)) {
			t.Errorf("end %q: joined body %q holds the rest of the secret or loses the text after it", c.end, joined)
		}
	}
}

// A start and an end with nothing left out between them, each as long as the request log keeps,
// still fit what it keeps once redacted, so it logs them whole rather than report a gap.
func TestRedactDiagnosticAdjacentPiecesFitTheLog(t *testing.T) {
	const secret = "sk-fake-credential-value-0123456789abcdef"
	head := strings.Repeat("a", protocol.CapturedHead-len("word sk-")) + "word sk-"
	end := "xyz tail " + strings.Repeat("b", protocol.CapturedEnd-len("xyz tail "))
	got := redactedRequestBody(captureRedactor(secret), protocol.CapturedBody{Body: head, BodyEnd: end, BodySize: int64(len(head) + len(end))})
	fits := got.BodyEnd == "" && len(got.Body) <= protocol.CapturedHead+protocol.CapturedEnd ||
		len(got.Body) <= protocol.CapturedHead && len(got.BodyEnd) <= protocol.CapturedEnd
	if !fits || got.Body+got.BodyEnd != head+end || got.BodySize != int64(len(head)+len(end)) {
		t.Errorf("body %d bytes, end %d bytes, size %d; want the same %d bytes within %d and %d, or one part within %d",
			len(got.Body), len(got.BodyEnd), got.BodySize, len(head)+len(end), protocol.CapturedHead, protocol.CapturedEnd, protocol.CapturedHead+protocol.CapturedEnd)
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

// captureSettings sends one request upstream, to its settings' base_url, with the secret in its
// settings' api_key in the URL's path, a header and the body, and fails the call.
func captureSettings() {
	err := sdk.Serve(sdk.Extension{
		ID:      "hosttest/capture-settings",
		Version: "0.0.0",
		Generate: func(ctx context.Context, call sdk.Call, out *sdk.Stream) error {
			var st struct {
				BaseURL string `json:"base_url"`
				APIKey  string `json:"api_key"`
			}
			_ = json.Unmarshal(call.Account.Settings, &st)
			req, err := http.NewRequestWithContext(ctx, "POST", st.BaseURL+"/p/"+st.APIKey, strings.NewReader(`{"key":"`+st.APIKey+`"}`))
			if err != nil {
				return err
			}
			req.Header.Set("X-Note", "note "+st.APIKey)
			resp, err := (&http.Client{Transport: sdk.Upstream(&http.Transport{})}).Do(req)
			if err != nil {
				return err
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			return &llm.Error{Code: llm.CodeUnavailable, Message: "done"}
		},
	})
	if err != nil {
		os.Exit(1)
	}
}

// The upstream exchanges a call captures have the secrets in the account's settings removed, as
// its replies have: in the URL, the headers and the bodies the extension sent and got back.
func TestCaptureHoldsNoSettingsSecret(t *testing.T) {
	const secret = "fake-settings-capture-0123456789"
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Echo", "echo "+secret)
		io.WriteString(w, `{"echo":"`+secret+`"}`)
	}))
	defer up.Close()
	ext, err := Start(filepath.Join(fakePackage(t, "capture-settings"), ManifestFile), Options{Log: io.Discard, SettingsSecrets: configdoc.SettingsSecrets})
	if err != nil {
		t.Fatal(err)
	}
	defer ext.Stop()
	var mu sync.Mutex
	var got []protocol.Diagnostic
	ctx := WithCapture(context.Background(), func(_ string, d protocol.Diagnostic) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, d)
	})
	settings, _ := json.Marshal(map[string]string{"base_url": up.URL, "api_key": secret})
	s := ext.Generate(ctx, protocol.Account{ID: "a", ProxyURL: "none", Settings: settings}, llm.Request{Model: "m"})
	for ev := s.Next(); ev.Type != llm.EventError && ev.Type != llm.EventFinish; ev = s.Next() {
	}
	s.Close()
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || len(got[0].Upstream) != 1 || got[0].Upstream[0].Response == nil {
		t.Fatalf("captured %+v, want one exchange with its response", got)
	}
	b, _ := json.Marshal(got)
	if strings.Contains(string(b), secret) || strings.Count(string(b), protocol.Redacted) < 5 {
		t.Errorf("the capture holds the settings secret, or not each place it was in redacted:\n%s", b)
	}
}

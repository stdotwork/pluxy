package host

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
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

func TestRedactDiagnosticSecretAtTheCut(t *testing.T) {
	const secret = "alpha!bravo!charlie"
	const cutOut = 1000
	head, end := `{"padding":"`+strings.Repeat("x", 50)+`","k":"alpha!bra`, `vo!charlie","z":1}`
	got := redactedRequestBody(captureRedactor(secret), protocol.CapturedBody{Body: head, BodyEnd: end,
		BodySize: int64(len(head) + len(end) + cutOut)})
	if strings.Contains(got.Body, "alpha") || strings.Contains(got.BodyEnd, "charlie") || strings.Contains(got.BodyEnd, "vo!") {
		t.Errorf("a part of the secret is left beside the cut: %q ... %q", got.Body, got.BodyEnd)
	}
	if !strings.HasPrefix(got.Body, `{"padding":"xxx`) || !strings.HasSuffix(got.BodyEnd, `","z":1}`) {
		t.Errorf("more than the secret's parts was removed: %q ... %q", got.Body, got.BodyEnd)
	}
	if left := got.BodySize - int64(len(got.Body)+len(got.BodyEnd)); left != cutOut+9+10 {
		t.Errorf("%d bytes left out, want %d", left, cutOut+9+10)
	}
	got = redactedRequestBody(captureRedactor(secret), protocol.CapturedBody{Body: "ends with alp", BodyEnd: "ie starts", BodySize: 5000})
	if got.Body != "ends with alp" || got.BodyEnd != "ie starts" {
		t.Errorf("a short run was removed: %+v", got)
	}
}

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

func TestRedactDiagnosticAdjacentPiecesAfterEscapeFallback(t *testing.T) {
	r := protocol.NewRedactor(&protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "my-api-keys"},
		&protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "3x-other-secret"})
	head := "err my-api-key" + fmt.Sprintf(`\u%04x`, 's') + "x-oth"
	got := redactedRequestBody(r, protocol.CapturedBody{Body: head, BodyEnd: "er-secret rejected"})
	if joined := got.Body + got.BodyEnd; joined != "err [redacted][redacted] rejected" {
		t.Errorf("joined body %q, want %q", joined, "err [redacted][redacted] rejected")
	}
}

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

func captureStop() {
	err := sdk.Serve(sdk.Extension{
		ID:      "hosttest/capture-stop",
		Version: "0.0.0",
		Generate: func(ctx context.Context, call sdk.Call, out *sdk.Stream) error {
			var st struct {
				BaseURL string `json:"base_url"`
				Mode    string `json:"mode"`
				Read    int    `json:"read"`
			}
			_ = json.Unmarshal(call.Account.Settings, &st)
			req, err := http.NewRequestWithContext(ctx, "GET", st.BaseURL+"/"+st.Mode, nil)
			if err != nil {
				return err
			}
			resp, err := (&http.Client{Transport: sdk.Upstream(&http.Transport{})}).Do(req)
			if err != nil {
				return err
			}
			switch st.Mode {
			case "cut":
				_, _ = io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			case "stop":
				_, _ = io.ReadFull(resp.Body, make([]byte, st.Read))
			default:
				_, _ = io.ReadAll(resp.Body)
			}
			resp.Body.Close()
			if st.Mode == "whole" {
				if err := out.Start(llm.ResponseInfo{ID: "r", Model: "m"}); err != nil {
					return err
				}
				return out.Finish(llm.Finish{Reason: llm.FinishStop}, nil)
			}
			return &llm.Error{Code: llm.CodeUnavailable, Message: "done"}
		},
	})
	if err != nil {
		os.Exit(1)
	}
}

func TestCaptureStoppedReadKeepsNoCredentialStart(t *testing.T) {
	const secret = "Zq7-stop-credential-0123456789abcdef"
	const mark = "MARK Bearer "
	pad := func(n int) string { return strings.Repeat("x", n) }
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cut":
			io.WriteString(w, pad(1<<20-10-len(mark))+mark+secret+pad(1<<20))
		case "/stop":
			io.WriteString(w, pad(5000)+mark+secret+pad(5000))
		case "/error":
			conn, buf, err := w.(http.Hijacker).Hijack()
			if err != nil {
				return
			}
			body := pad(5000) + mark + secret[:10]
			fmt.Fprintf(buf, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\n\r\n%s", len(body)+5000, body)
			buf.Flush()
			conn.Close()
		case "/whole":
			io.WriteString(w, pad(5000)+mark+secret[:10])
		}
	}))
	defer up.Close()
	ext, err := Start(filepath.Join(fakePackage(t, "capture-stop"), ManifestFile), Options{Log: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	defer ext.Stop()
	body := func(mode string, read int) string {
		t.Helper()
		var mu sync.Mutex
		var got []protocol.Diagnostic
		ctx := WithCapture(context.Background(), func(_ string, d protocol.Diagnostic) {
			mu.Lock()
			defer mu.Unlock()
			got = append(got, d)
		})
		settings, _ := json.Marshal(map[string]any{"base_url": up.URL, "mode": mode, "read": read})
		account := protocol.Account{ID: "a", ProxyURL: "none", Settings: settings,
			Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: secret}}
		s := ext.Generate(ctx, account, llm.Request{Model: "m"})
		for ev := s.Next(); ev.Type != llm.EventError && ev.Type != llm.EventFinish; ev = s.Next() {
		}
		s.Close()
		mu.Lock()
		defer mu.Unlock()
		if len(got) != 1 || len(got[0].Upstream) != 1 || got[0].Upstream[0].Response == nil {
			t.Fatalf("%s: captured %+v, want one exchange with its response", mode, got)
		}
		b := got[0].Upstream[0].Response.CapturedBody
		return b.Body + b.BodyEnd
	}
	for _, tc := range []struct {
		mode string
		read int
	}{{"cut", 0}, {"stop", 5000 + len(mark) + 10}, {"error", 0}} {
		t.Run(tc.mode, func(t *testing.T) {
			got := body(tc.mode, tc.read)
			if i := strings.Index(got, secret[:4]); i >= 0 {
				t.Errorf("the body keeps the credential's start where its read stopped: %q", got[max(0, i-len(mark)):])
			}
			if !strings.HasSuffix(got, mark) {
				t.Errorf("the body does not end where the credential's start was: it ends %q, want it to end %q",
					got[max(0, len(got)-40):], mark)
			}
		})
	}
	t.Run("whole", func(t *testing.T) {
		if got, want := body("whole", 0), pad(5000)+mark+secret[:10]; got != want {
			t.Errorf("a body read to its end was changed: it ends %q, want %q", got[max(0, len(got)-40):], want[len(want)-40:])
		}
	})
}

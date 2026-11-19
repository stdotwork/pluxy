package e2e

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/extensions/devin/devintest"
)

// Secrets an upstream echoes in part, or in a form core did not know, never reach the client,
// core's log, the account's last error or the request log. Each case here is one an upstream can
// send; each leaked before its fix.

// leakRun is what one request through core showed: the client's status and body, core's log, the
// account's last error and the request log.
type leakRun struct {
	status    int
	body      string
	logs      *coreLog
	lastError string
	reqLog    string
}

// shows returns where the run shows s, if anywhere; in the request log only when inLog is set.
func (r leakRun) shows(s string, inLog bool) []string {
	var where []string
	if strings.Contains(r.body, s) {
		where = append(where, "the client's error")
	}
	if r.logs.contains(s) {
		where = append(where, "core's log")
	}
	if strings.Contains(r.lastError, s) {
		where = append(where, "the account's last error")
	}
	if inLog && strings.Contains(r.reqLog, s) {
		where = append(where, "the request log")
	}
	return where
}

// wantNoPart fails t where the error the run made (the client's, core's log, the account's last
// error) shows any of parts. The request log is not checked: its upstream capture keeps the
// upstream's body as it came, without whole copies of a secret, and without the pieces of the
// account's secrets only when the reply's status is an error.
func (r leakRun) wantNoPart(t *testing.T, parts ...string) {
	t.Helper()
	r.want(t, false, parts)
}

// wantNoSecret fails t where the run shows any of secrets, the request log included.
func (r leakRun) wantNoSecret(t *testing.T, secrets ...string) {
	t.Helper()
	r.want(t, true, secrets)
}

func (r leakRun) want(t *testing.T, inLog bool, parts []string) {
	t.Helper()
	for _, p := range parts {
		if where := r.shows(p, inLog); len(where) > 0 {
			t.Errorf("%q reaches %s\nclient: %.600s\nlast error: %s", p, strings.Join(where, ", "), r.body, r.lastError)
		}
	}
}

// runLeak starts core with request logs on and one account (manifest, credential, extra fields
// such as headers and proxy_url, settings), sends one chat for model to it, and returns what that
// showed.
func runLeak(t *testing.T, manifest, model string, credential, fields, settings map[string]any) leakRun {
	t.Helper()
	account := map[string]any{"id": "leak-1", "models": []string{model}, "settings": settings, "credential": credential}
	for k, v := range fields {
		account[k] = v
	}
	base, path, logs := runCoreArgs(t, map[string]any{
		"listen":         "127.0.0.1:0",
		"client_keys":    []string{clientKey},
		"management_key": managementKey,
		"logs":           map[string]any{"request_log": true},
		"extension":      map[string]any{"manifest": manifest, "accounts": []any{account}},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp := post(t, ctx, base, clientKey, `{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`)
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	r := leakRun{status: resp.StatusCode, body: string(b), logs: logs}
	if a := getAccount(t, base, "leak-1"); a.LastError != nil {
		r.lastError = a.LastError.Code + ": " + a.LastError.Message
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		matches, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "logs", "v1-chat-completions-*.log"))
		if len(matches) == 1 {
			raw, _ := os.ReadFile(matches[0])
			if r.reqLog = string(raw); strings.Contains(r.reqLog, "=== RESPONSE ===") {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("request logs: %v", matches)
		}
	}
	if !strings.Contains(r.reqLog, "=== UPSTREAM") {
		t.Fatalf("the request log has no upstream capture:\n%s", r.reqLog)
	}
	return r
}

// runDevinLeak runs one Devin chat whose upstream answers with answer, for the session token
// token and the account's extra fields.
func runDevinLeak(t *testing.T, token string, fields map[string]any, answer http.HandlerFunc) leakRun {
	t.Helper()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		answer(w, r)
	}))
	t.Cleanup(up.Close)
	return runLeak(t, filepath.Join(binDir, "devin", "pluxy-extension.json"), "m-devin",
		map[string]any{"kind": "api_key", "api_key": token}, fields, map[string]any{"base_url": up.URL})
}

// jsonRefusal answers status with body as JSON.
func jsonRefusal(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

// trailerRefusal answers 200 with a reply that ends in an end frame carrying the error.
func trailerRefusal(code, message string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		devintest.Reply(w, devintest.End(code, message))
	}
}

// A Devin session token is stored with its public prefix, "devin-session-token$<jwt>"; Devin
// names the JWT alone when it refuses one. Core knows the JWT as a secret too.
func TestDevinBareJWTStaysSecret(t *testing.T) {
	token := "devin-session-token$" + devinJWT
	for _, tc := range []struct {
		name   string
		answer http.HandlerFunc
	}{
		{"refusal body", jsonRefusal(http.StatusUnauthorized, `{"code":"unauthenticated","message":"invalid JWT `+devinJWT+`"}`)},
		{"end frame", trailerRefusal("unauthenticated", "invalid JWT "+devinJWT)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := runDevinLeak(t, token, nil, tc.answer)
			if r.status != http.StatusUnauthorized {
				t.Errorf("status %d, want 401: %s", r.status, r.body)
			}
			r.wantNoSecret(t, devinJWT, devinJWT[40:80])
			if !strings.Contains(r.body, "invalid JWT [redacted]") {
				t.Errorf("the client's error does not say what Devin said: %s", r.body)
			}
		})
	}
}

// A Connect error's message, in a refusal's body or an end frame, is cleaned as other upstream
// text is: a part of the token it holds goes, too short for core or the 24-character rule.
func TestDevinConnectMessageIsCleaned(t *testing.T) {
	token := "devin-session-token$" + devinJWT
	piece := devinJWT[37:57] // 20 characters of the JWT's payload
	for _, tc := range []struct {
		name   string
		answer http.HandlerFunc
	}{
		{"refusal body", jsonRefusal(http.StatusBadRequest, `{"code":"invalid_argument","message":"bad token `+piece+`... (truncated)"}`)},
		{"end frame", trailerRefusal("invalid_argument", "bad token "+piece+"... (truncated)")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := runDevinLeak(t, token, nil, tc.answer)
			if r.status != http.StatusBadRequest {
				t.Errorf("status %d, want 400: %s", r.status, r.body)
			}
			r.wantNoPart(t, piece[:12], piece[8:])
			if !strings.Contains(r.body, "bad token [redacted] (truncated)") {
				t.Errorf("the client's error does not carry the cleaned message: %s", r.body)
			}
		})
	}
}

// Devin's text refusals are cleaned of pieces of every secret the account's requests carry, not
// only the session token: a header's value and a proxy's user name and password.
func TestDevinAccountSecretPiecesAreCleaned(t *testing.T) {
	const gwKey = "Gw5Rt8Yu1Io4Pa7Sd0Fg3Hj6Kl9"
	t.Run("header", func(t *testing.T) {
		r := runDevinLeak(t, "devin-session-token$"+devinJWT, map[string]any{"headers": map[string]string{"X-Gateway-Key": gwKey}},
			textRefusal("gateway rejected key "+gwKey[:14]+"... (truncated)"))
		r.wantNoPart(t, gwKey[:14])
		if !strings.Contains(r.body, "gateway rejected key [redacted] (truncated)") {
			t.Errorf("the client's error does not carry the cleaned text: %s", r.body)
		}
	})
	t.Run("proxy", func(t *testing.T) {
		const user, pass = "u3Rf6Tg9Yh2Uj5", "p8Ik1Ol4Pz7Xc0Vb3Nm"
		// The proxy answers every request itself, refusing it with a text that echoes the
		// login in part.
		proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w, "proxy rejected credentials for "+user[:9]+": "+pass[:10]+"...")
		}))
		t.Cleanup(proxy.Close)
		proxyURL := "http://" + user + ":" + pass + "@" + strings.TrimPrefix(proxy.URL, "http://")
		r := runLeak(t, filepath.Join(binDir, "devin", "pluxy-extension.json"), "m-devin",
			map[string]any{"kind": "api_key", "api_key": "devin-session-token$" + devinJWT},
			map[string]any{"proxy_url": proxyURL},
			map[string]any{"base_url": "http://" + net.JoinHostPort(proxiedHost, "80")})
		r.wantNoPart(t, user[:9], pass[:10])
		if !strings.Contains(r.body, "proxy rejected credentials for [redacted]: [redacted]") {
			t.Errorf("the client's error does not carry the cleaned text: %s", r.body)
		}
	})
}

// A piece of the token written with escapes between its characters (percent-encoding, JSON's
// \uXXXX) is found behind them.
func TestDevinEscapedPieceIsCleaned(t *testing.T) {
	piece := devinJWT[40:60]
	var pct, uni strings.Builder
	for i := range len(piece) {
		fmt.Fprintf(&pct, "%%%02X", piece[i])
		fmt.Fprintf(&uni, `\u%04x`, piece[i])
	}
	for _, tc := range []struct{ name, body string }{
		{"percent", "redirected to https://auth.example/?t=" + pct.String() + " and refused"},
		{"json escapes", "upstream said: bad token " + uni.String() + " refused"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := runDevinLeak(t, "devin-session-token$"+devinJWT, nil, textRefusal(tc.body))
			r.wantNoPart(t, pct.String()[:30], uni.String()[:30])
			if !strings.Contains(r.body, "[redacted] and refused") && !strings.Contains(r.body, "bad token [redacted] refused") {
				t.Errorf("the client's error does not carry the cleaned text: %s", r.body)
			}
		})
	}
}

// The token of an account's authorization header, configured with its scheme ("Bearer <token>"),
// is a secret on its own: an upstream that echoes the token alone has it removed from the client's
// error and the request log's upstream capture.
func TestAccountHeaderTokenStaysSecret(t *testing.T) {
	const gwToken = "gw0Bearer7Tk4Lm1Qz8Wx5Rv2"
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		tok := strings.TrimPrefix(r.Header.Get("X-Gateway-Auth"), "Bearer ")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"gateway token `+tok+` is not valid","type":"invalid_request_error","code":"bad_gateway_token"}}`)
	}))
	t.Cleanup(up.Close)
	r := runLeak(t, filepath.Join(binDir, "pluxy-extension.json"), "gpt-leak",
		map[string]any{"kind": "api_key", "api_key": "sk-leak-e2e-0123456789"},
		map[string]any{"headers": map[string]string{"X-Gateway-Auth": "Bearer " + gwToken}},
		map[string]any{"base_url": up.URL + "/v1"})
	if r.status != http.StatusUnauthorized {
		t.Errorf("status %d, want 401: %s", r.status, r.body)
	}
	r.wantNoSecret(t, gwToken)
	if !strings.Contains(r.body, "gateway token [redacted] is not valid") {
		t.Errorf("the client's error does not say what the upstream said: %s", r.body)
	}
}

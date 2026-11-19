package host

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/internal/configdoc"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

// echoParts is an extension that fails each call with the text of its account's "echo" setting,
// as an upstream's error might quote one part of a secret.
func echoParts() {
	_ = sdk.Upstream(&http.Transport{}) // an extension that sends an account's headers wraps its clients
	err := sdk.Serve(sdk.Extension{
		ID:      "hosttest/echo-parts",
		Version: "0.0.0",
		Generate: func(ctx context.Context, call sdk.Call, out *sdk.Stream) error {
			var st struct {
				Echo string `json:"echo"`
			}
			_ = json.Unmarshal(call.Account.Settings, &st)
			return &llm.Error{Code: llm.CodeUnavailable, Message: st.Echo}
		},
	})
	if err != nil {
		os.Exit(1)
	}
}

// partsJWT is a made-up HS256 JWT in the shape of a Devin session token's secret part.
const partsJWT = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiJob3N0LXBhcnRzIn0.aG9zdC1wYXJ0cy1zaWduYXR1cmU"

// A secret stored or configured as a whole is removed also where the upstream echoes only its
// secret part: a credential's token behind a public prefix (Devin, Meta), the token of an
// account's Authorization header, Basic's base64 text and password, a cookie's value, and the
// token of an Authorization header in the account's settings. The words around it stay, but for
// a word that shares 4 characters with the secret, which the error loses too
// (protocol.RemovePieces): these made-up secrets hold the words "device", "token", "password" and
// "csrf".
func TestSecretPartsRemovedFromCalls(t *testing.T) {
	basic := base64.StdEncoding.EncodeToString([]byte("gw-user:gw-password-123"))
	ext, err := Start(filepath.Join(fakePackage(t, "echo-parts"), ManifestFile), Options{Log: &syncBuffer{}, SettingsSecrets: configdoc.SettingsSecrets})
	if err != nil {
		t.Fatal(err)
	}
	defer ext.Stop()
	for _, c := range []struct {
		name     string
		account  protocol.Account
		settings map[string]any
		echo     string
		want     string
	}{
		{"devin session token", protocol.Account{Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "devin-session-token$" + partsJWT}},
			nil, "invalid JWT " + partsJWT, "invalid JWT [redacted]"},
		{"meta device token", protocol.Account{Credential: &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: "meta-minted-key-0123456789", RefreshToken: "dca:meta-device-0123456789"}},
			nil, "bad device token meta-device-0123456789", "bad [redacted] token [redacted]"},
		{"bearer header", protocol.Account{Headers: map[string]string{"Authorization": "Bearer gw-token-0123456789"}},
			nil, "gateway rejected token gw-token-0123456789", "gateway rejected [redacted] [redacted]"},
		{"basic header", protocol.Account{Headers: map[string]string{"Authorization": "Basic " + basic}},
			nil, "bad credentials " + basic + ", wrong password gw-password-123 for gw-user", "bad credentials [redacted], wrong [redacted] [redacted] for gw-user"},
		{"cookie header", protocol.Account{Headers: map[string]string{"Cookie": "sid=cookie-sid-0123456789; theme=dark; csrf=csrf-token-abcdef12"}},
			nil, "session cookie-sid-0123456789 expired (csrf csrf-token-abcdef12, theme=dark)", "session [redacted] expired ([redacted] [redacted], theme=dark)"},
		{"settings extra_headers", protocol.Account{},
			map[string]any{"extra_headers": map[string]string{"Authorization": "Bearer settings-token-0123456789"}},
			"gateway rejected token settings-token-0123456789", "gateway rejected [redacted] [redacted]"},
	} {
		t.Run(c.name, func(t *testing.T) {
			settings := map[string]any{"echo": c.echo}
			for k, v := range c.settings {
				settings[k] = v
			}
			a := c.account
			a.ID = strings.ReplaceAll(c.name, " ", "-")
			a.Settings, _ = json.Marshal(settings)
			s := ext.Generate(context.Background(), a, llm.Request{Model: "m"})
			ev := s.Next()
			s.Close()
			if ev.Type != llm.EventError || ev.Error.Message != c.want {
				t.Errorf("event %+v, want the error %q", ev, c.want)
			}
		})
	}
}

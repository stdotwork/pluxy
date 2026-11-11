package host

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// What the user entered never comes back in a login prompt, whichever field the extension
// echoes it in.
func TestLoginPromptIsRedacted(t *testing.T) {
	const secret = "user-entered-secret-1234"
	core, ext := net.Pipe()
	defer core.Close()
	defer ext.Close()
	l := &LoginCall{ext: &Extension{manifest: &Manifest{ID: "test/x"}}, ctx: context.Background(), conn: core}
	l.addSecrets(map[string]string{"api_key": secret})
	go protocol.WriteFrame(ext, protocol.Reply{Event: llm.Event{Type: protocol.TypeLoginPrompt}, Prompt: &protocol.Prompt{
		Kind: protocol.PromptBrowser, Message: "key " + secret, URL: "https://login.invalid/?key=" + secret,
		UserCode: secret, VerificationURI: "https://v.invalid/" + secret, VerificationURIComplete: "https://v.invalid/?c=" + secret,
		Inputs: []protocol.LoginInput{{Name: secret, Label: "label " + secret}},
	}})
	r := l.Next()
	if r.Prompt == nil {
		t.Fatalf("got %+v, want a prompt", r)
	}
	b := jsonOf(r.Prompt)
	if strings.Contains(b, secret) {
		t.Errorf("prompt still holds the input: %s", b)
	}
}

func jsonOf(v any) string {
	var sb strings.Builder
	_ = protocol.WriteFrame(&sb, v)
	return sb.String()
}

// Invalid extension settings fail at the protocol boundary, before storage sees them.
func TestLoginGrantSettingsValidation(t *testing.T) {
	for _, tc := range []struct {
		name, raw  string
		ok, absent bool
	}{
		{"object", "{}", true, false},
		{"null", "null", true, true},
		{"array", "[]", false, false},
		{"string", "\"value\"", false, false},
		{"number", "1", false, false},
		{"too large", "{\"value\":\"" + strings.Repeat("x", 8192) + "\"}", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			core, ext := net.Pipe()
			defer core.Close()
			defer ext.Close()
			l := &LoginCall{ext: &Extension{manifest: &Manifest{ID: "test/x"}}, ctx: context.Background(), conn: core}
			// Use the wire shape so the test also builds before settings validation exists.
			go func() {
				raw := map[string]any{"type": "login.done", "grant": map[string]any{"credential": map[string]string{"kind": "api_key", "api_key": "made-up-key"}, "settings": json.RawMessage(tc.raw)}}
				protocol.WriteFrame(ext, raw)
			}()
			r := l.Next()
			if !tc.ok {
				if r.Error == nil || r.Error.Code != llm.CodeExtensionProtocol {
					t.Fatalf("invalid grant settings accepted: %s", jsonOf(r))
				}
			} else {
				if r.Grant == nil {
					t.Fatalf("valid grant refused: %s", jsonOf(r))
				}
				if tc.absent && len(r.Grant.Settings) != 0 {
					t.Fatalf("null settings not absent: %s", r.Grant.Settings)
				}
			}
		})
	}
}

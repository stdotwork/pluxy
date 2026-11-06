package host

import (
	"context"
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

package host

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/stdotwork/pluxy/next/protocol"
)

func TestAddSecretsKeepsEveryBranch(t *testing.T) {
	const (
		settingsSecret = "settings-secret-value-1"
		apiKey         = "sk-abcdefghijklmnop1234"
		redirect       = "https://login.invalid/cb?code=redirect-code-5678"
	)
	ext := &Extension{manifest: &Manifest{ID: "test/x"}, opts: Options{SettingsSecrets: func([]byte) []string {
		return []string{settingsSecret}
	}}}
	jsonObj := `{"token":"` + settingsSecret + `"}`
	wants := map[string]pieces{
		"json":  protocol.AccountSecretsWith(protocol.Account{}, []string{settingsSecret}),
		"key":   protocol.AccountSecrets(protocol.Account{Credential: &protocol.Credential{APIKey: apiKey}}),
		"redir": protocol.AccountSecretsWith(protocol.Account{}, []string{redirect}),
	}
	for name, input := range map[string]map[string]string{
		"json":  {"sa": jsonObj},
		"key":   {"k": apiKey},
		"redir": {"url": redirect},
	} {
		t.Run(name, func(t *testing.T) {
			want := wants[name]
			if len(want) == 0 {
				t.Fatalf("test input yields no pieces")
			}
			l := &LoginCall{ext: ext, ctx: context.Background(), secretInputs: map[string]bool{"sa": true, "k": true}}
			l.addSecrets(input)
			for _, s := range want {
				if !slices.Contains(l.pieces, s) {
					t.Errorf("pieces %q lack %q", l.pieces, s)
				}
			}
		})
	}
	t.Run("all", func(t *testing.T) {
		l := &LoginCall{ext: ext, ctx: context.Background(), secretInputs: map[string]bool{"sa": true, "k": true}}
		l.addSecrets(map[string]string{"sa": jsonObj, "k": apiKey, "url": redirect})
		for n, want := range wants {
			for _, s := range want {
				if !slices.Contains(l.pieces, s) {
					t.Errorf("%s: pieces %q lack %q", n, l.pieces, s)
				}
			}
		}
	})
}

func BenchmarkAddSecretsManyFields(b *testing.B) {
	input := make(map[string]string, 34000)
	for i := 1; i <= 34000; i++ {
		input[fmt.Sprintf("f%05d", i)] = fmt.Sprintf("a://b?sig=%08d", i)
	}
	ext := &Extension{manifest: &Manifest{ID: "test/x"}}
	b.ResetTimer()
	for b.Loop() {
		l := &LoginCall{ext: ext, ctx: context.Background(), secretInputs: map[string]bool{}}
		l.addSecrets(input)
	}
}

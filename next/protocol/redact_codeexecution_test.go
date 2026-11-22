package protocol_test

import (
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

func TestRedactorEventCodeExecutions(t *testing.T) {
	secret := "sk-super-secret-token-1234567890"
	r := protocol.NewRedactor(&protocol.Credential{Kind: "api_key", APIKey: secret})

	orig := llm.Event{
		Type:   llm.EventFinish,
		Finish: &llm.Finish{Reason: llm.FinishStop},
		CodeExecutions: llm.CodeExecutions{
			{
				Code:     "print('" + secret + "')",
				Language: "lang-" + secret,
				Output:   "out: " + secret,
				Failed:   false,
			},
		},
	}
	red := r.Event(orig)
	if len(red.CodeExecutions) != 1 {
		t.Fatalf("redacted code_executions len = %d, want 1", len(red.CodeExecutions))
	}
	got := red.CodeExecutions[0]
	for name, val := range map[string]string{
		"Code":     got.Code,
		"Language": got.Language,
		"Output":   got.Output,
	} {
		if strings.Contains(val, secret) {
			t.Errorf("%s still contains secret: %q", name, val)
		}
		if !strings.Contains(val, protocol.Redacted) {
			t.Errorf("%s missing redaction marker: %q", name, val)
		}
	}
	if !strings.Contains(orig.CodeExecutions[0].Code, secret) {
		t.Errorf("redacting mutated caller's slice")
	}
}

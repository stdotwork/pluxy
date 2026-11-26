package server

import (
	"encoding/json"
	"testing"

	"github.com/stdotwork/pluxy/next/internal/configdoc"
	"github.com/stdotwork/pluxy/next/internal/route"
)

// A PATCH that sets an account's headers to null or to an empty object removes all of them; an
// object replaces them, so a header it leaves out is removed.
func TestPatchHeadersClear(t *testing.T) {
	m := &Management{Redactor: configdoc.NewRedactor()}
	stored := func() route.Account {
		return route.Account{ID: "a", Headers: map[string]string{"X-Gateway-Key": "fake-gateway-key-1", "X-Title": "plain-title"}}
	}
	for _, c := range []struct {
		name, raw string
		want      map[string]string
	}{
		{"null", `null`, nil},
		{"empty object", `{}`, nil},
		{"object without the secret header", `{"X-Title": "plain-title"}`, map[string]string{"X-Title": "plain-title"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			account := stored()
			if err := m.patchHeaders(&account, json.RawMessage(c.raw)); err != nil {
				t.Fatal(err)
			}
			if got, _ := json.Marshal(account.Headers); string(got) != mustJSON(c.want) {
				t.Errorf("PATCH headers %s left %s, want %s", c.raw, got, mustJSON(c.want))
			}
		})
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

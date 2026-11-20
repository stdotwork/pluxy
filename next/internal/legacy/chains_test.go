package legacy_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/internal/legacy"
	"github.com/stdotwork/pluxy/next/internal/route"
	"github.com/stdotwork/pluxy/next/internal/server"
)

// A pool whose models cannot become valid chains fails the import, naming the pool and the
// reason, so nothing is written: the models are not left cut with a row naming a chain that is
// not there. A config.yaml core runs on keeps what it can, as before: the chains are left out and
// a notice says why.
func TestImportPoolThatCannotBecomeChains(t *testing.T) {
	const doc = `
openai-compatibility:
  - name: p
    base-url: https://compat.example.com/v1
    api-key-entries: [{api-key: fake-1}]
    models:
      - {name: y, alias: x}
      - {name: z, alias: x}
      - {name: w, alias: y}
      - {name: v, alias: y}
`
	_, err := legacy.ImportConfig([]byte(doc), t.TempDir())
	for _, want := range []string{"api-keys.openai-compatibility[0]", `"x", "y"`, "cannot become fallback chains", `names the chain "y"`} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ImportConfig err = %v, want it to say %q", err, want)
		}
	}
	res, err := legacy.Translate([]byte(doc))
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if routing, _ := res.Config["routing"].(map[string]any); routing["chains"] != nil {
		t.Errorf("Translate wrote chains %v", routing["chains"])
	}
	if !strings.Contains(strings.Join(res.Notices, "\n"), "left out") {
		t.Errorf("Translate's notices do not say the chains were left out: %v", res.Notices)
	}
}

// A pooled alias (one openai-compatibility key over several models) and a name served by two
// providers become fallback chains that the router accepts: each step is served by an account
// of the imported config, as a management call that sets a chain checks it (CheckSteps).
func TestPoolsBecomeChainsTheRouterAccepts(t *testing.T) {
	res, err := legacy.ImportConfig([]byte(`
access: {api-keys: [fake-client-key]}
management: {secret-key: fake-management-key}
openai-compatibility:
  - name: compat
    base-url: https://compat.example.com/v1
    api-key-entries: [{api-key: fake-1}, {api-key: fake-2}]
    models:
      - {name: deepseek-v3.1, alias: fast}
      - {name: glm-5, alias: fast}
      - {name: claude-sonnet-4}
claude-api-key:
  - api-key: fake-3
    models: [{name: claude-sonnet-4}]
`), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(res.Config)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := server.ParseConfig(b, filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatalf("the imported config is refused: %v\n%s", err, b)
	}
	chains := cfg.Routing.Chains
	if len(chains["fast"]) != 2 || len(chains["claude-sonnet-4"]) != 2 {
		t.Fatalf("chains %+v", chains)
	}
	if s := chains["claude-sonnet-4"]; s[0].Provider != legacy.Claude || s[1].Provider != legacy.OpenAICompatible {
		t.Errorf("claude-sonnet-4 steps %+v, want the file's order: pluxy/claude, then pluxy/openai-compatible", s)
	}
	r, err := route.New(cfg.Routing, cfg.Accounts(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, steps := range chains {
		if err := r.CheckSteps(context.Background(), steps); err != nil {
			t.Errorf("chain %s: %v", name, err)
		}
	}
}

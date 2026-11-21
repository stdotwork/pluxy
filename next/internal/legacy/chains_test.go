package legacy_test

import (
	"context"
	"encoding/json"
	"maps"
	"path/filepath"
	"slices"
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
// of the imported config, as a management call that sets a chain checks it (CheckSteps). A name
// one provider serves gets no chain, and the steps come in the order the file lists a group's
// models, then the order core reads the provider sections.
func TestPoolsBecomeChainsTheRouterAccepts(t *testing.T) {
	res, err := legacy.ImportConfig([]byte(`
access: {api-keys: [fake-client-key]}
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
    models: [{name: claude-sonnet-4}, {name: claude-only}]
`), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// An import writes into core's config, which always has a management key: the import does
	// not bring one, so the test's config carries core's own.
	res.Config["management_key"] = "fake-management-key"
	b, err := json.Marshal(res.Config)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := server.ParseConfig(b, filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatalf("the imported config is refused: %v\n%s", err, b)
	}
	chains := cfg.Routing.Chains
	// The alias's models in the order the group lists them, then the providers a name is served
	// by in the order core reads the sections: pluxy/claude before pluxy/openai-compatible, which
	// is not the file's order.
	want := map[string][]route.Step{
		"fast": {{Model: "deepseek-v3.1", Provider: legacy.OpenAICompatible},
			{Model: "glm-5", Provider: legacy.OpenAICompatible}},
		"claude-sonnet-4": {{Model: "claude-sonnet-4", Provider: legacy.Claude},
			{Model: "claude-sonnet-4", Provider: legacy.OpenAICompatible}},
	}
	if len(chains) != len(want) {
		t.Fatalf("chains %+v, want those of %v", chains, slices.Sorted(maps.Keys(want)))
	}
	for name, wantSteps := range want {
		steps, ok := chains[name]
		if !ok {
			t.Errorf("no chain %s: %+v", name, chains)
			continue
		}
		if len(steps) != len(wantSteps) {
			t.Errorf("chain %s: %+v", name, steps)
			continue
		}
		for i, w := range wantSteps {
			if steps[i].Model != w.Model || steps[i].Provider != w.Provider {
				t.Errorf("chain %s step %d = %+v, want %s@%s", name, i+1, steps[i], w.Model, w.Provider)
			}
			// A pool's steps name the group's accounts; a name two providers serve is left to each
			// provider's own accounts.
			var accounts []string
			if name == "fast" {
				accounts = []string{"compat-1", "compat-2"}
			}
			if !slices.Equal(steps[i].Accounts, accounts) {
				t.Errorf("chain %s step %d accounts %v, want %v", name, i+1, steps[i].Accounts, accounts)
			}
		}
	}
	// The only chains are a pool's and a name two providers serve: one provider's name, however
	// many accounts it has, is served without one.
	if steps, ok := chains["claude-only"]; ok {
		t.Errorf("a name one provider serves has the chain %+v", steps)
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

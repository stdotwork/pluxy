package route

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// The naming rules follow what today's core does, as observed by running it: a config model's
// alias replaces its name, exclusions match the names before any alias (case and wildcards
// aside), an alias without fork hides the model's name, and a prefix adds "<prefix>/<name>",
// keeping the bare name unless prefixes are forced.

func TestModelEntryJSON(t *testing.T) {
	var got []ModelEntry
	if err := json.Unmarshal([]byte(`["a", {"name": "b", "alias": "c"}, {"name": "d"}]`), &got); err != nil {
		t.Fatal(err)
	}
	if want := []ModelEntry{{Name: "a"}, {Name: "b", Alias: "c"}, {Name: "d"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	b, _ := json.Marshal(got)
	if string(b) != `["a",{"name":"b","alias":"c"},"d"]` {
		t.Errorf("marshalled %s", b)
	}
	if err := json.Unmarshal([]byte(`[{"name": "b", "aliases": "c"}]`), &got); err == nil {
		t.Error("an unknown field in a model entry was accepted")
	}
}

func TestResolveConfigModels(t *testing.T) {
	a := &Account{ID: "a", Models: []ModelEntry{{Name: "up-a", Alias: "pub-a"}, {Name: "up-b"}, {Name: "Up-C"}, {Name: "up-d", Alias: "pub-d"}},
		ExcludedModels: []string{"up-d", "*-c"}}
	for model, want := range map[string]string{"pub-a": "up-a", "up-b": "up-b", "pub-d": "up-d"} {
		if got, ok := a.resolve(model, false); !ok || got != want {
			t.Errorf("resolve(%q) = %q, %v; want %q", model, got, ok, want)
		}
	}
	// The upstream name of an aliased model, another case, an excluded name: not served.
	for _, model := range []string{"up-a", "PUB-A", "Up-C", "nope"} {
		if got, ok := a.resolve(model, false); ok {
			t.Errorf("resolve(%q) = %q; want not served", model, got)
		}
	}
}

func TestResolveAnyModel(t *testing.T) {
	a := &Account{ID: "a", ExcludedModels: []string{"gpt-6.1-sol", "astra", "*mini*"},
		ModelAlias: []ModelAlias{{Name: "gpt-6.1-sol", Alias: "sol"}, {Name: "GPT-6-ASTRA", Alias: "astra", Fork: true}, {Name: "gpt-6-luna", Alias: "luna"}}}
	// An alias matches only as written, as in today's core, whose model registry refuses "LUNA"
	// before any alias applies; an account that serves any model passes it on as a name of its own.
	for model, want := range map[string]string{"astra": "GPT-6-ASTRA", "gpt-6-astra": "gpt-6-astra", "luna": "gpt-6-luna", "LUNA": "LUNA", "anything": "anything"} {
		if got, ok := a.resolve(model, false); !ok || got != want {
			t.Errorf("resolve(%q) = %q, %v; want %q", model, got, ok, want)
		}
	}
	// sol's model is excluded; luna's name is renamed away; mini models are excluded.
	for _, model := range []string{"sol", "gpt-6.1-sol", "gpt-6-luna", "GPT-6-Luna", "gpt-5-MINI-x"} {
		if got, ok := a.resolve(model, false); ok {
			t.Errorf("resolve(%q) = %q; want not served", model, got)
		}
	}
}

func TestResolvePrefix(t *testing.T) {
	a := &Account{ID: "a", Prefix: "team", Models: []ModelEntry{{Name: "up-a", Alias: "pub-a"}, {Name: "team"}}}
	cases := []struct {
		model string
		force bool
		want  string // empty: not served
	}{
		{"team/pub-a", false, "up-a"}, {"pub-a", false, "up-a"}, {"Team/pub-a", false, ""},
		{"team/pub-a", true, "up-a"}, {"pub-a", true, ""},
		// A model named as the prefix is served bare even when prefixes are forced.
		{"team", true, "team"}, {"team/team", true, "team"},
	}
	for _, c := range cases {
		got, ok := a.resolve(c.model, c.force)
		if (c.want == "") == ok || got != c.want {
			t.Errorf("resolve(%q, force %v) = %q, %v; want %q", c.model, c.force, got, ok, c.want)
		}
	}
	b := &Account{ID: "b"} // no prefix, serves any model: a name with a slash is just a name
	if got, ok := b.resolve("team/pub-a", true); !ok || got != "team/pub-a" {
		t.Errorf("resolve on an account without a prefix = %q, %v", got, ok)
	}
}

func publics(ns []Name) []string {
	var out []string
	for _, n := range ns {
		out = append(out, n.Public)
	}
	return out
}

func TestNames(t *testing.T) {
	a := &Account{ID: "a", Prefix: "team", ExcludedModels: []string{"x-*"},
		ModelAlias: []ModelAlias{{Name: "m1", Alias: "one"}, {Name: "M2", Alias: "two", Fork: true, DisplayName: "Two"}, {Name: "m1", Alias: "uno"}}}
	got := a.names([]string{"m1", "m2", "x-3", "m4"}, false)
	if want := []string{"one", "team/one", "uno", "team/uno", "m2", "team/m2", "two", "team/two", "m4", "team/m4"}; !slices.Equal(publics(got), want) {
		t.Fatalf("names = %v, want %v", publics(got), want)
	}
	if n := got[6]; n.Upstream != "m2" || n.DisplayName != "Two" {
		t.Errorf("fork alias: %+v", n)
	}
	if got := a.names([]string{"m4", "team"}, true); !slices.Equal(publics(got), []string{"team/m4", "team", "team/team"}) {
		t.Errorf("forced names = %v", publics(got))
	}
}

func TestRenamed(t *testing.T) {
	body := json.RawMessage(`{ "messages":[{"role":"user","content":"model"}],"model" : "pub-a", "x":{"model":"keep"},"model":"pub-a"}`)
	req := llm.Request{Model: "pub-a", Native: &llm.Native{Format: llm.FormatOpenAIChat, Body: body}}
	got := renamed(req, "up-a")
	want := `{ "messages":[{"role":"user","content":"model"}],"model" : "up-a", "x":{"model":"keep"},"model":"up-a"}`
	if got.Model != "up-a" || string(got.Native.Body) != want {
		t.Errorf("renamed: model %q, body %s", got.Model, got.Native.Body)
	}
	if string(req.Native.Body) != string(body) {
		t.Error("renamed changed the caller's request")
	}
	gemini := llm.Request{Model: "pub", Native: &llm.Native{Format: llm.FormatGeminiGenerate, Body: json.RawMessage(`{"contents":[]}`)}}
	if got := renamed(gemini, "up"); got.Model != "up" || string(got.Native.Body) != `{"contents":[]}` {
		t.Errorf("renamed a body without a model: %+v", got)
	}
}

// A request for an alias goes to the extension under the upstream name, and a model-scoped block
// is kept under it: asking by another name for the same model does not get around the block.
func TestRoutingUsesUpstreamNames(t *testing.T) {
	var sent []string
	h := newHarness(t, Config{}, Account{ID: "a", ModelAlias: []ModelAlias{{Name: "up", Alias: "pub", Fork: true}}})
	h.r.attempt = func(ctx context.Context, ext string, a protocol.Account, req llm.Request) Stream {
		sent = append(sent, req.Model)
		return h.attempt(ctx, ext, a, req)
	}
	h.on("a", script{{Type: llm.EventError, Error: &llm.Error{Code: llm.CodeRateLimit, Retryable: true, Scope: llm.ScopeModel, RetryAfterMS: 60000}}}, ok())
	h.run(h.req("pub"))
	evs := h.run(h.req("up"))
	if e := evs[0].Error; e == nil || e.Code != llm.CodeRateLimit {
		t.Errorf("the same model by its other name: %+v, want the block's rate_limit", evs[0])
	}
	h.now = h.now.Add(2 * time.Minute)
	h.runOK("up")
	if !slices.Equal(sent, []string{"up", "up"}) {
		t.Errorf("the extension was asked for %v, want [up up]", sent)
	}
}

func TestRouterModels(t *testing.T) {
	h := newHarness(t, Config{ForceModelPrefix: true},
		Account{ID: "cfg", Models: []ModelEntry{{Name: "up-a", Alias: "pub-a"}, {Name: "shared"}}},
		Account{ID: "cat", Prefix: "team", ModelAlias: []ModelAlias{{Name: "gemini-x", Alias: "gx", DisplayName: "GX"}}},
		Account{ID: "broken", Models: []ModelEntry{{Name: "shared"}}},
	)
	catalog := func(_ context.Context, a Account, _ *protocol.Credential) (*protocol.ModelList, *llm.Error) {
		switch a.ID {
		case "cfg":
			return &protocol.ModelList{OwnedBy: "openai", Models: []protocol.Model{{ID: "ignored"}}}, nil
		case "cat":
			return &protocol.ModelList{OwnedBy: "google", Models: []protocol.Model{
				{ID: "gemini-x", Name: "models/gemini-x", DisplayName: "Gemini X", Created: 7, ContextLength: 9},
				{ID: "other", OwnedBy: "someone"},
				{ID: "other", OwnedBy: "a repeat, left out"},
			}}, nil
		}
		return nil, &llm.Error{Code: llm.CodeUnavailable, Message: "down"}
	}
	got := h.r.Models(context.Background(), catalog)
	created := h.r.created.Unix()
	want := []protocol.Model{
		{ID: "pub-a", DisplayName: "up-a", Created: created, OwnedBy: "openai"},
		{ID: "shared", DisplayName: "shared", Created: created, OwnedBy: "openai"},
		{ID: "team/gx", Name: "models/team/gx", DisplayName: "GX", Created: 7, ContextLength: 9, OwnedBy: "google"},
		{ID: "team/other", OwnedBy: "someone"},
	}
	if !reflect.DeepEqual(got, want) {
		gb, _ := json.MarshalIndent(got, "", " ")
		t.Errorf("models:\n%s", gb)
	}
	// Without a catalog, accounts list the models their config names, owned by the account.
	if got := h.r.Models(context.Background(), nil); len(got) != 2 || got[0].OwnedBy != "cfg" {
		t.Errorf("models without a catalog: %+v", got)
	}
}

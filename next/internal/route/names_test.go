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
		if got, ok := a.resolve(model, false, nil); !ok || got != want {
			t.Errorf("resolve(%q) = %q, %v; want %q", model, got, ok, want)
		}
	}
	// The upstream name of an aliased model, another case, an excluded name: not served.
	for _, model := range []string{"up-a", "PUB-A", "Up-C", "nope"} {
		if got, ok := a.resolve(model, false, nil); ok {
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
		if got, ok := a.resolve(model, false, nil); !ok || got != want {
			t.Errorf("resolve(%q) = %q, %v; want %q", model, got, ok, want)
		}
	}
	// sol's model is excluded; luna's name is renamed away; mini models are excluded.
	for _, model := range []string{"sol", "gpt-6.1-sol", "gpt-6-luna", "GPT-6-Luna", "gpt-5-MINI-x"} {
		if got, ok := a.resolve(model, false, nil); ok {
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
		got, ok := a.resolve(c.model, c.force, nil)
		if (c.want == "") == ok || got != c.want {
			t.Errorf("resolve(%q, force %v) = %q, %v; want %q", c.model, c.force, got, ok, c.want)
		}
	}
	b := &Account{ID: "b"} // no prefix, serves any model: a name with a slash is just a name
	if got, ok := b.resolve("team/pub-a", true, nil); !ok || got != "team/pub-a" {
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
	h.r.UseCatalog(catalog)
	got := h.r.Models(context.Background())
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
	h.r.UseCatalog(nil)
	if got := h.r.Models(context.Background()); len(got) != 2 || got[0].OwnedBy != "cfg" {
		t.Errorf("models without a catalog: %+v", got)
	}
}

// An account without models of its own serves only the models its extension lists, under the
// account's names for them; it serves any model while its list is empty or cannot be read. An
// account whose config lists models keeps to them whatever its extension lists.
func TestRoutingRefusesModelsOutsideTheList(t *testing.T) {
	h := newHarness(t, Config{},
		Account{ID: "cat", ModelAlias: []ModelAlias{{Name: "m1", Alias: "one"}}, ExcludedModels: []string{"m3"}},
		Account{ID: "cfg", Models: []ModelEntry{{Name: "own"}}},
		Account{ID: "empty"},
	)
	lists := map[string][]protocol.Model{"cat": {{ID: "m1"}, {ID: "m2"}, {ID: "m3"}}, "cfg": {{ID: "m2"}}}
	h.r.UseCatalog(func(_ context.Context, a Account, _ *protocol.Credential) (*protocol.ModelList, *llm.Error) {
		return &protocol.ModelList{Models: lists[a.ID]}, nil
	})
	served := func(model string) []string {
		h.attempts = nil
		evs := h.run(Request{LLM: llm.Request{Model: model}, Pin: "cat"})
		if evs[len(evs)-1].Type != llm.EventFinish {
			return nil
		}
		return h.attempts
	}
	for model, want := range map[string]bool{"one": true, "m2": true, "m1": false, "m3": false, "unknown": false} {
		if got := served(model) != nil; got != want {
			t.Errorf("account with a list, model %q: served %v, want %v", model, got, want)
		}
	}
	evs := h.run(Request{LLM: llm.Request{Model: "unknown"}, Pin: "cat"})
	if e := evs[0].Error; e == nil || e.Code != llm.CodeNotFound || e.ProviderCode != "model_not_found" {
		t.Errorf("refused model: %+v, want model_not_found", evs[0])
	}
	h.attempts = nil
	if evs := h.run(Request{LLM: llm.Request{Model: "m2"}, Pin: "cfg"}); evs[0].Error == nil {
		t.Errorf("an account with models of its own served a model only its extension lists")
	}
	if evs := h.run(Request{LLM: llm.Request{Model: "anything"}, Pin: "empty"}); evs[len(evs)-1].Type != llm.EventFinish {
		t.Errorf("an account with an empty list refused a model: %+v", evs[0])
	}
}

// An account's list is read once and kept until the accounts are set again; a list that could
// not be read is asked for again after a while, and meanwhile the account serves any model.
func TestModelListsAreKept(t *testing.T) {
	h := newHarness(t, Config{}, Account{ID: "a"})
	calls, fail := 0, true
	h.r.UseCatalog(func(_ context.Context, a Account, _ *protocol.Credential) (*protocol.ModelList, *llm.Error) {
		calls++
		if fail {
			return nil, &llm.Error{Code: llm.CodeUnavailable, Message: "down"}
		}
		return &protocol.ModelList{Models: []protocol.Model{{ID: "m"}}}, nil
	})
	h.runOK("other")
	fail = false
	h.runOK("other")
	if calls != 1 {
		t.Fatalf("%d reads within a minute of a failed one, want 1", calls)
	}
	h.now = h.now.Add(catalogRetry)
	if evs := h.run(h.req("other")); evs[0].Error == nil || calls != 2 {
		t.Fatalf("after the wait: %d reads, first event %+v; want a new read that refuses the model", calls, evs[0])
	}
	h.runOK("m")
	h.r.Models(context.Background())
	if calls != 2 {
		t.Errorf("%d reads, want the kept list used", calls)
	}
	if err := h.r.SetAccounts([]Account{{ID: "a"}}); err != nil {
		t.Fatal(err)
	}
	h.runOK("m")
	if calls != 3 {
		t.Errorf("%d reads after the accounts were set again, want 3", calls)
	}
}

// A model list read that never ends is given up after catalogTimeout: the request goes on with
// the list unknown (any model) instead of waiting with it.
func TestModelListReadHasATimeLimit(t *testing.T) {
	old := catalogTimeout
	catalogTimeout = 50 * time.Millisecond
	defer func() { catalogTimeout = old }()
	h := newHarness(t, Config{}, Account{ID: "a"})
	h.r.UseCatalog(func(ctx context.Context, _ Account, _ *protocol.Credential) (*protocol.ModelList, *llm.Error) {
		<-ctx.Done()
		return nil, &llm.Error{Code: llm.CodeCancelled, Message: "cancelled"}
	})
	done := make(chan []llm.Event, 1)
	go func() { done <- h.run(h.req("any")) }()
	select {
	case evs := <-done:
		if last := evs[len(evs)-1]; last.Type != llm.EventFinish {
			t.Fatalf("request failed: %+v", last.Error)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the request waited for a model list read that never ends")
	}
}

// A request still routing over the accounts set before a change cannot leave its account's list
// for the account that replaced it under the same ID.
func TestOldAccountsLeaveNoModelList(t *testing.T) {
	h := newHarness(t, Config{}, Account{ID: "a", Settings: json.RawMessage(`{"v":1}`)})
	h.r.UseCatalog(func(_ context.Context, a Account, _ *protocol.Credential) (*protocol.ModelList, *llm.Error) {
		return &protocol.ModelList{Models: []protocol.Model{{ID: "m" + string(a.Settings[5])}}}, nil
	})
	accounts, creds := h.r.snapshot()
	if err := h.r.SetAccounts([]Account{{ID: "a", Settings: json.RawMessage(`{"v":2}`)}}); err != nil {
		t.Fatal(err)
	}
	// The old account's list is read after the change, as by a request that started before it.
	if l := h.r.listOf(context.Background(), accounts[0], creds, h.r.catalogFunc(), h.now); l == nil || l.Models[0].ID != "m1" {
		t.Fatalf("old account's list = %+v, want m1", l)
	}
	h.runOK("m2")
}

package route

import (
	"context"
	"encoding/json"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

type slowModelStream struct{ n int }

func (s *slowModelStream) Next() llm.Event {
	s.n++
	if s.n <= 50 {
		time.Sleep(200 * time.Microsecond)
		return llm.Event{Type: llm.EventTextDelta}
	}
	return llm.Event{Type: llm.EventFinish}
}
func (s *slowModelStream) Close() {}

// Adapted from prototype 3: replacing an account's model list via RefreshModels while requests
// stream never cuts or changes an in-flight stream mid-reply; requests keep the accounts and
// names they started with.
func TestRefreshMidStream(t *testing.T) {
	attempt := func(ctx context.Context, ext string, a protocol.Account, req llm.Request) Stream {
		return &slowModelStream{}
	}
	r, err := New(Config{}, []Account{{ID: "a", Extension: "x"}, {ID: "b", Extension: "x"}}, attempt, nil)
	if err != nil {
		t.Fatal(err)
	}
	var flip atomic.Int64
	catalog := func(ctx context.Context, a Account, c *protocol.Credential) (*protocol.ModelList, *llm.Error) {
		if flip.Add(1)%2 == 0 {
			return &protocol.ModelList{Models: []protocol.Model{{ID: "other"}}}, nil
		}
		return &protocol.ModelList{Models: []protocol.Model{{ID: "m"}, {ID: "other"}}}, nil
	}
	r.UseCatalog(catalog)

	stop := make(chan struct{})
	var refreshes atomic.Int64
	var refreshWG sync.WaitGroup
	refreshWG.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
				r.RefreshModels(context.Background(), "")
				refreshes.Add(1)
				time.Sleep(100 * time.Microsecond)
			}
		}
	})

	var wg sync.WaitGroup
	var served, refused, torn atomic.Int64
	for range 400 {
		wg.Go(func() {
			s := r.Generate(context.Background(), Request{LLM: llm.Request{Model: "m"}})
			defer s.Close()
			ev := s.Next()
			if ev.Type == llm.EventError {
				if ev.Error.ProviderCode != "model_not_found" {
					t.Errorf("unexpected error: %+v", ev.Error)
				}
				refused.Add(1)
				return
			}
			n := 1
			for !ev.Terminal() {
				ev = s.Next()
				n++
			}
			if ev.Type != llm.EventFinish || n != 51 {
				torn.Add(1)
			}
			served.Add(1)
		})
	}
	wg.Wait()
	close(stop)
	refreshWG.Wait()
	t.Logf("served whole=%d refused before start=%d torn=%d refreshes=%d", served.Load(), refused.Load(), torn.Load(), refreshes.Load())
	if torn.Load() != 0 {
		t.Fatalf("%d streams were cut or changed mid-reply", torn.Load())
	}
	if served.Load() == 0 {
		t.Fatalf("expected at least one served stream, got 0")
	}
}

// Known model lists refresh every 30 minutes with jitter (driven here with a fake clock and
// controllable jitter).
func TestModelListRefreshTimerAndJitter(t *testing.T) {
	h := newHarness(t, Config{}, Account{ID: "a"})
	var (
		mu     sync.Mutex
		calls  int
		models = []string{"m1"}
	)
	h.r.UseCatalog(func(_ context.Context, _ Account, _ *protocol.Credential) (*protocol.ModelList, *llm.Error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		out := make([]protocol.Model, len(models))
		for i, id := range models {
			out[i] = protocol.Model{ID: id}
		}
		return &protocol.ModelList{Models: out}, nil
	})
	h.runOK("m1")
	if calls != 1 {
		t.Fatalf("initial calls = %d, want 1", calls)
	}

	mu.Lock()
	models = []string{"m1", "m2"}
	mu.Unlock()

	// Before 30 minutes: RefreshDue does nothing.
	h.now = h.now.Add(catalogRefresh - time.Minute)
	h.r.RefreshDue(context.Background())
	if calls != 1 {
		t.Fatalf("calls at 29m = %d, want 1", calls)
	}

	// At 30 minutes (rand=0): RefreshDue picks up m2.
	h.now = h.now.Add(time.Minute)
	h.r.RefreshDue(context.Background())
	if calls != 2 {
		t.Fatalf("calls at 30m = %d, want 2", calls)
	}
	if got := modelIDs(&protocol.ModelList{Models: h.r.Models(context.Background())}); !slices.Equal(got, []string{"m1", "m2"}) {
		t.Fatalf("models after 30m refresh = %v, want [m1 m2]", got)
	}

	// With non-zero jitter, refresh waits past 30 minutes until jitter elapses.
	h.r.rand = func() float64 { return 1.0 }
	h.r.RefreshModels(context.Background(), "a") // schedules next refresh at now + 30m + 3m
	mu.Lock()
	callsBefore := calls
	models = []string{"m1", "m2", "m3"}
	mu.Unlock()

	h.now = h.now.Add(catalogRefresh)
	h.r.RefreshDue(context.Background())
	if calls != callsBefore {
		t.Fatalf("refreshed at 30m despite full jitter: calls=%d, want %d", calls, callsBefore)
	}
	h.now = h.now.Add(catalogRefreshJitter)
	h.r.RefreshDue(context.Background())
	if calls != callsBefore+1 {
		t.Fatalf("did not refresh after 30m + jitter: calls=%d, want %d", calls, callsBefore+1)
	}
	if got := modelIDs(&protocol.ModelList{Models: h.r.Models(context.Background())}); !slices.Equal(got, []string{"m1", "m2", "m3"}) {
		t.Fatalf("models after jittered refresh = %v, want [m1 m2 m3]", got)
	}
}

// A refresh that fails, times out or comes back empty keeps the last good list and never puts a
// known list back to unknown; requests arriving while a slow refresh runs do not wait for it.
func TestModelListRefreshKeepsLastGood(t *testing.T) {
	oldTimeout := catalogTimeout
	catalogTimeout = 40 * time.Millisecond
	defer func() { catalogTimeout = oldTimeout }()

	h := newHarness(t, Config{}, Account{ID: "a"})
	var (
		mu   sync.Mutex
		mode = "ok"
		gate chan struct{}
	)
	h.r.UseCatalog(func(ctx context.Context, _ Account, _ *protocol.Credential) (*protocol.ModelList, *llm.Error) {
		mu.Lock()
		m, g := mode, gate
		mu.Unlock()
		switch m {
		case "ok":
			return &protocol.ModelList{Models: []protocol.Model{{ID: "m1"}}}, nil
		case "fail":
			return nil, &llm.Error{Code: llm.CodeUpstream, Message: "boom"}
		case "empty":
			return &protocol.ModelList{Models: []protocol.Model{}}, nil
		case "hang":
			<-ctx.Done()
			return nil, &llm.Error{Code: llm.CodeCancelled, Message: "timed out"}
		case "gated":
			<-g
			return &protocol.ModelList{Models: []protocol.Model{{ID: "m1"}, {ID: "m2"}}}, nil
		}
		return nil, nil
	})
	h.runOK("m1")

	for _, tc := range []string{"fail", "hang", "empty"} {
		mu.Lock()
		mode = tc
		mu.Unlock()
		res := h.r.RefreshModels(context.Background(), "a")
		if len(res) != 1 || res[0].Models != 1 || !res[0].Kept || res[0].Error == "" {
			t.Fatalf("mode %s: RefreshModels = %+v, want Models=1 Kept=true Error!=empty", tc, res)
		}
		if got := modelIDs(&protocol.ModelList{Models: h.r.Models(context.Background())}); !slices.Equal(got, []string{"m1"}) {
			t.Fatalf("mode %s: Models = %v, want [m1]", tc, got)
		}
		h.runOK("m1")
		if evs := h.run(h.req("unknown")); evs[0].Error == nil || evs[0].Error.ProviderCode != "model_not_found" {
			t.Fatalf("mode %s: unknown model was not refused (list reverted to unknown): %+v", tc, evs[0])
		}
	}

	// Requests arriving during an in-flight refresh keep using the kept list without waiting.
	g := make(chan struct{})
	mu.Lock()
	mode, gate = "gated", g
	mu.Unlock()
	refreshDone := make(chan []RefreshResult, 1)
	go func() {
		refreshDone <- h.r.RefreshModels(context.Background(), "a")
	}()
	// Even before g closes, a request succeeds immediately with m1.
	h.runOK("m1")
	close(g)
	res := <-refreshDone
	if len(res) != 1 || res[0].Models != 2 || res[0].Kept || res[0].Error != "" {
		t.Fatalf("gated refresh = %+v, want Models=2 Kept=false", res)
	}
}

// SetAccounts with a same predicate keeps model lists for unchanged accounts and drops them
// only for changed or removed accounts.
func TestSetAccountsRetainsUnchangedModelLists(t *testing.T) {
	h := newHarness(t, Config{},
		Account{ID: "a", Settings: json.RawMessage(`{"v":1}`)},
		Account{ID: "b", Settings: json.RawMessage(`{"v":1}`)},
	)
	var (
		mu    sync.Mutex
		reads = map[string]int{}
	)
	h.r.UseCatalog(func(_ context.Context, a Account, _ *protocol.Credential) (*protocol.ModelList, *llm.Error) {
		mu.Lock()
		reads[a.ID]++
		mu.Unlock()
		return &protocol.ModelList{Models: []protocol.Model{{ID: a.ID + "-m"}}}, nil
	})
	h.runOK("a-m")
	h.runOK("b-m")

	err := h.r.SetAccounts([]Account{
		{ID: "a", Settings: json.RawMessage(`{"v":1}`)},
		{ID: "b", Settings: json.RawMessage(`{"v":2}`)},
	}, func(old, cur *Account) bool {
		return string(old.Settings) == string(cur.Settings)
	})
	if err != nil {
		t.Fatal(err)
	}
	h.runOK("a-m")
	h.runOK("b-m")

	mu.Lock()
	defer mu.Unlock()
	if reads["a"] != 1 || reads["b"] != 2 {
		t.Errorf("reads = %v, want a=1 (kept) and b=2 (re-read)", reads)
	}
}

// An empty list on the first read is kept as a known empty list (not retried after
// catalogRetry); served lists no models for that account, and a later refresh that returns a
// non-empty list replaces it.
func TestEmptyFirstReadIsKeptWithoutRetry(t *testing.T) {
	h := newHarness(t, Config{}, Account{ID: "a"})
	var (
		mu     sync.Mutex
		calls  int
		models = []protocol.Model{}
	)
	h.r.UseCatalog(func(_ context.Context, _ Account, _ *protocol.Credential) (*protocol.ModelList, *llm.Error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		return &protocol.ModelList{Models: slices.Clone(models)}, nil
	})

	accounts, creds := h.r.snapshot()
	if got := h.r.served(context.Background(), accounts, creds, h.r.catalogFunc()); len(got) != 0 {
		t.Fatalf("served after empty first read = %v, want empty", got)
	}
	if calls != 1 {
		t.Fatalf("first read calls = %d, want 1", calls)
	}

	// Advancing past catalogRetry must not trigger a second read: the empty list is known.
	mu.Lock()
	models = []protocol.Model{{ID: "m1"}}
	mu.Unlock()
	h.now = h.now.Add(catalogRetry)
	if got := h.r.served(context.Background(), accounts, creds, h.r.catalogFunc()); len(got) != 0 {
		t.Fatalf("served at catalogRetry = %v, want empty", got)
	}
	if calls != 1 {
		t.Fatalf("calls after catalogRetry = %d, want 1 (empty first read must not retry every minute)", calls)
	}

	// A later refresh returning a non-empty list replaces the kept empty list.
	res := h.r.RefreshModels(context.Background(), "a")
	if len(res) != 1 || res[0].Models != 1 || res[0].Kept || res[0].Error != "" {
		t.Fatalf("RefreshModels after empty first read = %+v, want Models=1 Kept=false", res)
	}
	if got := modelIDs(&protocol.ModelList{Models: h.r.Models(context.Background())}); !slices.Equal(got, []string{"m1"}) {
		t.Fatalf("models after refresh = %v, want [m1]", got)
	}
}

func TestMergeModelLimitsAndCapabilities(t *testing.T) {
	catModels := map[string]protocol.Model{
		"gpt-5.6-luna": {
			ID:                  "gpt-5.6-luna",
			ContextLength:       922000,
			MaxCompletionTokens: 128000,
			Capabilities: &protocol.ModelCapabilities{
				Tools:            true,
				StructuredOutput: true,
				Vision:           true,
				Reasoning:        true,
				ReasoningLevels:  []string{"none", "low", "medium", "high", "xhigh", "max"},
			},
		},
		"kimi-k3": {
			ID:                  "kimi-k3",
			ContextLength:       1048576,
			MaxCompletionTokens: 65536,
			Capabilities: &protocol.ModelCapabilities{
				Tools:           true,
				Vision:          true,
				Reasoning:       true,
				ReasoningLevels: []string{"low", "high", "max"},
			},
		},
		"claude-sonnet-4-6": {
			ID:                  "claude-sonnet-4-6",
			ContextLength:       1000000,
			MaxCompletionTokens: 128000,
			Capabilities: &protocol.ModelCapabilities{
				Tools:            true,
				StructuredOutput: true,
				Vision:           true,
				PDF:              true,
				Reasoning:        true,
				ReasoningLevels:  []string{"low", "medium", "high", "max"},
			},
		},
	}

	h := newHarness(t, Config{},
		Account{ID: "codex-sub", Extension: "pluxy/codex"},
		Account{ID: "kimi-live", Extension: "pluxy/openai-compatible"},
		Account{ID: "claude-builtin", Extension: "pluxy/claude"},
		Account{ID: "custom-ext", Extension: "pluxy/custom"},
	)
	h.r.UseCatalogue(func(id string) (protocol.Model, bool) {
		m, ok := catModels[id]
		return m, ok
	})
	h.r.UseCatalog(func(_ context.Context, a Account, _ *protocol.Credential) (*protocol.ModelList, *llm.Error) {
		switch a.ID {
		case "codex-sub":
			// Account list gives 272k window, silent on max_output_tokens and capabilities.
			return &protocol.ModelList{
				OwnedBy: "openai",
				From:    protocol.ModelListFromAccount,
				Models: []protocol.Model{
					{ID: "gpt-5.6-luna", ContextLength: 272000},
				},
			}, nil
		case "kimi-live":
			// Account list gives model ID only (silent on limits and capabilities).
			return &protocol.ModelList{
				OwnedBy: "moonshot",
				From:    protocol.ModelListFromAccount,
				Models: []protocol.Model{
					{ID: "kimi-k3"},
				},
			}, nil
		case "claude-builtin":
			// Extension built-in list gives 200k/64k, smaller than catalogue's 1M/128k.
			return &protocol.ModelList{
				OwnedBy: "anthropic",
				From:    protocol.ModelListFromExtension,
				Models: []protocol.Model{
					{ID: "claude-sonnet-4-6", ContextLength: 200000, MaxCompletionTokens: 64000},
				},
			}, nil
		case "custom-ext":
			// Extension built-in list for a model not in the catalogue.
			return &protocol.ModelList{
				OwnedBy: "custom",
				From:    protocol.ModelListFromExtension,
				Models: []protocol.Model{
					{ID: "custom-only", ContextLength: 64000, MaxCompletionTokens: 8192},
				},
			}, nil
		}
		return nil, nil
	})

	served := h.r.ServedModels(context.Background())
	byID := map[string]ServedModel{}
	for _, m := range served {
		byID[m.ID] = m
	}

	// 1. Account limit 272k vs catalogue 922k -> 272k wins (from: "account"), max_output_tokens
	// and capabilities filled from catalogue.
	luna, ok := byID["gpt-5.6-luna"]
	if !ok {
		t.Fatalf("gpt-5.6-luna missing from ServedModels")
	}
	if luna.ContextLength != 272000 || luna.MaxOutputTokens != 128000 || luna.MaxCompletionTokens != 128000 {
		t.Errorf("gpt-5.6-luna limits = (ctx=%d, out=%d, maxComp=%d), want (272000, 128000, 128000)",
			luna.ContextLength, luna.MaxOutputTokens, luna.MaxCompletionTokens)
	}
	if luna.From != protocol.ModelListFromAccount {
		t.Errorf("gpt-5.6-luna from = %q, want %q", luna.From, protocol.ModelListFromAccount)
	}
	if luna.Capabilities == nil || !luna.Capabilities.Tools || !luna.Capabilities.Vision ||
		!luna.Capabilities.Reasoning || !slices.Equal(luna.Capabilities.ReasoningLevels, []string{"none", "low", "medium", "high", "xhigh", "max"}) {
		t.Errorf("gpt-5.6-luna capabilities = %+v", luna.Capabilities)
	}

	// 2. Account list silent on limits and capabilities -> filled from catalogue (from: "catalogue").
	kimi, ok := byID["kimi-k3"]
	if !ok {
		t.Fatalf("kimi-k3 missing from ServedModels")
	}
	if kimi.ContextLength != 1048576 || kimi.MaxOutputTokens != 65536 {
		t.Errorf("kimi-k3 limits = (%d, %d), want (1048576, 65536)", kimi.ContextLength, kimi.MaxOutputTokens)
	}
	if kimi.From != protocol.ModelListFromCatalogue {
		t.Errorf("kimi-k3 from = %q, want %q", kimi.From, protocol.ModelListFromCatalogue)
	}
	if kimi.Capabilities == nil || !kimi.Capabilities.Tools || !kimi.Capabilities.Reasoning {
		t.Errorf("kimi-k3 capabilities = %+v", kimi.Capabilities)
	}

	// 3. Extension built-in list has smaller positive limits (200k/64k) than catalogue (1M/128k) ->
	// smaller limits win (from: "extension"), capabilities filled from catalogue.
	sonnet, ok := byID["claude-sonnet-4-6"]
	if !ok {
		t.Fatalf("claude-sonnet-4-6 missing from ServedModels")
	}
	if sonnet.ContextLength != 200000 || sonnet.MaxOutputTokens != 64000 {
		t.Errorf("claude-sonnet-4-6 limits = (%d, %d), want (200000, 64000)", sonnet.ContextLength, sonnet.MaxOutputTokens)
	}
	if sonnet.From != protocol.ModelListFromExtension {
		t.Errorf("claude-sonnet-4-6 from = %q, want %q", sonnet.From, protocol.ModelListFromExtension)
	}
	if sonnet.Capabilities == nil || !sonnet.Capabilities.PDF || !sonnet.Capabilities.Tools {
		t.Errorf("claude-sonnet-4-6 capabilities = %+v", sonnet.Capabilities)
	}

	// 4. Model only in extension's built-in list -> limits from extension (from: "extension").
	custom, ok := byID["custom-only"]
	if !ok {
		t.Fatalf("custom-only missing from ServedModels")
	}
	if custom.ContextLength != 64000 || custom.MaxOutputTokens != 8192 || custom.From != protocol.ModelListFromExtension {
		t.Errorf("custom-only = (ctx=%d, out=%d, from=%q), want (64000, 8192, extension)",
			custom.ContextLength, custom.MaxOutputTokens, custom.From)
	}
}

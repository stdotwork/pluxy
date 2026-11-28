package host

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

func tierRequest(source, policy string) llm.Request {
	return llm.Request{Model: "m", DropPolicy: policy, Native: &llm.Native{Format: source},
		Items:   []llm.Item{{Type: llm.ItemTypeMessage, Role: llm.RoleUser, Content: []llm.Part{{Type: llm.PartText, Text: "hi"}}}},
		Options: llm.Options{ServiceTier: llm.ServiceTierPriority, Verbosity: "low"}}
}

func TestServiceTierDowngrade(t *testing.T) {
	ctx := context.Background()
	acct := protocol.Account{ID: "a", Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "sk-1"}}
	for _, c := range []struct {
		minor      int
		source     string
		wantLosses []string
	}{
		{27, llm.FormatAnthropicMessages, []string{"metadata", "speed", "verbosity"}},
		{27, llm.FormatOpenAIResponses, []string{"metadata", "service_tier", "text.verbosity"}},
		{27, llm.FormatGeminiGenerate, []string{"metadata", "serviceTier", "verbosity"}},
		{28, llm.FormatAnthropicMessages, []string{"metadata"}},
	} {
		ext, err := Start(filepath.Join(fakePackage(t, fmt.Sprintf("service-tier-%d", c.minor)), ManifestFile), Options{Log: io.Discard})
		if err != nil {
			t.Fatal(err)
		}
		defer ext.Stop()
		sess, e := ext.OpenSession(ctx, acct, protocol.SessionInfo{Format: protocol.SessionOpenAIResponses})
		if e != nil {
			t.Fatalf("OpenSession: %+v", e)
		}
		defer sess.Close()
		first := func(name string, next func() llm.Event, close func()) llm.Event {
			defer close()
			ev := nextWithin(t, name, next)
			for e := ev; !e.Terminal(); e = nextWithin(t, name, next) {
			}
			return ev
		}
		for _, call := range []struct {
			name string
			next func(llm.Request, string) llm.Event
		}{
			{"generate", func(r llm.Request, name string) llm.Event {
				s := ext.Generate(ctx, acct, r)
				return first(name, s.Next, s.Close)
			}},
			{"session", func(r llm.Request, name string) llm.Event {
				s := sess.Turn(ctx, acct, r)
				return first(name, s.Next, s.Close)
			}},
		} {
			name := fmt.Sprintf("%s at minor %d from %s", call.name, c.minor, c.source)
			req := tierRequest(c.source, llm.DropReport)
			ev := call.next(req, name)
			if ev.Type != llm.EventResponseStart {
				t.Fatalf("%s: first event %+v, want response.start", name, ev)
			}
			var got struct{ Options llm.Options }
			if err := json.Unmarshal(ev.Response.Raw, &got); err != nil {
				t.Fatal(err)
			}
			wantOptions := llm.Options{}
			if c.minor >= 28 {
				wantOptions = llm.Options{ServiceTier: llm.ServiceTierPriority, Verbosity: "low"}
			}
			if !reflect.DeepEqual(got.Options, wantOptions) {
				t.Errorf("%s: extension got options %+v, want %+v", name, got.Options, wantOptions)
			}
			var paths []string
			for _, l := range ev.Response.Translation.Losses {
				paths = append(paths, l.Path)
			}
			if !reflect.DeepEqual(paths, c.wantLosses) {
				t.Errorf("%s: losses %q, want %q", name, paths, c.wantLosses)
			}
			if req.Options.ServiceTier != llm.ServiceTierPriority || req.Options.Verbosity != "low" {
				t.Errorf("%s: the caller's request was changed: %+v", name, req.Options)
			}
			if c.minor < 28 {
				ev := call.next(tierRequest(c.source, llm.DropReject), name)
				if ev.Type != llm.EventError || ev.Error.Code != llm.CodeUnsupported || !ev.Error.Retryable ||
					!reflect.DeepEqual(ev.Error.Dropped, c.wantLosses[1:]) {
					t.Errorf("%s, reject policy: %+v, want unsupported naming the dropped paths", name, ev)
				}
			}
		}
	}
}

func TestServiceTierDowngradeNeither(t *testing.T) {
	req := tierRequest(llm.FormatOpenAIChat, llm.DropReject)
	req.Options = llm.Options{}
	if got, lost := downgradeServiceTier(27, "x", req); len(lost) != 0 || !reflect.DeepEqual(got, req) {
		t.Errorf("a request with neither changed: %+v, %+v", got.Options, lost)
	}
}

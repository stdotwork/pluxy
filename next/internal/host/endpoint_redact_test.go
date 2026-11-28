//go:build unix

package host

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/internal/configdoc"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

const (
	splitKey     = "sk-splitkey-0123456789"
	splitSetting = "st-splitsetting-98765"
)

func splitBody() {
	err := sdk.Serve(sdk.Extension{
		ID:      "hosttest/split-body",
		Version: "0.0.0",
		Generate: func(context.Context, sdk.Call, *sdk.Stream) error {
			return &llm.Error{Code: llm.CodeUnsupported, Message: "endpoints only"}
		},
		Endpoint: func(ctx context.Context, call sdk.Call, out *sdk.Stream) error {
			key := call.Account.Credential.APIKey
			var s struct {
				APIKey string `json:"api_key"`
			}
			_ = json.Unmarshal(call.Account.Settings, &s)
			set := s.APIKey
			var pieces []string
			switch call.Request.Model {
			case "two":
				pieces = []string{"key " + key[:6], key[6:] + " setting " + set[:9], set[9:] + " done"}
			case "three":
				pieces = []string{"key " + key[:4], key[4:9], "", key[9:] + " done"}
			case "end":
				pieces = []string{"setting " + set[:3], set[3:]}
			case "prefix":
				pieces = []string{"cut ", key[:7]}
			case "error", "cancel":
				pieces = []string{"cut " + key[:7]}
			}
			if err := out.StartBody(call.Request.Native.Format, "text/event-stream"); err != nil {
				return err
			}
			for _, p := range pieces {
				if err := out.Body(p); err != nil {
					return err
				}
			}
			switch call.Request.Model {
			case "error":
				return &llm.Error{Code: llm.CodeUpstream, Message: "upstream broke"}
			case "cancel":
				<-ctx.Done()
				return ctx.Err()
			}
			return out.Finish(llm.Finish{Reason: llm.FinishStop}, nil)
		},
	})
	if err != nil {
		os.Exit(1)
	}
}

func readBody(t *testing.T, s *Stream) (string, llm.Event) {
	t.Helper()
	var body strings.Builder
	for {
		ev := s.Next()
		switch ev.Type {
		case llm.EventNative:
			var piece string
			if err := json.Unmarshal(ev.Raw, &piece); err != nil {
				t.Fatalf("a body piece is not a JSON string: %s", ev.Raw)
			}
			body.WriteString(piece)
		case llm.EventFinish, llm.EventError:
			return body.String(), ev
		default:
			t.Fatalf("unexpected event %+v", ev)
		}
	}
}

func startSplitBody(t *testing.T) *Extension {
	t.Helper()
	ext, err := Start(filepath.Join(fakePackage(t, "split-body"), ManifestFile), Options{Log: io.Discard, SettingsSecrets: configdoc.SettingsSecrets})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ext.Stop)
	return ext
}

var splitAccount = protocol.Account{ID: "split",
	Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: splitKey},
	Settings:   json.RawMessage(`{"base_url":"https://up.example/v1","api_key":"` + splitSetting + `"}`)}

func splitRequest(model string) llm.Request {
	return llm.Request{Model: model, Stream: true, Native: &llm.Native{Format: llm.FormatOpenAIResponsesCompact, Body: []byte(`{}`)}}
}

func TestEndpointBodySecretSplitAcrossPieces(t *testing.T) {
	ext := startSplitBody(t)
	for _, c := range []struct {
		model, want string
		end         string
	}{
		{"two", "key [redacted] setting [redacted] done", llm.EventFinish},
		{"three", "key [redacted] done", llm.EventFinish},
		{"end", "setting [redacted]", llm.EventFinish},
		{"prefix", "cut sk-spli", llm.EventFinish},
		{"error", "cut ", llm.EventError},
	} {
		t.Run(c.model, func(t *testing.T) {
			s := ext.Endpoint(context.Background(), splitAccount, splitRequest(c.model))
			defer s.Close()
			if ev := s.Next(); ev.Type != llm.EventResponseStart {
				t.Fatalf("first event = %+v; want response.start", ev)
			}
			body, last := readBody(t, s)
			if strings.Contains(body, splitKey) || strings.Contains(body, splitSetting) {
				t.Errorf("the client got a secret: %q", body)
			}
			if body != c.want || last.Type != c.end {
				t.Errorf("body %q ending with %s; want %q ending with %s", body, last.Type, c.want, c.end)
			}
		})
	}
}

func TestEndpointBodyHeldEndDroppedOnCancel(t *testing.T) {
	ext := startSplitBody(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := ext.Endpoint(ctx, splitAccount, splitRequest("cancel"))
	defer s.Close()
	if ev := s.Next(); ev.Type != llm.EventResponseStart {
		t.Fatalf("first event = %+v; want response.start", ev)
	}
	ev := s.Next()
	var first string
	if ev.Type != llm.EventNative || json.Unmarshal(ev.Raw, &first) != nil {
		t.Fatalf("second event = %+v; want a body piece", ev)
	}
	cancel()
	rest, last := readBody(t, s)
	if body := first + rest; body != "cut " || last.Type != llm.EventError || last.Error.Code != llm.CodeCancelled {
		t.Errorf("body %q ending with %+v; want %q ending with a cancelled error", body, last, "cut ")
	}
}

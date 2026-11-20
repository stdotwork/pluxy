package sdk

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/stdotwork/pluxy/next/format/openaichat"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

const nativeOnlyChatBody = `{"model":"m","messages":[{"role":"system","content":"s"},{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object"}}}],"user":"u"}`

// nativeOnlyRun sends one generate call and returns what the handler got (nil if it did not run)
// and the events of the reply.
func nativeOnlyRun(t *testing.T, call protocol.Call) (*Call, []llm.Event) {
	t.Helper()
	var got *Call
	handler := func(_ context.Context, c Call, out *Stream) error {
		got = &c
		if err := out.Start(llm.ResponseInfo{ID: "r"}); err != nil {
			return err
		}
		return out.Finish(llm.Finish{Reason: llm.FinishStop}, nil)
	}
	socket, stdin, _ := startServe(t, Extension{ID: "test/sdk", Version: "0.0.0", Generate: handler})
	defer stdin.Close()
	conn := dial(t, socket)
	defer conn.Close()
	if err := protocol.WriteFrame(conn, call); err != nil {
		t.Fatal(err)
	}
	var evs []llm.Event
	for {
		var r protocol.Reply
		if err := protocol.ReadFrame(conn, &r); err != nil {
			t.Fatalf("after %v: %v", evs, err)
		}
		evs = append(evs, r.Event)
		if r.Event.Terminal() {
			return got, evs
		}
	}
}

func nativeOnlyCall(body string, nativeOnly bool) protocol.Call {
	return protocol.Call{
		Type: protocol.TypeCall, ID: "c-1", Op: protocol.OpGenerate, NativeOnly: nativeOnly,
		Account: &protocol.Account{ID: "a"},
		Request: &llm.Request{
			Model: "renamed", Options: llm.Options{ThinkingSuffix: "high"}, Caller: "caller-1",
			Native: &llm.Native{Format: llm.FormatOpenAIChat, Body: json.RawMessage(body)},
		},
	}
}

func TestDescribeParsesNative(t *testing.T) {
	socket, stdin, _ := startServe(t, Extension{ID: "test/sdk", Version: "0.0.0", Generate: func(context.Context, Call, *Stream) error { return nil }})
	defer stdin.Close()
	conn := dial(t, socket)
	defer conn.Close()
	_ = protocol.WriteFrame(conn, protocol.Call{Type: protocol.TypeCall, ID: "c-1", Op: protocol.OpDescribe})
	var r protocol.Reply
	if err := protocol.ReadFrame(conn, &r); err != nil || r.Info == nil {
		t.Fatalf("describe: %+v, %v", r, err)
	}
	if !r.Info.ParsesNative || r.Info.ProtocolMinor < 24 { // minor 24 adds native-only calls
		t.Errorf("info = %+v, want parses_native and minor >= 24", r.Info)
	}
}

func TestNativeOnlyCallGivesTheParsedRequest(t *testing.T) {
	want, err := openaichat.ParseRequest([]byte(nativeOnlyChatBody))
	if err != nil {
		t.Fatal(err)
	}
	if len(want.Items) == 0 || len(want.Tools) == 0 || want.Origin == nil {
		t.Fatalf("test body parses to %+v", want)
	}
	got, evs := nativeOnlyRun(t, nativeOnlyCall(nativeOnlyChatBody, true))
	if got == nil {
		t.Fatalf("handler did not run: %+v", evs)
	}
	r := got.Request
	if !reflect.DeepEqual(r.Items, want.Items) || !reflect.DeepEqual(r.Tools, want.Tools) || !reflect.DeepEqual(r.Origin, want.Origin) {
		t.Errorf("handler got items/tools/origin %+v, want %+v", r, want)
	}
	if r.Model != "renamed" || r.Options.ThinkingSuffix != "high" || r.Caller != "caller-1" {
		t.Errorf("handler got model %q, options %+v, caller %q; want the frame's", r.Model, r.Options, r.Caller)
	}
}

func TestNativeOnlyCallWithBadBodyIsAnInternalError(t *testing.T) {
	got, evs := nativeOnlyRun(t, nativeOnlyCall(`{"messages":"not a list"}`, true))
	if got != nil {
		t.Errorf("handler ran for a body that does not parse")
	}
	last := evs[len(evs)-1]
	if last.Type != llm.EventError || last.Error == nil || last.Error.Code != llm.CodeInternal {
		t.Errorf("events = %+v, want an internal error", evs)
	}
}

func TestFullCallIsNotParsed(t *testing.T) {
	got, evs := nativeOnlyRun(t, nativeOnlyCall(nativeOnlyChatBody, false))
	if got == nil {
		t.Fatalf("handler did not run: %+v", evs)
	}
	if r := got.Request; len(r.Items) != 0 || len(r.Tools) != 0 || r.Origin != nil {
		t.Errorf("handler got %+v, want no items, tools or origin (a call that is not native-only is not parsed)", r)
	}
}

package main

import (
	"errors"
	"testing"
	"unicode/utf8"

	"github.com/stdotwork/pluxy/next/extensions/devin/devintest"
	"github.com/stdotwork/pluxy/next/llm"
)

// checkSequence fails the test when the events break the extension protocol's ordering rules.
func checkSequence(t *testing.T, rec *recorder) {
	t.Helper()
	var s llm.Sequence
	for i, ev := range rec.events {
		if err := s.Check(ev); err != nil {
			t.Fatalf("event %d (%s): %v\nevents: %s", i, ev.Type, err, rec.types())
		}
	}
}

// callArgs returns each tool call's ID, name and arguments, in item order.
func callArgs(rec *recorder) (ids, names, args []string) {
	at := map[int]int{}
	for _, ev := range rec.events {
		switch {
		case ev.Type == llm.EventItemStart && ev.Item.Kind == llm.ItemToolCall:
			at[ev.Index] = len(ids)
			ids, names, args = append(ids, ev.Item.CallID), append(names, ev.Item.Name), append(args, "")
		case ev.Type == llm.EventToolInputDelta:
			args[at[ev.Index]] += ev.Delta
		}
	}
	return ids, names, args
}

func joined(rec *recorder, typ string) string {
	var s string
	for _, ev := range rec.events {
		if ev.Type == typ {
			s += ev.Delta
		}
	}
	return s
}

// A call that continues after another call started stays one call: by its ID, or as the call last
// added to when the piece names none. A call whose name comes after its ID starts once both are
// known, with the arguments that came before.
func TestInterleavedToolCalls(t *testing.T) {
	rec, err := relayFrames(devintest.ToolCall("call_a", "weather", `{"ci`), devintest.ToolCall("call_b", "time", `{"tz"`),
		devintest.ToolCall("call_a", "", `ty":`), devintest.ToolCall("", "", `"Paris"}`), devintest.ToolCall("call_b", "", `:"UTC"}`),
		devintest.ToolCall("call_c", "", `{"x"`), devintest.ToolCall("call_c", "calc", `:1}`), devintest.End(""))
	if err != nil {
		t.Fatal(err)
	}
	checkSequence(t, rec)
	ids, names, args := callArgs(rec)
	want := []string{"call_a weather {\"city\":\"Paris\"}", "call_b time {\"tz\":\"UTC\"}", "call_c calc {\"x\":1}"}
	if len(ids) != len(want) {
		t.Fatalf("calls %v %v %v, want %v", ids, names, args, want)
	}
	for i := range want {
		if got := ids[i] + " " + names[i] + " " + args[i]; got != want[i] {
			t.Errorf("call %d: %s, want %s", i, got, want[i])
		}
	}
}

// A tool call that never gets its name cannot be passed on: the reply fails rather than
// breaking the event rules.
func TestNamelessToolCall(t *testing.T) {
	rec, err := relayFrames(devintest.Text("Hi"), devintest.ToolCall("call_a", "", `{}`), devintest.End(""))
	var e *llm.Error
	if !errors.As(err, &e) || e.Code != llm.CodeUpstream || e.Retryable {
		t.Fatalf("err %v, want a non-retryable upstream error", err)
	}
	checkSequence(t, rec)
}

// A tool call's arguments that Devin sends as invalid JSON (field 4) are passed on, not dropped.
func TestInvalidJSONArguments(t *testing.T) {
	c := devintest.Bytes(devintest.Bytes(nil, 1, []byte("call_a")), 2, []byte("apply_patch"))
	c = devintest.Bytes(c, 4, []byte("*** Begin Patch"))
	rec, err := relayFrames(devintest.Frame(devintest.Bytes(nil, 6, c)), devintest.End(""))
	if err != nil {
		t.Fatal(err)
	}
	checkSequence(t, rec)
	if _, _, args := callArgs(rec); len(args) != 1 || args[0] != "*** Begin Patch" {
		t.Errorf("arguments %q, want the field 4 text", args)
	}
}

// A character split across frames reaches the client whole, in text and in reasoning.
func TestSplitUTF8(t *testing.T) {
	ch := []byte("甲")
	rec, err := relayFrames(devintest.Frame(devintest.Bytes(nil, 9, ch[:1])), devintest.Frame(devintest.Bytes(nil, 9, ch[1:])),
		devintest.Frame(devintest.Bytes(nil, 3, ch[:2])), devintest.Frame(devintest.Bytes(nil, 3, ch[2:])), devintest.End(""))
	if err != nil {
		t.Fatal(err)
	}
	checkSequence(t, rec)
	for _, ev := range rec.events {
		if ev.Delta != "" && !utf8.ValidString(ev.Delta) {
			t.Errorf("%s delta %q is not valid UTF-8", ev.Type, ev.Delta)
		}
	}
	if r, x := joined(rec, llm.EventReasoningDelta), joined(rec, llm.EventTextDelta); r != "甲" || x != "甲" {
		t.Errorf("reasoning %q text %q, want 甲 each", r, x)
	}
}

// A signature that comes after the answer began still reaches the reasoning it signs, and a
// signature with no reasoning text gets a reasoning item of its own.
func TestLateSignature(t *testing.T) {
	sig := func(s string) []byte { return devintest.Frame(devintest.Bytes(nil, 10, []byte(s))) }
	opaque := func(rec *recorder) string {
		var s string
		for _, ev := range rec.events {
			if ev.Type == llm.EventItemEnd && ev.Opaque != nil {
				s += ev.Opaque.Data
			}
		}
		return s
	}
	rec, err := relayFrames(devintest.Thinking("Hmm"), devintest.Text("Sure."), sig("sealed.v1."), sig("abc"), devintest.End(""))
	if err != nil {
		t.Fatal(err)
	}
	checkSequence(t, rec)
	if got := opaque(rec); got != "sealed.v1.abc" {
		t.Errorf("late signature: opaque %q, want sealed.v1.abc", got)
	}
	if joined(rec, llm.EventTextDelta) != "Sure." {
		t.Errorf("text %q", joined(rec, llm.EventTextDelta))
	}
	rec, err = relayFrames(sig("sealed.v1.only"), devintest.Text("Hi"), devintest.End(""))
	if err != nil {
		t.Fatal(err)
	}
	checkSequence(t, rec)
	if got := opaque(rec); got != "sealed.v1.only" {
		t.Errorf("signature only: opaque %q, want sealed.v1.only", got)
	}
}

// Reasoning after the answer began is a new reasoning item, after the text.
func TestReasoningAfterText(t *testing.T) {
	rec, err := relayFrames(devintest.Thinking("a"), devintest.Text("b"), devintest.Thinking("c"), devintest.End(""))
	if err != nil {
		t.Fatal(err)
	}
	checkSequence(t, rec)
	var kinds []string
	for _, ev := range rec.events {
		if ev.Type == llm.EventItemStart {
			kinds = append(kinds, ev.Item.Kind)
		}
	}
	if len(kinds) != 3 || kinds[0] != llm.ItemReasoning || kinds[1] != llm.ItemMessage || kinds[2] != llm.ItemReasoning {
		t.Errorf("items %v, want reasoning, message, reasoning", kinds)
	}
}

// A usage message counts as reported even when its counts are zero; Devin's protobuf leaves zero
// counts out, so a count it leaves out is zero.
func TestUsageReported(t *testing.T) {
	rec, err := relayFrames(devintest.Text("Hi"), devintest.Frame(devintest.Bytes(nil, 7, nil)), devintest.End(""))
	if err != nil {
		t.Fatal(err)
	}
	u := rec.events[len(rec.events)-1].Usage
	if u == nil || u.InputTokens == nil || *u.InputTokens != 0 || u.OutputTokens == nil || *u.OutputTokens != 0 {
		t.Errorf("usage %+v, want zero input and output", u)
	}
	rec, err = relayFrames(devintest.Text("Hi"), devintest.End(""))
	if err != nil {
		t.Fatal(err)
	}
	if u := rec.events[len(rec.events)-1].Usage; u != nil {
		t.Errorf("usage %+v with no usage message, want none", u)
	}
}

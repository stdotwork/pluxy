package geminigenerate

import (
	"fmt"
	"strings"
	"testing"
)

func TestPendingCodesTakeOldestOfAnIDOrTheOnlyOne(t *testing.T) {
	var p pendingCodes
	p.add(pendingExecCode{id: "a", code: "1"})
	p.add(pendingExecCode{id: "b", code: "2"})
	p.add(pendingExecCode{id: "a", code: "3"})
	if pc, ok := p.takeByID("a"); !ok || pc.code != "1" {
		t.Fatalf("take a = %+v %v", pc, ok)
	}
	if _, ok := p.takeByID("zz"); ok {
		t.Fatal("took an ID nothing waits for")
	}
	if _, ok := p.takeOnly(); ok {
		t.Fatal("took the only one with two waiting")
	}
	p.takeByID("b")
	if pc, ok := p.takeOnly(); !ok || pc.code != "3" || p.len() != 0 {
		t.Fatalf("only = %+v %v, %d waiting", pc, ok, p.len())
	}
	p.add(pendingExecCode{code: "x"})
	if !p.dropNoID() || p.dropNoID() || p.len() != 0 {
		t.Fatal("dropNoID")
	}
}

func TestManyRunsInOrderAreAllKept(t *testing.T) {
	const n = 20000
	var b strings.Builder
	b.WriteString(`{"candidates":[{"content":{"parts":[`)
	for i := range n {
		fmt.Fprintf(&b, `{"executableCode":{"id":"%d","code":"x"}},`, i)
	}
	for i := range n {
		fmt.Fprintf(&b, `{"codeExecutionResult":{"id":"%d","outcome":"OUTCOME_OK"}},`, i)
	}
	b.WriteString(`{"text":"a"}]},"finishReason":"STOP"}]}`)
	evs, err := DecodeResponse([]byte(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	if got := evs[len(evs)-1].CodeExecutions; len(got) != n {
		t.Errorf("runs = %d, want %d", len(got), n)
	}
}

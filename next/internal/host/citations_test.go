package host

import (
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

func TestCitationsMinorGating(t *testing.T) {
	if CitationsMinor != 31 {
		t.Fatalf("CitationsMinor = %d, want 31", CitationsMinor)
	}
	if protocol.Minor < CitationsMinor {
		t.Fatalf("protocol.Minor = %d, want >= %d", protocol.Minor, CitationsMinor)
	}
	start, end := 0, 4
	itemEnd := llm.Event{
		Type:      llm.EventItemEnd,
		Index:     0,
		Citations: []llm.Citation{{URL: "https://example.com", Title: "Example", StartIndex: &start, EndIndex: &end}},
	}
	fin := llm.Event{
		Type:    llm.EventFinish,
		Finish:  &llm.Finish{Reason: llm.FinishStop},
		Sources: []llm.Source{{URL: "https://example.com", Title: "Example"}},
	}

	oldExt := &Extension{info: protocol.Info{ProtocolMinor: CitationsMinor - 1}}
	gotEnd := oldExt.normalizeEvent(itemEnd)
	if gotEnd.Citations != nil {
		t.Errorf("minor %d kept Citations: %+v, want nil", CitationsMinor-1, gotEnd.Citations)
	}
	gotFin := oldExt.normalizeEvent(fin)
	if gotFin.Sources != nil {
		t.Errorf("minor %d kept Sources: %+v, want nil", CitationsMinor-1, gotFin.Sources)
	}

	newExt := &Extension{info: protocol.Info{ProtocolMinor: CitationsMinor}}
	gotEnd = newExt.normalizeEvent(itemEnd)
	if len(gotEnd.Citations) != 1 || gotEnd.Citations[0].URL != "https://example.com" {
		t.Errorf("minor %d Citations = %+v, want preserved", CitationsMinor, gotEnd.Citations)
	}
	gotFin = newExt.normalizeEvent(fin)
	if len(gotFin.Sources) != 1 || gotFin.Sources[0].URL != "https://example.com" {
		t.Errorf("minor %d Sources = %+v, want preserved", CitationsMinor, gotFin.Sources)
	}
}

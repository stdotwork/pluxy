package main

import (
	"io"
	"strings"
	"testing"
	"time"
)

func TestPercentileAndSummarize(t *testing.T) {
	// Odd count, unsorted input.
	odd := []float64{50, 10, 40, 20, 30}
	s := Summarize(odd)
	if s.Median != 30 || s.Min != 10 || s.Max != 50 {
		t.Fatalf("Summarize(odd) = %+v, want median=30 min=10 max=50", s)
	}

	// Even count: median is average of the two middle values.
	even := []float64{40, 10, 30, 20}
	s = Summarize(even)
	if s.Median != 25 || s.Min != 10 || s.Max != 40 {
		t.Fatalf("Summarize(even) = %+v, want median=25 min=10 max=40", s)
	}

	// Nearest-rank percentiles over 1..100 (shuffled order).
	vals := make([]float64, 100)
	for i := range vals {
		vals[i] = float64(100 - i)
	}
	if p50 := Percentile(vals, 0.50); p50 != 50 {
		t.Fatalf("p50(1..100) = %v, want 50", p50)
	}
	if p99 := Percentile(vals, 0.99); p99 != 99 {
		t.Fatalf("p99(1..100) = %v, want 99", p99)
	}
	if p0 := Percentile(vals, 0); p0 != 1 {
		t.Fatalf("p0(1..100) = %v, want 1", p0)
	}
	if p100 := Percentile(vals, 1); p100 != 100 {
		t.Fatalf("p100(1..100) = %v, want 100", p100)
	}
	if empty := Summarize(nil); empty != (MetricSummary{}) {
		t.Fatalf("Summarize(nil) = %+v, want zero", empty)
	}
}

func TestScanMarkers(t *testing.T) {
	// Multiple markers in one payload, plus a broken bm: prefix before a valid marker.
	payload := `{"text":"bm:broken:notnum:123; prefix bm:0000000a:0:1700000000000000001; mid bm:0000000a:1:1700000000000000002;"}`
	got := ScanMarkers([]byte(payload))
	want := []Marker{
		{ReqID: "0000000a", Seq: 0, SendNanos: 1700000000000000001},
		{ReqID: "0000000a", Seq: 1, SendNanos: 1700000000000000002},
	}
	if len(got) != len(want) {
		t.Fatalf("ScanMarkers got %d markers (%+v), want %d (%+v)", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("marker[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// chunkedReader delivers fixed-size chunks per Read call so markers are split across reads.
type chunkedReader struct {
	data  string
	chunk int
	pos   int
}

func (r *chunkedReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n := min(r.chunk, len(p), len(r.data)-r.pos)
	copy(p, r.data[r.pos:r.pos+n])
	r.pos += n
	return n, nil
}

func TestReadSSESplitReadsAndMultipleMarkers(t *testing.T) {
	stream := strings.Join([]string{
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"bm:00000001:0:1111111111111111111;"}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"bm:00000001:1:2222222222222222222; bm:00000001:2:3333333333333333333;"}}`,
		``,
	}, "\n") + "\n"

	// 5-byte reads force markers and JSON lines to split across many Read() calls.
	r := &chunkedReader{data: stream, chunk: 5}
	var allMarkers []Marker
	var joined strings.Builder
	var events int

	err := ReadSSE(r, func(data []byte, arrivalNanos int64) {
		events++
		if arrivalNanos <= 0 {
			t.Errorf("arrivalNanos = %d, want > 0", arrivalNanos)
		}
		joined.WriteString(extractSSEText(data))
		allMarkers = append(allMarkers, ScanMarkers(data)...)
	})
	if err != nil {
		t.Fatalf("ReadSSE error: %v", err)
	}
	if events != 2 {
		t.Fatalf("ReadSSE saw %d events, want 2", events)
	}
	want := []Marker{
		{ReqID: "00000001", Seq: 0, SendNanos: 1111111111111111111},
		{ReqID: "00000001", Seq: 1, SendNanos: 2222222222222222222},
		{ReqID: "00000001", Seq: 2, SendNanos: 3333333333333333333},
	}
	if len(allMarkers) != len(want) {
		t.Fatalf("got %d markers (%+v), want %d (%+v)", len(allMarkers), allMarkers, len(want), want)
	}
	for i := range want {
		if allMarkers[i] != want[i] {
			t.Fatalf("marker[%d] = %+v, want %+v", i, allMarkers[i], want[i])
		}
	}
	wantText := "bm:00000001:0:1111111111111111111;bm:00000001:1:2222222222222222222; bm:00000001:2:3333333333333333333;"
	if joined.String() != wantText {
		t.Fatalf("joined text = %q, want %q", joined.String(), wantText)
	}
}

func TestPaddedDeltaMarkersAndFidelity(t *testing.T) {
	if len(deltaFiller) != 32 {
		t.Fatalf("len(deltaFiller) = %d, want 32", len(deltaFiller))
	}
	for i := 0; i < len(deltaFiller); i++ {
		c := deltaFiller[i]
		if c != ' ' && (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') {
			t.Fatalf("deltaFiller[%d] = %q, want ASCII letter or space", i, c)
		}
	}

	d0 := formatMarker("000000ab", 0, 1700000000000000100) + deltaFiller
	d1 := formatMarker("000000ab", 1, 1700000000000000200) + deltaFiller
	stream := strings.Join([]string{
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"` + d0 + `"}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"` + d1 + `"}}`,
		``,
	}, "\n") + "\n"

	var got []Marker
	var joined strings.Builder
	if err := ReadSSE(strings.NewReader(stream), func(data []byte, _ int64) {
		joined.WriteString(extractSSEText(data))
		got = append(got, ScanMarkers(data)...)
	}); err != nil {
		t.Fatalf("ReadSSE error: %v", err)
	}
	want := []Marker{
		{ReqID: "000000ab", Seq: 0, SendNanos: 1700000000000000100},
		{ReqID: "000000ab", Seq: 1, SendNanos: 1700000000000000200},
	}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("markers = %+v, want %+v", got, want)
	}
	if joined.String() != d0+d1 {
		t.Fatalf("joined = %q, want %q", joined.String(), d0+d1)
	}
}

func TestSelectProfileScenarios(t *testing.T) {
	specs := []ScenarioSpec{
		{Name: "agents"},
		{Name: "throughput"},
		{Name: "nonstream-pass", Latency: 250 * time.Millisecond},
	}

	got, err := selectProfileScenarios(specs, "")
	if err != nil {
		t.Fatalf("selectProfileScenarios default: %v", err)
	}
	if len(got) != 2 || got[0].Name != "agents" || got[1].Name != "throughput" {
		t.Fatalf("selectProfileScenarios default = %+v, want agents and throughput", got)
	}

	got, err = selectProfileScenarios(specs, "nonstream-pass")
	if err != nil {
		t.Fatalf("selectProfileScenarios nonstream-pass: %v", err)
	}
	if len(got) != 1 || got[0].Name != "nonstream-pass" || got[0].Latency != 250*time.Millisecond {
		t.Fatalf("selectProfileScenarios nonstream-pass = %+v, want the built scenario", got)
	}

	if _, err := selectProfileScenarios(specs, "missing"); err == nil {
		t.Fatal("selectProfileScenarios missing: want an unknown-scenario error")
	}
}

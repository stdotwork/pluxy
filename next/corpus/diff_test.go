package corpus

import (
	"encoding/json"
	"testing"
)

func TestCovers(t *testing.T) {
	for _, tc := range []struct {
		pattern, path string
		want          bool
	}{
		{"client.body", "client.body.error.message", true},
		{"client.body.error", "client.body", false},
		{"client.events[2]", "client.events[2].data.error", true},
		{"client.events[2]", "client.events[3].data", false},
		{"upstream[*].headers.User-Agent", "upstream[1].headers.User-Agent", true},
		{"upstream[*].headers.User-Agent", "upstream.headers.User-Agent", false},
		{"client.headers.retry-after", "client.headers.Retry-After", true}, // header names ignore case
		{"client.body.Error", "client.body.error", false},                  // other keys do not
		{"client.*.error", "client.body.error", true},
		{"client.*.error", "client[0].error", false},
	} {
		p, err := parsePath(tc.pattern)
		if err != nil {
			t.Fatalf("%s: %v", tc.pattern, err)
		}
		q, err := parsePath(tc.path)
		if err != nil {
			t.Fatalf("%s: %v", tc.path, err)
		}
		if got := covers(p, q); got != tc.want {
			t.Errorf("covers(%s, %s) = %v, want %v", tc.pattern, tc.path, got, tc.want)
		}
	}
}

func TestParsePathRejects(t *testing.T) {
	for _, p := range []string{"", "[0]", "a[", "a[x]", "a[0]b", "a..b", "a."} {
		if _, err := parsePath(p); err == nil {
			t.Errorf("parsePath(%q) succeeded", p)
		}
	}
}

func TestJudge(t *testing.T) {
	obs := func(s string) Observation {
		o, err := decodeObservation([]byte(s))
		if err != nil {
			t.Fatal(err)
		}
		return o
	}
	old := obs(`{"client":{"status":429,"headers":{"Retry-After":"20"},"body":{"error":{"type":"requests"}}}}`)
	new := obs(`{"client":{"status":429,"headers":{},"body":{"error":{"type":"rate_limit_error"}}}}`)

	v := Judge(old, new, nil, nil)
	if len(v.Uncovered) != 2 || v.OK() {
		t.Fatalf("no exceptions: uncovered %v", v.Uncovered)
	}
	ex := []Exception{
		{Path: "client.body.error", Kind: KindIntended, Reason: "r", Want: json.RawMessage(`{"type":"rate_limit_error"}`)},
		{Path: "client.headers.Retry-After", Kind: KindGap, Reason: "r"},
	}
	if v := Judge(old, new, ex, nil); !v.OK() {
		t.Fatalf("with exceptions: %+v", v)
	}
	ex[0].Want = json.RawMessage(`{"type":"requests"}`)
	if v := Judge(old, new, ex, nil); len(v.WantFailed) != 1 {
		t.Errorf("wrong want not reported: %+v", v)
	}
	stale := []Exception{{Path: "client.status", Kind: KindGap, Reason: "r"}}
	if v := Judge(old, old, stale, nil); len(v.Stale) != 1 {
		t.Errorf("stale exception not reported: %+v", v)
	}
	if v := Judge(old, old, nil, stale); len(v.Stale) != 0 {
		t.Errorf("global exception reported stale for one case: %+v", v)
	}
}

func TestFormatJSONKeepsOrderAndPutsEventsOnOneLine(t *testing.T) {
	in := `{"z":1,"a":{"sse":[{"data":{"id":"x","choices":[{"index":0,"delta":{"content":"a long enough piece of text to pass the line width limit of one hundred characters"}}]}}]}}`
	want := `{
  "z": 1,
  "a": {
    "sse": [
      {"data":{"id":"x","choices":[{"index":0,"delta":{"content":"a long enough piece of text to pass the line width limit of one hundred characters"}}]}}
    ]
  }
}
`
	got, err := FormatJSON([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

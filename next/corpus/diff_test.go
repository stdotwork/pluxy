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

func judgeObs(t *testing.T, s string) Observation {
	t.Helper()
	o, err := decodeObservation([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func TestExceptionDoesNotCoverWhatItWasNotWrittenFor(t *testing.T) {
	old := judgeObs(t, `{"client":{"status":200,"body":{"id":"a","choices":[{"i":0},{"i":1}]}}}`)

	// An exception on a parent excuses no difference below it.
	parent := []Exception{{Path: "client", Kind: KindGap, Reason: "r"}}
	arbitrary := judgeObs(t, `{"client":{"status":200,"body":"an arbitrary reply"}}`)
	if v := Judge(old, arbitrary, parent, nil); v.OK() {
		t.Errorf("an exception on client hid a replaced body")
	}
	choice := judgeObs(t, `{"client":{"status":200,"body":{"id":"a","choices":[{"i":0},"arbitrary"]}}}`)
	body := []Exception{{Path: "client.body", Kind: KindGap, Reason: "r"}}
	if v := Judge(old, choice, body, nil); v.OK() {
		t.Errorf("an exception on client.body hid a replaced choice")
	}

	// A plain value may differ at exactly the named path, whatever it becomes.
	leaf := []Exception{{Path: "client.status", Kind: KindGap, Reason: "r"}}
	if v := Judge(old, judgeObs(t, `{"client":{"status":404,"body":{"id":"a","choices":[{"i":0},{"i":1}]}}}`), leaf, nil); !v.OK() {
		t.Errorf("a plain value difference at the named path failed: %+v", v)
	}
	// ... but not turn into an object or array.
	if v := Judge(old, judgeObs(t, `{"client":{"status":{"x":1},"body":{"id":"a","choices":[{"i":0},{"i":1}]}}}`), leaf, nil); v.OK() {
		t.Errorf("an exception on a plain value excused an object")
	}
	// An object that the new core drops is a replaced subtree too: it needs absent.
	drop := []Exception{{Path: "client.body.choices[1]", Kind: KindGap, Reason: "r"}}
	dropped := judgeObs(t, `{"client":{"status":200,"body":{"id":"a","choices":[{"i":0}]}}}`)
	if v := Judge(old, dropped, drop, nil); v.OK() {
		t.Errorf("an unpinned exception excused a dropped object")
	}
	drop[0].Absent = true
	if v := Judge(old, dropped, drop, nil); !v.OK() {
		t.Errorf("an absent exception failed on a dropped object: %+v", v)
	}
	if v := Judge(old, choice, drop, nil); v.OK() {
		t.Errorf("an absent exception excused a value that is there")
	}
}

func TestPinnedExceptionCoversSubtreeAndFixesIt(t *testing.T) {
	old := judgeObs(t, `{"client":{"status":400,"body":{"error":{"code":"a","message":"m","param":"p"}}}}`)
	got := `{"client":{"status":400,"body":{"error":{"code":"b","message":"n"}}}}`
	ex := []Exception{{Path: "client.body.error", Kind: KindIntended, Reason: "r", Want: json.RawMessage(`{"code":"b","message":"n"}`)}}
	if v := Judge(old, judgeObs(t, got), ex, nil); !v.OK() {
		t.Fatalf("a pinned parent should excuse differences below it: %+v", v)
	}
	// The pin fixes the whole value, so another value below the path fails.
	other := `{"client":{"status":400,"body":{"error":{"code":"b","message":"arbitrary"}}}}`
	v := Judge(old, judgeObs(t, other), ex, nil)
	if v.OK() || len(v.WantFailed) != 1 || v.WantFailed[0].Path != "client.body.error" {
		t.Errorf("a changed value under a pinned path passed: %+v", v)
	}
	// Differences elsewhere are still not excused.
	elsewhere := `{"client":{"status":500,"body":{"error":{"code":"b","message":"n"}}}}`
	if v := Judge(old, judgeObs(t, elsewhere), ex, nil); v.OK() || len(v.Uncovered) != 1 {
		t.Errorf("a pinned exception excused a difference outside its path: %+v", v)
	}
}

func TestPinOfWildcardExceptionIsCheckedPerPath(t *testing.T) {
	old := judgeObs(t, `{"upstream":[{"body":{"stream_options":{"include_usage":true}}},{"body":{"stream_options":{"include_usage":true}}}]}`)
	ex := []Exception{{Path: "upstream[*].body.stream_options", Kind: KindGap, Reason: "r", Absent: true}}
	if v := Judge(old, judgeObs(t, `{"upstream":[{"body":{}},{"body":{}}]}`), nil, ex); !v.OK() {
		t.Fatalf("both dropped: %+v", v)
	}
	v := Judge(old, judgeObs(t, `{"upstream":[{"body":{}},{"body":{"stream_options":{"x":1}}}]}`), nil, ex)
	if v.OK() || len(v.WantFailed) != 1 || v.WantFailed[0].Path != "upstream[1].body.stream_options" {
		t.Errorf("the second request kept stream_options and should have failed: %+v", v)
	}
}

func TestGlobalPinOnlyAppliesWhereItCovers(t *testing.T) {
	same := judgeObs(t, `{"client":{"headers":{}}}`)
	ex := []Exception{{Path: "client.headers.X-Accel-Buffering", Kind: KindGap, Reason: "r", Want: json.RawMessage(`"no"`)}}
	if v := Judge(same, same, nil, ex); !v.OK() {
		t.Errorf("a global pin failed in a case it covers nothing in: %+v", v)
	}
}

func TestExceptionValidateRejectsWantAndAbsent(t *testing.T) {
	e := Exception{Path: "client.body", Kind: KindGap, Reason: "r", Want: json.RawMessage(`1`), Absent: true}
	if e.validate() == nil {
		t.Error("want and absent together were accepted")
	}
	e = Exception{Path: "client.body", Kind: KindGap, Reason: "r", Want: json.RawMessage(`{`)}
	if e.validate() == nil {
		t.Error("an unparsable want was accepted")
	}
}

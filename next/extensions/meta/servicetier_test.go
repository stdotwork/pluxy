package main

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/stdotwork/pluxy/next/format/openaichat"
)

func TestServiceTierAndVerbosityNotSent(t *testing.T) {
	req, err := openaichat.ParseRequest([]byte(`{"model":"muse-test","service_tier":"priority","verbosity":"low","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	body, tr, err := buildBody(req)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"service_tier", "verbosity", "text"} {
		if _, ok := m[f]; ok {
			t.Errorf("body has %s: %s", f, body)
		}
	}
	if d := tr.DroppedPaths(); !slices.Equal(d, []string{"service_tier", "verbosity"}) {
		t.Errorf("dropped %q, want service_tier and verbosity", d)
	}
}

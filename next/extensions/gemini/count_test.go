package main

import (
	"encoding/json"
	"reflect"
	"testing"
)

// A rebuilt request is counted whole: under generateContentRequest for the Gemini API, as its
// contents, system instruction and tools for Vertex AI.
func TestCountBody(t *testing.T) {
	body := []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"systemInstruction":{"parts":[{"text":"s"}]},"tools":[],"generationConfig":{"temperature":1}}`)
	for _, c := range []struct {
		vertex bool
		want   string
	}{
		{false, `{"generateContentRequest":{"model":"models/gemini-x","contents":[{"role":"user","parts":[{"text":"hi"}]}],"systemInstruction":{"parts":[{"text":"s"}]},"tools":[],"generationConfig":{"temperature":1}}}`},
		{true, `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"systemInstruction":{"parts":[{"text":"s"}]},"tools":[]}`},
	} {
		out, err := countBody(body, "gemini-x", c.vertex)
		if err != nil {
			t.Fatal(err)
		}
		var got, want any
		_ = json.Unmarshal(out, &got)
		_ = json.Unmarshal([]byte(c.want), &want)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("vertex %v: %s, want %s", c.vertex, out, c.want)
		}
	}
}

// Vertex AI gets a wrapped countTokens body's contents, system instruction and tools; a body of
// contents alone goes as it came.
func TestUnwrapCount(t *testing.T) {
	out, err := unwrapCount([]byte(`{"generateContentRequest":{"model":"models/gemini-x","contents":[{"role":"user","parts":[{"text":"hi"}]}],"systemInstruction":{"parts":[{"text":"s"}]},"generationConfig":{"temperature":1}}}`))
	if err != nil {
		t.Fatal(err)
	}
	var got, want any
	_ = json.Unmarshal(out, &got)
	_ = json.Unmarshal([]byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"systemInstruction":{"parts":[{"text":"s"}]}}`), &want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("unwrapCount = %s", out)
	}
	plain := `{"contents": [{"role":"user","parts":[{"text":"hi"}]}]}`
	if out, _ := unwrapCount([]byte(plain)); string(out) != plain {
		t.Errorf("unwrapCount changed a plain body: %s", out)
	}
}

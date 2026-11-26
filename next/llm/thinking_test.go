package llm

import "testing"

// Suffixes are read as today's core reads them (core/internal/thinking/suffix.go): the text in
// the last brackets at the end of the name, levels and none/auto in any case, -1 as auto, and
// whole numbers of 0 or more as budgets. Anything else is part of the model's name.
func TestSplitThinkingSuffix(t *testing.T) {
	for _, c := range []struct{ in, model, suffix string }{
		{"gpt-5(high)", "gpt-5", "high"},
		{"gpt-5(XHigh)", "gpt-5", "xhigh"},
		{"gpt-5(none)", "gpt-5", "none"},
		{"gpt-5(auto)", "gpt-5", "auto"},
		{"gpt-5(-1)", "gpt-5", "auto"},
		{"gpt-5(8192)", "gpt-5", "8192"},
		{"gpt-5(0)", "gpt-5", "0"},
		{"gpt-5(007)", "gpt-5", "7"},
		{"a(b)(low)", "a(b)", "low"},
		{"gpt-5", "gpt-5", ""},
		{"gpt-5(foo)", "gpt-5(foo)", ""},
		{"gpt-5()", "gpt-5()", ""},
		{"gpt-5(-2)", "gpt-5(-2)", ""},
		{"gpt-5( High )", "gpt-5", "high"},
		{"gpt-5(high", "gpt-5(high", ""},
		{"(high)", "(high)", ""},
	} {
		model, suffix := SplitThinkingSuffix(c.in)
		if model != c.model || suffix != c.suffix {
			t.Errorf("SplitThinkingSuffix(%q) = %q, %q; want %q, %q", c.in, model, suffix, c.model, c.suffix)
		}
	}
}

func TestThinkingEffort(t *testing.T) {
	for in, want := range map[string]string{
		"high": "high", "none": "none", "auto": "auto", "0": "none", "1": "minimal", "512": "minimal",
		"513": "low", "1024": "low", "8192": "medium", "8193": "high", "24576": "high", "24577": "xhigh",
	} {
		if got := ThinkingEffort(in); got != want {
			t.Errorf("ThinkingEffort(%q) = %q, want %q", in, got, want)
		}
	}
}

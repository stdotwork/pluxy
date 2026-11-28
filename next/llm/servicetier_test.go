package llm

import "testing"

func TestCanonicalServiceTier(t *testing.T) {
	for in, want := range map[string]string{
		"fast": "priority", "priority": "priority", "auto": "auto", "default": "default",
		"flex": "flex", "scale": "scale", "ultrafast": "ultrafast", "": "", "Fast": "Fast", "turbo": "turbo",
	} {
		if got := CanonicalServiceTier(in); got != want {
			t.Errorf("CanonicalServiceTier(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestServiceTierAndVerbosityPaths(t *testing.T) {
	for _, c := range []struct {
		name                  string
		req                   Request
		tierPath, verbosePath string
	}{
		{"chat", Request{Native: &Native{Format: FormatOpenAIChat}, Options: Options{ServiceTier: "priority"}}, "service_tier", "verbosity"},
		{"responses", Request{Native: &Native{Format: FormatOpenAIResponses}, Options: Options{ServiceTier: "priority"}}, "service_tier", "text.verbosity"},
		{"messages priority", Request{Native: &Native{Format: FormatAnthropicMessages}, Options: Options{ServiceTier: "priority"}}, "speed", "verbosity"},
		{"messages auto", Request{Native: &Native{Format: FormatAnthropicMessages}, Options: Options{ServiceTier: "auto"}}, "service_tier", "verbosity"},
		{"gemini", Request{Native: &Native{Format: FormatGeminiGenerate}, Options: Options{ServiceTier: "flex"}}, "serviceTier", "verbosity"},
		{"origin only", Request{Origin: &Origin{Format: FormatOpenAIResponses}}, "service_tier", "text.verbosity"},
		{"no source", Request{}, "service_tier", "verbosity"},
	} {
		if got := ServiceTierPath(c.req); got != c.tierPath {
			t.Errorf("%s: ServiceTierPath = %q, want %q", c.name, got, c.tierPath)
		}
		if got := VerbosityPath(c.req); got != c.verbosePath {
			t.Errorf("%s: VerbosityPath = %q, want %q", c.name, got, c.verbosePath)
		}
	}
}

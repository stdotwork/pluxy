package helps

import "testing"

func TestSensitiveWordMatcherFor(t *testing.T) {
	words := []string{"secret", "Proxy Key"}
	first := SensitiveWordMatcherFor(words)
	if first == nil {
		t.Fatal("matcher is nil for a valid word list")
	}
	if again := SensitiveWordMatcherFor([]string{"secret", "Proxy Key"}); again != first {
		t.Fatal("same word list returned a different matcher")
	}
	changed := SensitiveWordMatcherFor([]string{"secret", "Proxy Key", "token"})
	if changed == first {
		t.Fatal("changed word list returned the old matcher")
	}

	built := BuildSensitiveWordMatcher(words)
	for _, text := range []string{"my SECRET here", "a proxy key", "nothing to see", "tok"} {
		if first.Matches(text) != built.Matches(text) {
			t.Fatalf("Matches(%q) differs from BuildSensitiveWordMatcher", text)
		}
		if first.ObfuscateText(text) != built.ObfuscateText(text) {
			t.Fatalf("ObfuscateText(%q) differs from BuildSensitiveWordMatcher", text)
		}
	}
	if !first.Matches("my SECRET here") || first.Matches("nothing to see") {
		t.Fatal("matcher does not match the configured words case-insensitively")
	}
	if !changed.Matches("a token") {
		t.Fatal("changed matcher misses the added word")
	}

	if SensitiveWordMatcherFor(nil) != nil || SensitiveWordMatcherFor([]string{"x"}) != nil {
		t.Fatal("empty or unusable word lists must give a nil matcher, like BuildSensitiveWordMatcher")
	}
}

package host

import (
	"fmt"
	"slices"
	"testing"

	"github.com/stdotwork/pluxy/next/protocol"
)

func TestPiecesAddDedupOrder(t *testing.T) {
	tests := []struct {
		name string
		base pieces
		add  pieces
		want pieces
	}{
		{
			name: "duplicates_empty_and_overlapping",
			base: pieces{"fake-alpha-0001", "fake-beta-0002", ""},
			add:  pieces{"fake-beta-0002", "fake-gamma-0003", "", "fake-gamma-0003", "fake-alpha-0001", "fake-delta-0004"},
			want: pieces{"fake-alpha-0001", "fake-beta-0002", "", "fake-gamma-0003", "fake-delta-0004"},
		},
		{
			name: "empty_base_with_duplicates",
			base: nil,
			add:  pieces{"fake-one-0001", "fake-two-0002", "fake-one-0001", ""},
			want: pieces{"fake-one-0001", "fake-two-0002", ""},
		},
		{
			name: "empty_add",
			base: pieces{"fake-only-0001"},
			add:  nil,
			want: pieces{"fake-only-0001"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := slices.Clone(tc.base).add(tc.add)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("pieces.add result mismatch (got len %d, want len %d)", len(got), len(tc.want))
			}
		})
	}
}

func TestAccountSecretsWithDedupOrder(t *testing.T) {
	acc := protocol.Account{
		Credential: &protocol.Credential{
			APIKey:       "fake-cred-key-0001",
			AccessToken:  "fake-cred-tok-0002",
			RefreshToken: "fake-cred-key-0001",
		},
		Headers: map[string]string{
			"X-Api-Key": "fake-hdr-key-0003",
		},
		ProxyURL: "http://fakeproxyuser:fakeproxypass@proxy.example.invalid:8080",
	}
	settings := []string{
		"",
		"short",
		"fake-cred-key-0001",
		"fake-hdr-key-0003",
		"fake-set-val-0004",
		"fake-set-val-0004",
		"fake-set-val-0005",
		"fakeproxyuser",
		"https://example.invalid/cb?token=fake-url-tok-0006&code=fake-set-val-0005",
		"https://example.invalid/cb?token=fake-url-tok-0006",
	}

	got := protocol.AccountSecretsWith(acc, settings)
	want := []string{
		"fake-cred-key-0001",
		"fake-cred-tok-0002",
		"fake-hdr-key-0003",
		"fakeproxyuser",
		"fakeproxypass",
		"fake-set-val-0004",
		"fake-set-val-0005",
		"fake-url-tok-0006",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("AccountSecretsWith result mismatch (got len %d, want len %d)", len(got), len(want))
	}
}

func fakeSecretSlice(n int) []string {
	out := make([]string, n)
	for i := range n {
		out[i] = fmt.Sprintf("fake-secret-value-%08d", i)
	}
	return out
}

func BenchmarkAccountSecretsWith50k(b *testing.B) {
	settings := fakeSecretSlice(50_000)
	b.ResetTimer()
	for b.Loop() {
		got := protocol.AccountSecretsWith(protocol.Account{}, settings)
		if len(got) != 50_000 {
			b.Fatalf("unexpected count: %d", len(got))
		}
	}
}

func BenchmarkPiecesAdd50k(b *testing.B) {
	q := pieces(fakeSecretSlice(50_000))
	b.ResetTimer()
	for b.Loop() {
		var p pieces
		got := p.add(q)
		if len(got) != 50_000 {
			b.Fatalf("unexpected count: %d", len(got))
		}
	}
}

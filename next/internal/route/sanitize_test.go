package route

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

// The cases of today's core's TestExtractUpstreamErrorSummary_SanitizationAndTruncation and
// TestExtractUpstreamErrorSummary_AuthPackageDirectSanitization, on the summary that today's
// extraction hands the sanitizer ("code: message" for the JSON ones).
func TestSanitizeSummary(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		wantMask     string
		wantExact    string
		forbiddenRaw string
	}{
		{
			name:         "authorization header with bearer and topsecret redacted",
			input:        "authorization: Bearer abcdef+TOPSECRET==",
			wantMask:     "Authorization: [REDACTED]",
			forbiddenRaw: "TOPSECRET",
		},
		{
			name:         "authorization header with custom scheme redacted",
			input:        "authorization: ApiKey SUPERSECRET",
			wantMask:     "Authorization: [REDACTED]",
			forbiddenRaw: "SUPERSECRET",
		},
		{
			name:         "authorization header with comma separated parameters redacted",
			input:        "Authorization: ApiKey first,SECONDSECRET",
			wantMask:     "Authorization: [REDACTED]",
			forbiddenRaw: "SECONDSECRET",
		},
		{
			name:         "authorization header with digest parameters redacted",
			input:        `Authorization: Digest username="Mufasa", realm="myrealm", nonce="NONCE", uri="/dir/index.html", response="SIG"`,
			wantMask:     "Authorization: [REDACTED]",
			forbiddenRaw: "NONCE",
		},
		{
			name:         "double quoted multi word password redacted",
			input:        `password="correct horse battery staple"`,
			wantMask:     `[REDACTED]`,
			forbiddenRaw: "correct horse battery staple",
		},
		{
			name:         "double quoted comma containing api key redacted",
			input:        `api_key="secret,value"`,
			wantMask:     `[REDACTED]`,
			forbiddenRaw: "secret,value",
		},
		{
			name:         "bearer token redacted",
			input:        "upstream returned error: Bearer abcdef+TOPSECRET==",
			wantMask:     "Bearer [REDACTED]",
			forbiddenRaw: "TOPSECRET",
		},
		{
			name:         "sk key redacted",
			input:        "invalid key sk-live-secret-key-123456",
			wantMask:     "[REDACTED]",
			forbiddenRaw: "live-secret-key",
		},
		{
			name:         "user path redacted",
			input:        "open /Users/alice/configs/auth.json: permission denied",
			wantMask:     "[REDACTED_PATH]",
			forbiddenRaw: "alice",
		},
		{
			name:         "kv secret redacted",
			input:        "failed with api_key=secret-value-123 and token: my-secret-token",
			wantMask:     "[REDACTED]",
			forbiddenRaw: "secret-value-123",
		},
		{
			name:         "unstructured invalid api key redacted",
			input:        "invalid API key SUPERSECRET",
			wantMask:     "[REDACTED]",
			forbiddenRaw: "SUPERSECRET",
		},
		{
			name:         "unstructured invalid access token redacted",
			input:        "invalid access token SUPERSECRET",
			wantMask:     "[REDACTED]",
			forbiddenRaw: "SUPERSECRET",
		},
		{
			name:         "unstructured socks5 proxy auth redacted",
			input:        "proxyconnect tcp: socks5://alice:PASSSECRET@proxy.internal:1080",
			wantMask:     "[REDACTED_AUTH]",
			forbiddenRaw: "PASSSECRET",
		},
		{
			name:         "unstructured signature url query redacted",
			input:        "request failed: https://example.com?sig=SUPERSECRET",
			wantMask:     "[REDACTED]",
			forbiddenRaw: "SUPERSECRET",
		},
		{
			name:         "authorization header with basic redacted",
			input:        "authorization: Basic my-secret-basic-auth",
			wantMask:     "Authorization: [REDACTED]",
			forbiddenRaw: "my-secret-basic-auth",
		},
		{
			name:         "json message containing invalid token secret redacted",
			input:        `oops: invalid token SUPERSECRET`,
			wantMask:     "[REDACTED]",
			forbiddenRaw: "SUPERSECRET",
		},
		{
			name:         "single quoted key kv redacted",
			input:        `'api_key'=SUPERSECRET`,
			wantMask:     "[REDACTED]",
			forbiddenRaw: "SUPERSECRET",
		},
		{
			name:         "authorization equals bearer redacted",
			input:        `authorization=Bearer SUPERSECRET`,
			wantMask:     "[REDACTED]",
			forbiddenRaw: "SUPERSECRET",
		},
		{
			name:         "json message containing invalid token secret with escaped quotes redacted",
			input:        `oops: invalid token "SUPERSECRET"`,
			wantMask:     "[REDACTED]",
			forbiddenRaw: "SUPERSECRET",
		},
		{
			name:         "json message containing password with escaped quotes redacted",
			input:        `oops: password="abc\"SUPERSECRET"`,
			wantMask:     "[REDACTED]",
			forbiddenRaw: "SUPERSECRET",
		},
		{
			name:         "natural language api key redacted",
			input:        "upstream rejected API key: SUPERSECRET",
			wantMask:     "[REDACTED]",
			forbiddenRaw: "SUPERSECRET",
		},
		{
			name:         "natural language password is redacted",
			input:        "password is SUPERSECRET",
			wantMask:     "[REDACTED]",
			forbiddenRaw: "SUPERSECRET",
		},
		{
			name:         "mnt unix path redacted",
			input:        "open /mnt/secrets/alice: permission denied",
			wantMask:     "[REDACTED_PATH]",
			forbiddenRaw: "alice",
		},
		{
			name:         "windows path with spaces redacted",
			input:        `open C:\Users\Alice Smith\secret.txt: permission denied`,
			wantMask:     "[REDACTED_PATH]",
			forbiddenRaw: "Alice Smith",
		},
		{
			name:         "cookie header redacted",
			input:        "Cookie: sessionid=COOKIESECRET",
			wantMask:     "Cookie: [REDACTED]",
			forbiddenRaw: "COOKIESECRET",
		},
		{
			name:         "set-cookie header redacted",
			input:        "Set-Cookie: session=SETCOOKIESECRET",
			wantMask:     "Cookie: [REDACTED]",
			forbiddenRaw: "SETCOOKIESECRET",
		},
		{
			name:         "private key kv redacted",
			input:        "private_key=PRIVATEKEYSECRET",
			wantMask:     "[REDACTED]",
			forbiddenRaw: "PRIVATEKEYSECRET",
		},
		{
			name:         "uri userinfo redacted",
			input:        "https://user:PASSSECRET@example.com/api",
			wantMask:     "https://[REDACTED_AUTH]@",
			forbiddenRaw: "PASSSECRET",
		},
		{
			name:         "workspace unix path redacted",
			input:        "/workspace/tenants/alice/oauth-cache",
			wantMask:     "[REDACTED_PATH]",
			forbiddenRaw: "alice",
		},
		{
			name:         "opt unix path redacted",
			input:        `open /opt/cli-proxy/auth/alice.json: failed`,
			wantMask:     "[REDACTED_PATH]",
			forbiddenRaw: "alice",
		},
		{
			name:         "windows path redacted",
			input:        `open C:\Users\alice\secret.txt: failed`,
			wantMask:     "[REDACTED_PATH]",
			forbiddenRaw: "alice",
		},
		{
			name:         "incorrect api key provided redacted",
			input:        "Incorrect API key provided: SUPERSECRET",
			wantMask:     "[REDACTED]",
			forbiddenRaw: "SUPERSECRET",
		},
		{
			name:         "plural credentials redacted",
			input:        "credentials: SUPERSECRET",
			wantMask:     "[REDACTED]",
			forbiddenRaw: "SUPERSECRET",
		},
		{
			name:         "aws secret access key redacted",
			input:        "AWS_SECRET_ACCESS_KEY=SUPERSECRET",
			wantMask:     "[REDACTED]",
			forbiddenRaw: "SUPERSECRET",
		},
		{
			name:         "protocol relative uri userinfo redacted",
			input:        "//alice:PASSSECRET@example.com/api",
			wantMask:     "[REDACTED_AUTH]",
			forbiddenRaw: "PASSSECRET",
		},
		{
			name:         "run secrets unix path redacted",
			input:        "open /run/secrets/alice: permission denied",
			wantMask:     "[REDACTED_PATH]",
			forbiddenRaw: "alice",
		},
		{
			name:         "custom tenant unix path redacted",
			input:        "open /custom/tenant/alice: permission denied",
			wantMask:     "[REDACTED_PATH]",
			forbiddenRaw: "alice",
		},
		{
			name:         "windows unc path redacted",
			input:        `open \\server\share\alice\secret.txt: permission denied`,
			wantMask:     "[REDACTED_PATH]",
			forbiddenRaw: "alice",
		},
		{
			name:         "service key kv redacted",
			input:        "SERVICE_KEY=SUPERSECRET",
			wantMask:     "[REDACTED]",
			forbiddenRaw: "SUPERSECRET",
		},
		{
			name:         "openai key kv redacted",
			input:        "OPENAI_KEY=SUPERSECRET",
			wantMask:     "[REDACTED]",
			forbiddenRaw: "SUPERSECRET",
		},
		{
			name:         "x-key query param redacted",
			input:        "https://example.com?x-key=SUPERSECRET",
			wantMask:     "[REDACTED]",
			forbiddenRaw: "SUPERSECRET",
		},
		{
			name:         "unix path with spaces redacted",
			input:        "open /custom/tenant/Alice Smith/secret.txt: denied",
			wantMask:     "[REDACTED_PATH]",
			wantExact:    "open [REDACTED_PATH]: denied",
			forbiddenRaw: "Smith",
		},
		{
			name:         "unix path with unicode redacted",
			input:        "open /custom/租户/alice: denied",
			wantMask:     "[REDACTED_PATH]",
			forbiddenRaw: "alice",
		},
		{
			name:         "single segment unix path redacted",
			input:        "open /alice: permission denied",
			wantMask:     "[REDACTED_PATH]",
			forbiddenRaw: "alice",
		},
		{
			name:         "stat single segment unicode path redacted",
			input:        "stat /客户: no such file or directory",
			wantMask:     "[REDACTED_PATH]",
			forbiddenRaw: "客户",
		},
		{
			name:         "quoted path with colon and secret redacted",
			input:        `open "/tmp/customer:TOPSECRET/creds": denied`,
			wantMask:     "[REDACTED_PATH]",
			forbiddenRaw: "TOPSECRET",
		},
		{
			name:         "unquoted multi-word password redacted",
			input:        "password = correct horse battery staple",
			wantMask:     "[REDACTED]",
			forbiddenRaw: "battery staple",
		},
		{
			name:         "unquoted multi-word credentials redacted",
			input:        "credentials: alice secret",
			wantMask:     "[REDACTED]",
			forbiddenRaw: "alice secret",
		},
		{
			name:         "quoted path containing password assignment redacted",
			input:        `open "/Users/alice/password=foo/bar": denied`,
			wantMask:     "[REDACTED_PATH]",
			forbiddenRaw: "alice",
		},
		{
			name:         "unquoted path with colon and secret redacted",
			input:        "open /tmp/customer:TOPSECRET/creds: denied",
			wantMask:     "[REDACTED_PATH]",
			forbiddenRaw: "TOPSECRET",
		},
		{
			name:         "unquoted path with colon in leaf component redacted",
			input:        "open /tmp/customer:TOPSECRET: denied",
			wantMask:     "[REDACTED_PATH]",
			wantExact:    "open [REDACTED_PATH]: denied",
			forbiddenRaw: "TOPSECRET",
		},
		{
			name:         "multiple unquoted unix paths redacted",
			input:        "rename /Users/alice/source /Users/bob/private-data: denied",
			wantMask:     "[REDACTED_PATH]",
			wantExact:    "rename [REDACTED_PATH] [REDACTED_PATH]: denied",
			forbiddenRaw: "bob",
		},
		{
			name:         "non-terminal space path in multiple unix paths redacted",
			input:        "rename /Users/Alice Smith/source /Users/bob/private-data: denied",
			wantMask:     "[REDACTED_PATH]",
			wantExact:    "rename [REDACTED_PATH] [REDACTED_PATH]: denied",
			forbiddenRaw: "Alice Smith",
		},
		{
			name:         "leaf filename with space redacted",
			input:        "open /tmp/Alice Smith.txt: denied",
			wantMask:     "[REDACTED_PATH]",
			wantExact:    "open [REDACTED_PATH]: denied",
			forbiddenRaw: "Alice Smith",
		},
		{
			name:         "multiple paths with leaf filename spaces redacted",
			input:        "rename /tmp/Alice Smith /tmp/Bob Jones: denied",
			wantMask:     "[REDACTED_PATH]",
			wantExact:    "rename [REDACTED_PATH] [REDACTED_PATH]: denied",
			forbiddenRaw: "Alice Smith",
		},
		{
			name:         "unquoted path with colon space in leaf component redacted",
			input:        "open /tmp/customer: TOPSECRET: denied",
			wantMask:     "[REDACTED_PATH]",
			wantExact:    "open [REDACTED_PATH]: denied",
			forbiddenRaw: "TOPSECRET",
		},
		{
			name:         "parenthesized unquoted path redacted and parens preserved",
			input:        "open (/tmp/customer): denied",
			wantMask:     "[REDACTED_PATH]",
			wantExact:    "open ([REDACTED_PATH]): denied",
			forbiddenRaw: "customer",
		},
		{
			name:         "braced unquoted path redacted and braces preserved",
			input:        "open {/tmp/customer}: denied",
			wantMask:     "[REDACTED_PATH]",
			wantExact:    "open {[REDACTED_PATH]}: denied",
			forbiddenRaw: "customer",
		},
		{
			name:         "nested error text preserved after path",
			input:        "open /tmp/config: permission denied: retry later",
			wantMask:     "[REDACTED_PATH]",
			wantExact:    "open [REDACTED_PATH]: permission denied: retry later",
			forbiddenRaw: "config",
		},
		{
			name:         "nested known error text preserved after path",
			input:        "open /tmp/config: permission denied: access denied",
			wantMask:     "[REDACTED_PATH]",
			wantExact:    "open [REDACTED_PATH]: permission denied: access denied",
			forbiddenRaw: "config",
		},
		{
			name:         "uppercase connector between paths preserved",
			input:        "copy /tmp/a   TO\t/tmp/b: denied",
			wantMask:     "[REDACTED_PATH]",
			wantExact:    "copy [REDACTED_PATH]   TO\t[REDACTED_PATH]: denied",
			forbiddenRaw: "",
		},
		{
			name:         "long connector string bounded to 256 runes",
			input:        strings.Repeat("a", 300) + " to /tmp/x: denied",
			wantMask:     "...",
			forbiddenRaw: "x",
		},
		{
			name:     "long utf-8 text truncated to 256 runes",
			input:    strings.Repeat("你好世界🌟", 80),
			wantMask: "...",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeSummary(tc.input)
			if !strings.Contains(got, tc.wantMask) {
				t.Fatalf("sanitizeSummary(%q) = %q, want containing %q", tc.input, got, tc.wantMask)
			}
			if tc.wantExact != "" && got != tc.wantExact {
				t.Fatalf("sanitizeSummary(%q) = %q, want exact %q", tc.input, got, tc.wantExact)
			}
			if tc.forbiddenRaw != "" && strings.Contains(got, tc.forbiddenRaw) {
				t.Fatalf("sanitizeSummary(%q) = %q, leaked %q", tc.input, got, tc.forbiddenRaw)
			}
			if n := len([]rune(got)); n > 256 {
				t.Fatalf("sanitizeSummary(%q) is %d runes, want <= 256", tc.input, n)
			}
		})
	}
}

// errorSummary sanitizes what it extracts: a secret echoed in the upstream's error body, or in
// the error's own message, does not reach the usage-limit reply.
func TestErrorSummarySanitizes(t *testing.T) {
	for _, tc := range []struct {
		name string
		e    llm.Error
		want string
	}{
		{"secret in the body's message",
			llm.Error{Raw: json.RawMessage(`{"error":{"type":"usage_limit_reached","message":"token=sk-test-000000 rejected"}}`)},
			"usage_limit_reached: token: [REDACTED]"},
		{"secret after a code, as today's tests have it",
			llm.Error{Raw: json.RawMessage(`{"error":{"code":"oops","message":"invalid token SUPERSECRET"}}`)},
			"oops: invalid token: [REDACTED]"},
		{"path in the error's own message",
			llm.Error{ProviderCode: "io_error", Message: "open /Users/alice/configs/auth.json: permission denied"},
			"io_error: open [REDACTED_PATH]: permission denied"},
		{"nothing to remove",
			llm.Error{Raw: json.RawMessage(`{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached"}}`)},
			"usage_limit_reached: The usage limit has been reached"},
		{"long text is cut once, after sanitizing",
			llm.Error{Message: strings.Repeat("a", 300) + " to /tmp/x: denied"},
			strings.Repeat("a", 253) + "..."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := errorSummary(&tc.e)
			if got != tc.want {
				t.Errorf("errorSummary = %q, want %q", got, tc.want)
			}
			if strings.Contains(got, "sk-test-000000") || strings.Contains(got, "SUPERSECRET") || strings.Contains(got, "alice") {
				t.Errorf("errorSummary = %q, leaked a secret", got)
			}
		})
	}
}

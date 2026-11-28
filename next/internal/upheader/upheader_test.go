package upheader

import (
	"fmt"
	"maps"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
)

const value = "fake-header-value-91"

func TestCheck(t *testing.T) {
	for _, c := range []struct {
		h    map[string]string
		want string
	}{
		{map[string]string{"X-Gateway-Key": value, "User-Agent": "pluxy"}, ""},
		{nil, ""},
		{map[string]string{"host": value}, "header host cannot be set"},
		{map[string]string{"Transfer-Encoding": value}, "header Transfer-Encoding cannot be set"},
		{map[string]string{"X-A": value, "x-a": value}, "are the same header"},
		{map[string]string{"X A": value}, "not a valid header name"},
		{map[string]string{"": value}, "not a valid header name"},
		{map[string]string{"X-A": "  "}, "header X-A has no value"},
		{map[string]string{"X-A": value + "\r\nX-Injected: 1"}, "line break"},
		{map[string]string{"X-A": "a\tb"}, ""},
		{map[string]string{"X-Opencode-Session": "$x-opencode-session", "User-Agent": "$User-Agent"}, ""},
		{map[string]string{"X-A": "$"}, "it must be $ and a header name"},
		{map[string]string{"X-A": "$X B"}, "it must be $ and a header name"},
		{map[string]string{"X-A": "$ X-B"}, "it must be $ and a header name"},
		{map[string]string{"X-A": "$authorization"}, "cannot copy the client's Authorization header"},
		{map[string]string{"X-A": "$X-API-KEY"}, "cannot copy the client's X-Api-Key header"},
		{map[string]string{"X-A": "$x-goog-api-key"}, "cannot copy the client's X-Goog-Api-Key header"},
		{map[string]string{"X-A": "$Cookie"}, "cannot copy the client's Cookie header"},
		{map[string]string{"X-A": "$Proxy-Authorization"}, "cannot copy the client's Proxy-Authorization header"},
		{map[string]string{"X-A": "$CPA-SESSION-ID"}, "$CPA-SESSION-ID (a CLIProxyAPI session ID, which pluxy does not have) is not supported"},
		{map[string]string{"X-A": "id-$cpa-session-id"}, "$CPA-SESSION-ID (a CLIProxyAPI session ID, which pluxy does not have) is not supported"},
		{map[string]string{"X-Price": "5$"}, ""},
	} {
		err := Check("headers", c.h)
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%v: %v", c.h, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%v: error %v, want one with %q", c.h, err, c.want)
		case err != nil && strings.Contains(err.Error(), value):
			t.Errorf("%v: the error holds the value: %v", c.h, err)
		}
	}
}

func TestImport(t *testing.T) {
	got, left, err := Import(map[string]string{" X-Test ": " v ", "": "x", "X-Empty": " ", "X-Copy": " $ X-Client ",
		"X-Session": "id-$cpa-session-id", "X-Price": "5$", "X-Key": "$Authorization", "X-Nothing": "$", "X-Spaced": "$X B"})
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]string{"X-Test": "v", "X-Price": "5$", "X-Copy": "$X-Client"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Import = %v, want %v", got, want)
	}
	var names []string
	for _, l := range left {
		names = append(names, l.Name)
	}
	if want := []string{"X-Key", "X-Session"}; !reflect.DeepEqual(names, want) {
		t.Errorf("left out %v, want %v", left, want)
	}
	const wantSessionWhy = "its value holds $CPA-SESSION-ID, a CLIProxyAPI session ID that pluxy does not have"
	if len(left) == 2 && (!strings.Contains(left[0].Why, "client's Authorization header") || left[1].Why != wantSessionWhy) {
		t.Errorf("left out %v: want client's Authorization header and %q", left, wantSessionWhy)
	}
	if err := Check("headers", got); err != nil {
		t.Errorf("Check(Import) = %v", err)
	}
}

func TestFill(t *testing.T) {
	h := map[string]string{"X-Opencode-Session": "$x-opencode-session", "Session-Id": "$Session-Id", "X-Title": "pluxy",
		"User-Agent": "$User-Agent", "X-Bad": "$X-Bad", "X-Odd": "$session_id"}
	client := http.Header{
		"X-Opencode-Session": {"oc-1", "oc-2"},
		"Session-Id":         {"codex-1"},
		"User-Agent":         {""},
		"X-Bad":              {"a\nb"},
		"session_id":         {"odd-1"},
		"X-Unnamed":          {"never-copied"},
	}
	want := map[string]string{"X-Opencode-Session": "oc-1", "Session-Id": "codex-1", "X-Title": "pluxy", "X-Odd": "odd-1"}
	if got := Fill(h, client); !reflect.DeepEqual(got, want) {
		t.Errorf("Fill = %v, want %v", got, want)
	}
	if got := Fill(h, nil); !reflect.DeepEqual(got, map[string]string{"X-Title": "pluxy"}) {
		t.Errorf("Fill without a client = %v, want only the written value", got)
	}
	if got := Fill(map[string]string{"X-A": "$X-A"}, nil); got != nil {
		t.Errorf("Fill with nothing left = %v, want nil", got)
	}
	plain := map[string]string{"X-Title": "pluxy"}
	if got := Fill(plain, client); !reflect.DeepEqual(got, plain) || !Copies(h) || Copies(plain) {
		t.Errorf("Fill(plain) = %v; Copies(h) %v, Copies(plain) %v", got, Copies(h), Copies(plain))
	}
	if h["X-Opencode-Session"] != "$x-opencode-session" {
		t.Error("Fill changed the setting it was given")
	}
}

func TestSecrets(t *testing.T) {
	got := Secrets(map[string]string{"Authorization": "a", "X-Api-Key": "b", "X-Title": "c", "Cookie": "d", "X-Passphrase": "e"})
	if want := []string{"a", "d", "b", "e"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Secrets = %v, want %v (by name: Authorization, Cookie, X-Api-Key, X-Passphrase)", got, want)
	}
}

func TestImportRefusesSameHeaderTwice(t *testing.T) {
	for _, h := range []map[string]string{
		{"X-Api-Key": "fake-value-one", " X-Api-Key ": "fake-value-two"},
		{"X-Api-Key": "fake-value-one", "x-api-key": "fake-value-two"},
	} {
		for range 20 {
			out, _, err := Import(h)
			if err == nil {
				t.Fatalf("Import(%v) = %v, want an error", h, out)
			}
			for _, name := range slices.Collect(maps.Keys(h)) {
				if !strings.Contains(err.Error(), fmt.Sprintf("%q", name)) {
					t.Fatalf("error %q does not name header %q", err, name)
				}
			}
			if strings.Contains(err.Error(), "fake-value") {
				t.Fatalf("error shows a value: %q", err)
			}
		}
	}
	out, _, err := Import(map[string]string{"X-A": " ", "x-a": "v"})
	if want := map[string]string{"x-a": "v"}; err != nil || !reflect.DeepEqual(out, want) {
		t.Errorf("Import with one empty value = %v, %v; want %v", out, err, want)
	}
}

func TestForms(t *testing.T) {
	got := Forms(map[string]string{"x-api-key": "1", "Authorization": "Bearer abcdefgh", "X-Title": "plain-title", "Cookie": "sid=12345", "X-Token": "12345678"})
	for _, f := range got {
		if f == "1" || strings.Contains(f, "plain-title") {
			t.Errorf("Forms holds %q", f)
		}
	}
	for _, want := range []string{"Bearer abcdefgh", "sid=12345", "12345678", "Authorization: Bearer abcdefgh", `"cookie":"sid=12345"`} {
		if !slices.Contains(got, want) {
			t.Errorf("Forms %q lacks %q", got, want)
		}
	}
	if got[0] != "Authorization: Bearer abcdefgh" || got[len(got)-1] != `"X-API-KEY": "1"` {
		t.Errorf("Forms are not in name order: %q", got)
	}
	text := `model-1 failed: x-api-key=1, X-API-KEY=1, {"X-Api-Key": "1"}, {"x-api-key":"1"}, X-Api-Key: 1, X-Api-Key:1, retry 1`
	for _, f := range got {
		text = strings.ReplaceAll(text, f, "[redacted]")
	}
	if want := `model-1 failed: [redacted], [redacted], {[redacted]}, {[redacted]}, [redacted], [redacted], retry 1`; text != want {
		t.Errorf("redacted text %q, want %q", text, want)
	}
}

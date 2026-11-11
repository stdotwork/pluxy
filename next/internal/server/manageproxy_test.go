package server

import (
	"encoding/json"
	"testing"

	"github.com/stdotwork/pluxy/next/internal/route"
)

// A proxy_url sent back as GET /accounts shows it keeps its user name and password, with a
// password, with an empty one and with a user name alone.
func TestPatchProxyURLRoundTrip(t *testing.T) {
	for _, stored := range []string{
		"http://alice@proxy:8080",
		"http://alice:@proxy:8080",
		"http://alice:s3cret@proxy:8080",
		"socks5://alice:p%40ss@proxy:1080",
	} {
		t.Run(stored, func(t *testing.T) {
			account := route.Account{ID: "a", ProxyURL: stored}
			var view AccountView
			view.fields(account, nil)
			if view.ProxyURL == stored {
				t.Fatalf("the view shows the proxy with its user name: %q", view.ProxyURL)
			}
			raw, _ := json.Marshal(view.ProxyURL)
			if err := patchFields["proxy_url"](&account, raw); err != nil {
				t.Fatal(err)
			}
			if account.ProxyURL != stored {
				t.Errorf("PATCH of the shown value %s changed the stored proxy to %q, want %q", raw, account.ProxyURL, stored)
			}
		})
	}
}

// A proxy_url with empty user info ("http://@proxy:8080") holds no user name or password, so the
// view shows it as stored, and a PATCH of that value leaves the stored value as it was.
func TestPatchProxyURLWithoutSecretRoundTrip(t *testing.T) {
	const stored = "http://@proxy:8080"
	account := route.Account{ID: "a", ProxyURL: stored}
	var view AccountView
	view.fields(account, nil)
	if view.ProxyURL != stored {
		t.Fatalf("the view shows %q, want the stored %q", view.ProxyURL, stored)
	}
	raw, _ := json.Marshal(view.ProxyURL)
	if err := patchFields["proxy_url"](&account, raw); err != nil {
		t.Fatal(err)
	}
	if account.ProxyURL != stored {
		t.Errorf("GET showed %q, and PATCH of it changed the stored proxy to %q, want %q", view.ProxyURL, account.ProxyURL, stored)
	}
}

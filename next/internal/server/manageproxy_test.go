package server

import (
	"encoding/json"
	"testing"

	"github.com/stdotwork/pluxy/next/internal/route"
)

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
			setFields(&view, account, nil, route.Hold{}, nil)
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

func TestPatchProxyURLWithoutSecretRoundTrip(t *testing.T) {
	const stored = "http://@proxy:8080"
	account := route.Account{ID: "a", ProxyURL: stored}
	var view AccountView
	setFields(&view, account, nil, route.Hold{}, nil)
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

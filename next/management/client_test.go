package management

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A getter returns what the reply holds: the call fills the value before the getter reads it.
func TestGettersReturnTheDecodedReply(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case Prefix + "/status":
			w.Write([]byte(`{"version":"1.2.3","active":{"revision":7},"extensions":[{"id":"pluxy/claude","state":"running"}]}`))
		case Prefix + "/accounts":
			w.Write([]byte(`{"accounts":[{"id":"a1"},{"id":"a2"}]}`))
		case Prefix + "/client-keys":
			w.Write([]byte(`{"keys":[{"id":"k1","masked":"sk**1"}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c := &Client{URL: srv.URL}
	ctx := context.Background()

	st, err := c.Status(ctx)
	if err != nil || st.Version != "1.2.3" || st.Active.Revision != 7 || len(st.Extensions) != 1 || st.Extensions[0].State != "running" {
		t.Errorf("Status = %+v, %v; want the reply's version, revision and extension", st, err)
	}
	accounts, err := c.Accounts(ctx)
	if err != nil || len(accounts) != 2 || accounts[1].ID != "a2" {
		t.Errorf("Accounts = %+v, %v; want both accounts", accounts, err)
	}
	keys, err := c.ClientKeys(ctx)
	if err != nil || len(keys) != 1 || keys[0].Masked != "sk**1" {
		t.Errorf("ClientKeys = %+v, %v; want the reply's key", keys, err)
	}
}

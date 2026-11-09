package cred

import (
	"errors"
	"testing"

	"github.com/stdotwork/pluxy/next/protocol"
)

// A new login to an account must sign in to the same upstream account: every field of the stored
// identity must match (letter case aside); without a stored identity the labels are compared.
func TestSameIdentity(t *testing.T) {
	id := func(kv ...string) map[string]string {
		m := map[string]string{}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return m
	}
	for _, tc := range []struct {
		name  string
		cur   Record
		grant protocol.Grant
		ok    bool
	}{
		{"same", Record{Identity: id("email", "a@x.test", "account_id", "1")}, protocol.Grant{Identity: id("email", "A@X.test", "account_id", "1")}, true},
		{"extra field in the grant", Record{Identity: id("email", "a@x.test")}, protocol.Grant{Identity: id("email", "a@x.test", "org", "o")}, true},
		{"other account id", Record{Identity: id("email", "a@x.test", "account_id", "1")}, protocol.Grant{Identity: id("email", "a@x.test", "account_id", "2")}, false},
		{"field missing from the grant", Record{Identity: id("email", "a@x.test", "account_id", "1")}, protocol.Grant{Identity: id("email", "a@x.test")}, false},
		{"grant without identity", Record{Identity: id("email", "a@x.test")}, protocol.Grant{Label: "a@x.test"}, false},
		{"labels only, same", Record{Label: "a@x.test"}, protocol.Grant{Label: "A@x.test"}, true},
		{"labels only, other", Record{Label: "a@x.test"}, protocol.Grant{Label: "b@x.test"}, false},
		{"stored label only, grant without one", Record{Label: "a@x.test"}, protocol.Grant{}, true},
		{"nothing stored", Record{}, protocol.Grant{Identity: id("email", "b@x.test"), Label: "b@x.test"}, true},
	} {
		err := sameIdentity(tc.cur, &tc.grant)
		if (err == nil) != tc.ok || err != nil && !errors.Is(err, ErrIdentity) {
			t.Errorf("%s: got %v, want ok=%v", tc.name, err, tc.ok)
		}
	}
}

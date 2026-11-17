package corpus

import "testing"

// A case turns the management API on for the core when its client request, or any request after
// it, sends the management key. What the old core was sent (old_client) is only a record of the
// recording and does not count.
func TestUsesManagement(t *testing.T) {
	for name, tc := range map[string]struct {
		c    *Case
		want bool
	}{
		"no key":       {&Case{Client: ClientRequest{Path: "/v1/chat/completions"}}, false},
		"client":       {&Case{Client: ClientRequest{Path: "/a", Auth: AuthManagementKey}}, true},
		"then":         {&Case{Client: ClientRequest{Path: "/a"}, Then: []ClientRequest{{Path: "/b", Auth: AuthManagementKey}}}, true},
		"then, no key": {&Case{Client: ClientRequest{Path: "/a"}, Then: []ClientRequest{{Path: "/b"}}}, false},
		"old_client":   {&Case{Client: ClientRequest{Path: "/a"}, OldClient: &ClientRequest{Path: "/b", Auth: AuthManagementKey}}, false},
	} {
		if got := tc.c.UsesManagement(); got != tc.want {
			t.Errorf("%s: UsesManagement = %v, want %v", name, got, tc.want)
		}
	}
}

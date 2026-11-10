package corpus

import (
	"encoding/json"
	"strings"
	"testing"
)

// A request's body_text may name the fake upstream and the run's RSA key: the key comes out as the
// inside of a JSON string, so a key file written around it is valid JSON that holds the key.
func TestExpandBodyText(t *testing.T) {
	text := `{"private_key":"{{private_key}}","token_uri":"{{upstream}}/token"}`
	out := expandBodyText(ClientRequest{Path: "/x", BodyText: &text}, "http://127.0.0.1:1")
	if text != `{"private_key":"{{private_key}}","token_uri":"{{upstream}}/token"}` {
		t.Fatal("expanding changed the case's own request")
	}
	var key struct {
		PrivateKey string `json:"private_key"`
		TokenURI   string `json:"token_uri"`
	}
	if err := json.Unmarshal([]byte(*out.BodyText), &key); err != nil {
		t.Fatalf("expanded body is not JSON: %v\n%s", err, *out.BodyText)
	}
	if key.PrivateKey != corpusRSA() || !strings.HasPrefix(key.PrivateKey, "-----BEGIN PRIVATE KEY-----\n") || key.TokenURI != "http://127.0.0.1:1/token" {
		t.Fatalf("expanded key file = %+v", key)
	}
	if got := expandBodyText(ClientRequest{Path: "/x"}, "u"); got.BodyText != nil {
		t.Fatal("a request without body_text gained one")
	}
}

package configdoc

import (
	"errors"
	"testing"
)

// A chain is written under routing.chains, keeping the keys around it in their order, and its
// removal takes routing.chains with the last chain.
func TestSetAndRemoveChain(t *testing.T) {
	d, err := Parse([]byte("{\n  \"listen\": \"127.0.0.1:0\",\n  \"routing\": {\"strategy\": \"fill-first\"},\n  \"client_keys\": [\"k\"]\n}\n"), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetChain("opus", []byte(`[{"model":"opus","provider":"pluxy/claude"},{"model":"sol"}]`)); err != nil {
		t.Fatal(err)
	}
	b, _ := d.Bytes()
	want := "{\n  \"listen\": \"127.0.0.1:0\",\n  \"routing\": {\n    \"strategy\": \"fill-first\",\n    \"chains\": {\n      \"opus\": [\n        {\n          \"model\": \"opus\",\n          \"provider\": \"pluxy/claude\"\n        },\n        {\n          \"model\": \"sol\"\n        }\n      ]\n    }\n  },\n  \"client_keys\": [\n    \"k\"\n  ]\n}\n"
	if string(b) != want {
		t.Errorf("after SetChain:\n%s\nwant\n%s", b, want)
	}
	if d.RemoveChain("nothing") {
		t.Error("RemoveChain of a chain that is not there reported one")
	}
	if !d.RemoveChain("opus") {
		t.Fatal("RemoveChain found no chain")
	}
	b, _ = d.Bytes()
	if want := "{\n  \"listen\": \"127.0.0.1:0\",\n  \"routing\": {\n    \"strategy\": \"fill-first\"\n  },\n  \"client_keys\": [\n    \"k\"\n  ]\n}\n"; string(b) != want {
		t.Errorf("after RemoveChain:\n%s\nwant\n%s", b, want)
	}
	y, err := Parse([]byte("routing:\n  strategy: fill-first\n"), true)
	if err != nil {
		t.Fatal(err)
	}
	if err := y.SetChain("opus", []byte(`[{"model":"opus"}]`)); !errors.Is(err, ErrInvalid) {
		t.Errorf("SetChain in a config.yaml: %v, want ErrInvalid", err)
	}
}

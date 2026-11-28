package configdoc

import (
	"errors"
	"maps"
	"testing"
)

func TestSetClientKeyLabel(t *testing.T) {
	const shared = "shared: &s\n  k1: One\n"
	tests := []struct {
		name, doc, id, label string
		want                 map[string]string
	}{
		{"set beside others", "client_key_labels:\n  k1: One\n", "k2", "Two", map[string]string{"k1": "One", "k2": "Two"}},
		{"change its own", "client_key_labels:\n  k1: One\n", "k1", "Uno", map[string]string{"k1": "Uno"}},
		{"clear its own", "client_key_labels:\n  k1: One\n  k2: Two\n", "k1", "", map[string]string{"k2": "Two"}},
		{"clear one that has none", "client_key_labels:\n  k1: One\n", "k2", "", map[string]string{"k1": "One"}},
		{"set with no labels yet", "listen: x\n", "k1", "One", map[string]string{"k1": "One"}},
		{"merge: another key's label", shared + "client_key_labels:\n  <<: *s\n", "k2", "Two", map[string]string{"k1": "One", "k2": "Two"}},
		{"merge: set the merged key's", shared + "client_key_labels:\n  <<: *s\n", "k1", "Uno", nil},
		{"merge: clear the merged key's", shared + "client_key_labels:\n  <<: *s\n", "k1", "", nil},
		{"merge: clear what hides a merged label", shared + "client_key_labels:\n  <<: *s\n  k1: Mine\n", "k1", "", nil},
		{"merge: change what hides a merged label", shared + "client_key_labels:\n  <<: *s\n  k1: Mine\n", "k1", "Yours", map[string]string{"k1": "Yours"}},
		{"alias for the labels: set", shared + "client_key_labels: *s\n", "k1", "Uno", nil},
		{"alias for the labels: set another", shared + "client_key_labels: *s\n", "k2", "Two", nil},
		{"alias for the labels: clear", shared + "client_key_labels: *s\n", "k1", "", nil},
		{"alias for a label: set", "name: &n One\nclient_key_labels:\n  k1: *n\n", "k1", "Uno", nil},
		{"alias for a label: clear", "name: &n One\nclient_key_labels:\n  k1: *n\n", "k1", "", nil},
		{"anchor of a shared label: set", "client_key_labels:\n  k1: &n One\n  k2: *n\n", "k1", "Uno", nil},
		{"anchor of a shared label: clear", "client_key_labels:\n  k1: &n One\n  k2: *n\n", "k1", "", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d, err := Parse([]byte(tc.doc), true)
			if err != nil {
				t.Fatal(err)
			}
			err = d.SetClientKeyLabel(tc.id, tc.label)
			if tc.want == nil {
				if !errors.Is(err, ErrLabelIndirect) {
					t.Fatalf("SetClientKeyLabel(%q, %q) = %v, want ErrLabelIndirect", tc.id, tc.label, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			b, err := d.Bytes()
			if err != nil {
				t.Fatal(err)
			}
			again, err := Parse(b, true)
			if err != nil {
				t.Fatalf("the written file does not parse: %v\n%s", err, b)
			}
			got, err := again.labelsInEffect()
			if err != nil || !maps.Equal(got, tc.want) {
				t.Errorf("labels in effect after the edit = %q (%v), want %q\n%s", got, err, tc.want, b)
			}
		})
	}
}

func TestSetClientKeyLabelJSON(t *testing.T) {
	d, err := Parse([]byte(`{"client_key_labels": {"k1": "One"}}`), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetClientKeyLabel("k1", ""); err != nil {
		t.Fatal(err)
	}
	if b, _ := d.Bytes(); string(b) != "{}\n" && string(b) != "{}" {
		t.Errorf("after clearing the last label: %s", b)
	}
}

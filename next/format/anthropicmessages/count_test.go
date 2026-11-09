package anthropicmessages

import "testing"

// A rebuilt Messages request keeps only the fields count_tokens takes.
func TestCountRequest(t *testing.T) {
	out, err := CountRequest([]byte(`{"model":"m","max_tokens":10,"stream":true,"temperature":1,"system":"s","messages":[],"tools":[],"tool_choice":{"type":"auto"},"thinking":{"type":"adaptive"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !jsonEqual(out, []byte(`{"model":"m","system":"s","messages":[],"tools":[],"tool_choice":{"type":"auto"},"thinking":{"type":"adaptive"}}`)) {
		t.Errorf("CountRequest = %s", out)
	}
	if n, err := DecodeCount([]byte(`{"input_tokens":42}`)); n != 42 || err != nil {
		t.Errorf("DecodeCount = %d, %v", n, err)
	}
	if _, err := DecodeCount([]byte(`{"tokens":42}`)); err == nil {
		t.Error("DecodeCount read a reply without input_tokens")
	}
}

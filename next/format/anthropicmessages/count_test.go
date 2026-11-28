package anthropicmessages

import "testing"

func TestCountRequest(t *testing.T) {
	out, err := CountRequest([]byte(`{"model":"m","max_tokens":10,"stream":true,"temperature":1,"system":"s","messages":[],"tools":[],"tool_choice":{"type":"auto"},"thinking":{"type":"adaptive"},"output_config":{"effort":"high"},"cache_control":{"type":"ephemeral"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !jsonEqual(out, []byte(`{"model":"m","system":"s","messages":[],"tools":[],"tool_choice":{"type":"auto"},"thinking":{"type":"adaptive"},"output_config":{"effort":"high"},"cache_control":{"type":"ephemeral"}}`)) {
		t.Errorf("CountRequest = %s", out)
	}
	if n, err := DecodeCount([]byte(`{"input_tokens":42}`)); n != 42 || err != nil {
		t.Errorf("DecodeCount = %d, %v", n, err)
	}
	if _, err := DecodeCount([]byte(`{"tokens":42}`)); err == nil {
		t.Error("DecodeCount read a reply without input_tokens")
	}
}

func TestCountBody(t *testing.T) {
	out, err := CountBody([]byte(`{"model":"m","max_tokens":10,"stream":false,"temperature":1,"metadata":{"user_id":"u"},"messages":[],"output_config":{"effort":"high"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !jsonEqual(out, []byte(`{"model":"m","messages":[],"output_config":{"effort":"high"}}`)) {
		t.Errorf("CountBody = %s", out)
	}
	in := `{"model":"m",  "messages":[]}`
	if out, _ := CountBody([]byte(in)); string(out) != in {
		t.Errorf("CountBody changed a body without generation fields: %s", out)
	}
}

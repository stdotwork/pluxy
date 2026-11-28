package geminigenerate

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

func TestErrorCarriesPluxyMember(t *testing.T) {
	e := &llm.Error{Code: llm.CodeRateLimit, Status: 429, Message: "slow down",
		Explain: &llm.Explain{Trace: "t-1",
			Tried:   []llm.Tried{{Account: "acc_1", Provider: "pluxy/x", Model: "m", Result: "quota"}},
			LeftOut: []llm.LeftOut{{Account: "acc_2", State: "banned"}}}}
	_, body := ErrorResponse(e)
	var got struct {
		Error map[string]json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(body, &got); err != nil || got.Error["pluxy"] == nil {
		t.Fatalf("no error.pluxy in %s (%v)", body, err)
	}
	var have, want any
	_ = json.Unmarshal(got.Error["pluxy"], &have)
	_ = json.Unmarshal([]byte(`{"trace":"t-1","tried":[{"account":"acc_1","provider":"pluxy/x","model":"m","result":"quota"}],"left_out":[{"account":"acc_2","state":"banned"}]}`), &want)
	if !reflect.DeepEqual(have, want) {
		t.Errorf("pluxy = %s", got.Error["pluxy"])
	}
	e.Explain = nil
	_, body = ErrorResponse(e)
	var plain struct {
		Error map[string]json.RawMessage `json:"error"`
	}
	if json.Unmarshal(body, &plain) != nil || plain.Error == nil || plain.Error["pluxy"] != nil {
		t.Errorf("an error with no explanation has a pluxy member: %s", body)
	}
}

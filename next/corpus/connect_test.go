package corpus

import (
	"encoding/json"
	"reflect"
	"testing"
)

// A message written from its JSON form decodes back to the same form with the schema that names
// its nested and repeated fields; text, varints, doubles and bytes keep their kinds.
func TestConnectRoundTrip(t *testing.T) {
	in := `{"1":{"1":"chisel","3":"token"},"3":[{"2":1,"3":"Hi"},{"2":2,"3":"Hello","12":{"bytes":"/w=="}}],"8":{"2":64000,"5":{"double":0.25}},"21":"swe-2-high"}`
	frames, err := encodeConnect([]ConnectFrame{{Message: json.RawMessage(in)}, {End: json.RawMessage(`{}`)}})
	if err != nil {
		t.Fatal(err)
	}
	// The data frame is followed by the end frame: flags 2, length 2, "{}".
	end := frames[len(frames)-7:]
	if end[0] != 2 || end[4] != 2 || string(end[5:]) != "{}" {
		t.Fatalf("end frame %q", end)
	}
	schema := &ProtoSchema{Messages: []string{"1", "3", "8"}, Repeated: []string{"3"}}
	got, ok := decodeConnectRequest(frames[:len(frames)-7], true, schema)
	if !ok {
		t.Fatal("the data frame did not decode")
	}
	var want any
	_ = json.Unmarshal([]byte(in), &want)
	g, _ := json.Marshal(got)
	var gotAny any
	_ = json.Unmarshal(g, &gotAny)
	if !reflect.DeepEqual(gotAny, want) {
		t.Errorf("decoded\n %s\nwant\n %s", g, in)
	}
}

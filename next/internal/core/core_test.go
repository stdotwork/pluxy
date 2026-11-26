package core

import "testing"

func TestSameJSON(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{`{"a":1,"b":[true,null]}`, `{ "b": [true, null], "a": 1 }`, true},
		{`{"n":9007199254740992}`, `{"n":9007199254740993}`, false}, // equal as float64
		{`{"a":1}`, `{"a":2}`, false},
		{``, ``, true},
		{``, `{}`, false},
	} {
		if got := sameJSON([]byte(tc.a), []byte(tc.b)); got != tc.want {
			t.Errorf("sameJSON(%s, %s) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

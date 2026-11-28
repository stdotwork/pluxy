package jsonscan

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

type (
	testSource struct {
		Type      string "json:\"type\""
		MediaType string "json:\"media_type\""
		Data      string "json:\"data\""
	}
	testBlock struct {
		Type     string          "json:\"type\""
		Text     string          "json:\"text\""
		Input    json.RawMessage "json:\"input\""
		Content  json.RawMessage "json:\"content\""
		Source   *testSource     "json:\"source\""
		Disabled *bool           "json:\"disable_parallel_tool_use\""
	}
	testRequest struct {
		Model         string            "json:\"model\""
		Messages      []json.RawMessage "json:\"messages\""
		System        json.RawMessage   "json:\"system\""
		MaxTokens     *int              "json:\"max_tokens\""
		Stream        bool              "json:\"stream\""
		Temperature   *float64          "json:\"temperature\""
		StopSequences []string          "json:\"stop_sequences\""
		Blocks        []testBlock       "json:\"blocks\""
		Count         int32             "json:\"count\""
		Ratio         float32           "json:\"ratio\""
		Untagged      string
		//lint:ignore U1000 an unexported field the decoder must skip
		skipped string
		Ignored string "json:\"-\""
	}
	testEmbedded struct {
		testSource
		Extra string "json:\"extra\""
	}
	testStringOpt struct {
		N int "json:\"n,string\""
	}
	testMap struct {
		M map[string]string "json:\"m\""
	}
	testName    string
	testNumbers struct {
		N    json.Number            "json:\"n\""
		Name testName               "json:\"name\""
		Ns   []json.Number          "json:\"ns\""
		M    map[string]json.Number "json:\"m\""
	}
	testFrame struct {
		Type    string                     "json:\"type\""
		Headers map[string]string          "json:\"headers\""
		Raws    map[string]json.RawMessage "json:\"raws\""
		Any     any                        "json:\"any\""
		When    time.Time                  "json:\"when\""
		Embed   *testEmbedded              "json:\"embed\""
		Opt     testStringOpt              "json:\"opt\""
		Ints    map[int]string             "json:\"ints\""
		Req     *testRequest               "json:\"req\""
		Nums    []float64                  "json:\"nums\""
	}
)

func targets() []any {
	return []any{
		new(testRequest), new(testBlock), new(testSource), new(string), new(bool), new(int), new(*int),
		new(float64), new([]string), new([]json.RawMessage), new(json.RawMessage), new([]testBlock),
		new(testEmbedded), new(testStringOpt), new(testMap), new(map[string]json.RawMessage), new(any),
		new(testFrame), new(map[string]string), new(map[string]*testSource), new([]map[string]any),
		new(json.Number), new(*json.Number), new(testName), new(testNumbers), new([]json.Number),
	}
}

var seeds = []string{
	"", "null", "true", "false", "0", "-0", "5", "1.5", "1e3", "-12", "99999999999999999999", "1e400", "3.4e39",
	"\"s\"", "\"\\u00e9\\n\"", "\"\xff\"", "\"\\ud83d\\ude00\"", "[]", "[\"a\",null,\"b\"]", "[1]", "{}", "[{}]",
	"{\"model\":\"m\",\"max_tokens\":5,\"stream\":true,\"temperature\":0.5,\"stop_sequences\":[\"x\"],\"messages\":[{\"role\":\"user\"}, \"s\" , 5 ],\"system\":\"sys\"}",
	"{\"model\":\"a\",\"model\":\"b\",\"model\":null}", "{\"Model\":\"a\",\"MODEL\":\"b\"}", "{\"mo\\u0064el\":\"x\"}", "{\"\\u006dodel\":\"y\",\"model\":\"z\"}",
	"{\"max_tokens\":\"x\"}", "{\"max_tokens\":1.5}", "{\"max_tokens\":null}", "{\"max_tokens\":5,\"max_tokens\":null}", "{\"stream\":\"yes\"}", "{\"stream\":1}",
	"{\"messages\":{}}", "{\"messages\":null}", "{\"messages\":[]}", "{\"stop_sequences\":[\"a\",5]}", "{\"stop_sequences\":[\"a\",\"b\"],\"stop_sequences\":[\"c\"]}",
	"{\"type\":\"image\",\"source\":{\"type\":\"base64\",\"data\":\"AA\"},\"source\":{\"type\":\"url\"}}", "{\"source\":{\"type\":\"x\"},\"source\":null}", "{\"source\":5}",
	"{\"blocks\":[{\"type\":\"text\",\"text\":\"t\"},{\"input\":{ \"a\" : [ 1 ] },\"content\":null}]}", "{\"blocks\":[5]}", "{\"count\":2147483648}", "{\"count\":7}", "{\"ratio\":3.4e39}",
	"{\"Untagged\":\"u\",\"untagged\":\"v\",\"skipped\":\"s\",\"Ignored\":\"i\",\"-\":\"d\"}", "{\"\xff\":1,\"model\":\"\xff\"}", "{\"KModel\":1}", "{\"text\":\"\\ud800\"}",
	"{\"type\":\"t\",\"extra\":\"e\"}", "{\"n\":\"5\"}", "{\"m\":{\"a\":\"b\"}}", " { \"model\" : \"spaced\" } ", "{\"model\":\"m\"} x", "{\"model\":", "{\"disable_parallel_tool_use\":true}",
	"{\"model\":\"m\",\"unknown\":{\"deep\":[1,2,{\"x\":null}]},\"messages\":[]}",
	"{\"type\":\"call\",\"headers\":{\"a\":\"b\",\"a\":\"c\",\"d\":null},\"raws\":{\"x\":{\"y\":[1]}},\"any\":{\"n\":1.5,\"l\":[true]},\"when\":\"2026-11-20T00:00:00Z\"}",
	"{\"embed\":{\"type\":\"t\",\"extra\":\"e\"},\"opt\":{\"n\":\"7\"},\"ints\":{\"1\":\"one\"},\"nums\":[1,2.5,-3e2]}",
	"{\"when\":\"not a time\"}", "{\"headers\":{\"a\":5}}", "{\"headers\":null}", "{\"headers\":[]}", "{\"opt\":{\"n\":7}}",
	"{\"req\":{\"model\":\"m\",\"messages\":[{\"role\":\"user\",\"content\":\"a \\\"q\\\" \\\\\"}]},\"req\":{\"stream\":true}}",
	"{\"a\":{\"type\":\"x\"},\"b\":null}", "[{\"k\":[1,\"s\\\\\",{}]},null,{}]",
	"\"1.5\"", "\"x\"", "[1,\"2\",\"y\"]", "{\"n\":1,\"name\":\"nm\",\"ns\":[2,\"3\"],\"m\":{\"a\":4}}", "{\"n\":\"x\"}", "{\"n\":\"7\"}", "{\"name\":5}",
}

func TestUnmarshalMatchesEncodingJSON(t *testing.T) {
	for _, s := range seeds {
		compare(t, []byte(s))
	}
}

func FuzzUnmarshalMatchesEncodingJSON(f *testing.F) {
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) { compare(t, data) })
}

func compare(t *testing.T, data []byte) {
	t.Helper()
	got, want := targets(), targets()
	for i := range got {
		gerr := Unmarshal(data, got[i])
		werr := json.Unmarshal(data, want[i])
		if errText(gerr) != errText(werr) {
			t.Fatalf("Unmarshal(%q, %T) error %q, json.Unmarshal %q", data, got[i], errText(gerr), errText(werr))
		}
		if g, w := dump(got[i]), dump(want[i]); g != w {
			t.Fatalf("Unmarshal(%q, %T) = %s, json.Unmarshal gives %s", data, got[i], g, w)
		}
	}
}

func errText(err error) string {
	if err == nil {
		return "<nil>"
	}
	return err.Error()
}

func dump(v any) string {
	var b strings.Builder
	dumpValue(&b, reflect.ValueOf(v))
	return b.String()
}

func dumpValue(b *strings.Builder, v reflect.Value) {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			b.WriteString("nil")
			return
		}
		b.WriteString("&")
		dumpValue(b, v.Elem())
	case reflect.Slice:
		if v.IsNil() {
			b.WriteString("nil")
			return
		}
		if v.Type().Elem().Kind() == reflect.Uint8 {
			fmt.Fprintf(b, "%q", v.Bytes())
			return
		}
		b.WriteString("[")
		for i := range v.Len() {
			dumpValue(b, v.Index(i))
			b.WriteString(",")
		}
		b.WriteString("]")
	case reflect.Map:
		if v.IsNil() {
			b.WriteString("nil")
			return
		}
		keys := v.MapKeys()
		sort.Slice(keys, func(i, j int) bool { return fmt.Sprint(keys[i]) < fmt.Sprint(keys[j]) })
		b.WriteString("map{")
		for _, k := range keys {
			fmt.Fprintf(b, "%v:", k)
			dumpValue(b, v.MapIndex(k))
			b.WriteString(",")
		}
		b.WriteString("}")
	case reflect.Struct:
		b.WriteString("{")
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				fmt.Fprintf(b, "%s:", v.Type().Field(i).Name)
				dumpValue(b, v.Field(i))
				b.WriteString(" ")
			}
		}
		b.WriteString("}")
	default:
		fmt.Fprintf(b, "%#v", v.Interface())
	}
}

func TestUnmarshalRawMessagesShareTheText(t *testing.T) {
	data := []byte("{\"messages\":[{\"a\":1},{\"b\":2}],\"system\":\"s\"}")
	var r testRequest
	if err := Unmarshal(data, &r); err != nil {
		t.Fatal(err)
	}
	if &r.Messages[0][0] != &data[13] {
		t.Errorf("messages[0] is a copy, not part of the text")
	}
	_ = append(r.Messages[0], 'x')
	if string(data) != "{\"messages\":[{\"a\":1},{\"b\":2}],\"system\":\"s\"}" {
		t.Errorf("appending to a value wrote into the text: %s", data)
	}
}

func TestUnmarshalTypeErrorUnwraps(t *testing.T) {
	var s string
	err := Unmarshal([]byte("[1,2]"), &s)
	var te *json.UnmarshalTypeError
	if err == nil || s != "" {
		t.Fatalf("Unmarshal of an array into a string: %v, %q", err, s)
	}
	if !errorsAs(err, &te) {
		t.Errorf("errors.As found no *json.UnmarshalTypeError in %v", err)
	}
}

func errorsAs(err error, target any) bool { return errors.As(err, target) }

func TestUnmarshalRepeatedSliceKeyMatchesEncodingJSON(t *testing.T) {
	type item struct {
		X string `json:"x"`
		Y string `json:"y"`
	}
	type holder struct {
		A []item `json:"a"`
	}
	data := []byte(`{"a":[{"x":"1"}],"a":[{"y":"2"}]}`)
	var got, want holder
	if err := Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Unmarshal = %+v, json.Unmarshal = %+v", got, want)
	}
}

package protocol

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/stdotwork/pluxy/next/llm"
)

// slowFrame is the frame WriteFrame wrote before it had a fast path: encoding/json, HTML escaping off.
func slowFrame(t testing.TB, v any) ([]byte, bool) {
	t.Helper()
	var buf bytes.Buffer
	buf.Write(make([]byte, 4))
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, false
	}
	b := buf.Bytes()
	b = b[:len(b)-1]
	binary.BigEndian.PutUint32(b, uint32(len(b)-4))
	return b, true
}

var weirdStrings = []string{
	"", "a", "hello world", "<b>&amp;</b>", "line\nbreak\r\ttab", "quote\" back\\slash", "\x00\x01\x1f\x7f",
	"\b\f", "caf\u00e9 \U0001F600", "\u2028\u2029", "bad\xffutf8\xc3", "\xe2\x80", "/slash/",
	strings.Repeat("x", 5000),
}

var weirdRaws = []string{
	``, `{}`, `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`,
	"{\n  \"a\": 1,\n  \"b\": [1, 2, \"x y\"]\n}", ` {"a":"  spaced  "} `, `"str"`, `null`, `1.5e3`, `[]`,
	`{"a":"\u2028 \" \\ \/"}`, "{\"a\":\"\u2028<>&\"}", `{"a":`, `{`, `nope`, `{"a":1}x`, "{\"a\":\"bad\xff\"}",
}

func TestWriteFrameMatchesEncodingJSON(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	pick := func(s []string) string { return s[r.Intn(len(s))] }
	for i := 0; i < 3000; i++ {
		rep := Reply{Event: llm.Event{Type: pick([]string{"text.delta", "native", "alive", "item.end", "", "reasoning.delta"})}}
		if r.Intn(3) > 0 {
			rep.Index = r.Intn(5) - 1
			if r.Intn(10) == 0 {
				rep.Index = r.Int()
			}
		}
		if r.Intn(3) > 0 {
			rep.Delta = pick(weirdStrings)
		}
		if r.Intn(3) > 0 {
			rep.Raw = json.RawMessage(pick(weirdRaws))
		}
		if r.Intn(8) == 0 {
			rep.Event.Usage = &llm.Usage{}
		}
		for _, v := range []any{rep, &rep} {
			want, wantOK := slowFrame(t, v)
			var got bytes.Buffer
			err := WriteFrame(&got, v)
			if wantOK != (err == nil) {
				t.Fatalf("%#v: encoding/json ok=%v, WriteFrame err=%v", rep, wantOK, err)
			}
			if wantOK && !bytes.Equal(got.Bytes(), want) {
				t.Fatalf("%#v:\n got %q\nwant %q", rep, got.Bytes(), want)
			}
		}
	}
}

// TestFastEncodeDeclinesEveryOtherField fails when a field is added to Event or Reply that
// appendReply does not know: it must then leave the frame to encoding/json.
func TestFastEncodeDeclinesEveryOtherField(t *testing.T) {
	handled := map[string]bool{"Event": true, "Type": true, "Index": true, "Delta": true, "Raw": true}
	check := func(v reflect.Value) {
		typ := v.Type()
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			if handled[f.Name] {
				continue
			}
			var rep Reply
			fv := reflect.ValueOf(&rep).Elem()
			if typ == reflect.TypeOf(llm.Event{}) {
				fv = fv.FieldByName("Event")
			}
			fv = fv.FieldByName(f.Name)
			switch f.Type.Kind() {
			case reflect.Pointer:
				fv.Set(reflect.New(f.Type.Elem()))
			case reflect.Slice:
				fv.Set(reflect.MakeSlice(f.Type, 1, 1))
			case reflect.String:
				fv.SetString("x")
			case reflect.Int, reflect.Int64:
				fv.SetInt(1)
			case reflect.Bool:
				fv.SetBool(true)
			default:
				t.Fatalf("field %s has kind %v: teach this test and appendReply", f.Name, f.Type.Kind())
			}
			if _, ok := appendReply(nil, &rep); ok {
				t.Errorf("appendReply wrote a Reply with %s set, which it does not encode", f.Name)
			}
		}
	}
	check(reflect.ValueOf(llm.Event{}))
	check(reflect.ValueOf(Reply{}))
}

func TestDecodeReplyMatchesEncodingJSON(t *testing.T) {
	bodies := []string{
		`{"type":"text.delta","index":0,"delta":"hi"}`,
		`{"type":"text.delta","index":3,"delta":"a\nb\"c\\d\/e\u00e9\ud83d\ude00","raw":{"type":"x","text":"hi"}}`,
		`{"type":"native","index":0,"raw":{"a":[1,2,{"b":null}]}}`,
		"{ \"type\" : \"alive\" ,\n \"index\":0 }", `{"type":"alive"}`, `{}`, `{"index":-4}`, `{"index":4.0}`, `{"index":1e2}`,
		`{"index":"3"}`, `{"index":99999999999999999999}`, `{"index":-0}`, `{"type":null}`, `{"delta":null}`, `{"raw":null}`,
		`{"Type":"x"}`, `{"TYPE":"x","index":1}`, `{"type":"a","type":"b"}`, `{"raw":1,"raw":2}`,
		`{"type":"a","extra":1}`, `{"type":"finish","finish":{"reason":"stop"}}`, `{"info":{}}`,
		`{"delta":"\ud800"}`, `{"delta":"\ud800\u0041"}`, "{\"delta\":\"bad\xff\"}", `{"delta":"\x"}`, `{"delta":1}`, `{"type":1}`,
		`{"raw":}`, `{"type":"a"`, `[]`, `"s"`, ``, `{"type":"a"} x`, `{"type":"a","index":1,"delta":"b","raw":"c","extra":1}`,
		`{"raw":"  "}`, `{"raw":[ 1 , 2 ]}`, "{\"type\":\"a\u2028\"}",
	}
	for _, body := range bodies {
		var want Reply
		wantErr := json.Unmarshal([]byte(body), &want)
		var got Reply
		if !decodeReply([]byte(body), &got) {
			if !reflect.DeepEqual(got, Reply{}) {
				t.Errorf("%q: decodeReply declined but changed the reply: %#v", body, got)
			}
			continue
		}
		if wantErr != nil {
			t.Errorf("%q: decodeReply took a body encoding/json rejects: %v", body, wantErr)
		} else if !reflect.DeepEqual(got, want) {
			t.Errorf("%q:\n got %#v\nwant %#v", body, got, want)
		}
	}
}

// TestDecodeReplyRoundTrip checks that what the encoder writes decodes back to the same reply,
// and the fast decoder agrees with encoding/json about it.
func TestDecodeReplyRoundTrip(t *testing.T) {
	r := rand.New(rand.NewSource(2))
	for i := 0; i < 2000; i++ {
		rep := Reply{Event: llm.Event{Type: "text.delta", Index: r.Intn(4)}}
		rep.Delta = weirdStrings[r.Intn(len(weirdStrings))]
		if r.Intn(2) == 0 {
			rep.Raw = json.RawMessage(`{"a":"` + weirdStrings[r.Intn(len(weirdStrings)-1)][:0] + `x"}`)
		}
		var buf bytes.Buffer
		if err := WriteFrame(&buf, rep); err != nil {
			t.Fatal(err)
		}
		body := buf.Bytes()[4:]
		var want, got Reply
		if err := json.Unmarshal(body, &want); err != nil {
			t.Fatal(err)
		}
		if !decodeReply(body, &got) {
			continue // the delta had invalid UTF-8 or the like: left to encoding/json
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%q:\n got %#v\nwant %#v", body, got, want)
		}
	}
}

func frames(t testing.TB, vs ...any) []byte {
	t.Helper()
	var buf bytes.Buffer
	for _, v := range vs {
		if err := WriteFrame(&buf, v); err != nil {
			t.Fatal(err)
		}
	}
	return buf.Bytes()
}

func TestFrameReaderReadsFramesInOrder(t *testing.T) {
	var vs []any
	for i := 0; i < 50; i++ {
		vs = append(vs, Reply{Event: llm.Event{Type: "text.delta", Index: i, Delta: strings.Repeat("d", i*7)}})
	}
	vs = append(vs, Reply{Event: llm.Event{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{ID: "r1"}}})
	vs = append(vs, Reply{Event: llm.Event{Type: "native", Raw: json.RawMessage(`{"big":"` + strings.Repeat("z", 3*frameBufSize) + `"}`)}})
	vs = append(vs, Reply{Event: llm.Event{Type: "finish", Finish: &llm.Finish{}}})
	data := frames(t, vs...)
	readers := map[string]func() io.Reader{
		"whole":   func() io.Reader { return bytes.NewReader(data) },
		"onebyte": func() io.Reader { return iotest.OneByteReader(bytes.NewReader(data)) },
		"half":    func() io.Reader { return iotest.HalfReader(bytes.NewReader(data)) },
		"dataerr": func() io.Reader { return iotest.DataErrReader(bytes.NewReader(data)) },
	}
	for name, mk := range readers {
		fr := NewFrameReader(mk())
		for i, v := range vs {
			var got Reply
			if err := fr.Read(&got); err != nil {
				t.Fatalf("%s: frame %d: %v", name, i, err)
			}
			var want Reply
			b, _ := slowFrame(t, v)
			if err := json.Unmarshal(b[4:], &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("%s: frame %d:\n got %#v\nwant %#v", name, i, got, want)
			}
		}
		if err := fr.Read(new(Reply)); err != io.EOF {
			t.Fatalf("%s: after the last frame: %v, want io.EOF", name, err)
		}
	}
}

func TestFrameReaderErrorsMatchReadFrame(t *testing.T) {
	good := frames(t, Reply{Event: llm.Event{Type: "text.delta", Delta: "hello"}})
	tooBig := make([]byte, 4)
	binary.BigEndian.PutUint32(tooBig, MaxFrame+1)
	badJSON := append([]byte{0, 0, 0, 5}, "{nope"...)
	wrongType := append([]byte{0, 0, 0, 13}, `{"delta":"x"}`...)
	wrongType = append([]byte{0, 0, 0, 14}, `{"index":"no"}`...)
	cases := map[string][]byte{
		"empty": nil, "partial header": good[:2], "no body": good[:4], "partial body": good[:len(good)-1],
		"too big": tooBig, "bad json": badJSON, "wrong type": wrongType,
	}
	for name, data := range cases {
		var want, got Reply
		wantErr := ReadFrame(bytes.NewReader(data), &want)
		gotErr := NewFrameReader(bytes.NewReader(data)).Read(&got)
		if (wantErr == nil) != (gotErr == nil) || (wantErr != nil && wantErr.Error() != gotErr.Error()) {
			t.Errorf("%s: ReadFrame error %v, FrameReader error %v", name, wantErr, gotErr)
		}
		if errors.Is(wantErr, ErrFrameTooLarge) != errors.Is(gotErr, ErrFrameTooLarge) {
			t.Errorf("%s: ErrFrameTooLarge differs: %v, %v", name, wantErr, gotErr)
		}
		var wse, gse *json.SyntaxError
		var wte, gte *json.UnmarshalTypeError
		if errors.As(wantErr, &wse) != errors.As(gotErr, &gse) || errors.As(wantErr, &wte) != errors.As(gotErr, &gte) {
			t.Errorf("%s: JSON error types differ: %T, %T", name, wantErr, gotErr)
		}
	}
}

// A reader that fails after some bytes, as a connection past its deadline does.
type failAfter struct {
	data []byte
	err  error
}

func (f *failAfter) Read(p []byte) (int, error) {
	if len(f.data) == 0 {
		return 0, f.err
	}
	n := copy(p, f.data)
	f.data = f.data[n:]
	return n, nil
}

func TestFrameReaderKeepsReadErrors(t *testing.T) {
	good := frames(t, Reply{Event: llm.Event{Type: "text.delta", Delta: "hello"}})
	boom := errors.New("boom")
	for _, n := range []int{0, 2, 6, len(good) - 1} {
		err := NewFrameReader(&failAfter{data: good[:n], err: boom}).Read(new(Reply))
		if !errors.Is(err, boom) {
			t.Errorf("after %d bytes: %v, want the read error", n, err)
		}
	}
}

func BenchmarkReplyFrame(b *testing.B) {
	rep := Reply{Event: llm.Event{Type: "text.delta", Index: 0, Delta: "hello there", Raw: json.RawMessage(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello there"}}`)}}
	data := frames(b, rep)
	b.Run("write", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_ = WriteFrame(io.Discard, rep)
		}
	})
	b.Run("write-json", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_, _ = slowFrame(b, rep)
		}
	})
	b.Run("read", func(b *testing.B) {
		b.ReportAllocs()
		src := bytes.NewReader(data)
		fr := NewFrameReader(src)
		for i := 0; i < b.N; i++ {
			src.Reset(data)
			var r Reply
			_ = fr.Read(&r)
		}
	})
	b.Run("read-json", func(b *testing.B) {
		b.ReportAllocs()
		src := bytes.NewReader(data)
		for i := 0; i < b.N; i++ {
			src.Reset(data)
			var r Reply
			_ = ReadFrame(src, &r)
		}
	})
}

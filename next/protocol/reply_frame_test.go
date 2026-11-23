package protocol

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/stdotwork/pluxy/next/llm"
)

// replyBodies are reply frame bodies: the shapes a streamed reply has, and the ones another
// release, a bug or a hostile extension could send. The same bodies seed the edits below.
func replyBodies() []string {
	return []string{
		`{"type":"text.delta","index":0,"delta":"hi"}`,
		`{"type":"text.delta","index":3,"delta":"a\nb\"c\\d\/e\u00e9\ud83d\ude00","raw":{"type":"x","text":"hi"}}`,
		`{"type":"text.delta","index":0,"delta":"Hello","raw":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}}`,
		`{"type":"native","index":0,"raw":{"a":[1,2,{"b":null}]}}`,
		"{ \"type\" : \"alive\" ,\n \"index\":0 }", `{"type":"alive"}`, `{}`, `{"index":-4}`, `{"index":4.0}`, `{"index":1e2}`,
		`{"index":"3"}`, `{"index":99999999999999999999}`, `{"index":-0}`, `{"index":-}`, `{"index":007}`,
		`{"type":null}`, `{"delta":null}`, `{"raw":null}`, `{"index":null}`,
		`{"Type":"x"}`, `{"TYPE":"x","index":1}`, `{"type":"a","type":"b"}`, `{"raw":1,"raw":2}`, `{"t\u0079pe":"x"}`,
		`{"type":"a","extra":1}`, `{"type":"finish","finish":{"reason":"stop"}}`, `{"info":{}}`,
		`{"type":"finish","finish":{"reason":"stop"},"finish":{"usage":1}}`,
		`{"type":"item.end","citations":[{"url":"a"}],"citations":[{"url":"b"}]}`,
		`{"type":"item.end","citations":[],"sources":[]}`,
		`{"delta":"\ud800"}`, `{"delta":"\ud800\u0041"}`, "{\"delta\":\"bad\xff\"}", `{"delta":"\x"}`, `{"delta":1}`, `{"type":1}`,
		`{"raw":}`, `{"type":"a"`, `[]`, `"s"`, `null`, ``, `{"type":"a"} x`, `{"type":"a"}{"type":"b"}`,
		`{"type":"a","index":1,"delta":"b","raw":"c","extra":1}`,
		`{"raw":"  "}`, `{"raw":[ 1 , 2 ]}`, `{"raw": { "a" : 1 } }`, "{\"type\":\"a\u2028\"}", "{\"delta\":\"\u0000\"}",
		`{"type":"a","index":1,"delta":"` + strings.Repeat("d", 5000) + `"}`,
	}
}

// sameFrameRead reads frame with a FrameReader on the reader made by mk and with the reference
// ReadFrame, into a Reply that holds the same earlier value, and reports a difference.
func sameFrameRead(frame []byte, mk func(io.Reader) io.Reader, prior Reply) string {
	got, want := prior, prior
	gotErr := NewFrameReader(mk(bytes.NewReader(frame))).Read(&got)
	wantErr := referenceRead(bytes.NewReader(frame), &want)
	if errText(gotErr) != errText(wantErr) {
		return fmt.Sprintf("error %s, want %s", errText(gotErr), errText(wantErr))
	}
	var gse, wse *json.SyntaxError
	var gte, wte *json.UnmarshalTypeError
	if errors.As(gotErr, &gse) != errors.As(wantErr, &wse) || errors.As(gotErr, &gte) != errors.As(wantErr, &wte) ||
		errors.Is(gotErr, ErrFrameTooLarge) != errors.Is(wantErr, ErrFrameTooLarge) {
		return fmt.Sprintf("error type %T, want %T", gotErr, wantErr)
	}
	if !reflect.DeepEqual(got, want) {
		return fmt.Sprintf("value %+v, want %+v", got, want)
	}
	return ""
}

func headed(body []byte) []byte {
	return append(binary.BigEndian.AppendUint32(nil, uint32(len(body))), body...)
}

var readerKinds = map[string]func(io.Reader) io.Reader{
	"whole":   func(r io.Reader) io.Reader { return r },
	"onebyte": iotest.OneByteReader,
	"half":    iotest.HalfReader,
	"dataerr": iotest.DataErrReader,
}

// TestReplyFrameReadSameAsUnmarshal reads every reply body above, each cut short at many places,
// and thousands of random edits of them, with a FrameReader on four kinds of reader, and checks
// the value and the error are those of the reference (encoding/json after two reads).
func TestReplyFrameReadSameAsUnmarshal(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	junk := []byte("\"{}[],:\\ 0n\xff\x00ueEtT-.")
	priors := []Reply{
		{},
		{Event: llm.Event{Type: "old", Index: 9, Delta: "old", Raw: json.RawMessage(`{"old":1}`)}},
	}
	cases, fast := 0, 0
	check := func(label string, frame []byte) {
		for kind, mk := range readerKinds {
			for _, prior := range priors {
				if d := sameFrameRead(frame, mk, prior); d != "" {
					t.Errorf("%s (%s reader, prior %v): %s\nframe %.200q", label, kind, prior.Type, d, frame)
				}
				cases++
			}
		}
	}
	for _, s := range replyBodies() {
		b := []byte(s)
		var r Reply
		if decodeReply(b, &r) {
			fast++
		}
		frame := headed(b)
		check("whole", frame)
		for cut := 0; cut < len(frame); cut += 1 + rng.Intn(len(frame)/12+1) {
			check(fmt.Sprintf("cut at %d", cut), frame[:cut])
		}
		for i := 0; i < 400; i++ {
			e := append([]byte(nil), b...)
			for n := 1 + rng.Intn(3); n > 0 && len(e) > 0; n-- {
				at := rng.Intn(len(e))
				switch rng.Intn(4) {
				case 0:
					e = append(e[:at], e[at+1:]...)
				case 1:
					e = append(e[:at], append([]byte{junk[rng.Intn(len(junk))]}, e[at:]...)...)
				case 2:
					e[at] = junk[rng.Intn(len(junk))]
				default: // another case for a letter, as a key spelled in another case
					if c := e[at]; c >= 'a' && c <= 'z' {
						e[at] = c - 32
					}
				}
			}
			frame := headed(e)
			if i%50 == 0 {
				frame = frame[:rng.Intn(len(frame)+1)]
			}
			var r Reply
			if len(e) > 4 && decodeReply(e, &r) {
				fast++
			}
			check("edit", frame)
		}
	}
	// A header that says more than MaxFrame, and one that says more than follows.
	check("too big", binary.BigEndian.AppendUint32(nil, MaxFrame+1))
	check("short", append(binary.BigEndian.AppendUint32(nil, 100), `{"type":"a"}`...))
	if fast == 0 {
		t.Fatal("no body took the fast decoder")
	}
	t.Logf("%d reads compared (%d bodies decoded by the fast decoder)", cases, fast)
}

// TestFrameReaderReadsFramesInOrder reads many frames, big ones among them, through every kind of
// reader and checks each equals what encoding/json reads.
func TestFrameReaderReadsFramesInOrder(t *testing.T) {
	var vs []any
	for i := 0; i < 50; i++ {
		vs = append(vs, Reply{Event: llm.Event{Type: "text.delta", Index: i, Delta: strings.Repeat("d", i*7)}})
	}
	vs = append(vs, Reply{Event: llm.Event{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{ID: "r1"}}})
	vs = append(vs, Reply{Event: llm.Event{Type: "native", Raw: json.RawMessage(`{"big":"` + strings.Repeat("z", 3*frameBufSize) + `"}`)}})
	vs = append(vs, Reply{Event: llm.Event{Type: "text.delta", Delta: "after a big frame"}})
	vs = append(vs, Reply{Event: llm.Event{Type: "finish", Finish: &llm.Finish{}}})
	var stream bytes.Buffer
	for _, v := range vs {
		if err := WriteFrame(&stream, v); err != nil {
			t.Fatal(err)
		}
	}
	for kind, mk := range readerKinds {
		fr := NewFrameReader(mk(bytes.NewReader(stream.Bytes())))
		var kept []Reply
		for i, v := range vs {
			var got Reply
			if err := fr.Read(&got); err != nil {
				t.Fatalf("%s: frame %d: %v", kind, i, err)
			}
			b, _ := referenceWrite(v)
			var want Reply
			if err := json.Unmarshal(b[4:], &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("%s: frame %d:\n got %#v\nwant %#v", kind, i, got, want)
			}
			kept = append(kept, got)
		}
		if err := fr.Read(new(Reply)); err != io.EOF {
			t.Fatalf("%s: after the last frame: %v, want io.EOF", kind, err)
		}
		// Nothing handed on may share the reader's buffer: the values read earlier still hold.
		for i, v := range vs {
			b, _ := referenceWrite(v)
			var want Reply
			_ = json.Unmarshal(b[4:], &want)
			if !reflect.DeepEqual(kept[i], want) {
				t.Fatalf("%s: frame %d changed after later frames were read:\n got %#v\nwant %#v", kind, i, kept[i], want)
			}
		}
	}
}

// TestFrameReaderHandsOnNoBuffer overwrites the reader's buffer after each read and checks the
// reply read keeps its value, for the fast decoder and for the fallback.
func TestFrameReaderHandsOnNoBuffer(t *testing.T) {
	for _, s := range []string{
		`{"type":"text.delta","index":1,"delta":"abc","raw":{"k":"vvvv"}}`,
		`{"type":"text.delta","index":1,"delta":"abc","raw":{"k":"vvvv"},"finish":{"reason":"stop"}}`,
		`{"type":"item.end","citations":[{"url":"https://a.example/xyz"}]}`,
	} {
		var want Reply
		if err := json.Unmarshal([]byte(s), &want); err != nil {
			t.Fatal(err)
		}
		fr := NewFrameReader(bytes.NewReader(headed([]byte(s))))
		var got Reply
		if err := fr.Read(&got); err != nil {
			t.Fatal(err)
		}
		for i := range fr.buf {
			fr.buf[i] = '#'
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: reply changed when the buffer did:\n got %#v\nwant %#v", s, got, want)
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
	good := headed([]byte(`{"type":"text.delta","delta":"hello"}`))
	boom := errors.New("boom")
	for _, n := range []int{0, 2, 6, len(good) - 1} {
		got := NewFrameReader(&failAfter{data: good[:n], err: boom}).Read(new(Reply))
		want := referenceRead(&failAfter{data: good[:n], err: boom}, new(Reply))
		if !errors.Is(got, boom) || errText(got) != errText(want) {
			t.Errorf("after %d bytes: %v, want %v", n, got, want)
		}
	}
}

// TestFrameReaderReadsOnePerFrame fails when a frame that arrived whole costs more than one read.
func TestFrameReaderReadsOnePerFrame(t *testing.T) {
	var data []byte
	for i := 0; i < 20; i++ {
		data = append(data, headed([]byte(`{"type":"text.delta","index":0,"delta":"x"}`))...)
	}
	src := &countReader{r: bytes.NewReader(data)}
	fr := NewFrameReader(src)
	for j := 0; j < 20; j++ {
		if err := fr.Read(new(Reply)); err != nil {
			t.Fatal(err)
		}
	}
	if src.n != 1 {
		t.Fatalf("20 small frames took %d reads, want 1", src.n)
	}
}

type countReader struct {
	r io.Reader
	n int
}

func (c *countReader) Read(p []byte) (int, error) { c.n++; return c.r.Read(p) }

func BenchmarkReadReplyFrame(b *testing.B) {
	body := []byte(`{"type":"text.delta","index":0,"delta":"hello there","raw":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello there"}}}`)
	data := headed(body)
	b.Run("FrameReader", func(b *testing.B) {
		b.ReportAllocs()
		src := bytes.NewReader(data)
		fr := NewFrameReader(src)
		for i := 0; i < b.N; i++ {
			src.Reset(data)
			var r Reply
			_ = fr.Read(&r)
		}
	})
	b.Run("ReadFrame", func(b *testing.B) {
		b.ReportAllocs()
		src := bytes.NewReader(data)
		for i := 0; i < b.N; i++ {
			src.Reset(data)
			var r Reply
			_ = ReadFrame(src, &r)
		}
	})
}

package protocol

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

// referenceWrite is the frame WriteFrame wrote before large call frames took their own path: v
// through the encoder, with no HTML escaping.
func referenceWrite(v any) ([]byte, error) {
	var buf bytes.Buffer
	buf.Write(make([]byte, 4))
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	b := buf.Bytes()
	b = b[:len(b)-1]
	if len(b)-4 > MaxFrame {
		return nil, ErrFrameTooLarge
	}
	binary.BigEndian.PutUint32(b, uint32(len(b)-4))
	return b, nil
}

// referenceRead is ReadFrame as it was before large call frames took their own path.
func referenceRead(r io.Reader, v any) error {
	var head [4]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		if err == io.ErrUnexpectedEOF {
			return fmt.Errorf("protocol: truncated frame header: %w", err)
		}
		return err
	}
	n := binary.BigEndian.Uint32(head[:])
	if n > MaxFrame {
		return ErrFrameTooLarge
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return fmt.Errorf("protocol: truncated frame: %w", err)
	}
	return json.Unmarshal(body, v)
}

func errText(err error) string {
	if err == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%T: %v", err, err)
}

func padded(before, after string) string {
	return before + `"pad":"` + strings.Repeat("x", spliceMin) + `"` + after
}

// bodies are native bodies of at least spliceMin bytes: valid ones in every form the encoder
// compacts, and invalid ones it refuses.
func frameBodies() map[string]string {
	pretty := func(s string) string {
		var b bytes.Buffer
		if err := json.Indent(&b, []byte(s), "\t ", "  "); err != nil {
			panic(err)
		}
		return " \r\n" + b.String() + "\n\t "
	}
	text := func(s string) string { return padded(`{"text":"`+s+`",`, `}`) }
	valid := map[string]string{
		"plain":      text("hello"),
		"html":       text("<b>a & b</b> > c"),
		"separators": text("a b c"),
		"bad utf8":   text("a\xffb\xc0\xafc\xed\xa0\x80"),
		"escapes":    text(`< \/\"\\\n😀\ud800`),
		"spaced":     pretty(text(`a  b\tc`)),
		"empty key":  padded(`{"":1,`, `,"nested":{"a":[1,2.5e3,-0,true,null,{}],"b":[]}}`),
		"array":      "[" + padded(`{`, `}`) + ", 1 ,\"s\"]",
		"number":     strings.Repeat(" ", spliceMin) + "123\n",
		"string":     `"` + strings.Repeat("y<", spliceMin) + `"`,
		"deep":       strings.Repeat("[", 10000) + strings.Repeat("]", 10000) + strings.Repeat(" ", spliceMin),
	}
	invalid := map[string]string{
		"truncated":      text("a")[:spliceMin+10],
		"trailing":       text("a") + " x",
		"two values":     text("a") + text("a"),
		"newline":        text("a\nb"),
		"nul":            text("a\x00b"),
		"bad escape":     text(`a\xb`),
		"short escape":   text(`a\u12`),
		"trailing comma": padded(`{`, `,}`),
		"blank":          strings.Repeat(" ", spliceMin),
		"too deep":       strings.Repeat("[", 10001) + strings.Repeat("]", 10001) + strings.Repeat(" ", spliceMin),
		"bad number":     padded(`{"n":01,`, `}`),
		"bad literal":    padded(`{"n":nul,`, `}`),
	}
	for k, v := range invalid {
		valid["invalid "+k] = v
	}
	return valid
}

func TestCallFrameWriteSameAsEncoder(t *testing.T) {
	cases := 0
	check := func(name string, v any) {
		t.Helper()
		want, wantErr := referenceWrite(v)
		var got bytes.Buffer
		gotErr := WriteFrame(&got, v)
		cases++
		if errText(gotErr) != errText(wantErr) || !bytes.Equal(got.Bytes(), want) {
			t.Errorf("%s: frame or error differs from the encoder's\n got %.200q %s\nwant %.200q %s", name, got.Bytes(), errText(gotErr), want, errText(wantErr))
		}
	}
	mark := strings.Trim(string(spliceMark), `"`)
	for name, body := range frameBodies() {
		native := func() *llm.Native {
			return &llm.Native{Format: llm.FormatAnthropicMessages, Body: json.RawMessage(body), Headers: map[string]string{"anthropic-beta": "<a>&"}}
		}
		withItems := llm.Request{Model: "m<", Items: []llm.Item{{Type: llm.ItemMessage, Role: llm.RoleUser, Content: []llm.Part{{Type: llm.PartText, Text: "a <b> &   \xff"}}}}, Origin: &llm.Origin{Format: "f", Raw: json.RawMessage(` {"a" : 1} `)}, Native: native()}
		call := Call{Type: TypeCall, ID: "1", Op: OpGenerate, NativeOnly: true, Account: &Account{ID: "a"}, Request: &llm.Request{Model: "m", Stream: true, Native: native()}}
		withItemsCall := Call{Type: TypeCall, Op: OpGenerate, Request: &withItems, Capture: true}
		sameMark := call
		sameMark.Request = &llm.Request{Native: &llm.Native{Format: mark, Body: json.RawMessage(body)}}
		noNative := Call{Type: TypeCall, Request: &llm.Request{}}
		check(name+"/call", call)
		check(name+"/call pointer", &call)
		check(name+"/items", withItemsCall)
		check(name+"/format holds the mark", sameMark)
		check(name+"/no native", noNative)
	}
	// Random bodies: values with whitespace between tokens, strings from awkward text, and edits
	// that mostly make them invalid.
	rng := rand.New(rand.NewSource(2))
	for i := 0; i < randomBodies; i++ {
		var b strings.Builder
		randomValue(rng, &b, 0)
		body := padded(`{"v":`+b.String()+`,`, ` }`)
		if i%3 == 0 {
			p := []byte(body)
			junk := "\"{}[],: \n\\\xff\x00"
			p[rng.Intn(len(p))] = junk[rng.Intn(len(junk))]
			body = string(p)
		}
		check(fmt.Sprintf("random %d", i), Call{Type: TypeCall, Request: &llm.Request{Native: &llm.Native{Format: "f", Body: json.RawMessage(body)}}})
	}
	check("nil call", (*Call)(nil))
	check("reply", Reply{Event: llm.Event{Type: "x"}})
	t.Logf("%d frames", cases)
}

// randomBodies is how many random bodies TestCallFrameWriteSameAsEncoder writes.
const randomBodies = 2000

func randomSpace(rng *rand.Rand, b *strings.Builder) {
	for n := rng.Intn(3) * rng.Intn(2); n > 0; n-- {
		b.WriteByte(" \t\n\r"[rng.Intn(4)])
	}
}

func randomValue(rng *rand.Rand, b *strings.Builder, depth int) {
	randomSpace(rng, b)
	defer randomSpace(rng, b)
	words := []string{"a", "<b>", "&", ">", "\u2028", "\u2029", "\xff", "\xe2\x80", "é", "😀", `\u003c`, `\/`, `\"`, `\\`, `\n`, `\ud83d\ude00`, `\ud800`, " ", "\x7f", "\xc0\xaf"}
	switch k := rng.Intn(8); {
	case k < 3 && depth < 4:
		b.WriteByte('[')
		for i := rng.Intn(4); i > 0; i-- {
			randomValue(rng, b, depth+1)
			if i > 1 {
				b.WriteByte(',')
			}
		}
		b.WriteByte(']')
	case k < 5 && depth < 4:
		b.WriteByte('{')
		for i := rng.Intn(4); i > 0; i-- {
			randomSpace(rng, b)
			b.WriteString(`"k` + words[rng.Intn(len(words))] + `":`)
			randomValue(rng, b, depth+1)
			if i > 1 {
				b.WriteByte(',')
			}
		}
		b.WriteByte('}')
	case k == 5:
		b.WriteString([]string{"0", "-1.5e+10", "123456789012345678901234567890", "true", "false", "null"}[rng.Intn(6)])
	default:
		b.WriteByte('"')
		for i := rng.Intn(6); i > 0; i-- {
			b.WriteString(words[rng.Intn(len(words))])
		}
		b.WriteByte('"')
	}
}

// sameRead decodes frame with ReadFrame and with the reference and reports a difference.
func sameRead(frame []byte) string {
	var got, want Call
	gotErr := ReadFrame(bytes.NewReader(frame), &got)
	wantErr := referenceRead(bytes.NewReader(frame), &want)
	if errText(gotErr) != errText(wantErr) {
		return fmt.Sprintf("error %s, want %s", errText(gotErr), errText(wantErr))
	}
	if !reflect.DeepEqual(got, want) {
		return fmt.Sprintf("value %+v, want %+v", got, want)
	}
	return ""
}

func reframe(body []byte) []byte {
	return binary.BigEndian.AppendUint32(nil, uint32(len(body)))
}

func TestCallFrameReadSameAsUnmarshal(t *testing.T) {
	body := frameBodies()["escapes"]
	frameOf := func(s string) []byte { return append(reframe([]byte(s)), s...) }
	call := `"type":"call","id":"1","op":"generate","native_only":true`
	req := func(native string) string {
		return `{` + call + `,"request":{"model":"m","stream":true,"native":` + native + `}}`
	}
	nat := `{"format":"anthropic.messages","body":` + body + `,"headers":{"a":"b"}}`
	seeds := []string{
		req(nat),
		// Keys reordered, repeated, in other case, spelled with escapes, and unknown ones.
		`{"request":{"native":{"headers":{"a":"b"},"body":` + body + `,"format":"f"},"model":"m"},` + call + `}`,
		`{"request":{"model":"a","native":{"body":1,"format":"f"}},` + call + `,"request":{"model":"b","native":{"body":` + body + `}}}`,
		`{"request":{"native":{"body":` + body + `,"body":2}},` + call + `}`,
		`{` + call + `,"Request":{"Native":{"BODY":` + body + `}},"request":{"native":{"format":"x"}}}`,
		`{` + call + `,"request":{"native":{"body":` + body + `}}}`,
		`{` + call + `,"unknown":` + body + `,"request":{"x":[1,2],"native":{"body":` + body + `}}}`,
		`{` + call + `,"request":{"native":null,"pad":"` + strings.Repeat("p", spliceMin) + `"}}`,
		`{` + call + `,"request":"` + strings.Repeat("p", spliceMin) + `"}`,
		`{` + call + `,"request":{"stream":"yes","native":{"body":` + body + `}},"account":7}`,
		// A repeated list key, with different elements, past 4 KB of padding in another field.
		`{` + call + `,"request":{"model":"m","pad":"` + strings.Repeat("p", 4<<10) + `","items":[{"role":"user"}],"items":[{"type":"message"}]}}`,
		` ` + req(nat) + "\n",
		`[` + req(nat) + `]`,
		`null`, `{}`, ``,
	}
	rng := rand.New(rand.NewSource(1))
	junk := []byte("\"{}[],:\\ 0n\xff\x00u")
	cases := 0
	for _, s := range seeds {
		frame := frameOf(s)
		for cut := 0; cut <= len(frame); cut += 1 + rng.Intn(len(frame)/20+1) {
			if d := sameRead(frame[:cut]); d != "" { // a frame cut short
				t.Errorf("cut at %d of %.120q: %s", cut, s, d)
			}
			cases++
		}
		for i := 0; i < 300; i++ { // a few edits of the text, under a header that fits
			b := []byte(s)
			for n := 1 + rng.Intn(3); n > 0 && len(b) > 0; n-- {
				at := rng.Intn(len(b))
				if i%2 == 0 { // near the start and end, where the call's own fields are
					at = rng.Intn(min(len(b), 200)) + (len(b)-min(len(b), 200))*rng.Intn(2)
				}
				switch rng.Intn(3) {
				case 0:
					b = append(b[:at], b[at+1:]...)
				case 1:
					b = append(b[:at], append([]byte{junk[rng.Intn(len(junk))]}, b[at:]...)...)
				default:
					b[at] = junk[rng.Intn(len(junk))]
				}
			}
			if d := sameRead(append(reframe(b), b...)); d != "" {
				t.Errorf("edited %.200q: %s", b, d)
			}
			cases++
		}
	}
	for name, body := range frameBodies() {
		if d := sameRead(frameOf(req(`{"format":"f","body":` + body + `}`))); d != "" {
			t.Errorf("%s: %s", name, d)
		}
		cases++
	}
	t.Logf("%d frames", cases)
}

// TestLargeCallFrameTakesFastPaths fails if writeSplicedCall or ReadFrame stops taking its fast
// path for a plain large call, which would still give the right bytes but at a higher cost.
func TestLargeCallFrameTakesFastPaths(t *testing.T) {
	call := benchCall(100 << 10)
	var spliced bytes.Buffer
	done, err := writeSplicedCall(&spliced, &call)
	if !done || err != nil {
		t.Fatalf("writeSplicedCall: done %v, err %v; want done and no error", done, err)
	}
	var framed bytes.Buffer
	if err := WriteFrame(&framed, call); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(spliced.Bytes(), framed.Bytes()) {
		t.Errorf("writeSplicedCall wrote %d bytes, WriteFrame %d: they differ", spliced.Len(), framed.Len())
	}

	frame := framed.Bytes()
	const runs = 20
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for range runs {
		var c Call
		if err := ReadFrame(bytes.NewReader(frame), &c); err != nil {
			t.Fatal(err)
		}
	}
	runtime.ReadMemStats(&after)
	perRead := float64(after.TotalAlloc-before.TotalAlloc) / runs
	if limit := 1.5 * float64(len(frame)); perRead >= limit {
		t.Errorf("ReadFrame allocated %.0f bytes per call for a %d byte frame, want less than %.0f: the fast path is not used", perRead, len(frame), limit)
	}
}

func BenchmarkWriteCallFrame(b *testing.B) {
	call := benchCall(100 << 10)
	b.ReportAllocs()
	b.SetBytes(int64(len(call.Request.Native.Body)))
	for b.Loop() {
		if err := WriteFrame(io.Discard, call); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkReadCallFrame(b *testing.B) {
	var buf bytes.Buffer
	if err := WriteFrame(&buf, benchCall(100<<10)); err != nil {
		b.Fatal(err)
	}
	frame := buf.Bytes()
	b.ReportAllocs()
	b.SetBytes(int64(len(frame)))
	for b.Loop() {
		var c Call
		if err := ReadFrame(bytes.NewReader(frame), &c); err != nil {
			b.Fatal(err)
		}
	}
}

// benchCall is a native-only generate call carrying a client body of about size bytes.
func benchCall(size int) Call {
	var b strings.Builder
	b.WriteString(`{"model":"claude-x","max_tokens":4096,"stream":true,"messages":[`)
	for i := 0; b.Len() < size; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"role":"user","content":[{"type":"text","text":"line %d of a prompt with <tags> & \"quotes\" and a little more text to fill the message out"}]}`, i)
	}
	b.WriteString(`]}`)
	return Call{Type: TypeCall, ID: "1", Op: OpGenerate, NativeOnly: true, Account: &Account{ID: "a"},
		Request: &llm.Request{Model: "claude-x", Stream: true,
			Native: &llm.Native{Format: llm.FormatAnthropicMessages, Body: []byte(b.String()), Headers: map[string]string{"anthropic-version": "2023-06-01"}}}}
}

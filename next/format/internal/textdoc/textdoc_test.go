package textdoc

import "testing"

func TestFromAnthropic(t *testing.T) {
	for _, c := range []struct {
		name  string
		raw   string
		text  string
		title string
		ok    bool
	}{
		{
			name:  "text document",
			raw:   `{"type":"document","source":{"type":"text","media_type":"text/plain","data":"Hello"},"title":"notes.txt"}`,
			text:  "Hello",
			title: "notes.txt",
			ok:    true,
		},
		{
			name: "text document without a title",
			raw:  `{"type":"document","source":{"type":"text","media_type":"text/plain","data":"Hello"}}`,
			text: "Hello",
			ok:   true,
		},
		{name: "base64 pdf", raw: `{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"AAAA"}}`},
		{name: "image", raw: `{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}}`},
		{name: "not json", raw: `{`},
		{name: "no source", raw: `{"type":"document","title":"x"}`},
	} {
		text, title, ok := FromAnthropic([]byte(c.raw))
		if text != c.text || title != c.title || ok != c.ok {
			t.Errorf("%s: FromAnthropic = %q, %q, %v; want %q, %q, %v", c.name, text, title, ok, c.text, c.title, c.ok)
		}
	}
}

func TestWrap(t *testing.T) {
	if got, want := Wrap("notes.txt", "one\ntwo"), "[Document: notes.txt]\none\ntwo\n[End of document: notes.txt]"; got != want {
		t.Errorf("Wrap with title = %q, want %q", got, want)
	}
	if got, want := Wrap("", "one\ntwo"), "[Document]\none\ntwo\n[End of document]"; got != want {
		t.Errorf("Wrap without title = %q, want %q", got, want)
	}
	if got, want := Wrap("a\nb", "x"), "[Document: a b]\nx\n[End of document: a b]"; got != want {
		t.Errorf("Wrap with a line break in the title = %q, want %q", got, want)
	}
}

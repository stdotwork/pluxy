package media

import "testing"

func TestBase64(t *testing.T) {
	for s, want := range map[string]bool{
		"TWFu":         true,
		"TWE=":         true,
		"TQ==":         true,
		"TWE":          true,
		"TQ":           true,
		"TWFuTQ==":     true,
		"TWFu\r\n":     true,
		"TW\r\nFu":     true,
		"TQ\r\n==":     true,
		"TW\nFu\rTQ==": true,
		"AA==":         true,
		"AAA=":         true,
		"AAAA":         true,
		"":             false,
		"T":            false,
		"TQ=":          false,
		"T===":         false,
		"TQ===":        false,
		"====":         false,
		"TWFu!":        false,
		"TW Fu":        false,
		"QQ==QQ==":     false,
		"-_-_":         false,
		"AAAA\nAA!A":   false,
		"\r\n":         false,
		"TQ=\n=":       true,
	} {
		if got := Base64(s); got != want {
			t.Errorf("Base64(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestDataURL(t *testing.T) {
	for _, c := range []struct {
		in        string
		mediaType string
		data      string
		ok        bool
	}{
		{"data:image/png;base64,AAAA", "image/png", "AAAA", true},
		{"data:image/svg+xml;base64,PHN2Zz4=", "image/svg+xml", "PHN2Zz4=", true},
		{"DATA:image/png;BASE64,AAAA", "image/png", "AAAA", true},
		{"Data:image/png;base64,AAAA", "image/png", "AAAA", true},
		{"data:image/png,AAAA", "", "", false},
		{"data:;base64,AAAA", "", "", false},
		{"data:image/png;base64", "", "", false},
		{"https://example.com/cat.png", "", "", false},
		{"", "", "", false},
	} {
		mediaType, data, ok := DataURL(c.in)
		if mediaType != c.mediaType || data != c.data || ok != c.ok {
			t.Errorf("DataURL(%q) = %q, %q, %v; want %q, %q, %v", c.in, mediaType, data, ok, c.mediaType, c.data, c.ok)
		}
	}
}

func TestInlineImage(t *testing.T) {
	allowed := []string{"image/png", "image/jpeg"}
	for _, c := range []struct {
		in      string
		problem string
	}{
		{"data:image/png;base64,AAAA", ""},
		{"data:image/jpeg;base64,AAAA", ""},
		{"data:image/gif;base64,AAAA", "its media type is image/gif"},
		{"data:image/png;base64,not base64", "its data is not valid base64"},
		{"data:image/png;base64,", "its data is not valid base64"},
		{"https://example.com/cat.png", "it is not a base64 data URL"},
	} {
		if _, problem := InlineImage(c.in, allowed); problem != c.problem {
			t.Errorf("InlineImage(%q) = %q, want %q", c.in, problem, c.problem)
		}
	}
}

func TestIsDataURL(t *testing.T) {
	for s, want := range map[string]bool{
		"data:image/png;base64,AAAA": true,
		"DATA:image/png;base64,AAAA": true,
		"Data:image/png;base64,AAAA": true,
		"data:":                      true,
		"data":                       false,
		"https://example.com":        false,
	} {
		if got := IsDataURL(s); got != want {
			t.Errorf("IsDataURL(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestFileDataOK(t *testing.T) {
	for s, want := range map[string]bool{
		"data:application/pdf;base64,JVBERi0=": true,
		"DATA:application/pdf;BASE64,JVBERi0=": true,
		"JVBERi0=":                             true,
		"data:application/pdf,raw":             false,
		"https://example.com/r.pdf":            true,
		"HTTPS://example.com/r.pdf":            true,
		"http://example.com/r.pdf":             true,
		"%%%":                                  false,
		"":                                     false,
		"ftp://example.com/r.pdf":              false,
	} {
		if got := FileDataOK(s); got != want {
			t.Errorf("FileDataOK(%q) = %v, want %v", s, got, want)
		}
	}
}

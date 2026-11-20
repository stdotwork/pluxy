package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http/httptest"
	"reflect"
	"testing"
)

// The multipart rebuild keeps every field, the filename, the media type and the file bytes.
func TestAudioMultipart(t *testing.T) {
	data := base64.StdEncoding.EncodeToString([]byte("RIFF\x00\xffWAVE"))
	form := `{"model":"whisper-1","response_format":"verbose_json","timestamp_granularities[]":["word","segment"],` +
		`"file":{"filename":"sample.wav","content_type":"audio/wav","data":"` + data + `"}}`
	body, contentType, err := audioMultipart(json.RawMessage(form))
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", bytes.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	if err := req.ParseMultipartForm(1 << 20); err != nil {
		t.Fatal(err)
	}
	if got := req.MultipartForm.Value["timestamp_granularities[]"]; !reflect.DeepEqual(got, []string{"word", "segment"}) {
		t.Errorf("timestamp_granularities[] = %v", got)
	}
	if got := req.MultipartForm.Value["response_format"]; !reflect.DeepEqual(got, []string{"verbose_json"}) {
		t.Errorf("response_format = %v", got)
	}
	f, fh, err := req.FormFile("file")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got, _ := io.ReadAll(f)
	if fh.Filename != "sample.wav" || fh.Header.Get("Content-Type") != "audio/wav" || !bytes.Equal(got, []byte("RIFF\x00\xffWAVE")) {
		t.Errorf("file = %q %q %q", fh.Filename, fh.Header.Get("Content-Type"), got)
	}
}

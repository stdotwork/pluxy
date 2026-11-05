package handlers

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
)

func readRequestBodyForTest(t *testing.T, body []byte, encoding string, maxBytes int64) ([]byte, error) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	if encoding != "" {
		c.Request.Header.Set("Content-Encoding", encoding)
	}
	if maxBytes > 0 {
		c.Set(MaxRequestBodyBytesContextKey, maxBytes)
	}
	return ReadRequestBody(c)
}

func zstdCompressForTest(t *testing.T, data []byte) []byte {
	t.Helper()
	encoder, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatalf("zstd.NewWriter: %v", err)
	}
	defer encoder.Close()
	return encoder.EncodeAll(data, nil)
}

func TestReadRequestBody_ZstdDecodesToOriginalBytes(t *testing.T) {
	original := []byte(`{"model":"gpt-5","input":"` + strings.Repeat("hello zstd ", 5000) + `"}`)
	compressed := zstdCompressForTest(t, original)
	if len(compressed) >= len(original) {
		t.Fatalf("test payload did not compress: %d >= %d", len(compressed), len(original))
	}

	// Repeat so that any decoder reuse across requests is exercised.
	for i := 0; i < 5; i++ {
		got, err := readRequestBodyForTest(t, compressed, "zstd", 0)
		if err != nil {
			t.Fatalf("request %d: ReadRequestBody: %v", i, err)
		}
		if !bytes.Equal(got, original) {
			t.Fatalf("request %d: decoded %d bytes, want the original %d bytes", i, len(got), len(original))
		}
	}
}

func TestReadRequestBody_ZstdSizeLimit(t *testing.T) {
	original := []byte(`{"input":"` + strings.Repeat("x", 4000) + `"}`)
	compressed := zstdCompressForTest(t, original)
	limit := int64(len(original))

	got, err := readRequestBodyForTest(t, compressed, "zstd", limit)
	if err != nil || !bytes.Equal(got, original) {
		t.Fatalf("body of exactly the limit: err=%v, equal=%v", err, bytes.Equal(got, original))
	}

	_, err = readRequestBodyForTest(t, compressed, "zstd", limit-1)
	var maxBytesErr *http.MaxBytesError
	if !errors.As(err, &maxBytesErr) || maxBytesErr.Limit != limit-1 {
		t.Fatalf("body over the limit: err=%v, want *http.MaxBytesError with Limit %d", err, limit-1)
	}
}

func TestReadRequestBody_ZstdInvalidBody(t *testing.T) {
	validJSON := []byte(`{"model":"gpt-5"}`)
	got, err := readRequestBodyForTest(t, validJSON, "zstd", 0)
	if err != nil || !bytes.Equal(got, validJSON) {
		t.Fatalf("valid JSON labelled zstd: got %q, err=%v, want the raw body", got, err)
	}

	notJSON := []byte("this is neither zstd nor json")
	if got, err = readRequestBodyForTest(t, notJSON, "zstd", 0); err == nil {
		t.Fatalf("invalid zstd that is not JSON: got %q, want an error", got)
	}
	var maxBytesErr *http.MaxBytesError
	if errors.As(err, &maxBytesErr) {
		t.Fatalf("invalid zstd reported as MaxBytesError: %v", err)
	}

	original := []byte(`{"input":"` + strings.Repeat("y", 4000) + `"}`)
	compressed := zstdCompressForTest(t, original)
	if _, err = readRequestBodyForTest(t, compressed[:len(compressed)/2], "zstd", 0); err == nil {
		t.Fatal("truncated zstd frame: want an error")
	}

	// A good body after the failures above still decodes.
	got, err = readRequestBodyForTest(t, compressed, "zstd", 0)
	if err != nil || !bytes.Equal(got, original) {
		t.Fatalf("good body after failures: err=%v, equal=%v", err, bytes.Equal(got, original))
	}
}

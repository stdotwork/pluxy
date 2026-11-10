package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

// A multipart edit reaches the extension in its JSON form: fields as members, numbers and stream
// typed, mask fields nested, image files appended to images and the mask file as a data URL.
func TestImagesEditFormToJSON(t *testing.T) {
	h, _, seen := endpointHandler(t, imagesReply)
	b := "--x\r\nContent-Disposition: form-data; name=\"prompt\"\r\n\r\nadd a hat\r\n" +
		"--x\r\nContent-Disposition: form-data; name=\"n\"\r\n\r\n2\r\n" +
		"--x\r\nContent-Disposition: form-data; name=\"stream\"\r\n\r\ntrue\r\n" +
		"--x\r\nContent-Disposition: form-data; name=\"mask[file_id]\"\r\n\r\nfile-m\r\n" +
		"--x\r\nContent-Disposition: form-data; name=\"image[]\"; filename=\"a.png\"\r\nContent-Type: image/png\r\n\r\nAB\r\n" +
		"--x\r\nContent-Disposition: form-data; name=\"image[]\"; filename=\"b\"\r\n\r\n%PDF-1.4\r\n" +
		"--x--\r\n"
	w := postEndpoint(h, "/v1/images/edits", b, map[string]string{"Content-Type": "multipart/form-data; boundary=x"})
	if w.Code != http.StatusOK {
		t.Fatalf("got %d %s", w.Code, w.Body)
	}
	req := (*seen)[0]
	var got map[string]any
	if err := json.Unmarshal(req.Native.Body, &got); err != nil {
		t.Fatal(err)
	}
	want := `{"images":[{"image_url":"data:image/png;base64,QUI="},{"image_url":"data:application/pdf;base64,JVBERi0xLjQ="}],"mask":{"file_id":"file-m"},"n":2,"prompt":"add a hat","stream":true}`
	if b, _ := json.Marshal(got); string(b) != want {
		t.Fatalf("body = %s\nwant %s", b, want)
	}
	if req.Model != defaultImageModel || !req.Stream || req.Native.Format != llm.FormatOpenAIImagesEdits {
		t.Fatalf("request = %+v", req)
	}
}

// Images requests without a prompt, a form edit without an image file, and an edit in another
// content type are refused before routing; JSON edits need no image file.
func TestImagesRefusals(t *testing.T) {
	h, accounts, _ := endpointHandler(t, imagesReply)
	for _, tc := range []struct{ path, ct, body, want string }{
		{"/v1/images/generations", "application/json", `{"model":"gpt-image-2"}`, "prompt is required"},
		{"/v1/images/edits", "application/json", `{"prompt":" "}`, "prompt is required"},
		{"/v1/images/edits", "multipart/form-data; boundary=x", "--x\r\nContent-Disposition: form-data; name=\"prompt\"\r\n\r\nhi\r\n--x--\r\n", "image is required"},
		{"/v1/images/edits", "text/plain", "hi", `unsupported Content-Type \"text/plain\"`},
		{"/v1/images/edits", "multipart/form-data", "hi", "no boundary"},
	} {
		w := postEndpoint(h, tc.path, tc.body, map[string]string{"Content-Type": tc.ct})
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), tc.want) {
			t.Errorf("%s %s: got %d %s", tc.path, tc.ct, w.Code, w.Body)
		}
	}
	if len(*accounts) != 0 {
		t.Fatalf("attempts went to %v", *accounts)
	}
	w := postEndpoint(h, "/v1/images/edits", `{"prompt":"hi","model":"gpt-image-1.5"}`, map[string]string{"Content-Type": "application/json"})
	if w.Code != http.StatusOK || len(*accounts) != 1 {
		t.Fatalf("a JSON edit: got %d %s", w.Code, w.Body)
	}
}

// imagesReply answers an images request with an empty JSON body in the request's own format.
func imagesReply(_ string, req llm.Request) []llm.Event {
	evs := bodyReply("application/json", `{}`)
	evs[0].Response.Format = req.Native.Format
	return evs
}

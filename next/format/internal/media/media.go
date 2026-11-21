// Package media reads the inline media a request carries and checks it before a format encoder
// hands it to an upstream. Nothing here decodes an image or fetches a URL: a data URL is split
// into its media type and base64 text, and the base64 is checked in place, so the request body
// stays the only copy of the bytes.
package media

import "strings"

// DataURL splits a data: URL into its media type and base64 data. It does not decode the data.
// ok is false for anything that is not a data URL carrying base64.
func DataURL(s string) (mediaType, data string, ok bool) {
	rest, found := strings.CutPrefix(s, "data:")
	if !found {
		return "", "", false
	}
	meta, data, found := strings.Cut(rest, ",")
	if !found {
		return "", "", false
	}
	mediaType, found = strings.CutSuffix(meta, ";base64")
	if !found || mediaType == "" {
		return "", "", false
	}
	return mediaType, data, true
}

// Type returns a data URL's media type normalised for comparison and for sending upstream: lower
// case, with any parameters after the first semicolon removed.
func Type(mediaType string) string {
	if i := strings.IndexByte(mediaType, ';'); i >= 0 {
		mediaType = mediaType[:i]
	}
	return strings.ToLower(mediaType)
}

// Base64 reports whether s is base64 of the standard alphabet, padded or not, without decoding
// it. A data URL carries its bytes in the request body already, so validation reads them in
// place instead of making a second copy of the image.
func Base64(s string) bool {
	n := len(s)
	pad := 0
	for n > 0 && s[n-1] == '=' {
		n--
		pad++
	}
	if n == 0 || pad > 2 || (pad > 0 && len(s)%4 != 0) {
		return false
	}
	for i := 0; i < n; i++ {
		c := s[i]
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '+' || c == '/') {
			return false
		}
	}
	switch n % 4 {
	case 0:
		return true
	case 2:
		return pad == 0 || pad == 2
	case 3:
		return pad == 0 || pad == 1
	}
	return false
}

// InlineImage checks an inline image against the media types a target accepts. url must be a
// base64 data URL of one of allowed. It returns the media type it found and, when the image is
// not acceptable, a short reason: "it is not a base64 data URL", "its data is not valid base64"
// or "its media type is X". The empty reason means the image can be sent.
func InlineImage(url string, allowed []string) (mediaType, problem string) {
	mediaType, data, ok := DataURL(url)
	if !ok {
		return "", "it is not a base64 data URL"
	}
	mediaType = Type(mediaType)
	if !Base64(data) {
		return mediaType, "its data is not valid base64"
	}
	for _, a := range allowed {
		if mediaType == a {
			return mediaType, ""
		}
	}
	return mediaType, "its media type is " + mediaType
}

// AnyImage checks an inline image without a target's media type list: its data must be base64
// and its media type an image type. The Chat Completions and Responses encoders also serve
// OpenAI-compatible vendors and Kimi, which take image types OpenAI's own list does not name,
// so they check only this much.
func AnyImage(url string) (mediaType, problem string) {
	mediaType, data, ok := DataURL(url)
	if !ok {
		return "", "it is not a base64 data URL"
	}
	mediaType = Type(mediaType)
	if !Base64(data) {
		return mediaType, "its data is not valid base64"
	}
	if !strings.HasPrefix(mediaType, "image/") {
		return mediaType, "its media type is " + mediaType
	}
	return mediaType, ""
}

// The image media types each target documents for its inline image input.
var (
	// ClaudeImages is what Claude accepts in an image block: JPEG, PNG, GIF and WebP
	// (https://docs.anthropic.com/en/docs/build-with-claude/vision#supported-formats).
	ClaudeImages = []string{"image/jpeg", "image/png", "image/gif", "image/webp"}
	// GeminiImages is what GenerateContent's inlineData documents
	// (https://ai.google.dev/api/generate-content#Blob); the vision guide lists the same set
	// without the GIF and AVIF the API reference adds
	// (https://ai.google.dev/gemini-api/docs/image-understanding#supported-formats).
	GeminiImages = []string{"image/png", "image/jpeg", "image/jpg", "image/webp", "image/heic", "image/heif", "image/gif", "image/avif"}
)

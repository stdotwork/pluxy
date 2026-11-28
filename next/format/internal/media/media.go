package media

import "strings"

const dataScheme = "data:"

// IsDataURL reports whether s starts with the data URL scheme. RFC 3986 section 3.1 makes the
// scheme case-insensitive, so DATA: and Data: are data URLs too.
func IsDataURL(s string) bool {
	return len(s) >= len(dataScheme) && strings.EqualFold(s[:len(dataScheme)], dataScheme)
}

func DataURL(s string) (mediaType, data string, ok bool) {
	if !IsDataURL(s) {
		return "", "", false
	}
	meta, data, found := strings.Cut(s[len(dataScheme):], ",")
	if !found {
		return "", "", false
	}
	const marker = ";base64"
	if len(meta) < len(marker) || !strings.EqualFold(meta[len(meta)-len(marker):], marker) {
		return "", "", false
	}
	mediaType = meta[:len(meta)-len(marker)]
	if mediaType == "" {
		return "", "", false
	}
	return mediaType, data, true
}

func FileDataOK(s string) bool {
	if IsDataURL(s) {
		_, data, ok := DataURL(s)
		return ok && Base64(data)
	}
	lower := strings.ToLower(s)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		return true
	}
	return Base64(s)
}

func Type(mediaType string) string {
	if i := strings.IndexByte(mediaType, ';'); i >= 0 {
		mediaType = mediaType[:i]
	}
	return strings.ToLower(mediaType)
}

func Base64(s string) bool {
	chars := 0
	pad := 0
	padded := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\r' || c == '\n':
			continue // MIME wraps base64 at 76 characters with CRLF; it stays base64.
		case c == '=':
			pad++
			padded = true
		case padded:
			return false
		case c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '+' || c == '/':
			chars++
		default:
			return false
		}
	}
	if chars == 0 || pad > 2 {
		return false
	}
	if padded {
		return (chars+pad)%4 == 0 && ((chars%4 == 2 && pad == 2) || (chars%4 == 3 && pad == 1))
	}
	return chars%4 != 1
}

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

// The Chat Completions and Responses encoders also serve OpenAI-compatible vendors and Kimi,
// which take image types OpenAI's own list does not name, so they check only this much.
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

var (
	ClaudeImages = []string{"image/jpeg", "image/png", "image/gif", "image/webp"}
	GeminiImages = []string{"image/png", "image/jpeg", "image/jpg", "image/webp", "image/heic", "image/heif", "image/gif", "image/avif"}
)

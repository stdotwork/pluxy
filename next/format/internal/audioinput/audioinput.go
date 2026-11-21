// Package audioinput reads and writes the audio a Chat Completions or Responses request carries
// inline, as the canonical file part whose data URL holds it.
package audioinput

import (
	"strings"

	"github.com/stdotwork/pluxy/next/format/internal/media"
	"github.com/stdotwork/pluxy/next/llm"
)

// MIME returns the media type of an audio format token, the way today's core mapped it for
// Gemini: Chat and Responses name a wav or mp3 blob, or one of the live API's formats, with a bare
// token. An empty token is a wav blob.
func MIME(format string) string {
	switch format {
	case "", "wav":
		return "audio/wav"
	case "mp3":
		return "audio/mpeg"
	case "ogg":
		return "audio/ogg"
	case "flac":
		return "audio/flac"
	case "aac":
		return "audio/aac"
	case "webm":
		return "audio/webm"
	case "pcm16":
		return "audio/pcm"
	case "g711_ulaw", "g711_alaw":
		return "audio/basic"
	}
	return "audio/" + format
}

// Format returns the token for an audio media type: the reverse of MIME, for a media type MIME
// did not produce.
func Format(mediaType string) string {
	switch mediaType {
	case "audio/wav":
		return "wav"
	case "audio/mpeg":
		return "mp3"
	case "audio/pcm":
		return "pcm16"
	case "audio/basic":
		// Both live API spellings map to this media type; ulaw is the one to name back.
		return "g711_ulaw"
	}
	return strings.TrimPrefix(mediaType, "audio/")
}

// DataURL returns the inline form of audio bytes: a base64 data URL.
func DataURL(mediaType, data string) string {
	return "data:" + mediaType + ";base64," + data
}

// Inline returns the media type and the base64 bytes of p when p is a file part holding inline
// audio.
func Inline(p llm.Part) (mediaType, data string, ok bool) {
	if p.Type != llm.PartFile {
		return "", "", false
	}
	mediaType, data, ok = media.DataURL(p.FileData)
	if !ok || data == "" || !strings.HasPrefix(strings.ToLower(mediaType), "audio/") {
		return "", "", false
	}
	return mediaType, data, true
}

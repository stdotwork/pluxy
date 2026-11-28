package audioinput

import (
	"strings"

	"github.com/stdotwork/pluxy/next/format/internal/media"
	"github.com/stdotwork/pluxy/next/llm"
)

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

func Format(mediaType string) string {
	mediaType = media.Type(mediaType)
	switch mediaType {
	case "audio/wav":
		return "wav"
	case "audio/mpeg":
		return "mp3"
	case "audio/pcm":
		return "pcm16"
	case "audio/basic":
		return "g711_ulaw"
	}
	return strings.TrimPrefix(mediaType, "audio/")
}

func DataURL(mediaType, data string) string {
	return "data:" + mediaType + ";base64," + data
}

func Inline(p llm.Part) (mediaType, data string, ok bool) {
	if p.Type != llm.PartFile {
		return "", "", false
	}
	mediaType, data, ok = media.DataURL(p.FileData)
	if !ok || data == "" {
		return "", "", false
	}
	mediaType = media.Type(mediaType)
	if !strings.HasPrefix(mediaType, "audio/") {
		return "", "", false
	}
	return mediaType, data, true
}

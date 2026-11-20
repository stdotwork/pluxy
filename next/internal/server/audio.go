package server

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/stdotwork/pluxy/next/llm"
)

// audioSpeechEndpoint is OpenAI's speech endpoint. Its JSON body and the upstream's Content-Type
// go through as they came. A binary reply travels to core as base64 body pieces so the extension
// protocol's text pieces do not corrupt it; an SSE reply is text and crosses unchanged.
var audioSpeechEndpoint = endpoint{
	format: llm.FormatOpenAIAudioSpeech,
}

// audioTranscriptionsEndpoint is OpenAI's transcription endpoint, whose multipart body core reads
// into the JSON form described by llm.FormatOpenAIAudioTranscriptions.
var audioTranscriptionsEndpoint = endpoint{
	format: llm.FormatOpenAIAudioTranscriptions,
	form:   true,
	check:  needAudioFile,
}

// audioTranslationsEndpoint is OpenAI's translation endpoint, with the same form as
// transcriptions.
var audioTranslationsEndpoint = endpoint{
	format: llm.FormatOpenAIAudioTranslations,
	form:   true,
	check:  needAudioFile,
}

// needAudioFile refuses a transcription or translation without its uploaded audio file.
func needAudioFile(body map[string]json.RawMessage, _ bool) *llm.Error {
	var file struct {
		Data string `json:"data"`
	}
	if json.Unmarshal(body["file"], &file) != nil || strings.TrimSpace(file.Data) == "" {
		return &llm.Error{Code: llm.CodeInvalidRequest, Status: http.StatusBadRequest, Scope: llm.ScopeRequest,
			Message: "Invalid request: file is required"}
	}
	return nil
}

// audioBodyIsBase64 reports whether format's reply body is binary base64. Speech is the only
// binary endpoint; its SSE form is text.
func audioBodyIsBase64(format, contentType string) bool {
	return format == llm.FormatOpenAIAudioSpeech && !strings.HasPrefix(contentType, "text/event-stream")
}

// decodeAudioBody decodes one base64 piece of a binary speech reply.
func decodeAudioBody(piece string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(piece)
}

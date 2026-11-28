package server

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/stdotwork/pluxy/next/llm"
)

var audioSpeechEndpoint = endpoint{
	format: llm.FormatOpenAIAudioSpeech,
}

var audioTranscriptionsEndpoint = endpoint{
	format: llm.FormatOpenAIAudioTranscriptions,
	form:   true,
	check:  needAudioFile,
}

var audioTranslationsEndpoint = endpoint{
	format: llm.FormatOpenAIAudioTranslations,
	form:   true,
	check:  needAudioFile,
}

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

func audioBodyIsBase64(format, contentType string) bool {
	return format == llm.FormatOpenAIAudioSpeech && !strings.HasPrefix(contentType, "text/event-stream")
}

func decodeAudioBody(piece string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(piece)
}

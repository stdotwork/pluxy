package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/sdk"
)

const maxAudioReply = 64 << 20

// maxGeminiInlineRequestBytes is Google's documented 20 MB total request size limit for inline
// audio data (https://ai.google.dev/gemini-api/docs/audio#inline-audio).
var maxGeminiInlineRequestBytes = 20 * 1000 * 1000

var audioBinaryChunkSize = 768 << 10

var audioTextChunkSize = 1 << 20

// defaultGeminiPCMRate is Google's documented sample rate for Gemini TTS linear 16-bit PCM output
// (24 kHz, mono, 16-bit signed little-endian).
const defaultGeminiPCMRate = 24000

var openAIToGeminiTTSVoices = map[string]string{
	"alloy":   "Kore",
	"ash":     "Iapetus",
	"ballad":  "Algieba",
	"cedar":   "Achird",
	"coral":   "Sulafat",
	"echo":    "Charon",
	"fable":   "Umbriel",
	"marin":   "Despina",
	"onyx":    "Orus",
	"sage":    "Vindemiatrix",
	"shimmer": "Achernar",
	"verse":   "Puck",
}

// supportedGeminiAudioMIMEs lists the audio MIME types documented by Google's audio understanding
// page (https://ai.google.dev/gemini-api/docs/audio#supported-formats), plus common aliases
// normalized to Google's documented spelling.
var supportedGeminiAudioMIMEs = map[string]string{
	"audio/wav":    "audio/wav",
	"audio/x-wav":  "audio/wav",
	"audio/wave":   "audio/wav",
	"audio/mp3":    "audio/mp3",
	"audio/mpeg":   "audio/mpeg",
	"audio/aiff":   "audio/aiff",
	"audio/x-aiff": "audio/aiff",
	"audio/aac":    "audio/aac",
	"audio/ogg":    "audio/ogg",
	"audio/flac":   "audio/flac",
	"audio/x-flac": "audio/flac",
	"audio/m4a":    "audio/m4a",
	"audio/x-m4a":  "audio/m4a",
	"audio/mp4":    "audio/m4a",
	"audio/l16":    "audio/l16",
	"audio/opus":   "audio/opus",
	"audio/alaw":   "audio/alaw",
	"audio/mulaw":  "audio/mulaw",
	"audio/webm":   "audio/webm",
}

var audioExtToMIME = map[string]string{
	".wav":  "audio/wav",
	".mp3":  "audio/mpeg",
	".mpeg": "audio/mpeg",
	".mpga": "audio/mpeg",
	".aiff": "audio/aiff",
	".aif":  "audio/aiff",
	".aac":  "audio/aac",
	".ogg":  "audio/ogg",
	".oga":  "audio/ogg",
	".flac": "audio/flac",
	".m4a":  "audio/m4a",
	".mp4":  "audio/m4a",
	".opus": "audio/opus",
	".webm": "audio/webm",
}

type speechRequest struct {
	input          string
	voice          string
	responseFormat string
}

type speechGenerateBody struct {
	Contents         []audioContent       `json:"contents"`
	GenerationConfig speechGenerateConfig `json:"generationConfig"`
}

type speechGenerateConfig struct {
	ResponseModalities []string          `json:"responseModalities"`
	SpeechConfig       speechVoiceConfig `json:"speechConfig"`
}

type speechVoiceConfig struct {
	VoiceConfig speechSingleVoice `json:"voiceConfig"`
}

type speechSingleVoice struct {
	PrebuiltVoiceConfig speechPrebuiltVoice `json:"prebuiltVoiceConfig"`
}

type speechPrebuiltVoice struct {
	VoiceName string `json:"voiceName"`
}

type audioContent struct {
	Role  string      `json:"role"`
	Parts []audioPart `json:"parts"`
}

type audioPart struct {
	Text       string           `json:"text,omitempty"`
	InlineData *audioInlineData `json:"inlineData,omitempty"`
}

type audioInlineData struct {
	MIMEType string `json:"mimeType"`
	Data     string `json:"data"`
}

type transcriptionRequest struct {
	mimeType       string
	dataBase64     string
	responseFormat string
	language       string
	prompt         string
	temperature    *float64
}

type transcriptionGenerateBody struct {
	Contents         []audioContent               `json:"contents"`
	GenerationConfig *transcriptionGenerateConfig `json:"generationConfig,omitempty"`
}

type transcriptionGenerateConfig struct {
	Temperature *float64 `json:"temperature,omitempty"`
}

type geminiAudioReply struct {
	Candidates []struct {
		Content *struct {
			Parts []struct {
				Text       string `json:"text"`
				Thought    bool   `json:"thought"`
				InlineData *struct {
					MIMEType string `json:"mimeType"`
					Data     string `json:"data"`
				} `json:"inlineData"`
			} `json:"parts"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	PromptFeedback *struct {
		BlockReason string `json:"blockReason"`
	} `json:"promptFeedback"`
	UsageMetadata *struct {
		PromptTokenCount        *int64 `json:"promptTokenCount"`
		CandidatesTokenCount    *int64 `json:"candidatesTokenCount"`
		CachedContentTokenCount *int64 `json:"cachedContentTokenCount"`
		ThoughtsTokenCount      *int64 `json:"thoughtsTokenCount"`
	} `json:"usageMetadata"`
}

func (r *geminiAudioReply) usage() *llm.Usage {
	if r.UsageMetadata == nil {
		return nil
	}
	u := &llm.Usage{
		InputTokens:       r.UsageMetadata.PromptTokenCount,
		OutputTokens:      r.UsageMetadata.CandidatesTokenCount,
		CachedInputTokens: r.UsageMetadata.CachedContentTokenCount,
		ReasoningTokens:   r.UsageMetadata.ThoughtsTokenCount,
	}
	if u.InputTokens == nil && u.OutputTokens == nil && u.CachedInputTokens == nil && u.ReasoningTokens == nil {
		return nil
	}
	return u
}

func audioSpeech(ctx context.Context, call sdk.Call, out *sdk.Stream) error {
	return audioSpeechTo(ctx, call, out)
}

func audioSpeechTo(ctx context.Context, call sdk.Call, out sender) (err error) {
	req, err := readSpeechRequest(call.Request)
	if err != nil {
		return err
	}
	s, err := readSettings(call.Account.Settings)
	if err != nil {
		return err
	}
	model := strings.TrimPrefix(call.Request.Model, "models/")
	t, err := resolve(ctx, s, call.Account.Credential, model, false)
	if err != nil {
		return err
	}
	send := &reporting{out: out}
	if t.token != "" {
		send.redact = protocolRedactor(t.token)
		defer func() {
			var e *llm.Error
			if errors.As(err, &e) {
				err = send.redact.Event(llm.Event{Type: llm.EventError, Error: e}).Error
			}
		}()
	}

	payload := speechGenerateBody{
		Contents: []audioContent{{
			Role:  "user",
			Parts: []audioPart{{Text: req.input}},
		}},
		GenerationConfig: speechGenerateConfig{
			ResponseModalities: []string{"AUDIO"},
			SpeechConfig: speechVoiceConfig{
				VoiceConfig: speechSingleVoice{
					PrebuiltVoiceConfig: speechPrebuiltVoice{VoiceName: req.voice},
				},
			},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	raw, err := postAudioGenerateContent(ctx, t, model, body, "speech")
	if err != nil {
		return err
	}

	var reply geminiAudioReply
	if err := json.Unmarshal(raw, &reply); err != nil {
		return upstreamAudioError("Gemini's speech reply is not JSON")
	}
	audioBytes, contentType, err := extractSpeechAudio(&reply, req.responseFormat)
	if err != nil {
		return err
	}
	if err := send.StartBody(llm.FormatOpenAIAudioSpeech, contentType); err != nil {
		return err
	}
	if err := sendBinaryBase64Chunks(send, audioBytes, audioBinaryChunkSize); err != nil {
		return err
	}
	return send.Finish(llm.Finish{Reason: llm.FinishStop}, reply.usage())
}

func audioTranscriptions(ctx context.Context, call sdk.Call, out *sdk.Stream) error {
	return audioTranscriptionsTo(ctx, call, out)
}

func audioTranscriptionsTo(ctx context.Context, call sdk.Call, out sender) (err error) {
	req, err := readTranscriptionRequest(call.Request)
	if err != nil {
		return err
	}
	s, err := readSettings(call.Account.Settings)
	if err != nil {
		return err
	}
	model := strings.TrimPrefix(call.Request.Model, "models/")
	t, err := resolve(ctx, s, call.Account.Credential, model, false)
	if err != nil {
		return err
	}
	send := &reporting{out: out}
	if t.token != "" {
		send.redact = protocolRedactor(t.token)
		defer func() {
			var e *llm.Error
			if errors.As(err, &e) {
				err = send.redact.Event(llm.Event{Type: llm.EventError, Error: e}).Error
			}
		}()
	}

	instruction := buildTranscriptionInstruction(req.language, req.prompt)
	payload := transcriptionGenerateBody{
		Contents: []audioContent{{
			Role: "user",
			Parts: []audioPart{
				{Text: instruction},
				{InlineData: &audioInlineData{MIMEType: req.mimeType, Data: req.dataBase64}},
			},
		}},
	}
	if req.temperature != nil {
		payload.GenerationConfig = &transcriptionGenerateConfig{Temperature: req.temperature}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if len(body) > maxGeminiInlineRequestBytes {
		return unsupportedAudio(fmt.Sprintf("file: inline audio request exceeds Gemini's %d MB inline limit", maxGeminiInlineRequestBytes/1_000_000))
	}

	raw, err := postAudioGenerateContent(ctx, t, model, body, "transcription")
	if err != nil {
		return err
	}
	var reply geminiAudioReply
	if err := json.Unmarshal(raw, &reply); err != nil {
		return upstreamAudioError("Gemini's transcription reply is not JSON")
	}
	text, err := extractTranscriptionText(&reply)
	if err != nil {
		return err
	}

	contentType := "application/json"
	var replyBytes []byte
	if req.responseFormat == "text" {
		contentType = "text/plain; charset=utf-8"
		replyBytes = []byte(text)
	} else {
		replyBytes, err = json.Marshal(struct {
			Text string `json:"text"`
		}{Text: text})
		if err != nil {
			return err
		}
	}

	if err := send.StartBody(llm.FormatOpenAIAudioTranscriptions, contentType); err != nil {
		return err
	}
	bw := newChunkedBodyWriter(send, audioTextChunkSize)
	if _, err := bw.Write(replyBytes); err != nil {
		return err
	}
	if err := bw.Close(); err != nil {
		return err
	}
	return send.Finish(llm.Finish{Reason: llm.FinishStop}, reply.usage())
}

func postAudioGenerateContent(ctx context.Context, t target, model string, body []byte, kind string) ([]byte, error) {
	resp, err := postBody(ctx, t, body, false, model)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		return nil, upstreamAudioError("Gemini streamed a " + kind + " reply")
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxAudioReply+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, upstreamAudioError("reading upstream response: " + err.Error())
	}
	if len(raw) > maxAudioReply {
		return nil, upstreamAudioError(fmt.Sprintf("the upstream's reply is larger than %d MiB", maxAudioReply>>20))
	}
	return raw, nil
}

func sendBinaryBase64Chunks(send *reporting, data []byte, chunkSize int) error {
	if chunkSize <= 0 {
		chunkSize = 768 << 10
	}
	for len(data) > 0 {
		n := min(len(data), chunkSize)
		if err := send.Body(base64.StdEncoding.EncodeToString(data[:n])); err != nil {
			return err
		}
		data = data[n:]
	}
	return nil
}

func readSpeechRequest(r llm.Request) (speechRequest, error) {
	var out speechRequest
	if r.Native == nil {
		return out, badAudioRequest("the request body is missing")
	}
	var body map[string]json.RawMessage
	if json.Unmarshal(r.Native.Body, &body) != nil || body == nil {
		return out, badAudioRequest("the request body is not a JSON object")
	}
	var input string
	if raw, ok := body["input"]; !ok || string(raw) == "null" {
		return out, badAudioRequest("input is required")
	} else if err := json.Unmarshal(raw, &input); err != nil || input == "" {
		return out, badAudioRequest("input must be a non-empty string")
	}
	out.input = input

	voiceRaw, hasVoice := body["voice"]
	if !hasVoice || string(voiceRaw) == "null" {
		return out, badAudioRequest("voice is required")
	}
	var voiceStr string
	if err := json.Unmarshal(voiceRaw, &voiceStr); err == nil {
		voiceStr = strings.TrimSpace(voiceStr)
		if voiceStr == "" {
			return out, badAudioRequest("voice must not be empty")
		}
		lower := strings.ToLower(voiceStr)
		if lower == "nova" {
			return out, unsupportedAudio(`voice: Gemini speech has no mapping for OpenAI voice "nova"`)
		}
		if mapped, ok := openAIToGeminiTTSVoices[lower]; ok {
			out.voice = mapped
		} else {
			out.voice = voiceStr
		}
	} else {
		var voiceObj map[string]json.RawMessage
		if json.Unmarshal(voiceRaw, &voiceObj) == nil && voiceObj != nil {
			return out, unsupportedAudio("voice: Gemini generateContent speech does not support custom voice objects")
		}
		return out, badAudioRequest("voice must be a string")
	}

	if r.Options.ThinkingSuffix != "" {
		return out, unsupportedAudio("model: a thinking suffix on the model name is not supported for Gemini speech")
	}
	if string(body["stream"]) == "true" || r.Stream {
		return out, unsupportedAudio("stream: Gemini speech does not support streaming")
	}

	format := "mp3"
	if raw, ok := body["response_format"]; ok && string(raw) != "null" {
		if err := json.Unmarshal(raw, &format); err != nil {
			return out, unsupportedAudio("response_format: Gemini speech serves \"pcm\" and \"wav\" only")
		}
		format = strings.ToLower(strings.TrimSpace(format))
	}
	switch format {
	case "pcm", "wav":
		out.responseFormat = format
	default:
		return out, unsupportedAudio(fmt.Sprintf("response_format: Gemini speech serves \"pcm\" and \"wav\" only, not %q", format))
	}

	if raw, ok := body["stream_format"]; ok && string(raw) != "null" {
		var sf string
		if err := json.Unmarshal(raw, &sf); err != nil {
			return out, unsupportedAudio("stream_format: Gemini speech supports \"audio\" only")
		}
		if strings.ToLower(strings.TrimSpace(sf)) != "audio" {
			return out, unsupportedAudio(fmt.Sprintf("stream_format: Gemini speech does not support %q", sf))
		}
	}

	if raw, ok := body["speed"]; ok && string(raw) != "null" {
		var speed float64
		if err := json.Unmarshal(raw, &speed); err != nil || math.IsNaN(speed) || math.IsInf(speed, 0) || speed != 1.0 {
			return out, unsupportedAudio("speed: Gemini generateContent speech does not support speed other than 1.0")
		}
	}

	if raw, ok := body["instructions"]; ok && string(raw) != "null" {
		var instr string
		if err := json.Unmarshal(raw, &instr); err != nil {
			return out, unsupportedAudio("instructions: Gemini generateContent speech does not support instructions")
		}
		if strings.TrimSpace(instr) != "" {
			return out, unsupportedAudio("instructions: Gemini generateContent speech does not support instructions")
		}
	}

	return out, nil
}

func extractSpeechAudio(reply *geminiAudioReply, wantFormat string) ([]byte, string, error) {
	var (
		pcmChunks    [][]byte
		wavBytes     []byte
		sampleRate   = defaultGeminiPCMRate
		channels     = 1
		foundKind    string
		finishReason string
	)
	for _, cand := range reply.Candidates {
		if cand.FinishReason != "" && cand.FinishReason != "STOP" {
			return nil, "", upstreamAudioError("Gemini speech reply stopped early (finish reason " + cand.FinishReason + ")")
		}
		if finishReason == "" && cand.FinishReason != "" {
			finishReason = cand.FinishReason
		}
		if cand.Content == nil {
			continue
		}
		for _, part := range cand.Content.Parts {
			if part.InlineData == nil || part.InlineData.Data == "" {
				continue
			}
			mt, params, err := mime.ParseMediaType(part.InlineData.MIMEType)
			if err != nil {
				return nil, "", upstreamAudioError("Gemini returned an audio part with an invalid mimeType")
			}
			mt = strings.ToLower(mt)
			decoded, err := base64.StdEncoding.DecodeString(part.InlineData.Data)
			if err != nil {
				return nil, "", upstreamAudioError("Gemini returned invalid base64 audio data")
			}
			switch mt {
			case "audio/l16", "audio/pcm":
				if foundKind == "wav" {
					return nil, "", upstreamAudioError("Gemini mixed WAV and PCM audio parts in one reply")
				}
				foundKind = "pcm"
				if rStr := strings.TrimSpace(params["rate"]); rStr != "" {
					rVal, err := strconv.Atoi(rStr)
					if err != nil || rVal <= 0 || rVal > 384000 {
						return nil, "", upstreamAudioError(fmt.Sprintf("Gemini returned an invalid PCM sample rate %q", rStr))
					}
					sampleRate = rVal
				}
				if cStr := strings.TrimSpace(params["channels"]); cStr != "" {
					cVal, err := strconv.Atoi(cStr)
					if err != nil || cVal < 1 || cVal > 8 {
						return nil, "", upstreamAudioError(fmt.Sprintf("Gemini returned an invalid PCM channel count %q", cStr))
					}
					channels = cVal
				}
				pcmChunks = append(pcmChunks, decoded)
			case "audio/wav", "audio/x-wav", "audio/wave":
				if foundKind == "pcm" || len(wavBytes) > 0 {
					return nil, "", upstreamAudioError("Gemini returned multiple or mixed audio parts in one speech reply")
				}
				foundKind = "wav"
				wavBytes = decoded
			default:
				return nil, "", upstreamAudioError(fmt.Sprintf("Gemini returned unsupported speech mimeType %q", part.InlineData.MIMEType))
			}
		}
	}

	if foundKind == "" {
		msg := "Gemini returned no audio"
		if finishReason != "" {
			msg += " (finish reason " + finishReason + ")"
		} else if reply.PromptFeedback != nil && reply.PromptFeedback.BlockReason != "" {
			msg += " (blocked: " + reply.PromptFeedback.BlockReason + ")"
		}
		return nil, "", upstreamAudioError(msg)
	}

	if foundKind == "pcm" {
		pcm := bytes.Join(pcmChunks, nil)
		if wantFormat == "pcm" {
			return pcm, "audio/pcm", nil
		}
		wav, err := wrapPCMAsWAV(pcm, sampleRate, channels, 16)
		if err != nil {
			return nil, "", err
		}
		return wav, "audio/wav", nil
	}

	if len(wavBytes) < 12 || string(wavBytes[0:4]) != "RIFF" || string(wavBytes[8:12]) != "WAVE" {
		return nil, "", upstreamAudioError("Gemini returned audio/wav without a RIFF/WAVE header")
	}
	if wantFormat == "wav" {
		return wavBytes, "audio/wav", nil
	}
	pcm, err := stripWAVHeaderToPCM(wavBytes)
	if err != nil {
		return nil, "", err
	}
	return pcm, "audio/pcm", nil
}

func wrapPCMAsWAV(pcm []byte, sampleRate, channels, bitsPerSample int) ([]byte, error) {
	if uint64(len(pcm)) > math.MaxUint32-36 {
		return nil, upstreamAudioError("speech audio exceeds WAV size limit")
	}
	dataSize := uint32(len(pcm))
	byteRate := uint32(sampleRate * channels * (bitsPerSample / 8))
	blockAlign := uint16(channels * (bitsPerSample / 8))

	out := make([]byte, 44+len(pcm))
	copy(out[0:4], "RIFF")
	binary.LittleEndian.PutUint32(out[4:8], 36+dataSize)
	copy(out[8:12], "WAVE")
	copy(out[12:16], "fmt ")
	binary.LittleEndian.PutUint32(out[16:20], 16)
	binary.LittleEndian.PutUint16(out[20:22], 1)
	binary.LittleEndian.PutUint16(out[22:24], uint16(channels))
	binary.LittleEndian.PutUint32(out[24:28], uint32(sampleRate))
	binary.LittleEndian.PutUint32(out[28:32], byteRate)
	binary.LittleEndian.PutUint16(out[32:34], blockAlign)
	binary.LittleEndian.PutUint16(out[34:36], uint16(bitsPerSample))
	copy(out[36:40], "data")
	binary.LittleEndian.PutUint32(out[40:44], dataSize)
	copy(out[44:], pcm)
	return out, nil
}

func stripWAVHeaderToPCM(wav []byte) ([]byte, error) {
	pos := 12
	var isPCM bool
	for pos+8 <= len(wav) {
		chunkID := string(wav[pos : pos+4])
		chunkSize := int(binary.LittleEndian.Uint32(wav[pos+4 : pos+8]))
		pos += 8
		if chunkSize < 0 || pos+chunkSize > len(wav) {
			return nil, upstreamAudioError("Gemini returned a truncated WAV chunk")
		}
		switch chunkID {
		case "fmt ":
			if chunkSize < 16 {
				return nil, upstreamAudioError("Gemini returned a malformed WAV fmt chunk")
			}
			audioFmt := binary.LittleEndian.Uint16(wav[pos : pos+2])
			bits := binary.LittleEndian.Uint16(wav[pos+14 : pos+16])
			isPCM = audioFmt == 1 && bits == 16
		case "data":
			if !isPCM {
				return nil, upstreamAudioError("Gemini returned a non-PCM WAV file")
			}
			return wav[pos : pos+chunkSize], nil
		}
		pos += chunkSize
		if chunkSize%2 == 1 && pos < len(wav) {
			pos++
		}
	}
	return nil, upstreamAudioError("Gemini returned a WAV file with no data chunk")
}

func readTranscriptionRequest(r llm.Request) (transcriptionRequest, error) {
	var out transcriptionRequest
	if r.Native == nil {
		return out, badAudioRequest("the request body is missing")
	}
	var body map[string]json.RawMessage
	if json.Unmarshal(r.Native.Body, &body) != nil || body == nil {
		return out, badAudioRequest("the request body is not a JSON object")
	}

	var file struct {
		Filename    string `json:"filename"`
		ContentType string `json:"content_type"`
		Data        string `json:"data"`
	}
	if err := json.Unmarshal(body["file"], &file); err != nil || strings.TrimSpace(file.Data) == "" {
		return out, badAudioRequest("file is required")
	}
	if _, err := base64.StdEncoding.DecodeString(file.Data); err != nil {
		return out, badAudioRequest("file.data is not valid base64")
	}

	if r.Options.ThinkingSuffix != "" {
		return out, unsupportedAudio("model: a thinking suffix on the model name is not supported for Gemini transcriptions")
	}
	if string(body["stream"]) == "true" || r.Stream {
		return out, unsupportedAudio("stream: Gemini transcription does not support streaming")
	}

	mt, err := resolveGeminiAudioMIME(file.ContentType, file.Filename)
	if err != nil {
		return out, err
	}
	out.mimeType = mt
	out.dataBase64 = file.Data

	format := "json"
	if raw, ok := body["response_format"]; ok && string(raw) != "null" {
		if err := json.Unmarshal(raw, &format); err != nil {
			return out, unsupportedAudio("response_format: Gemini transcription serves \"json\" and \"text\" only")
		}
		format = strings.ToLower(strings.TrimSpace(format))
	}
	switch format {
	case "json", "text":
		out.responseFormat = format
	default:
		return out, unsupportedAudio(fmt.Sprintf("response_format: Gemini transcription serves \"json\" and \"text\" only, not %q", format))
	}

	for _, field := range []struct {
		keys []string
		name string
	}{
		{[]string{"timestamp_granularities[]", "timestamp_granularities"}, "timestamp_granularities"},
		{[]string{"include[]", "include"}, "include"},
		{[]string{"known_speaker_names[]", "known_speaker_names"}, "known_speaker_names"},
		{[]string{"known_speaker_references[]", "known_speaker_references"}, "known_speaker_references"},
		{[]string{"chunking_strategy"}, "chunking_strategy"},
	} {
		for _, k := range field.keys {
			if hasNonEmptyField(body[k]) {
				return out, unsupportedAudio(fmt.Sprintf("%s: Gemini generateContent transcription does not support %s", field.name, field.name))
			}
		}
	}

	if raw, ok := body["language"]; ok && string(raw) != "null" {
		var lang string
		if err := json.Unmarshal(raw, &lang); err != nil {
			return out, unsupportedAudio("language: Gemini transcription expects language as a string")
		}
		out.language = strings.TrimSpace(lang)
	}
	if raw, ok := body["prompt"]; ok && string(raw) != "null" {
		var p string
		if err := json.Unmarshal(raw, &p); err != nil {
			return out, unsupportedAudio("prompt: Gemini transcription expects prompt as a string")
		}
		out.prompt = strings.TrimSpace(p)
	}
	if raw, ok := body["temperature"]; ok && string(raw) != "null" {
		temp, err := parseFormFloat(raw)
		if err != nil || math.IsNaN(temp) || math.IsInf(temp, 0) || temp < 0 || temp > 1 {
			return out, unsupportedAudio("temperature: Gemini transcription supports temperature between 0 and 1")
		}
		out.temperature = &temp
	}

	return out, nil
}

func resolveGeminiAudioMIME(contentType, filename string) (string, error) {
	mt := ""
	if strings.TrimSpace(contentType) != "" {
		parsed, _, err := mime.ParseMediaType(contentType)
		if err == nil {
			mt = strings.ToLower(strings.TrimSpace(parsed))
		}
	}
	if mt == "" || mt == "application/octet-stream" {
		ext := strings.ToLower(filepath.Ext(strings.TrimSpace(filename)))
		if inferred, ok := audioExtToMIME[ext]; ok {
			return inferred, nil
		}
		return "", unsupportedAudio("file: Gemini transcription does not recognise this audio file's media type")
	}
	if normalized, ok := supportedGeminiAudioMIMEs[mt]; ok {
		return normalized, nil
	}
	return "", unsupportedAudio(fmt.Sprintf("file: Gemini transcription does not support media type %q", mt))
}

func hasNonEmptyField(raw json.RawMessage) bool {
	if len(raw) == 0 || string(raw) == "null" {
		return false
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s) != ""
	}
	var list []json.RawMessage
	if json.Unmarshal(raw, &list) == nil {
		return len(list) > 0
	}
	return true
}

func parseFormFloat(raw json.RawMessage) (float64, error) {
	var f float64
	if err := json.Unmarshal(raw, &f); err == nil {
		return f, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strconv.ParseFloat(strings.TrimSpace(s), 64)
	}
	return 0, errors.New("not a number")
}

func buildTranscriptionInstruction(language, prompt string) string {
	var b strings.Builder
	b.WriteString("Generate a verbatim transcript of the spoken words in this audio. Return only the transcript text, with no extra commentary.")
	if language != "" {
		b.WriteString("\nThe audio is in language: ")
		b.WriteString(language)
		b.WriteString(".")
	}
	if prompt != "" {
		b.WriteString("\nContext or style hint: ")
		b.WriteString(prompt)
	}
	return b.String()
}

func extractTranscriptionText(reply *geminiAudioReply) (string, error) {
	if len(reply.Candidates) == 0 {
		msg := "Gemini returned no transcription candidate"
		if reply.PromptFeedback != nil && reply.PromptFeedback.BlockReason != "" {
			msg += " (blocked: " + reply.PromptFeedback.BlockReason + ")"
		}
		return "", upstreamAudioError(msg)
	}
	cand := reply.Candidates[0]
	if cand.FinishReason != "" && cand.FinishReason != "STOP" {
		return "", upstreamAudioError("Gemini transcription reply stopped early (finish reason " + cand.FinishReason + ")")
	}
	var parts []string
	if cand.Content != nil {
		for _, p := range cand.Content.Parts {
			if p.Thought || p.Text == "" {
				continue
			}
			parts = append(parts, p.Text)
		}
	}
	return strings.Join(parts, ""), nil
}

func badAudioRequest(message string) *llm.Error {
	return &llm.Error{Code: llm.CodeInvalidRequest, Status: http.StatusBadRequest, Scope: llm.ScopeRequest, Message: message}
}

func unsupportedAudio(message string) *llm.Error {
	return &llm.Error{Code: llm.CodeUnsupported, Status: http.StatusBadRequest, Scope: llm.ScopeCredential, Retryable: true, Message: message}
}

func upstreamAudioError(message string) *llm.Error {
	return &llm.Error{Code: llm.CodeUpstream, Status: http.StatusBadGateway, Scope: llm.ScopeRequest, Message: message}
}

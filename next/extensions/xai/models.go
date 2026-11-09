package main

import (
	"context"
	_ "embed"
	"encoding/json"

	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

// modelsJSON is the list an API key or a Grok login serves, in protocol.ModelList form: the text
// models of today's core's catalog (core/internal/registry/models/models.json, "xai"), with their
// metadata. It leaves out the image and video models of that catalog and the speech models today's
// core adds (grok-imagine-*, grok-tts, grok-voice-tts-1.0): they answer on xAI's image, video and
// speech endpoints (core/internal/runtime/executor/xai_executor_media.go, xai_executor_speech.go),
// and this extension sends only Responses requests.
//
//go:embed models.json
var modelsJSON []byte

// models lists the models an account serves when its configuration names none. Today's core lists
// the same models for an API key and for a Grok login (core/sdk/cliproxy/service_models.go, case
// "xai"), and gives them owned_by xai.
func models(_ context.Context, _ sdk.ModelsCall) (*protocol.ModelList, error) {
	var list protocol.ModelList
	if err := json.Unmarshal(modelsJSON, &list); err != nil {
		return nil, err
	}
	return &list, nil
}

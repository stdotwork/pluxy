package translator

import (
	"context"

	"github.com/luispater/CLIProxyAPI/internal/interfaces"
	log "github.com/sirupsen/logrus"
)

var (
	Requests  map[string]map[string]interfaces.TranslateRequestFunc
	Responses map[string]map[string]interfaces.TranslateResponse
)

func init() {
	Requests = make(map[string]map[string]interfaces.TranslateRequestFunc)
	Responses = make(map[string]map[string]interfaces.TranslateResponse)
}

func Register(from, to string, request interfaces.TranslateRequestFunc, response interfaces.TranslateResponse) {
	log.Debugf("Registering translator from %s to %s", from, to)
	if _, ok := Requests[from]; !ok {
		Requests[from] = make(map[string]interfaces.TranslateRequestFunc)
	}
	Requests[from][to] = request

	if _, ok := Responses[from]; !ok {
		Responses[from] = make(map[string]interfaces.TranslateResponse)
	}
	Responses[from][to] = response
}

func Request(from, to, modelName string, rawJSON []byte, stream bool) []byte {
	if translator, ok := Requests[from][to]; ok {
		return translator(modelName, rawJSON, stream)
	}
	return rawJSON
}

func NeedConvert(from, to string) bool {
	_, ok := Responses[from][to]
	return ok
}

func Response(from, to string, ctx context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) []string {
	if translator, ok := Responses[from][to]; ok {
		return translator.Stream(ctx, modelName, originalRequestRawJSON, requestRawJSON, rawJSON, param)
	}
	return []string{string(rawJSON)}
}

func ResponseNonStream(from, to string, ctx context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) string {
	if translator, ok := Responses[from][to]; ok {
		return translator.NonStream(ctx, modelName, originalRequestRawJSON, requestRawJSON, rawJSON, param)
	}
	return string(rawJSON)
}

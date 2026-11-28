package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/stdotwork/pluxy/next/internal/jsonscan"
	"github.com/stdotwork/pluxy/next/internal/route"
	"github.com/stdotwork/pluxy/next/llm"
)

const PinHeader = "X-Pluxy-Account"

var sessionHeaders = []string{"X-Claude-Code-Session-Id", "Session-Id", "X-Session-Id", "X-Session-Affinity"}

func routeRequest(r *http.Request, req llm.Request, body []byte) route.Request {
	if t := traceOf(r); t != nil {
		t.running.describe(req)
	}
	return route.Request{
		LLM:     req,
		Tenant:  tenant(r),
		Session: sessionKey(r, body),
		Pin:     strings.TrimSpace(r.Header.Get(PinHeader)),
	}
}

func tenant(r *http.Request) string {
	token := clientToken(r, true)
	if token == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:8])
}

func sessionKey(r *http.Request, body []byte) string {
	for _, h := range sessionHeaders {
		if v := strings.TrimSpace(r.Header.Get(h)); v != "" {
			return h + ":" + v
		}
	}
	if k := promptCacheKey(body); k != "" {
		return "prompt_cache_key:" + k
	}
	return ""
}

func promptCacheKey(body []byte) string {
	members, ok := jsonscan.Object(body)
	if !ok {
		return ""
	}
	key := ""
	for _, m := range members {
		name, ok := jsonscan.Unquote(m.Key)
		if !ok {
			return promptCacheKeyDecoded(body) // a name encoding/json repairs
		}
		if !strings.EqualFold(name, "prompt_cache_key") {
			continue
		}
		switch {
		case jsonscan.IsNull(m.Value):
		case jsonscan.IsString(m.Value):
			if key, ok = jsonscan.Unquote(m.Value); !ok {
				return promptCacheKeyDecoded(body)
			}
		default:
			return ""
		}
	}
	return key
}

func promptCacheKeyDecoded(body []byte) string {
	var b struct {
		PromptCacheKey string `json:"prompt_cache_key"`
	}
	if json.Unmarshal(body, &b) != nil {
		return ""
	}
	return b.PromptCacheKey
}

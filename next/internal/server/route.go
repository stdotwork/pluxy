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

// PinHeader names the account a request must use. A request that pins an account that does
// not serve its model fails with model_not_found; one whose pinned account is blocked fails
// rather than going elsewhere.
const PinHeader = "X-Pluxy-Account"

// sessionHeaders are the headers clients use to name their session, most specific first.
var sessionHeaders = []string{"X-Claude-Code-Session-Id", "Session-Id", "X-Session-Id", "X-Session-Affinity"}

// routeRequest gathers what routing needs from a client request.
func routeRequest(r *http.Request, req llm.Request, body []byte) route.Request {
	return route.Request{
		LLM:     req,
		Tenant:  tenant(r),
		Session: sessionKey(r, body),
		Pin:     strings.TrimSpace(r.Header.Get(PinHeader)),
	}
}

// tenant identifies the caller by a digest of its client key, so session bindings of one
// client key never route another's requests. It reads every header a client key may come in:
// where authorization reads fewer, the ones it reads come first, so the key is the same.
func tenant(r *http.Request) string {
	token := clientToken(r, true)
	if token == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:8])
}

// sessionKey returns the client's session key: a session header, or else the body's
// prompt_cache_key (OpenAI's own key for routing requests that share a prompt prefix).
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

// promptCacheKey returns the body's prompt_cache_key as json.Unmarshal reads it into a struct
// with that one string field: the last member named so in any letter case, or "" when the body
// is not a JSON object or such a member holds neither a string nor null. It reads only the top
// level of the body, without decoding the rest.
func promptCacheKey(body []byte) string {
	members, ok := jsonscan.Object(body)
	if !ok {
		return ""
	}
	key := ""
	for _, m := range members {
		name, ok := jsonscan.Unquote(m.Key)
		if !ok {
			return promptCacheKeyDecoded(body) // a name encoding/json repairs: let it read the body
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
			return "" // json.Unmarshal fails on the type
		}
	}
	return key
}

// promptCacheKeyDecoded is promptCacheKey by json.Unmarshal.
func promptCacheKeyDecoded(body []byte) string {
	var b struct {
		PromptCacheKey string `json:"prompt_cache_key"`
	}
	if json.Unmarshal(body, &b) != nil {
		return ""
	}
	return b.PromptCacheKey
}

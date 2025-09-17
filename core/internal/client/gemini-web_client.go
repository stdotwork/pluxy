package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/luispater/CLIProxyAPI/v5/internal/auth/gemini"
	geminiWeb "github.com/luispater/CLIProxyAPI/v5/internal/client/gemini-web"
	"github.com/luispater/CLIProxyAPI/v5/internal/config"
	. "github.com/luispater/CLIProxyAPI/v5/internal/constant"
	"github.com/luispater/CLIProxyAPI/v5/internal/interfaces"
	"github.com/luispater/CLIProxyAPI/v5/internal/translator/translator"
	"github.com/luispater/CLIProxyAPI/v5/internal/util"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// This file wires the external-facing client for Gemini Web.

// Defaults for Gemini Web behavior that are no longer configurable via YAML.
const (
	// geminiWebDefaultTimeoutSec defines the per-request HTTP timeout seconds.
	geminiWebDefaultTimeoutSec = 300
	// geminiWebDefaultRefreshIntervalSec defines background cookie auto-refresh interval seconds.
	geminiWebDefaultRefreshIntervalSec = 540
	// geminiWebDefaultPersistIntervalSec defines how often rotated cookies are persisted to disk (3 hours).
	geminiWebDefaultPersistIntervalSec = 10800
)

type GeminiWebClient struct {
	ClientBase
	gwc           *geminiWeb.GeminiClient
	tokenFilePath string
	convStore     map[string][]string
	convMutex     sync.RWMutex

	// JSON-based conversation persistence
	convData  map[string]geminiWeb.ConversationRecord
	convIndex map[string]string

	// restart-stable id for conversation hashing/lookup
	stableClientID string

	cookieRotationStarted bool
	cookiePersistCancel   context.CancelFunc
	lastPersistedTS       string

	// register models once after successful auth init
	modelsRegistered bool
}

func (c *GeminiWebClient) UnregisterClient() {
	if c.cookiePersistCancel != nil {
		c.cookiePersistCancel()
		c.cookiePersistCancel = nil
	}
	// Flush sidecar cookies to main token file and remove sidecar
	c.flushCookiesSidecarToMain()
	if c.gwc != nil {
		c.gwc.Close(0)
		c.gwc = nil
	}
	c.ClientBase.UnregisterClient()
}

func NewGeminiWebClient(cfg *config.Config, ts *gemini.GeminiWebTokenStorage, tokenFilePath string) (*GeminiWebClient, error) {
	jar, _ := cookiejar.New(nil)
	httpClient := util.SetProxy(cfg, &http.Client{Jar: jar})

	// derive a restart-stable id from tokens (sha256 of 1PSID, hex prefix)
	stableSuffix := geminiWeb.Sha256Hex(ts.Secure1PSID)
	if len(stableSuffix) > 16 {
		stableSuffix = stableSuffix[:16]
	}
	idPrefix := stableSuffix
	if len(idPrefix) > 8 {
		idPrefix = idPrefix[:8]
	}
	clientID := fmt.Sprintf("gemini-web-%s-%d", idPrefix, time.Now().UnixNano())

	client := &GeminiWebClient{
		ClientBase: ClientBase{
			RequestMutex:       &sync.Mutex{},
			httpClient:         httpClient,
			cfg:                cfg,
			tokenStorage:       ts,
			modelQuotaExceeded: make(map[string]*time.Time),
		},
		tokenFilePath:  tokenFilePath,
		convStore:      make(map[string][]string),
		convData:       make(map[string]geminiWeb.ConversationRecord),
		convIndex:      make(map[string]string),
		stableClientID: "gemini-web-" + stableSuffix,
	}
	// Load persisted conversation stores
	if store, err := geminiWeb.LoadConvStore(geminiWeb.ConvStorePath(tokenFilePath)); err == nil {
		client.convStore = store
	}
	if items, index, err := geminiWeb.LoadConvData(geminiWeb.ConvDataPath(tokenFilePath)); err == nil {
		client.convData = items
		client.convIndex = index
	}

	client.InitializeModelRegistry(clientID)

	// Prefer sidecar cookies at startup if present
	if ok, err := geminiWeb.ApplyCookiesSidecarToTokenStorage(tokenFilePath, ts); err == nil && ok {
		log.Debugf("Loaded Gemini Web cookies from sidecar: %s", filepath.Base(geminiWeb.CookiesSidecarPath(tokenFilePath)))
	}

	client.gwc = geminiWeb.NewGeminiClient(ts.Secure1PSID, ts.Secure1PSIDTS, cfg.ProxyURL, geminiWeb.WithAccountLabel(strings.TrimSuffix(filepath.Base(tokenFilePath), ".json")))
	timeoutSec := geminiWebDefaultTimeoutSec
	refreshIntervalSec := cfg.GeminiWeb.TokenRefreshSeconds
	if refreshIntervalSec <= 0 {
		refreshIntervalSec = geminiWebDefaultRefreshIntervalSec
	}
	if err := client.gwc.Init(float64(timeoutSec), false, 300, true, float64(refreshIntervalSec), false); err != nil {
		log.Warnf("Gemini Web init failed for %s: %v. Will retry in background.", client.GetEmail(), err)
		go client.backgroundInitRetry()
	} else {
		client.cookieRotationStarted = true
		// Persist immediately once after successful init to capture fresh cookies
		_ = client.SaveTokenToFile()
		client.startCookiePersist()
	}
	return client, nil
}

func (c *GeminiWebClient) Init() error {
	ts := c.tokenStorage.(*gemini.GeminiWebTokenStorage)
	c.gwc = geminiWeb.NewGeminiClient(ts.Secure1PSID, ts.Secure1PSIDTS, c.cfg.ProxyURL, geminiWeb.WithAccountLabel(c.GetEmail()))
	timeoutSec := geminiWebDefaultTimeoutSec
	refreshIntervalSec := c.cfg.GeminiWeb.TokenRefreshSeconds
	if refreshIntervalSec <= 0 {
		refreshIntervalSec = geminiWebDefaultRefreshIntervalSec
	}
	if err := c.gwc.Init(float64(timeoutSec), false, 300, true, float64(refreshIntervalSec), false); err != nil {
		return err
	}
	c.registerModelsOnce()
	// Persist immediately once after successful init to capture fresh cookies
	_ = c.SaveTokenToFile()
	c.startCookiePersist()
	return nil
}

// IsReady reports whether the underlying Gemini Web client is initialized and running.
func (c *GeminiWebClient) IsReady() bool {
	return c != nil && c.gwc != nil && c.gwc.Running
}

func (c *GeminiWebClient) registerModelsOnce() {
	if c.modelsRegistered {
		return
	}
	c.RegisterModels(GEMINI, geminiWeb.GetGeminiWebAliasedModels())
	c.modelsRegistered = true
}

// EnsureRegistered registers models if the client is ready and not yet registered.
// It is safe to call multiple times.
func (c *GeminiWebClient) EnsureRegistered() {
	if c.IsReady() {
		c.registerModelsOnce()
	}
}

func (c *GeminiWebClient) Type() string     { return GEMINI }
func (c *GeminiWebClient) Provider() string { return GEMINI }
func (c *GeminiWebClient) CanProvideModel(modelName string) bool {
	geminiWeb.EnsureGeminiWebAliasMap()
	_, ok := geminiWeb.GeminiWebAliasMap[strings.ToLower(modelName)]
	return ok
}
func (c *GeminiWebClient) GetEmail() string {
	base := filepath.Base(c.tokenFilePath)
	return strings.TrimSuffix(base, ".json")
}
func (c *GeminiWebClient) StableClientID() string {
	if c.stableClientID != "" {
		return c.stableClientID
	}
	sum := geminiWeb.Sha256Hex(c.GetEmail())
	if len(sum) > 16 {
		sum = sum[:16]
	}
	return "gemini-web-" + sum
}

// useReusableContext reports whether JSON-based reusable conversation matching is enabled.
// Controlled by `gemini-web.context` boolean in config (true enables reuse, default true).
func (c *GeminiWebClient) useReusableContext() bool {
	if c == nil || c.cfg == nil {
		return true
	}
	return c.cfg.GeminiWeb.Context
}

// chatPrep encapsulates shared request preparation results for both stream and non-stream flows.
type chatPrep struct {
	chat          *geminiWeb.ChatSession
	prompt        string
	uploaded      []string
	reuse         bool
	metaLen       int
	handlerType   string
	tagged        bool
	underlying    string
	cleaned       []geminiWeb.RoleText
	translatedRaw []byte
}

// prepareChat performs translation, message parsing, metadata reuse, prompt build and StartChat.
func (c *GeminiWebClient) prepareChat(ctx context.Context, modelName string, rawJSON []byte, isStream bool) (*chatPrep, *interfaces.ErrorMessage) {
	res := &chatPrep{}
	if handler, ok := ctx.Value("handler").(interfaces.APIHandler); ok {
		res.handlerType = handler.HandlerType()
		rawJSON = translator.Request(res.handlerType, c.Type(), modelName, rawJSON, isStream)
	}
	res.translatedRaw = rawJSON
	if c.cfg.RequestLog {
		if ginContext, ok := ctx.Value("gin").(*gin.Context); ok {
			ginContext.Set("API_REQUEST", rawJSON)
		}
	}
	messages, files, mimes, msgFileIdx, err := geminiWeb.ParseMessagesAndFiles(rawJSON)
	if err != nil {
		return nil, &interfaces.ErrorMessage{StatusCode: 400, Error: fmt.Errorf("bad request: %w", err)}
	}
	cleaned := geminiWeb.SanitizeAssistantMessages(messages)
	res.cleaned = cleaned
	res.underlying = geminiWeb.MapAliasToUnderlying(modelName)
	model, err := geminiWeb.ModelFromName(res.underlying)
	if err != nil {
		return nil, &interfaces.ErrorMessage{StatusCode: 400, Error: err}
	}

	var (
		meta        []string
		useMsgs     []geminiWeb.RoleText
		filesSubset [][]byte
		mimesSubset []string
	)
	if c.useReusableContext() {
		reuseMeta, remaining := c.findReusableSession(res.underlying, cleaned)
		res.reuse = len(reuseMeta) > 0
		if res.reuse {
			meta = reuseMeta
			if len(remaining) == 1 {
				useMsgs = []geminiWeb.RoleText{remaining[0]}
			} else {
				useMsgs = remaining
			}
		} else {
			meta = nil
			useMsgs = cleaned
		}
		res.tagged = geminiWeb.NeedRoleTags(useMsgs)
		if res.reuse && len(useMsgs) == 1 {
			res.tagged = false
		}
		if res.reuse && len(useMsgs) == 1 && len(messages) > 0 {
			lastIdx := len(messages) - 1
			if lastIdx >= 0 && lastIdx < len(msgFileIdx) {
				for _, fi := range msgFileIdx[lastIdx] {
					if fi >= 0 && fi < len(files) {
						filesSubset = append(filesSubset, files[fi])
						if fi < len(mimes) {
							mimesSubset = append(mimesSubset, mimes[fi])
						} else {
							mimesSubset = append(mimesSubset, "")
						}
					}
				}
			}
		} else {
			filesSubset = files
			mimesSubset = mimes
		}
		res.metaLen = len(meta)
	} else {
		key := geminiWeb.AccountMetaKey(c.GetEmail(), modelName)
		c.convMutex.RLock()
		meta = c.convStore[key]
		c.convMutex.RUnlock()
		useMsgs = cleaned
		res.tagged = geminiWeb.NeedRoleTags(useMsgs)
		filesSubset = files
		mimesSubset = mimes
		res.reuse = false
		res.metaLen = len(meta)
	}

	uploadedFiles, upErr := geminiWeb.MaterializeInlineFiles(filesSubset, mimesSubset)
	if upErr != nil {
		return nil, upErr
	}
	res.uploaded = uploadedFiles

	// XML hint follows code-mode only:
	// - code-mode = true  -> enable XML wrapping hint
	// - code-mode = false -> disable XML wrapping hint
	enableXMLHint := c.cfg != nil && c.cfg.GeminiWeb.CodeMode
	useMsgs = geminiWeb.AppendXMLWrapHintIfNeeded(useMsgs, !enableXMLHint)
	res.prompt = geminiWeb.BuildPrompt(useMsgs, res.tagged, res.tagged)
	if strings.TrimSpace(res.prompt) == "" {
		return nil, &interfaces.ErrorMessage{StatusCode: 400, Error: errors.New("bad request: empty prompt after filtering system/thought content")}
	}
	c.appendUpstreamRequestLog(ctx, modelName, res.tagged, true, res.prompt, len(uploadedFiles), res.reuse, res.metaLen)
	gem := c.getConfiguredGem()
	res.chat = c.gwc.StartChat(model, gem, meta)
	return res, nil
}

func (c *GeminiWebClient) SendRawMessage(ctx context.Context, modelName string, rawJSON []byte, alt string) ([]byte, *interfaces.ErrorMessage) {
	original := bytes.Clone(rawJSON)
	prep, prepErr := c.prepareChat(ctx, modelName, rawJSON, false)
	if prepErr != nil {
		return nil, prepErr
	}
	defer geminiWeb.CleanupFiles(prep.uploaded)
	log.Debugf("Use Gemini Web account %s for model %s", c.GetEmail(), modelName)
	out, genErr := geminiWeb.SendWithSplit(prep.chat, prep.prompt, prep.uploaded, c.cfg)
	if genErr != nil {
		return nil, c.handleSendError(genErr, modelName)
	}
	gemBytes, errMsg := c.handleSendSuccess(ctx, prep, &out, modelName)
	if errMsg != nil {
		return nil, errMsg
	}
	if translator.NeedConvert(prep.handlerType, c.Type()) {
		var param any
		out := translator.ResponseNonStream(prep.handlerType, c.Type(), ctx, modelName, original, prep.translatedRaw, gemBytes, &param)
		if prep.handlerType == OPENAI && out != "" {
			newID := fmt.Sprintf("chatcmpl-%x", time.Now().UnixNano())
			if v := gjson.Parse(out).Get("id"); v.Exists() {
				out, _ = sjson.Set(out, "id", newID)
			}
		}
		return []byte(out), nil
	}
	return gemBytes, nil
}

func (c *GeminiWebClient) SendRawMessageStream(ctx context.Context, modelName string, rawJSON []byte, alt string) (<-chan []byte, <-chan *interfaces.ErrorMessage) {
	dataChan := make(chan []byte)
	errChan := make(chan *interfaces.ErrorMessage)
	go func() {
		defer close(dataChan)
		defer close(errChan)
		original := bytes.Clone(rawJSON)
		prep, prepErr := c.prepareChat(ctx, modelName, rawJSON, true)
		if prepErr != nil {
			errChan <- prepErr
			return
		}
		defer geminiWeb.CleanupFiles(prep.uploaded)
		log.Debugf("Use Gemini Web account %s for model %s", c.GetEmail(), modelName)
		out, genErr := geminiWeb.SendWithSplit(prep.chat, prep.prompt, prep.uploaded, c.cfg)
		if genErr != nil {
			errChan <- c.handleSendError(genErr, modelName)
			return
		}
		gemBytes, errMsg := c.handleSendSuccess(ctx, prep, &out, modelName)
		if errMsg != nil {
			errChan <- errMsg
			return
		}
		// Branch by handler type:
		// - Native Gemini handler: emit at most two messages (thoughts, then others), no [DONE].
		// - Translated handlers (e.g., OpenAI Responses): split first payload into two (if thoughts exist), then emit translator's [DONE].
		if prep.handlerType == GEMINI {
			root := gjson.ParseBytes(gemBytes)
			parts := root.Get("candidates.0.content.parts")
			if parts.Exists() && parts.IsArray() {
				var thoughtArr, otherArr strings.Builder
				thoughtCount := 0
				thoughtArr.WriteByte('[')
				otherArr.WriteByte('[')
				firstThought := true
				firstOther := true
				parts.ForEach(func(_, part gjson.Result) bool {
					if part.Get("thought").Bool() {
						if !firstThought {
							thoughtArr.WriteByte(',')
						}
						thoughtArr.WriteString(part.Raw)
						firstThought = false
						thoughtCount++
					} else {
						if !firstOther {
							otherArr.WriteByte(',')
						}
						otherArr.WriteString(part.Raw)
						firstOther = false
					}
					return true
				})
				thoughtArr.WriteByte(']')
				otherArr.WriteByte(']')
				if thoughtCount > 0 {
					thoughtOnly, _ := sjson.SetRaw(string(gemBytes), "candidates.0.content.parts", thoughtArr.String())
					// Only when the first chunk contains thoughts, set finishReason to null
					thoughtOnly, _ = sjson.SetRaw(thoughtOnly, "candidates.0.finishReason", "null")
					dataChan <- []byte(thoughtOnly)
				}
				othersOnly, _ := sjson.SetRaw(string(gemBytes), "candidates.0.content.parts", otherArr.String())
				// Do not modify finishReason for non-thought first chunks or subsequent chunks
				dataChan <- []byte(othersOnly)
				return
			}
			// Fallback: no parts array; emit single message
			// No special handling when no parts or no thoughts
			dataChan <- gemBytes
			return
		}

		// Translated handlers: when code-mode is ON, merge <think> into content and emit a single chunk; otherwise keep split.
		newCtx := context.WithValue(ctx, "alt", alt)
		var param any
		if c.cfg.GeminiWeb.CodeMode {
			combined := mergeThoughtIntoSingleContent(gemBytes)
			lines := translator.Response(prep.handlerType, c.Type(), newCtx, modelName, original, prep.translatedRaw, combined, &param)
			for _, l := range lines {
				if l != "" {
					dataChan <- []byte(l)
				}
			}
			done := translator.Response(prep.handlerType, c.Type(), newCtx, modelName, original, prep.translatedRaw, []byte("[DONE]"), &param)
			for _, l := range done {
				if l != "" {
					dataChan <- []byte(l)
				}
			}
			return
		}
		root := gjson.ParseBytes(gemBytes)
		parts := root.Get("candidates.0.content.parts")
		if parts.Exists() && parts.IsArray() {
			var thoughtArr, otherArr strings.Builder
			thoughtCount := 0
			thoughtArr.WriteByte('[')
			otherArr.WriteByte('[')
			firstThought := true
			firstOther := true
			parts.ForEach(func(_, part gjson.Result) bool {
				if part.Get("thought").Bool() {
					if !firstThought {
						thoughtArr.WriteByte(',')
					}
					thoughtArr.WriteString(part.Raw)
					firstThought = false
					thoughtCount++
				} else {
					if !firstOther {
						otherArr.WriteByte(',')
					}
					otherArr.WriteString(part.Raw)
					firstOther = false
				}
				return true
			})
			thoughtArr.WriteByte(']')
			otherArr.WriteByte(']')

			if thoughtCount > 0 {
				thoughtOnly, _ := sjson.SetRaw(string(gemBytes), "candidates.0.content.parts", thoughtArr.String())
				// Only when the first chunk contains thoughts, suppress finishReason before translation
				thoughtOnly, _ = sjson.Delete(thoughtOnly, "candidates.0.finishReason")
				// If CodeMode enabled, demote thought parts to content before translating
				if c.cfg.GeminiWeb.CodeMode {
					processed := collapseThoughtPartsToContent([]byte(thoughtOnly))
					lines := translator.Response(prep.handlerType, c.Type(), newCtx, modelName, original, prep.translatedRaw, processed, &param)
					for _, l := range lines {
						if l != "" {
							dataChan <- []byte(l)
						}
					}
				} else {
					lines := translator.Response(prep.handlerType, c.Type(), newCtx, modelName, original, prep.translatedRaw, []byte(thoughtOnly), &param)
					for _, l := range lines {
						if l != "" {
							dataChan <- []byte(l)
						}
					}
				}
			}
			othersOnly, _ := sjson.SetRaw(string(gemBytes), "candidates.0.content.parts", otherArr.String())
			// Do not modify finishReason if there is no thought chunk
			if c.cfg.GeminiWeb.CodeMode {
				processed := collapseThoughtPartsToContent([]byte(othersOnly))
				lines := translator.Response(prep.handlerType, c.Type(), newCtx, modelName, original, prep.translatedRaw, processed, &param)
				for _, l := range lines {
					if l != "" {
						dataChan <- []byte(l)
					}
				}
			} else {
				lines := translator.Response(prep.handlerType, c.Type(), newCtx, modelName, original, prep.translatedRaw, []byte(othersOnly), &param)
				for _, l := range lines {
					if l != "" {
						dataChan <- []byte(l)
					}
				}
			}
			done := translator.Response(prep.handlerType, c.Type(), newCtx, modelName, original, prep.translatedRaw, []byte("[DONE]"), &param)
			for _, l := range done {
				if l != "" {
					dataChan <- []byte(l)
				}
			}
			return
		}
		// Fallback: no parts array; forward as a single translated payload then DONE
		// If code-mode is ON, still merge to a single content block.
		if c.cfg.GeminiWeb.CodeMode {
			processed := mergeThoughtIntoSingleContent(gemBytes)
			lines := translator.Response(prep.handlerType, c.Type(), newCtx, modelName, original, prep.translatedRaw, processed, &param)
			for _, l := range lines {
				if l != "" {
					dataChan <- []byte(l)
				}
			}
		} else {
			lines := translator.Response(prep.handlerType, c.Type(), newCtx, modelName, original, prep.translatedRaw, gemBytes, &param)
			for _, l := range lines {
				if l != "" {
					dataChan <- []byte(l)
				}
			}
		}
		done := translator.Response(prep.handlerType, c.Type(), newCtx, modelName, original, prep.translatedRaw, []byte("[DONE]"), &param)
		for _, l := range done {
			if l != "" {
				dataChan <- []byte(l)
			}
		}
	}()
	return dataChan, errChan
}

func (c *GeminiWebClient) handleSendError(genErr error, modelName string) *interfaces.ErrorMessage {
	log.Errorf("failed to generate content: %v", genErr)
	status := 500
	var eUsage *geminiWeb.UsageLimitExceeded
	var eTempBlock *geminiWeb.TemporarilyBlocked
	if errors.As(genErr, &eUsage) || errors.As(genErr, &eTempBlock) {
		status = 429
	}
	var eModelInvalid *geminiWeb.ModelInvalid
	if status == 500 && errors.As(genErr, &eModelInvalid) {
		status = 400
	}
	var eValue *geminiWeb.ValueError
	if status == 500 && errors.As(genErr, &eValue) {
		status = 400
	}
	var eTimeout *geminiWeb.TimeoutError
	if status == 500 && errors.As(genErr, &eTimeout) {
		status = 504
	}
	if status == 429 {
		now := time.Now()
		c.modelQuotaExceeded[modelName] = &now
		c.SetModelQuotaExceeded(modelName)
	}
	return &interfaces.ErrorMessage{StatusCode: status, Error: genErr}
}

func (c *GeminiWebClient) handleSendSuccess(ctx context.Context, prep *chatPrep, output *geminiWeb.ModelOutput, modelName string) ([]byte, *interfaces.ErrorMessage) {
	delete(c.modelQuotaExceeded, modelName)
	c.ClearModelQuotaExceeded(modelName)
	gemBytes, err := geminiWeb.ConvertOutputToGemini(output, modelName, prep.prompt)
	if err != nil {
		return nil, &interfaces.ErrorMessage{StatusCode: 500, Error: err}
	}
	c.AddAPIResponseData(ctx, gemBytes)
	if output != nil {
		metaAfter := prep.chat.Metadata()
		if len(metaAfter) > 0 {
			key := geminiWeb.AccountMetaKey(c.GetEmail(), modelName)
			c.convMutex.Lock()
			c.convStore[key] = metaAfter
			snapshot := c.convStore
			c.convMutex.Unlock()
			_ = geminiWeb.SaveConvStore(geminiWeb.ConvStorePath(c.tokenFilePath), snapshot)
		}
		if c.useReusableContext() {
			c.storeConversationJSON(prep.underlying, prep.cleaned, prep.chat.Metadata(), output)
		}
	}
	return gemBytes, nil
}

// collapseThoughtPartsToContent flattens Gemini "thought" parts into regular text parts
// so downstream OpenAI translators emit them as `content` instead of `reasoning_content`.
// It preserves part order and keeps non-text parts intact.
func collapseThoughtPartsToContent(gemBytes []byte) []byte {
	parts := gjson.GetBytes(gemBytes, "candidates.0.content.parts")
	if !parts.Exists() || !parts.IsArray() {
		return gemBytes
	}
	arr := parts.Array()
	newParts := make([]json.RawMessage, 0, len(arr))
	for _, part := range arr {
		if t := part.Get("text"); t.Exists() {
			obj, _ := json.Marshal(map[string]string{"text": t.String()})
			newParts = append(newParts, obj)
		} else {
			newParts = append(newParts, json.RawMessage(part.Raw))
		}
	}
	var sb strings.Builder
	sb.WriteByte('[')
	for i, p := range newParts {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.Write(p)
	}
	sb.WriteByte(']')
	if updated, err := sjson.SetRawBytes(gemBytes, "candidates.0.content.parts", []byte(sb.String())); err == nil {
		return updated
	}
	return gemBytes
}

// mergeThoughtIntoSingleContent merges all thought text and normal text into one text part.
// The output places the thought text inside <think>...</think> followed by a newline and then the normal text.
// Non-text parts are ignored for the combined output chunk.
func mergeThoughtIntoSingleContent(gemBytes []byte) []byte {
	parts := gjson.GetBytes(gemBytes, "candidates.0.content.parts")
	if !parts.Exists() || !parts.IsArray() {
		return gemBytes
	}
	var thought strings.Builder
	var visible strings.Builder
	parts.ForEach(func(_, part gjson.Result) bool {
		if t := part.Get("text"); t.Exists() {
			if part.Get("thought").Bool() {
				thought.WriteString(t.String())
			} else {
				visible.WriteString(t.String())
			}
		}
		return true
	})
	var combined strings.Builder
	if thought.Len() > 0 {
		combined.WriteString("<think>")
		combined.WriteString(thought.String())
		combined.WriteString("</think>\n\n")
	}
	combined.WriteString(visible.String())

	// Build a single-part array
	obj, _ := json.Marshal(map[string]string{"text": combined.String()})
	var arr strings.Builder
	arr.WriteByte('[')
	arr.Write(obj)
	arr.WriteByte(']')
	if updated, err := sjson.SetRawBytes(gemBytes, "candidates.0.content.parts", []byte(arr.String())); err == nil {
		return updated
	}
	return gemBytes
}

func (c *GeminiWebClient) appendUpstreamRequestLog(ctx context.Context, modelName string, useTags, explicitContext bool, prompt string, filesCount int, reuse bool, metaLen int) {
	if !c.cfg.RequestLog {
		return
	}
	ginContext, ok := ctx.Value("gin").(*gin.Context)
	if !ok || ginContext == nil {
		return
	}
	preview := geminiWeb.BuildUpstreamRequestLog(c.GetEmail(), c.useReusableContext(), useTags, explicitContext, prompt, filesCount, reuse, metaLen, c.getConfiguredGem())
	if existing, exists := ginContext.Get("API_REQUEST"); exists {
		if base, ok2 := existing.([]byte); ok2 {
			merged := append(append([]byte{}, base...), []byte(preview)...)
			ginContext.Set("API_REQUEST", merged)
		}
	}
}

func (c *GeminiWebClient) SendRawTokenCount(ctx context.Context, modelName string, rawJSON []byte, alt string) ([]byte, *interfaces.ErrorMessage) {
	est := geminiWeb.EstimateTotalTokensFromRawJSON(rawJSON)
	return []byte(fmt.Sprintf(`{"totalTokens":%d}`, est)), nil
}

// SaveTokenToFile persists current cookies to a sidecar file via gemini-web helpers.
func (c *GeminiWebClient) SaveTokenToFile() error {
	ts := c.tokenStorage.(*gemini.GeminiWebTokenStorage)
	if c.gwc != nil && c.gwc.Cookies != nil {
		if v, ok := c.gwc.Cookies["__Secure-1PSID"]; ok && v != "" {
			ts.Secure1PSID = v
		}
		if v, ok := c.gwc.Cookies["__Secure-1PSIDTS"]; ok && v != "" {
			ts.Secure1PSIDTS = v
		}
	}
	log.Debugf("Saving Gemini Web cookies sidecar to %s", filepath.Base(geminiWeb.CookiesSidecarPath(c.tokenFilePath)))
	return geminiWeb.SaveCookiesSidecar(c.tokenFilePath, c.gwc.Cookies)
}

// startCookiePersist periodically writes refreshed cookies into the sidecar file.
func (c *GeminiWebClient) startCookiePersist() {
	if c.gwc == nil {
		return
	}
	if c.cookiePersistCancel != nil {
		c.cookiePersistCancel()
		c.cookiePersistCancel = nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.cookiePersistCancel = cancel
	go func() {
		// Persist cookies at the same cadence as auto-refresh when enabled,
		// otherwise use a coarse default interval.
		persistSec := geminiWebDefaultPersistIntervalSec
		if c.gwc != nil && c.gwc.AutoRefresh {
			if sec := int(c.gwc.RefreshInterval / time.Second); sec > 0 {
				persistSec = sec
			}
		}
		ticker := time.NewTicker(time.Duration(persistSec) * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if c.gwc != nil && c.gwc.Cookies != nil {
					if err := c.SaveTokenToFile(); err != nil {
						log.Errorf("Failed to persist cookies sidecar for %s: %v", c.GetEmail(), err)
					} else {
						log.Debugf("Persisted cookies sidecar for %s", c.GetEmail())
					}
				}
			}
		}
	}()
}

func (c *GeminiWebClient) IsModelQuotaExceeded(model string) bool {
	if t, ok := c.modelQuotaExceeded[model]; ok {
		return time.Since(*t) <= 30*time.Minute
	}
	return false
}

func (c *GeminiWebClient) GetUserAgent() string {
	if ua := geminiWeb.HeadersGemini.Get("User-Agent"); ua != "" {
		return ua
	}
	return "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
}

func (c *GeminiWebClient) GetRequestMutex() *sync.Mutex { return nil }

func (c *GeminiWebClient) RefreshTokens(ctx context.Context) error { return c.Init() }

func (c *GeminiWebClient) backgroundInitRetry() {
	backoffs := []time.Duration{5 * time.Second, 10 * time.Second, 30 * time.Second, 1 * time.Minute, 2 * time.Minute, 5 * time.Minute}
	i := 0
	for {
		if err := c.Init(); err == nil {
			log.Infof("Gemini Web token recovered for %s", c.GetEmail())
			if !c.cookieRotationStarted {
				c.cookieRotationStarted = true
			}
			c.startCookiePersist()
			return
		}
		d := backoffs[i]
		if i < len(backoffs)-1 {
			i++
		}
		time.Sleep(d)
	}
}

// IsSelfPersistedToken compares provided token storage with currently active cookies.
// Removed: IsSelfPersistedToken (no longer needed with sidecar-only periodic persistence)

// flushCookiesSidecarToMain merges sidecar cookies into the main token file.
func (c *GeminiWebClient) flushCookiesSidecarToMain() {
	if c.tokenFilePath == "" {
		return
	}
	base := c.tokenStorage.(*gemini.GeminiWebTokenStorage)
	if err := geminiWeb.FlushCookiesSidecarToMain(c.tokenFilePath, c.gwc.Cookies, base); err != nil {
		log.Errorf("Failed to flush cookies sidecar to main for %s: %v", filepath.Base(c.tokenFilePath), err)
	}
}

// findReusableSession and storeConversationJSON live here as client bridges; hashing/records in gemini-web

func (c *GeminiWebClient) getConfiguredGem() *geminiWeb.Gem {
	if c.cfg.GeminiWeb.CodeMode {
		return &geminiWeb.Gem{ID: "coding-partner", Name: "Coding partner", Predefined: true}
	}
	return nil
}

// findReusableSession bridges to gemini-web conversation reuse using in-memory stores.
func (c *GeminiWebClient) findReusableSession(model string, msgs []geminiWeb.RoleText) ([]string, []geminiWeb.RoleText) {
	c.convMutex.RLock()
	items := c.convData
	index := c.convIndex
	c.convMutex.RUnlock()
	return geminiWeb.FindReusableSessionIn(items, index, c.StableClientID(), c.GetEmail(), model, msgs)
}

// storeConversationJSON persists conversation records and updates in-memory indexes.
func (c *GeminiWebClient) storeConversationJSON(model string, history []geminiWeb.RoleText, metadata []string, output *geminiWeb.ModelOutput) {
	rec, ok := geminiWeb.BuildConversationRecord(model, c.StableClientID(), history, output, metadata)
	if !ok {
		return
	}
	stableID := rec.ClientID
	stableHash := geminiWeb.HashConversation(stableID, model, rec.Messages)
	legacyID := c.GetEmail()
	legacyHash := geminiWeb.HashConversation(legacyID, model, rec.Messages)
	c.convMutex.Lock()
	c.convData[stableHash] = rec
	c.convIndex["hash:"+stableHash] = stableHash
	if legacyID != stableID {
		c.convIndex["hash:"+legacyHash] = stableHash
	}
	items := c.convData
	index := c.convIndex
	c.convMutex.Unlock()
	_ = geminiWeb.SaveConvData(geminiWeb.ConvDataPath(c.tokenFilePath), items, index)
}

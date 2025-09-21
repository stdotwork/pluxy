package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/gemini"
	geminiwebapi "github.com/router-for-me/CLIProxyAPI/v6/internal/client/gemini-web"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/translator/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	geminiWebDefaultTimeoutSec         = 300
	geminiWebDefaultRefreshIntervalSec = 540
)

type geminiWebState struct {
	cfg         *config.Config
	token       *gemini.GeminiWebTokenStorage
	storagePath string

	stableClientID string
	accountID      string

	reqMu  sync.Mutex
	client *geminiwebapi.GeminiClient

	tokenMu    sync.Mutex
	tokenDirty bool

	convMu    sync.RWMutex
	convStore map[string][]string
	convData  map[string]geminiwebapi.ConversationRecord
	convIndex map[string]string

	refreshInterval time.Duration
	lastRefresh     time.Time
}

func (s *geminiWebState) RefreshLead() time.Duration {
	if s.refreshInterval > 0 {
		return s.refreshInterval
	}
	return 9 * time.Minute
}

func newGeminiWebState(cfg *config.Config, token *gemini.GeminiWebTokenStorage, storagePath string) *geminiWebState {
	state := &geminiWebState{
		cfg:         cfg,
		token:       token,
		storagePath: storagePath,
		convStore:   make(map[string][]string),
		convData:    make(map[string]geminiwebapi.ConversationRecord),
		convIndex:   make(map[string]string),
	}
	suffix := geminiwebapi.Sha256Hex(token.Secure1PSID)
	if len(suffix) > 16 {
		suffix = suffix[:16]
	}
	state.stableClientID = "gemini-web-" + suffix
	if storagePath != "" {
		base := strings.TrimSuffix(filepath.Base(storagePath), filepath.Ext(storagePath))
		if base != "" {
			state.accountID = base
		} else {
			state.accountID = suffix
		}
	} else {
		state.accountID = suffix
	}
	state.loadConversationCaches()
	intervalSec := geminiWebDefaultRefreshIntervalSec
	if cfg != nil && cfg.GeminiWeb.TokenRefreshSeconds > 0 {
		intervalSec = cfg.GeminiWeb.TokenRefreshSeconds
	}
	state.refreshInterval = time.Duration(intervalSec) * time.Second
	return state
}

func (s *geminiWebState) loadConversationCaches() {
	if path := s.convStorePath(); path != "" {
		if store, err := geminiwebapi.LoadConvStore(path); err == nil {
			s.convStore = store
		}
	}
	if path := s.convDataPath(); path != "" {
		if items, index, err := geminiwebapi.LoadConvData(path); err == nil {
			s.convData = items
			s.convIndex = index
		}
	}
}

func (s *geminiWebState) convStorePath() string {
	base := s.storagePath
	if base == "" {
		base = s.accountID + ".json"
	}
	return geminiwebapi.ConvStorePath(base)
}

func (s *geminiWebState) convDataPath() string {
	base := s.storagePath
	if base == "" {
		base = s.accountID + ".json"
	}
	return geminiwebapi.ConvDataPath(base)
}

func (s *geminiWebState) getRequestMutex() *sync.Mutex { return &s.reqMu }

func (s *geminiWebState) ensureClient() error {
	if s.client != nil && s.client.Running {
		return nil
	}
	proxyURL := ""
	if s.cfg != nil {
		proxyURL = s.cfg.ProxyURL
	}
	s.client = geminiwebapi.NewGeminiClient(
		s.token.Secure1PSID,
		s.token.Secure1PSIDTS,
		proxyURL,
		geminiwebapi.WithOnCookiesRefreshed(s.onCookiesRefreshed),
	)
	timeout := geminiWebDefaultTimeoutSec
	refresh := geminiWebDefaultRefreshIntervalSec
	if s.cfg != nil && s.cfg.GeminiWeb.TokenRefreshSeconds > 0 {
		refresh = s.cfg.GeminiWeb.TokenRefreshSeconds
	}
	if err := s.client.Init(float64(timeout), false, 300, false, float64(refresh), false); err != nil {
		s.client = nil
		return err
	}
	s.lastRefresh = time.Now()
	return nil
}

func (s *geminiWebState) refresh(ctx context.Context) error {
	_ = ctx
	proxyURL := ""
	if s.cfg != nil {
		proxyURL = s.cfg.ProxyURL
	}
	s.client = geminiwebapi.NewGeminiClient(
		s.token.Secure1PSID,
		s.token.Secure1PSIDTS,
		proxyURL,
		geminiwebapi.WithOnCookiesRefreshed(s.onCookiesRefreshed),
	)
	timeout := geminiWebDefaultTimeoutSec
	refresh := geminiWebDefaultRefreshIntervalSec
	if s.cfg != nil && s.cfg.GeminiWeb.TokenRefreshSeconds > 0 {
		refresh = s.cfg.GeminiWeb.TokenRefreshSeconds
	}
	if err := s.client.Init(float64(timeout), false, 300, false, float64(refresh), false); err != nil {
		return err
	}
	s.lastRefresh = time.Now()
	return nil
}

func (s *geminiWebState) onCookiesRefreshed() {
	s.tokenMu.Lock()
	defer s.tokenMu.Unlock()
	if s.client == nil || s.client.Cookies == nil {
		return
	}
	if v := s.client.Cookies["__Secure-1PSID"]; v != "" {
		s.token.Secure1PSID = v
	}
	if v := s.client.Cookies["__Secure-1PSIDTS"]; v != "" {
		s.token.Secure1PSIDTS = v
	}
	s.tokenDirty = true
}

func (s *geminiWebState) tokenSnapshot() *gemini.GeminiWebTokenStorage {
	s.tokenMu.Lock()
	defer s.tokenMu.Unlock()
	c := *s.token
	return &c
}

func (s *geminiWebState) ShouldRefresh(now time.Time, _ *cliproxyauth.Auth) bool {
	interval := s.refreshInterval
	if interval <= 0 {
		interval = time.Duration(geminiWebDefaultRefreshIntervalSec) * time.Second
	}
	if s.lastRefresh.IsZero() {
		return true
	}
	return now.Sub(s.lastRefresh) >= interval
}

type geminiWebPrepared struct {
	handlerType   string
	translatedRaw []byte
	prompt        string
	uploaded      []string
	chat          *geminiwebapi.ChatSession
	cleaned       []geminiwebapi.RoleText
	underlying    string
	reuse         bool
	tagged        bool
	originalRaw   []byte
}

func (s *geminiWebState) prepare(ctx context.Context, modelName string, rawJSON []byte, stream bool, original []byte) (*geminiWebPrepared, *interfaces.ErrorMessage) {
	res := &geminiWebPrepared{originalRaw: original}
	res.translatedRaw = bytes.Clone(rawJSON)
	if handler, ok := ctx.Value("handler").(interfaces.APIHandler); ok && handler != nil {
		res.handlerType = handler.HandlerType()
		res.translatedRaw = translator.Request(res.handlerType, constant.GeminiWeb, modelName, res.translatedRaw, stream)
	}
	recordAPIRequest(ctx, s.cfg, res.translatedRaw)

	messages, files, mimes, msgFileIdx, err := geminiwebapi.ParseMessagesAndFiles(res.translatedRaw)
	if err != nil {
		return nil, &interfaces.ErrorMessage{StatusCode: 400, Error: fmt.Errorf("bad request: %w", err)}
	}
	cleaned := geminiwebapi.SanitizeAssistantMessages(messages)
	res.cleaned = cleaned
	res.underlying = geminiwebapi.MapAliasToUnderlying(modelName)
	model, err := geminiwebapi.ModelFromName(res.underlying)
	if err != nil {
		return nil, &interfaces.ErrorMessage{StatusCode: 400, Error: err}
	}

	var meta []string
	useMsgs := cleaned
	filesSubset := files
	mimesSubset := mimes

	if s.useReusableContext() {
		reuseMeta, remaining := s.findReusableSession(res.underlying, cleaned)
		if len(reuseMeta) > 0 {
			res.reuse = true
			meta = reuseMeta
			if len(remaining) == 1 {
				useMsgs = []geminiwebapi.RoleText{remaining[0]}
			} else if len(remaining) > 1 {
				useMsgs = remaining
			} else if len(cleaned) > 0 {
				useMsgs = []geminiwebapi.RoleText{cleaned[len(cleaned)-1]}
			}
			if len(useMsgs) == 1 && len(messages) > 0 && len(msgFileIdx) == len(messages) {
				lastIdx := len(msgFileIdx) - 1
				idxs := msgFileIdx[lastIdx]
				if len(idxs) > 0 {
					filesSubset = make([][]byte, 0, len(idxs))
					mimesSubset = make([]string, 0, len(idxs))
					for _, fi := range idxs {
						if fi >= 0 && fi < len(files) {
							filesSubset = append(filesSubset, files[fi])
							if fi < len(mimes) {
								mimesSubset = append(mimesSubset, mimes[fi])
							} else {
								mimesSubset = append(mimesSubset, "")
							}
						}
					}
				} else {
					filesSubset = nil
					mimesSubset = nil
				}
			} else {
				filesSubset = nil
				mimesSubset = nil
			}
		} else {
			if len(cleaned) >= 2 && strings.EqualFold(cleaned[len(cleaned)-2].Role, "assistant") {
				keyUnderlying := geminiwebapi.AccountMetaKey(s.accountID, res.underlying)
				keyAlias := geminiwebapi.AccountMetaKey(s.accountID, modelName)
				s.convMu.RLock()
				fallbackMeta := s.convStore[keyUnderlying]
				if len(fallbackMeta) == 0 {
					fallbackMeta = s.convStore[keyAlias]
				}
				s.convMu.RUnlock()
				if len(fallbackMeta) > 0 {
					meta = fallbackMeta
					useMsgs = []geminiwebapi.RoleText{cleaned[len(cleaned)-1]}
					res.reuse = true
					filesSubset = nil
					mimesSubset = nil
				}
			}
		}
	} else {
		keyUnderlying := geminiwebapi.AccountMetaKey(s.accountID, res.underlying)
		keyAlias := geminiwebapi.AccountMetaKey(s.accountID, modelName)
		s.convMu.RLock()
		if v, ok := s.convStore[keyUnderlying]; ok && len(v) > 0 {
			meta = v
		} else {
			meta = s.convStore[keyAlias]
		}
		s.convMu.RUnlock()
	}

	res.tagged = geminiwebapi.NeedRoleTags(useMsgs)
	if res.reuse && len(useMsgs) == 1 {
		res.tagged = false
	}

	enableXML := s.cfg != nil && s.cfg.GeminiWeb.CodeMode
	useMsgs = geminiwebapi.AppendXMLWrapHintIfNeeded(useMsgs, !enableXML)

	res.prompt = geminiwebapi.BuildPrompt(useMsgs, res.tagged, res.tagged)
	if strings.TrimSpace(res.prompt) == "" {
		return nil, &interfaces.ErrorMessage{StatusCode: 400, Error: errors.New("bad request: empty prompt after filtering system/thought content")}
	}

	uploaded, upErr := geminiwebapi.MaterializeInlineFiles(filesSubset, mimesSubset)
	if upErr != nil {
		return nil, upErr
	}
	res.uploaded = uploaded

	if err = s.ensureClient(); err != nil {
		return nil, &interfaces.ErrorMessage{StatusCode: 500, Error: err}
	}
	chat := s.client.StartChat(model, s.getConfiguredGem(), meta)
	chat.SetRequestedModel(modelName)
	res.chat = chat

	return res, nil
}

func (s *geminiWebState) send(ctx context.Context, modelName string, reqPayload []byte, opts cliproxyexecutor.Options) ([]byte, *interfaces.ErrorMessage, *geminiWebPrepared) {
	prep, errMsg := s.prepare(ctx, modelName, reqPayload, opts.Stream, opts.OriginalRequest)
	if errMsg != nil {
		return nil, errMsg, nil
	}
	defer geminiwebapi.CleanupFiles(prep.uploaded)

	output, err := geminiwebapi.SendWithSplit(prep.chat, prep.prompt, prep.uploaded, s.cfg)
	if err != nil {
		return nil, s.wrapSendError(err), nil
	}

	gemBytes, err := geminiwebapi.ConvertOutputToGemini(&output, modelName, prep.prompt)
	if err != nil {
		return nil, &interfaces.ErrorMessage{StatusCode: 500, Error: err}, nil
	}

	s.addAPIResponseData(ctx, gemBytes)
	s.persistConversation(modelName, prep, &output)
	return gemBytes, nil, prep
}

func (s *geminiWebState) wrapSendError(genErr error) *interfaces.ErrorMessage {
	status := 500
	var usage *geminiwebapi.UsageLimitExceeded
	var blocked *geminiwebapi.TemporarilyBlocked
	var invalid *geminiwebapi.ModelInvalid
	var valueErr *geminiwebapi.ValueError
	var timeout *geminiwebapi.TimeoutError
	switch {
	case errors.As(genErr, &usage):
		status = 429
	case errors.As(genErr, &blocked):
		status = 429
	case errors.As(genErr, &invalid):
		status = 400
	case errors.As(genErr, &valueErr):
		status = 400
	case errors.As(genErr, &timeout):
		status = 504
	}
	return &interfaces.ErrorMessage{StatusCode: status, Error: genErr}
}

func (s *geminiWebState) persistConversation(modelName string, prep *geminiWebPrepared, output *geminiwebapi.ModelOutput) {
	if output == nil || prep == nil || prep.chat == nil {
		return
	}
	metadata := prep.chat.Metadata()
	if len(metadata) > 0 {
		keyUnderlying := geminiwebapi.AccountMetaKey(s.accountID, prep.underlying)
		keyAlias := geminiwebapi.AccountMetaKey(s.accountID, modelName)
		s.convMu.Lock()
		s.convStore[keyUnderlying] = metadata
		s.convStore[keyAlias] = metadata
		storeSnapshot := make(map[string][]string, len(s.convStore))
		for k, v := range s.convStore {
			if v == nil {
				continue
			}
			cp := make([]string, len(v))
			copy(cp, v)
			storeSnapshot[k] = cp
		}
		s.convMu.Unlock()
		_ = geminiwebapi.SaveConvStore(s.convStorePath(), storeSnapshot)
	}

	if !s.useReusableContext() {
		return
	}
	rec, ok := geminiwebapi.BuildConversationRecord(prep.underlying, s.stableClientID, prep.cleaned, output, metadata)
	if !ok {
		return
	}
	stableHash := geminiwebapi.HashConversation(rec.ClientID, prep.underlying, rec.Messages)
	accountHash := geminiwebapi.HashConversation(s.accountID, prep.underlying, rec.Messages)

	s.convMu.Lock()
	s.convData[stableHash] = rec
	s.convIndex["hash:"+stableHash] = stableHash
	if accountHash != stableHash {
		s.convIndex["hash:"+accountHash] = stableHash
	}
	dataSnapshot := make(map[string]geminiwebapi.ConversationRecord, len(s.convData))
	for k, v := range s.convData {
		dataSnapshot[k] = v
	}
	indexSnapshot := make(map[string]string, len(s.convIndex))
	for k, v := range s.convIndex {
		indexSnapshot[k] = v
	}
	s.convMu.Unlock()
	_ = geminiwebapi.SaveConvData(s.convDataPath(), dataSnapshot, indexSnapshot)
}

func (s *geminiWebState) addAPIResponseData(ctx context.Context, line []byte) {
	appendAPIResponseChunk(ctx, s.cfg, line)
}

func (s *geminiWebState) convertToTarget(ctx context.Context, modelName string, prep *geminiWebPrepared, gemBytes []byte) []byte {
	if prep == nil || prep.handlerType == "" {
		return gemBytes
	}
	if !translator.NeedConvert(prep.handlerType, constant.GeminiWeb) {
		return gemBytes
	}
	var param any
	out := translator.ResponseNonStream(prep.handlerType, constant.GeminiWeb, ctx, modelName, prep.originalRaw, prep.translatedRaw, gemBytes, &param)
	if prep.handlerType == constant.OpenAI && out != "" {
		newID := fmt.Sprintf("chatcmpl-%x", time.Now().UnixNano())
		if v := gjson.Parse(out).Get("id"); v.Exists() {
			out, _ = sjson.Set(out, "id", newID)
		}
	}
	return []byte(out)
}

func (s *geminiWebState) convertStream(ctx context.Context, modelName string, prep *geminiWebPrepared, gemBytes []byte) []string {
	if prep == nil || prep.handlerType == "" {
		return []string{string(gemBytes)}
	}
	if !translator.NeedConvert(prep.handlerType, constant.GeminiWeb) {
		return []string{string(gemBytes)}
	}
	var param any
	return translator.Response(prep.handlerType, constant.GeminiWeb, ctx, modelName, prep.originalRaw, prep.translatedRaw, gemBytes, &param)
}

func (s *geminiWebState) doneStream(ctx context.Context, modelName string, prep *geminiWebPrepared) []string {
	if prep == nil || prep.handlerType == "" {
		return nil
	}
	if !translator.NeedConvert(prep.handlerType, constant.GeminiWeb) {
		return nil
	}
	var param any
	return translator.Response(prep.handlerType, constant.GeminiWeb, ctx, modelName, prep.originalRaw, prep.translatedRaw, []byte("[DONE]"), &param)
}

func (s *geminiWebState) useReusableContext() bool {
	if s.cfg == nil {
		return true
	}
	return s.cfg.GeminiWeb.Context
}

func (s *geminiWebState) findReusableSession(modelName string, msgs []geminiwebapi.RoleText) ([]string, []geminiwebapi.RoleText) {
	s.convMu.RLock()
	items := s.convData
	index := s.convIndex
	s.convMu.RUnlock()
	return geminiwebapi.FindReusableSessionIn(items, index, s.stableClientID, s.accountID, modelName, msgs)
}

func (s *geminiWebState) getConfiguredGem() *geminiwebapi.Gem {
	if s.cfg != nil && s.cfg.GeminiWeb.CodeMode {
		return &geminiwebapi.Gem{ID: "coding-partner", Name: "Coding partner", Predefined: true}
	}
	return nil
}

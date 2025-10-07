package geminiwebapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/gemini"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
	conversation "github.com/router-for-me/CLIProxyAPI/v6/internal/provider/gemini-web/conversation"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/translator/translator"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	bolt "go.etcd.io/bbolt"
)

const (
	geminiWebDefaultTimeoutSec = 300
)

type GeminiWebState struct {
	cfg         *config.Config
	token       *gemini.GeminiWebTokenStorage
	storagePath string
	authLabel   string

	stableClientID string
	accountID      string

	reqMu  sync.Mutex
	client *GeminiClient

	tokenMu    sync.Mutex
	tokenDirty bool

	convMu    sync.RWMutex
	convStore map[string][]string
	convData  map[string]ConversationRecord
	convIndex map[string]string

	lastRefresh time.Time

	pendingMatchMu sync.Mutex
	pendingMatch   *conversation.MatchResult
}

type reuseComputation struct {
	metadata []string
	history  []RoleText
	overlap  int
}

func NewGeminiWebState(cfg *config.Config, token *gemini.GeminiWebTokenStorage, storagePath, authLabel string) *GeminiWebState {
	state := &GeminiWebState{
		cfg:         cfg,
		token:       token,
		storagePath: storagePath,
		authLabel:   strings.TrimSpace(authLabel),
		convStore:   make(map[string][]string),
		convData:    make(map[string]ConversationRecord),
		convIndex:   make(map[string]string),
	}
	suffix := conversation.Sha256Hex(token.Secure1PSID)
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
	return state
}

func (s *GeminiWebState) setPendingMatch(match *conversation.MatchResult) {
	if s == nil {
		return
	}
	s.pendingMatchMu.Lock()
	s.pendingMatch = match
	s.pendingMatchMu.Unlock()
}

func (s *GeminiWebState) consumePendingMatch() *conversation.MatchResult {
	s.pendingMatchMu.Lock()
	defer s.pendingMatchMu.Unlock()
	match := s.pendingMatch
	s.pendingMatch = nil
	return match
}

// SetPendingMatch makes a cached conversation match available for the next request.
func (s *GeminiWebState) SetPendingMatch(match *conversation.MatchResult) {
	s.setPendingMatch(match)
}

// Label returns a stable account label for logging and persistence.
// If a storage file path is known, it uses the file base name (without extension).
// Otherwise, it falls back to the stable client ID (e.g., "gemini-web-<hash>").
func (s *GeminiWebState) Label() string {
	if s == nil {
		return ""
	}
	if s.token != nil {
		if lbl := strings.TrimSpace(s.token.Label); lbl != "" {
			return lbl
		}
	}
	if lbl := strings.TrimSpace(s.authLabel); lbl != "" {
		return lbl
	}
	if s.storagePath != "" {
		base := strings.TrimSuffix(filepath.Base(s.storagePath), filepath.Ext(s.storagePath))
		if base != "" {
			return base
		}
	}
	return s.stableClientID
}

func (s *GeminiWebState) loadConversationCaches() {
	path := s.convPath()
	if path == "" {
		return
	}
	if store, err := LoadConvStore(path); err == nil {
		s.convStore = store
	}
	if items, index, err := LoadConvData(path); err == nil {
		s.convData = items
		s.convIndex = index
	}
}

// convPath returns the BoltDB file path used for both account metadata and conversation data.
func (s *GeminiWebState) convPath() string {
	base := s.storagePath
	if base == "" {
		// Use accountID directly as base name; ConvBoltPath will append .bolt.
		base = s.accountID
	}
	return ConvBoltPath(base)
}

func cloneRoleTextSlice(in []RoleText) []RoleText {
	if len(in) == 0 {
		return nil
	}
	out := make([]RoleText, len(in))
	copy(out, in)
	return out
}

func cloneStringSlice(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}

func longestHistoryOverlap(history, incoming []RoleText) int {
	max := len(history)
	if len(incoming) < max {
		max = len(incoming)
	}
	for overlap := max; overlap > 0; overlap-- {
		if conversation.EqualMessages(history[len(history)-overlap:], incoming[:overlap]) {
			return overlap
		}
	}
	return 0
}

func equalStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func storedMessagesToRoleText(stored []conversation.StoredMessage) []RoleText {
	if len(stored) == 0 {
		return nil
	}
	converted := make([]RoleText, len(stored))
	for i, msg := range stored {
		converted[i] = RoleText{Role: msg.Role, Text: msg.Content}
	}
	return converted
}

func (s *GeminiWebState) findConversationByMetadata(model string, metadata []string) ([]RoleText, bool) {
	if len(metadata) == 0 {
		return nil, false
	}
	s.convMu.RLock()
	defer s.convMu.RUnlock()
	for _, rec := range s.convData {
		if !strings.EqualFold(strings.TrimSpace(rec.Model), strings.TrimSpace(model)) {
			continue
		}
		if !equalStringSlice(rec.Metadata, metadata) {
			continue
		}
		return cloneRoleTextSlice(storedMessagesToRoleText(rec.Messages)), true
	}
	return nil, false
}

func (s *GeminiWebState) GetRequestMutex() *sync.Mutex { return &s.reqMu }

func (s *GeminiWebState) EnsureClient() error {
	if s.client != nil && s.client.Running {
		return nil
	}
	proxyURL := ""
	if s.cfg != nil {
		proxyURL = s.cfg.ProxyURL
	}
	s.client = NewGeminiClient(
		s.token.Secure1PSID,
		s.token.Secure1PSIDTS,
		proxyURL,
	)
	timeout := geminiWebDefaultTimeoutSec
	if err := s.client.Init(float64(timeout), false); err != nil {
		s.client = nil
		return err
	}
	s.lastRefresh = time.Now()
	return nil
}

func (s *GeminiWebState) Refresh(ctx context.Context) error {
	_ = ctx
	proxyURL := ""
	if s.cfg != nil {
		proxyURL = s.cfg.ProxyURL
	}
	s.client = NewGeminiClient(
		s.token.Secure1PSID,
		s.token.Secure1PSIDTS,
		proxyURL,
	)
	timeout := geminiWebDefaultTimeoutSec
	if err := s.client.Init(float64(timeout), false); err != nil {
		return err
	}
	// Attempt rotation proactively to persist new TS sooner
	if newTS, err := s.client.RotateTS(); err == nil && newTS != "" && newTS != s.token.Secure1PSIDTS {
		s.tokenMu.Lock()
		s.token.Secure1PSIDTS = newTS
		s.tokenDirty = true
		if s.client != nil && s.client.Cookies != nil {
			s.client.Cookies["__Secure-1PSIDTS"] = newTS
		}
		s.tokenMu.Unlock()
		// Detailed debug log: provider and account label.
		label := strings.TrimSpace(s.Label())
		if label == "" {
			label = s.accountID
		}
		log.Debugf("gemini web account %s rotated 1PSIDTS: %s", label, MaskToken28(newTS))
	}
	s.lastRefresh = time.Now()
	return nil
}

func (s *GeminiWebState) TokenSnapshot() *gemini.GeminiWebTokenStorage {
	s.tokenMu.Lock()
	defer s.tokenMu.Unlock()
	c := *s.token
	return &c
}

type geminiWebPrepared struct {
	handlerType   string
	translatedRaw []byte
	prompt        string
	uploaded      []string
	chat          *ChatSession
	cleaned       []RoleText
	underlying    string
	reuse         bool
	tagged        bool
	originalRaw   []byte
}

func (s *GeminiWebState) prepare(ctx context.Context, modelName string, rawJSON []byte, stream bool, original []byte) (*geminiWebPrepared, *interfaces.ErrorMessage) {
	res := &geminiWebPrepared{originalRaw: original}
	res.translatedRaw = bytes.Clone(rawJSON)
	if handler, ok := ctx.Value("handler").(interfaces.APIHandler); ok && handler != nil {
		res.handlerType = handler.HandlerType()
		res.translatedRaw = translator.Request(res.handlerType, constant.GeminiWeb, modelName, res.translatedRaw, stream)
	}
	recordAPIRequest(ctx, s.cfg, res.translatedRaw)

	messages, files, mimes, msgFileIdx, err := ParseMessagesAndFiles(res.translatedRaw)
	if err != nil {
		return nil, &interfaces.ErrorMessage{StatusCode: 400, Error: fmt.Errorf("bad request: %w", err)}
	}
	cleaned := SanitizeAssistantMessages(messages)
	fullCleaned := cloneRoleTextSlice(cleaned)
	res.underlying = MapAliasToUnderlying(modelName)
	model, err := ModelFromName(res.underlying)
	if err != nil {
		return nil, &interfaces.ErrorMessage{StatusCode: 400, Error: err}
	}

	var meta []string
	useMsgs := cleaned
	filesSubset := files
	mimesSubset := mimes

	if s.useReusableContext() {
		reusePlan := s.reuseFromPending(res.underlying, cleaned)
		if reusePlan == nil {
			reusePlan = s.findReusableSession(res.underlying, cleaned)
		}
		if reusePlan != nil {
			res.reuse = true
			meta = cloneStringSlice(reusePlan.metadata)
			overlap := reusePlan.overlap
			if overlap > len(cleaned) {
				overlap = len(cleaned)
			} else if overlap < 0 {
				overlap = 0
			}
			delta := cloneRoleTextSlice(cleaned[overlap:])
			if len(reusePlan.history) > 0 {
				fullCleaned = append(cloneRoleTextSlice(reusePlan.history), delta...)
			} else {
				fullCleaned = append(cloneRoleTextSlice(cleaned[:overlap]), delta...)
			}
			useMsgs = delta
			if len(delta) == 0 && len(cleaned) > 0 {
				useMsgs = []RoleText{cleaned[len(cleaned)-1]}
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
				keyUnderlying := AccountMetaKey(s.accountID, res.underlying)
				keyAlias := AccountMetaKey(s.accountID, modelName)
				s.convMu.RLock()
				fallbackMeta := s.convStore[keyUnderlying]
				if len(fallbackMeta) == 0 {
					fallbackMeta = s.convStore[keyAlias]
				}
				s.convMu.RUnlock()
				if len(fallbackMeta) > 0 {
					meta = fallbackMeta
					useMsgs = []RoleText{cleaned[len(cleaned)-1]}
					res.reuse = true
					filesSubset = nil
					mimesSubset = nil
				}
			}
		}
	} else {
		keyUnderlying := AccountMetaKey(s.accountID, res.underlying)
		keyAlias := AccountMetaKey(s.accountID, modelName)
		s.convMu.RLock()
		if v, ok := s.convStore[keyUnderlying]; ok && len(v) > 0 {
			meta = v
		} else {
			meta = s.convStore[keyAlias]
		}
		s.convMu.RUnlock()
	}

	res.cleaned = fullCleaned

	res.tagged = NeedRoleTags(useMsgs)
	if res.reuse && len(useMsgs) == 1 {
		res.tagged = false
	}

	enableXML := s.cfg != nil && s.cfg.GeminiWeb.CodeMode
	useMsgs = AppendXMLWrapHintIfNeeded(useMsgs, !enableXML)

	res.prompt = BuildPrompt(useMsgs, res.tagged, res.tagged)
	if strings.TrimSpace(res.prompt) == "" {
		return nil, &interfaces.ErrorMessage{StatusCode: 400, Error: errors.New("bad request: empty prompt after filtering system/thought content")}
	}

	uploaded, upErr := MaterializeInlineFiles(filesSubset, mimesSubset)
	if upErr != nil {
		return nil, upErr
	}
	res.uploaded = uploaded

	if err = s.EnsureClient(); err != nil {
		return nil, &interfaces.ErrorMessage{StatusCode: 500, Error: err}
	}
	chat := s.client.StartChat(model, s.getConfiguredGem(), meta)
	chat.SetRequestedModel(modelName)
	res.chat = chat

	return res, nil
}

func (s *GeminiWebState) Send(ctx context.Context, modelName string, reqPayload []byte, opts cliproxyexecutor.Options) ([]byte, *interfaces.ErrorMessage, *geminiWebPrepared) {
	prep, errMsg := s.prepare(ctx, modelName, reqPayload, opts.Stream, opts.OriginalRequest)
	if errMsg != nil {
		return nil, errMsg, nil
	}
	defer CleanupFiles(prep.uploaded)

	output, err := SendWithSplit(prep.chat, prep.prompt, prep.uploaded, s.cfg)
	if err != nil {
		return nil, s.wrapSendError(err), nil
	}

	// Hook: For gemini-2.5-flash-image-web, if the API returns only images without any text,
	// inject a small textual summary so that conversation persistence has non-empty assistant text.
	// This helps conversation recovery (conv store) to match sessions reliably.
	if strings.EqualFold(modelName, "gemini-2.5-flash-image-web") {
		if len(output.Candidates) > 0 {
			c := output.Candidates[output.Chosen]
			hasNoText := strings.TrimSpace(c.Text) == ""
			hasImages := len(c.GeneratedImages) > 0 || len(c.WebImages) > 0
			if hasNoText && hasImages {
				// Build a stable, concise fallback text. Avoid dynamic details to keep hashes stable.
				// Prefer a deterministic phrase with count to aid users while keeping consistency.
				fallback := "Done"
				// Mutate the chosen candidate's text so both response conversion and
				// conversation persistence observe the same fallback.
				output.Candidates[output.Chosen].Text = fallback
			}
		}
	}

	gemBytes, err := ConvertOutputToGemini(&output, modelName, prep.prompt)
	if err != nil {
		return nil, &interfaces.ErrorMessage{StatusCode: 500, Error: err}, nil
	}

	s.addAPIResponseData(ctx, gemBytes)
	s.persistConversation(modelName, prep, &output)
	return gemBytes, nil, prep
}

func (s *GeminiWebState) wrapSendError(genErr error) *interfaces.ErrorMessage {
	status := 500
	var usage *UsageLimitExceeded
	var blocked *TemporarilyBlocked
	var invalid *ModelInvalid
	var valueErr *ValueError
	var timeout *TimeoutError
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

func (s *GeminiWebState) persistConversation(modelName string, prep *geminiWebPrepared, output *ModelOutput) {
	if output == nil || prep == nil || prep.chat == nil {
		return
	}
	metadata := prep.chat.Metadata()
	if len(metadata) > 0 {
		keyUnderlying := AccountMetaKey(s.accountID, prep.underlying)
		keyAlias := AccountMetaKey(s.accountID, modelName)
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
		_ = SaveConvStore(s.convPath(), storeSnapshot)
	}

	if !s.useReusableContext() {
		return
	}
	rec, ok := BuildConversationRecord(prep.underlying, s.stableClientID, prep.cleaned, output, metadata)
	if !ok {
		return
	}
	label := strings.TrimSpace(s.Label())
	if label == "" {
		label = s.accountID
	}
	conversationMsgs := conversation.StoredToMessages(rec.Messages)
	if err := conversation.StoreConversation(label, prep.underlying, conversationMsgs, metadata); err != nil {
		log.Debugf("gemini web: failed to persist global conversation index: %v", err)
	}
	stableHash := conversation.HashConversationForAccount(rec.ClientID, prep.underlying, rec.Messages)
	accountHash := conversation.HashConversationForAccount(s.accountID, prep.underlying, rec.Messages)

	suffixSeen := make(map[string]struct{})
	suffixSeen["hash:"+stableHash] = struct{}{}
	if accountHash != stableHash {
		suffixSeen["hash:"+accountHash] = struct{}{}
	}

	s.convMu.Lock()
	s.convData[stableHash] = rec
	s.convIndex["hash:"+stableHash] = stableHash
	if accountHash != stableHash {
		s.convIndex["hash:"+accountHash] = stableHash
	}

	sanitizedHistory := conversation.SanitizeAssistantMessages(conversation.StoredToMessages(rec.Messages))
	for start := 1; start < len(sanitizedHistory); start++ {
		segment := sanitizedHistory[start:]
		if len(segment) < 2 {
			continue
		}
		tailRole := strings.ToLower(strings.TrimSpace(segment[len(segment)-1].Role))
		if tailRole != "assistant" && tailRole != "system" {
			continue
		}
		storedSegment := conversation.ToStoredMessages(segment)
		segmentStableHash := conversation.HashConversationForAccount(rec.ClientID, prep.underlying, storedSegment)
		keyStable := "hash:" + segmentStableHash
		if _, exists := suffixSeen[keyStable]; !exists {
			s.convIndex[keyStable] = stableHash
			suffixSeen[keyStable] = struct{}{}
		}
		segmentAccountHash := conversation.HashConversationForAccount(s.accountID, prep.underlying, storedSegment)
		if segmentAccountHash != segmentStableHash {
			keyAccount := "hash:" + segmentAccountHash
			if _, exists := suffixSeen[keyAccount]; !exists {
				s.convIndex[keyAccount] = stableHash
				suffixSeen[keyAccount] = struct{}{}
			}
		}
	}
	dataSnapshot := make(map[string]ConversationRecord, len(s.convData))
	for k, v := range s.convData {
		dataSnapshot[k] = v
	}
	indexSnapshot := make(map[string]string, len(s.convIndex))
	for k, v := range s.convIndex {
		indexSnapshot[k] = v
	}
	s.convMu.Unlock()
	_ = SaveConvData(s.convPath(), dataSnapshot, indexSnapshot)
}

func (s *GeminiWebState) addAPIResponseData(ctx context.Context, line []byte) {
	appendAPIResponseChunk(ctx, s.cfg, line)
}

func (s *GeminiWebState) ConvertToTarget(ctx context.Context, modelName string, prep *geminiWebPrepared, gemBytes []byte) []byte {
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

func (s *GeminiWebState) ConvertStream(ctx context.Context, modelName string, prep *geminiWebPrepared, gemBytes []byte) []string {
	if prep == nil || prep.handlerType == "" {
		return []string{string(gemBytes)}
	}
	if !translator.NeedConvert(prep.handlerType, constant.GeminiWeb) {
		return []string{string(gemBytes)}
	}
	var param any
	return translator.Response(prep.handlerType, constant.GeminiWeb, ctx, modelName, prep.originalRaw, prep.translatedRaw, gemBytes, &param)
}

func (s *GeminiWebState) DoneStream(ctx context.Context, modelName string, prep *geminiWebPrepared) []string {
	if prep == nil || prep.handlerType == "" {
		return nil
	}
	if !translator.NeedConvert(prep.handlerType, constant.GeminiWeb) {
		return nil
	}
	var param any
	return translator.Response(prep.handlerType, constant.GeminiWeb, ctx, modelName, prep.originalRaw, prep.translatedRaw, []byte("[DONE]"), &param)
}

func (s *GeminiWebState) useReusableContext() bool {
	if s.cfg == nil {
		return true
	}
	return s.cfg.GeminiWeb.Context
}

func (s *GeminiWebState) reuseFromPending(modelName string, msgs []RoleText) *reuseComputation {
	match := s.consumePendingMatch()
	if match == nil {
		return nil
	}
	if !strings.EqualFold(strings.TrimSpace(match.Model), strings.TrimSpace(modelName)) {
		return nil
	}
	metadata := cloneStringSlice(match.Record.Metadata)
	if len(metadata) == 0 {
		return nil
	}
	history, ok := s.findConversationByMetadata(modelName, metadata)
	if !ok {
		return nil
	}
	overlap := longestHistoryOverlap(history, msgs)
	return &reuseComputation{metadata: metadata, history: history, overlap: overlap}
}

func (s *GeminiWebState) findReusableSession(modelName string, msgs []RoleText) *reuseComputation {
	s.convMu.RLock()
	items := s.convData
	index := s.convIndex
	s.convMu.RUnlock()
	rec, metadata, overlap, ok := FindReusableSessionIn(items, index, s.stableClientID, s.accountID, modelName, msgs)
	if !ok {
		return nil
	}
	history := cloneRoleTextSlice(storedMessagesToRoleText(rec.Messages))
	if len(history) == 0 {
		return nil
	}
	// Ensure overlap reflects the actual history alignment.
	if computed := longestHistoryOverlap(history, msgs); computed > 0 {
		overlap = computed
	}
	return &reuseComputation{metadata: cloneStringSlice(metadata), history: history, overlap: overlap}
}

func (s *GeminiWebState) getConfiguredGem() *Gem {
	if s.cfg == nil {
		return nil
	}
	// New behavior: attach Gem based on explicit GemMode selection.
	// Only attaches the Gem; does not toggle any other behavior.
	if gm := strings.ToLower(strings.TrimSpace(s.cfg.GeminiWeb.GemMode)); gm != "" {
		switch gm {
		case "coding-partner":
			return &Gem{ID: "coding-partner", Name: "Coding partner", Predefined: true}
		case "writing-editor":
			return &Gem{ID: "writing-editor", Name: "Writing editor", Predefined: true}
		}
	}
	// Backwards compatibility: legacy CodeMode still attaches Coding partner
	// and may enable extra behaviors elsewhere.
	if s.cfg.GeminiWeb.CodeMode {
		return &Gem{ID: "coding-partner", Name: "Coding partner", Predefined: true}
	}
	return nil
}

// recordAPIRequest stores the upstream request payload in Gin context for request logging.
func recordAPIRequest(ctx context.Context, cfg *config.Config, payload []byte) {
	if cfg == nil || !cfg.RequestLog || len(payload) == 0 {
		return
	}
	if ginCtx, ok := ctx.Value("gin").(*gin.Context); ok && ginCtx != nil {
		ginCtx.Set("API_REQUEST", bytes.Clone(payload))
	}
}

// appendAPIResponseChunk appends an upstream response chunk to Gin context for request logging.
func appendAPIResponseChunk(ctx context.Context, cfg *config.Config, chunk []byte) {
	if cfg == nil || !cfg.RequestLog {
		return
	}
	data := bytes.TrimSpace(bytes.Clone(chunk))
	if len(data) == 0 {
		return
	}
	if ginCtx, ok := ctx.Value("gin").(*gin.Context); ok && ginCtx != nil {
		if existing, exists := ginCtx.Get("API_RESPONSE"); exists {
			if prev, okBytes := existing.([]byte); okBytes {
				prev = append(prev, data...)
				prev = append(prev, []byte("\n\n")...)
				ginCtx.Set("API_RESPONSE", prev)
				return
			}
		}
		ginCtx.Set("API_RESPONSE", data)
	}
}

// ConvBoltPath returns the BoltDB file path used for both account metadata and conversation data.
// Different logical datasets are kept in separate buckets within this single DB file.
func ConvBoltPath(tokenFilePath string) string {
	wd, err := os.Getwd()
	if err != nil || wd == "" {
		wd = "."
	}
	convDir := filepath.Join(wd, "conv")
	base := strings.TrimSuffix(filepath.Base(tokenFilePath), filepath.Ext(tokenFilePath))
	return filepath.Join(convDir, base+".bolt")
}

// LoadConvStore reads the account-level metadata store from disk.
func LoadConvStore(path string) (map[string][]string, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = db.Close()
	}()
	out := map[string][]string{}
	err = db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("account_meta"))
		if b == nil {
			return nil
		}
		return b.ForEach(func(k, v []byte) error {
			var arr []string
			if len(v) > 0 {
				if e := json.Unmarshal(v, &arr); e != nil {
					// Skip malformed entries instead of failing the whole load
					return nil
				}
			}
			out[string(k)] = arr
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// SaveConvStore writes the account-level metadata store to disk atomically.
func SaveConvStore(path string, data map[string][]string) error {
	if data == nil {
		data = map[string][]string{}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 2 * time.Second})
	if err != nil {
		return err
	}
	defer func() {
		_ = db.Close()
	}()
	return db.Update(func(tx *bolt.Tx) error {
		// Recreate bucket to reflect the given snapshot exactly.
		if b := tx.Bucket([]byte("account_meta")); b != nil {
			if err = tx.DeleteBucket([]byte("account_meta")); err != nil {
				return err
			}
		}
		b, errCreateBucket := tx.CreateBucket([]byte("account_meta"))
		if errCreateBucket != nil {
			return errCreateBucket
		}
		for k, v := range data {
			enc, e := json.Marshal(v)
			if e != nil {
				return e
			}
			if e = b.Put([]byte(k), enc); e != nil {
				return e
			}
		}
		return nil
	})
}

// AccountMetaKey builds the key for account-level metadata map.
func AccountMetaKey(email, modelName string) string {
	return fmt.Sprintf("account-meta|%s|%s", email, modelName)
}

// LoadConvData reads the full conversation data and index from disk.
func LoadConvData(path string) (map[string]ConversationRecord, map[string]string, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, nil, err
	}
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return nil, nil, err
	}
	defer func() {
		_ = db.Close()
	}()
	items := map[string]ConversationRecord{}
	index := map[string]string{}
	err = db.View(func(tx *bolt.Tx) error {
		// Load conv_items
		if b := tx.Bucket([]byte("conv_items")); b != nil {
			if e := b.ForEach(func(k, v []byte) error {
				var rec ConversationRecord
				if len(v) > 0 {
					if e2 := json.Unmarshal(v, &rec); e2 != nil {
						// Skip malformed
						return nil
					}
					items[string(k)] = rec
				}
				return nil
			}); e != nil {
				return e
			}
		}
		// Load conv_index
		if b := tx.Bucket([]byte("conv_index")); b != nil {
			if e := b.ForEach(func(k, v []byte) error {
				index[string(k)] = string(v)
				return nil
			}); e != nil {
				return e
			}
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return items, index, nil
}

// SaveConvData writes the full conversation data and index to disk atomically.
func SaveConvData(path string, items map[string]ConversationRecord, index map[string]string) error {
	if items == nil {
		items = map[string]ConversationRecord{}
	}
	if index == nil {
		index = map[string]string{}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 2 * time.Second})
	if err != nil {
		return err
	}
	defer func() {
		_ = db.Close()
	}()
	return db.Update(func(tx *bolt.Tx) error {
		// Recreate items bucket
		if b := tx.Bucket([]byte("conv_items")); b != nil {
			if err = tx.DeleteBucket([]byte("conv_items")); err != nil {
				return err
			}
		}
		bi, errCreateBucket := tx.CreateBucket([]byte("conv_items"))
		if errCreateBucket != nil {
			return errCreateBucket
		}
		for k, rec := range items {
			enc, e := json.Marshal(rec)
			if e != nil {
				return e
			}
			if e = bi.Put([]byte(k), enc); e != nil {
				return e
			}
		}

		// Recreate index bucket
		if b := tx.Bucket([]byte("conv_index")); b != nil {
			if err = tx.DeleteBucket([]byte("conv_index")); err != nil {
				return err
			}
		}
		bx, errCreateBucket := tx.CreateBucket([]byte("conv_index"))
		if errCreateBucket != nil {
			return errCreateBucket
		}
		for k, v := range index {
			if e := bx.Put([]byte(k), []byte(v)); e != nil {
				return e
			}
		}
		return nil
	})
}

// BuildConversationRecord constructs a ConversationRecord from history and the latest output.
// Returns false when output is empty or has no candidates.
func BuildConversationRecord(model, clientID string, history []RoleText, output *ModelOutput, metadata []string) (ConversationRecord, bool) {
	if output == nil || len(output.Candidates) == 0 {
		return ConversationRecord{}, false
	}
	text := ""
	if t := output.Candidates[0].Text; t != "" {
		text = RemoveThinkTags(t)
	}
	final := append([]RoleText{}, history...)
	final = append(final, RoleText{Role: "assistant", Text: text})
	rec := ConversationRecord{
		Model:     model,
		ClientID:  clientID,
		Metadata:  metadata,
		Messages:  conversation.ToStoredMessages(final),
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	return rec, true
}

// FindByMessageListIn looks up a conversation record by hashed message list.
// It attempts both the stable client ID and a legacy email-based ID.
func FindByMessageListIn(items map[string]ConversationRecord, index map[string]string, stableClientID, email, model string, msgs []RoleText) (ConversationRecord, bool) {
	stored := conversation.ToStoredMessages(msgs)
	stableHash := conversation.HashConversationForAccount(stableClientID, model, stored)
	fallbackHash := conversation.HashConversationForAccount(email, model, stored)

	// Try stable hash via index indirection first
	if key, ok := index["hash:"+stableHash]; ok {
		if rec, ok2 := items[key]; ok2 {
			return rec, true
		}
	}
	if rec, ok := items[stableHash]; ok {
		return rec, true
	}
	// Fallback to legacy hash (email-based)
	if key, ok := index["hash:"+fallbackHash]; ok {
		if rec, ok2 := items[key]; ok2 {
			return rec, true
		}
	}
	if rec, ok := items[fallbackHash]; ok {
		return rec, true
	}
	return ConversationRecord{}, false
}

// FindConversationIn tries exact then sanitized assistant messages.
func FindConversationIn(items map[string]ConversationRecord, index map[string]string, stableClientID, email, model string, msgs []RoleText) (ConversationRecord, bool) {
	if len(msgs) == 0 {
		return ConversationRecord{}, false
	}
	if rec, ok := FindByMessageListIn(items, index, stableClientID, email, model, msgs); ok {
		return rec, true
	}
	if rec, ok := FindByMessageListIn(items, index, stableClientID, email, model, SanitizeAssistantMessages(msgs)); ok {
		return rec, true
	}
	return ConversationRecord{}, false
}

// FindReusableSessionIn returns reusable metadata and the remaining message suffix.
func FindReusableSessionIn(items map[string]ConversationRecord, index map[string]string, stableClientID, email, model string, msgs []RoleText) (ConversationRecord, []string, int, bool) {
	if len(msgs) < 2 {
		return ConversationRecord{}, nil, 0, false
	}
	searchEnd := len(msgs)
	for searchEnd >= 2 {
		sub := msgs[:searchEnd]
		tail := sub[len(sub)-1]
		if strings.EqualFold(tail.Role, "assistant") || strings.EqualFold(tail.Role, "system") {
			if rec, ok := FindConversationIn(items, index, stableClientID, email, model, sub); ok {
				return rec, rec.Metadata, searchEnd, true
			}
		}
		searchEnd--
	}
	return ConversationRecord{}, nil, 0, false
}

// SetConfig updates the configuration reference used by the state.
// This allows hot-reload of configuration to take effect for existing
// runtime states that were cached on auth during previous requests.
func (s *GeminiWebState) SetConfig(cfg *config.Config) {
	s.cfg = cfg
}

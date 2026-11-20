// Package catalogue holds the embedded models.dev snapshot of model limits and capabilities
// and refreshes it daily into state/catalogue.json.
package catalogue

import (
	"cmp"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/stdotwork/pluxy/next/protocol"
)

//go:generate go run ./gen.go

//go:embed snapshot.json
var embeddedSnapshot []byte

const (
	DefaultURL      = "https://models.dev/api.json"
	DefaultTimeout  = 10 * time.Second
	RefreshInterval = 24 * time.Hour
	maxRefreshBytes = 16 << 20
)

var (
	canonicalEfforts = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}
	dateSuffixRE     = regexp.MustCompile(`-\d{8}$`)
)

// keptProviders lists the models.dev provider sections core keeps when trimming a full
// models.dev payload during a daily refresh.
var keptProviders = []string{
	"anthropic",
	"google-vertex-anthropic",
	"openai",
	"google",
	"google-vertex",
	"moonshotai",
	"moonshotai-cn",
	"kimi-code-plan-global",
	"kimi-code-plan-cn",
	"xai",
	"meta",
}

// Options configures a Catalogue.
type Options struct {
	// StatePath is where daily refreshes save the trimmed catalogue (for example
	// state/catalogue.json beside config.yaml). Empty disables disk persistence.
	StatePath string
	// URL overrides DefaultURL (https://models.dev/api.json).
	URL string
	// Client overrides http.DefaultClient for Refresh calls.
	Client *http.Client
	// Timeout overrides DefaultTimeout (10 s) for each Refresh call.
	Timeout time.Duration
}

// Catalogue holds model token limits and capabilities indexed by normalized model ID.
type Catalogue struct {
	statePath string
	url       string
	client    *http.Client
	timeout   time.Duration

	mu         sync.RWMutex
	base       map[string]map[string]protocol.Model
	byProvider map[string]map[string]protocol.Model
	etag       string
}

// New creates a Catalogue initialized from the embedded snapshot and, when Options.StatePath
// names a readable non-empty snapshot file on disk, overlays it without waiting on the network.
func New(opts Options) *Catalogue {
	c := &Catalogue{
		statePath: opts.StatePath,
		url:       cmp.Or(opts.URL, DefaultURL),
		client:    opts.Client,
		timeout:   opts.Timeout,
	}
	if c.client == nil {
		c.client = http.DefaultClient
	}
	if c.timeout <= 0 {
		c.timeout = DefaultTimeout
	}
	base, _, _, err := parseCatalogueJSON(embeddedSnapshot)
	if err != nil {
		base = map[string]map[string]protocol.Model{}
	}
	c.base = base
	c.byProvider = cloneProviders(base)

	if c.statePath != "" {
		if raw, err := os.ReadFile(c.statePath); err == nil && len(raw) > 0 {
			if diskModels, etag, _, err := parseCatalogueJSON(raw); err == nil && len(diskModels) > 0 {
				c.byProvider = mergeProviders(c.base, diskModels)
				c.etag = etag
			}
		}
	}
	return c
}

const openAICompatible = "pluxy/openai-compatible"

// extensionProviders names the models.dev sections each built-in extension's upstream serves.
// An extension with no entry has no catalogue section: its models must not inherit another
// provider's limits or capabilities.
var extensionProviders = map[string][]string{
	"pluxy/claude": {"anthropic"},
	"pluxy/codex":  {"openai"},
	"pluxy/kimi":   {"moonshotai"},
	"pluxy/xai":    {"xai"},
	"pluxy/meta":   {"meta"},
}

// LookupFor returns the limits and capabilities of model id for an account of extension with
// settings. It searches only the models.dev provider sections that extension's upstream serves.
// openai-compatible accounts may name any upstream, so theirs are the only name-only lookup.
func (c *Catalogue) LookupFor(extension string, settings json.RawMessage, id string) (protocol.Model, bool) {
	key := strings.ToLower(strings.TrimSpace(id))
	key = strings.TrimPrefix(key, "models/")
	if key == "" {
		return protocol.Model{}, false
	}

	providers := providersFor(extension, settings)
	if len(providers) == 0 {
		return protocol.Model{}, false
	}

	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, provider := range providers {
		models := c.byProvider[provider]
		if m, ok := lookupModel(models, key); ok {
			return cloneModel(m), true
		}
		if i := strings.LastIndexByte(key, '/'); i >= 0 && i+1 < len(key) {
			if m, ok := lookupModel(models, key[i+1:]); ok {
				return cloneModel(m), true
			}
		}
	}
	return protocol.Model{}, false
}

func providersFor(extension string, settings json.RawMessage) []string {
	if extension == openAICompatible {
		return keptProviders
	}
	if extension == "pluxy/gemini" {
		var account struct {
			Vertex json.RawMessage `json:"vertex"`
		}
		if json.Unmarshal(settings, &account) == nil && len(account.Vertex) > 0 && string(account.Vertex) != "null" {
			return []string{"google-vertex"}
		}
		return []string{"google"}
	}
	return extensionProviders[extension]
}

func lookupModel(models map[string]protocol.Model, key string) (protocol.Model, bool) {
	if m, ok := models[key]; ok {
		return m, true
	}
	for _, trimmed := range []string{
		strings.TrimSuffix(key, "[1m]"),
		strings.TrimSuffix(key, "-1m"),
		strings.TrimSuffix(key, "-preview"),
		key + "-preview",
	} {
		if trimmed != key && trimmed != "" {
			if m, ok := models[trimmed]; ok {
				return m, true
			}
		}
	}
	if undated := stripDate(key); undated != key && undated != "" {
		if m, ok := models[undated]; ok {
			return m, true
		}
	}
	return protocol.Model{}, false
}

func stripDate(id string) string {
	return dateSuffixRE.ReplaceAllString(id, "")
}

// Stale reports whether the saved catalogue is missing or has not been successfully checked
// for RefreshInterval. A missing state path counts as stale.
func (c *Catalogue) Stale(now time.Time) bool {
	if c.statePath == "" {
		return true
	}
	info, err := os.Stat(c.statePath)
	if err != nil {
		return true
	}
	return now.Sub(info.ModTime()) >= RefreshInterval
}

// Refresh fetches the remote catalogue with a 10 s timeout (or Options.Timeout) and
// If-None-Match. A 200 response with at least one valid model updates the in-memory
// catalogue and atomically replaces Options.StatePath; a 304 keeps the data and marks the
// file checked; an error or empty response keeps the last good data and file.
func (c *Catalogue) Refresh(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	c.mu.RLock()
	etag := c.etag
	c.mu.RUnlock()
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotModified {
		if c.statePath != "" {
			now := time.Now()
			if err := os.Chtimes(c.statePath, now, now); err != nil {
				return err
			}
		}
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("catalogue refresh: status %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxRefreshBytes+1))
	if err != nil {
		return err
	}
	if len(raw) == 0 || len(raw) > maxRefreshBytes {
		return errors.New("catalogue refresh: empty or oversized response body")
	}

	newETag := strings.TrimSpace(resp.Header.Get("ETag"))
	parsed, fileETag, trimmedJSON, err := parseCatalogueJSON(raw)
	if err != nil {
		return err
	}
	if len(parsed) == 0 {
		return errors.New("catalogue refresh: response contained no models")
	}
	if newETag == "" {
		newETag = fileETag
	}

	if c.statePath != "" {
		toWrite := trimmedJSON
		if newETag != "" {
			toWrite = attachETag(trimmedJSON, newETag)
		}
		if err := writeStateFile(c.statePath, toWrite); err != nil {
			return err
		}
	}

	c.mu.Lock()
	c.byProvider = mergeProviders(c.base, parsed)
	if newETag != "" {
		c.etag = newETag
	}
	c.mu.Unlock()
	return nil
}

func writeStateFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".catalogue-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

type rawProvider struct {
	Models map[string]rawModel `json:"models"`
}

type rawModel struct {
	Limit            *rawLimit         `json:"limit,omitempty"`
	Modalities       *rawModalities    `json:"modalities,omitempty"`
	ToolCall         bool              `json:"tool_call,omitempty"`
	Reasoning        bool              `json:"reasoning,omitempty"`
	ReasoningOptions []rawReasoningOpt `json:"reasoning_options,omitempty"`
	StructuredOutput bool              `json:"structured_output,omitempty"`
	Attachment       bool              `json:"attachment,omitempty"`
}

type rawLimit struct {
	Context int `json:"context,omitempty"`
	Input   int `json:"input,omitempty"`
	Output  int `json:"output,omitempty"`
}

type rawModalities struct {
	Input  []string `json:"input,omitempty"`
	Output []string `json:"output,omitempty"`
}

type rawReasoningOpt struct {
	Type   string   `json:"type"`
	Values []string `json:"values,omitempty"`
}

func parseCatalogueJSON(raw []byte) (models map[string]map[string]protocol.Model, etag string, trimmed []byte, err error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, "", nil, err
	}
	if rawETag, ok := top["_etag"]; ok {
		_ = json.Unmarshal(rawETag, &etag)
		delete(top, "_etag")
	}

	// Keep only the providers this product serves, in canonical order.
	order := make([]string, 0, len(top))
	for _, p := range keptProviders {
		if _, ok := top[p]; ok {
			order = append(order, p)
		}
	}

	models = map[string]map[string]protocol.Model{}
	trimmedProviders := map[string]rawProvider{}

	for _, p := range order {
		var rp rawProvider
		if err := json.Unmarshal(top[p], &rp); err != nil || len(rp.Models) == 0 {
			continue
		}
		ids := make([]string, 0, len(rp.Models))
		for id := range rp.Models {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		keptMap := make(map[string]rawModel, len(ids))
		providerModels := map[string]protocol.Model{}
		for _, id := range ids {
			rm := trimRawModel(rp.Models[id])
			keptMap[id] = rm
			pm := toProtocolModel(id, rm)
			for _, key := range indexKeys(id) {
				if _, exists := providerModels[key]; !exists {
					providerModels[key] = pm
				}
			}
		}
		if len(keptMap) > 0 {
			models[p] = providerModels
			trimmedProviders[p] = rawProvider{Models: keptMap}
		}
	}

	if len(trimmedProviders) > 0 {
		trimmed, err = json.Marshal(trimmedProviders)
		if err != nil {
			return nil, "", nil, err
		}
	}
	return models, etag, trimmed, nil
}

func attachETag(trimmed []byte, etag string) []byte {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &top); err != nil {
		return trimmed
	}
	if b, err := json.Marshal(etag); err == nil {
		top["_etag"] = b
	}
	if out, err := json.Marshal(top); err == nil {
		return out
	}
	return trimmed
}

func indexKeys(id string) []string {
	low := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(id), "models/"))
	if low == "" {
		return nil
	}
	keys := []string{low}
	add := func(k string) {
		if k != "" && !slices.Contains(keys, k) {
			keys = append(keys, k)
		}
	}
	if i := strings.LastIndexByte(low, '/'); i >= 0 && i+1 < len(low) {
		add(low[i+1:])
	}
	// Vertex Anthropic names dated models with @YYYYMMDD or @default.
	if idx := strings.IndexByte(low, '@'); idx > 0 {
		base, suffix := low[:idx], low[idx+1:]
		if suffix == "default" {
			add(base)
		} else {
			add(base + "-" + suffix)
			add(base)
		}
	}
	return keys
}

func trimRawModel(m rawModel) rawModel {
	var out rawModel
	if m.Limit != nil && (m.Limit.Context > 0 || m.Limit.Input > 0 || m.Limit.Output > 0) {
		out.Limit = &rawLimit{
			Context: max(m.Limit.Context, 0),
			Input:   max(m.Limit.Input, 0),
			Output:  max(m.Limit.Output, 0),
		}
	}
	if m.Modalities != nil && (len(m.Modalities.Input) > 0 || len(m.Modalities.Output) > 0) {
		out.Modalities = &rawModalities{
			Input:  slices.Clone(m.Modalities.Input),
			Output: slices.Clone(m.Modalities.Output),
		}
	}
	out.ToolCall = m.ToolCall
	out.Reasoning = m.Reasoning
	out.StructuredOutput = m.StructuredOutput
	out.Attachment = m.Attachment
	for _, opt := range m.ReasoningOptions {
		if opt.Type == "effort" && len(opt.Values) > 0 {
			var vals []string
			for _, v := range opt.Values {
				v = strings.ToLower(strings.TrimSpace(v))
				if slices.Contains(canonicalEfforts, v) && !slices.Contains(vals, v) {
					vals = append(vals, v)
				}
			}
			if len(vals) > 0 {
				out.ReasoningOptions = []rawReasoningOpt{{Type: "effort", Values: vals}}
				out.Reasoning = true
			}
			break
		}
	}
	return out
}

func toProtocolModel(id string, rm rawModel) protocol.Model {
	var ctxLen, maxOut int
	if rm.Limit != nil {
		ctxLen = cmp.Or(rm.Limit.Input, rm.Limit.Context)
		maxOut = rm.Limit.Output
	}
	var inMod, outMod []string
	var vision, pdf bool
	if rm.Modalities != nil {
		for _, m := range rm.Modalities.Input {
			low := strings.ToLower(strings.TrimSpace(m))
			if low != "" {
				inMod = append(inMod, low)
			}
			switch low {
			case "image":
				vision = true
			case "pdf":
				pdf = true
			}
		}
		for _, m := range rm.Modalities.Output {
			low := strings.ToLower(strings.TrimSpace(m))
			if low != "" {
				outMod = append(outMod, low)
			}
		}
	}
	var levels []string
	for _, opt := range rm.ReasoningOptions {
		if opt.Type == "effort" && len(opt.Values) > 0 {
			levels = slices.Clone(opt.Values)
			break
		}
	}
	return protocol.Model{
		ID:                  id,
		ContextLength:       ctxLen,
		MaxCompletionTokens: maxOut,
		InputModalities:     inMod,
		OutputModalities:    outMod,
		Capabilities: &protocol.ModelCapabilities{
			Tools:            rm.ToolCall,
			StructuredOutput: rm.StructuredOutput,
			Vision:           vision,
			PDF:              pdf,
			Reasoning:        rm.Reasoning || len(levels) > 0,
			ReasoningLevels:  levels,
		},
	}
}

func cloneModel(m protocol.Model) protocol.Model {
	m.GenerationMethods = slices.Clone(m.GenerationMethods)
	m.InputModalities = slices.Clone(m.InputModalities)
	m.OutputModalities = slices.Clone(m.OutputModalities)
	if m.Capabilities != nil {
		c := *m.Capabilities
		c.ReasoningLevels = slices.Clone(c.ReasoningLevels)
		m.Capabilities = &c
	}
	return m
}

func cloneProviders(src map[string]map[string]protocol.Model) map[string]map[string]protocol.Model {
	dst := make(map[string]map[string]protocol.Model, len(src))
	for provider, models := range src {
		dst[provider] = cloneMap(models)
	}
	return dst
}

func mergeProviders(base, overlay map[string]map[string]protocol.Model) map[string]map[string]protocol.Model {
	merged := cloneProviders(base)
	for provider, models := range overlay {
		if merged[provider] == nil {
			merged[provider] = map[string]protocol.Model{}
		}
		for key, model := range models {
			merged[provider][key] = cloneModel(model)
		}
	}
	return merged
}

func cloneMap(src map[string]protocol.Model) map[string]protocol.Model {
	dst := make(map[string]protocol.Model, len(src))
	for k, v := range src {
		dst[k] = cloneModel(v)
	}
	return dst
}

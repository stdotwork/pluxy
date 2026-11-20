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

	mu   sync.RWMutex
	base map[string]protocol.Model
	byID map[string]protocol.Model
	etag string
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
		base = map[string]protocol.Model{}
	}
	c.base = base
	c.byID = cloneMap(base)

	if c.statePath != "" {
		if raw, err := os.ReadFile(c.statePath); err == nil && len(raw) > 0 {
			if diskModels, etag, _, err := parseCatalogueJSON(raw); err == nil && len(diskModels) > 0 {
				merged := cloneMap(c.base)
				for k, v := range diskModels {
					merged[k] = v
				}
				c.byID = merged
				c.etag = etag
			}
		}
	}
	return c
}

// Lookup returns the limits and capabilities of model id from the catalogue.
func (c *Catalogue) Lookup(id string) (protocol.Model, bool) {
	key := strings.ToLower(strings.TrimSpace(id))
	key = strings.TrimPrefix(key, "models/")
	if key == "" {
		return protocol.Model{}, false
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	if m, ok := c.lookupLocked(key); ok {
		return cloneModel(m), true
	}
	if i := strings.LastIndexByte(key, '/'); i >= 0 && i+1 < len(key) {
		if m, ok := c.lookupLocked(key[i+1:]); ok {
			return cloneModel(m), true
		}
	}
	return protocol.Model{}, false
}

func (c *Catalogue) lookupLocked(key string) (protocol.Model, bool) {
	if m, ok := c.byID[key]; ok {
		return m, true
	}
	for _, trimmed := range []string{
		strings.TrimSuffix(key, "[1m]"),
		strings.TrimSuffix(key, "-1m"),
		strings.TrimSuffix(key, "-preview"),
		key + "-preview",
	} {
		if trimmed != key && trimmed != "" {
			if m, ok := c.byID[trimmed]; ok {
				return m, true
			}
		}
	}
	if undated := stripDate(key); undated != key && undated != "" {
		if m, ok := c.byID[undated]; ok {
			return m, true
		}
	}
	return protocol.Model{}, false
}

func stripDate(id string) string {
	return dateSuffixRE.ReplaceAllString(id, "")
}

// Refresh fetches the remote catalogue with a 10 s timeout (or Options.Timeout) and
// If-None-Match. A 200 response with at least one valid model updates the in-memory
// catalogue and atomically replaces Options.StatePath; a 304, an error, or an empty
// response keeps the last good data and file.
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
	merged := cloneMap(c.base)
	for k, v := range parsed {
		merged[k] = v
	}
	c.byID = merged
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
	Status           string            `json:"status,omitempty"`
	LastUpdated      string            `json:"last_updated,omitempty"`
	ReleaseDate      string            `json:"release_date,omitempty"`
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

func parseCatalogueJSON(raw []byte) (models map[string]protocol.Model, etag string, trimmed []byte, err error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, "", nil, err
	}
	if rawETag, ok := top["_etag"]; ok {
		_ = json.Unmarshal(rawETag, &etag)
		delete(top, "_etag")
	}

	// Prefer the canonical provider priority order so primary vendor entries take precedence.
	order := make([]string, 0, len(top))
	seenProvider := map[string]bool{}
	for _, p := range keptProviders {
		if _, ok := top[p]; ok {
			order = append(order, p)
			seenProvider[p] = true
		}
	}
	// If none of keptProviders were in top (e.g. a small synthetic test payload), accept all keys.
	if len(order) == 0 {
		for p := range top {
			order = append(order, p)
		}
		slices.Sort(order)
	}

	models = map[string]protocol.Model{}
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
		for _, id := range ids {
			rm := trimRawModel(rp.Models[id])
			keptMap[id] = rm
			pm := toProtocolModel(id, rm)
			for _, key := range indexKeys(id) {
				if _, exists := models[key]; !exists {
					models[key] = pm
				}
			}
		}
		if len(keptMap) > 0 {
			trimmedProviders[p] = rawProvider{Models: keptMap}
		}
		_ = seenProvider
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
	if m.Status == "deprecated" {
		out.Status = "deprecated"
		out.LastUpdated = cmp.Or(m.LastUpdated, m.ReleaseDate)
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
	var deprecatedAt int64
	if rm.Status == "deprecated" {
		dateStr := cmp.Or(rm.LastUpdated, rm.ReleaseDate)
		if t, err := time.Parse("2006-01-02", dateStr); err == nil {
			deprecatedAt = t.UTC().Unix()
		} else if t, err := time.Parse(time.RFC3339, dateStr); err == nil {
			deprecatedAt = t.UTC().Unix()
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
			DeprecatedAt:     deprecatedAt,
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

func cloneMap(src map[string]protocol.Model) map[string]protocol.Model {
	dst := make(map[string]protocol.Model, len(src))
	for k, v := range src {
		dst[k] = cloneModel(v)
	}
	return dst
}

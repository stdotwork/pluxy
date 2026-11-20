//go:build ignore

// Command gen fetches https://models.dev/api.json, keeps only the four providers' models
// (Claude/Anthropic, OpenAI/Codex, Google/Gemini, and the OpenAI-compatible vendors pluxy
// ships built-in lists for) and only the fields core uses for limits and capabilities,
// pulls any built-in list model that models.dev carries under another provider key in
// api.json, and writes snapshot.json. It never invents or derives entries outside models.dev.
package main

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"
)

const defaultSourceURL = "https://models.dev/api.json"

var canonicalEfforts = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}

var dateSuffixRE = regexp.MustCompile(`-\d{8}$`)

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

func trimModel(m rawModel) rawModel {
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

func main() {
	srcURL := os.Getenv("PLUXY_MODELS_DEV_URL")
	if srcURL == "" {
		srcURL = defaultSourceURL
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var raw []byte
	if strings.HasPrefix(srcURL, "http://") || strings.HasPrefix(srcURL, "https://") {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, srcURL, nil)
		if err != nil {
			fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			fatal(fmt.Errorf("GET %s: status %d", srcURL, resp.StatusCode))
		}
		raw, err = io.ReadAll(resp.Body)
		if err != nil {
			fatal(err)
		}
	} else {
		var err error
		raw, err = os.ReadFile(srcURL)
		if err != nil {
			fatal(err)
		}
	}

	var all map[string]rawProvider
	if err := json.Unmarshal(raw, &all); err != nil {
		fatal(fmt.Errorf("parse models.dev json: %w", err))
	}

	// Primary provider buckets in snapshot.json.
	buckets := map[string]map[string]rawModel{
		"anthropic":     {},
		"openai":        {},
		"google":        {},
		"google-vertex": {},
		"moonshotai":    {},
		"xai":           {},
		"meta":          {},
	}

	copyInto := func(dstBucket, srcProvider string) {
		p, ok := all[srcProvider]
		if !ok {
			return
		}
		dst := buckets[dstBucket]
		for id, m := range p.Models {
			tm := trimModel(m)
			if tm.Limit == nil {
				continue
			}
			if _, exists := dst[id]; !exists {
				dst[id] = tm
			}
		}
	}

	copyInto("anthropic", "anthropic")
	copyInto("anthropic", "google-vertex-anthropic")
	copyInto("openai", "openai")
	copyInto("google", "google")
	copyInto("google-vertex", "google-vertex")
	copyInto("moonshotai", "moonshotai")
	copyInto("moonshotai", "moonshotai-cn")
	copyInto("moonshotai", "kimi-code-plan-global")
	copyInto("moonshotai", "kimi-code-plan-cn")
	copyInto("xai", "xai")
	copyInto("meta", "meta")

	// Index every model across all models.dev providers by normalized short ID for fallback lookup
	// when a built-in model appears under another provider key in models.dev.
	anyProvider := map[string]rawModel{}
	providerOrder := make([]string, 0, len(all))
	for p := range all {
		providerOrder = append(providerOrder, p)
	}
	slices.Sort(providerOrder)
	for _, p := range providerOrder {
		ids := make([]string, 0, len(all[p].Models))
		for id := range all[p].Models {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		for _, id := range ids {
			tm := trimModel(all[p].Models[id])
			if tm.Limit == nil || (tm.Limit.Context == 0 && tm.Limit.Input == 0) {
				continue
			}
			short := strings.ToLower(id)
			if i := strings.LastIndexByte(short, '/'); i >= 0 {
				short = short[i+1:]
			}
			if _, ok := anyProvider[short]; !ok {
				anyProvider[short] = tm
			}
		}
	}

	// Pull built-in models that models.dev carries under another provider key into their vendor bucket.
	pullFromOtherProviders(buckets["anthropic"], anyProvider, "../../extensions/claude/models.json")
	pullFromOtherProviders(buckets["openai"], anyProvider, "../../extensions/codex/models.json")
	pullFromOtherProviders(buckets["google"], anyProvider, "../../extensions/gemini/models.json")
	pullFromOtherProviders(buckets["google-vertex"], anyProvider, "../../extensions/gemini/vertex-models.json")
	pullFromOtherProviders(buckets["moonshotai"], anyProvider, "../../extensions/kimi/models.json")
	pullFromOtherProviders(buckets["xai"], anyProvider, "../../extensions/xai/models.json")
	pullFromOtherProviders(buckets["meta"], anyProvider, "../../extensions/meta/models.json")

	out := make(map[string]rawProvider, len(buckets))
	for k, models := range buckets {
		if len(models) > 0 {
			out[k] = rawProvider{Models: models}
		}
	}

	encoded, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		fatal(err)
	}
	encoded = append(encoded, '\n')
	if err := os.WriteFile("snapshot.json", encoded, 0o644); err != nil {
		fatal(err)
	}
}

type builtinItem struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func pullFromOtherProviders(bucket map[string]rawModel, anyProvider map[string]rawModel, path string) {
	raw, err := os.ReadFile(path)
	if err != nil {
		fatal(err)
	}
	var items []builtinItem
	var wrapper struct {
		Models []builtinItem `json:"models"`
	}
	if err := json.Unmarshal(raw, &wrapper); err == nil && len(wrapper.Models) > 0 {
		items = wrapper.Models
	} else {
		var byPlan map[string][]builtinItem
		if err := json.Unmarshal(raw, &byPlan); err != nil {
			fatal(fmt.Errorf("parse %s: %w", path, err))
		}
		for _, list := range byPlan {
			items = append(items, list...)
		}
	}

	resolvesIn := func(id string) bool {
		id = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(id), "models/"))
		for _, candidate := range []string{
			id,
			dateSuffixRE.ReplaceAllString(id, ""),
			strings.TrimSuffix(id, "-preview"),
			id + "-preview",
		} {
			if _, ok := bucket[candidate]; ok {
				return true
			}
			for k := range bucket {
				if strings.ReplaceAll(strings.ToLower(k), "@", "-") == candidate {
					return true
				}
			}
		}
		return false
	}

	for _, it := range items {
		id := cmp.Or(it.ID, it.Name)
		id = strings.TrimPrefix(strings.TrimSpace(id), "models/")
		if id == "" || resolvesIn(id) {
			continue
		}
		low := strings.ToLower(id)
		stripped := dateSuffixRE.ReplaceAllString(low, "")
		// Prefer a canonical undated entry in anyProvider when id has a -YYYYMMDD suffix, so
		// Lookup exercises its date-stripping fallback on dated model IDs.
		if stripped != low {
			if m, ok := anyProvider[stripped]; ok {
				bucket[stripped] = m
				continue
			}
		}
		if m, ok := anyProvider[low]; ok {
			bucket[low] = m
		}
	}
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "catalogue gen: %v\n", err)
	os.Exit(1)
}

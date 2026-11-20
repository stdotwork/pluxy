//go:build ignore

// Command gen fetches https://models.dev/api.json, keeps only the providers' own models that
// pluxy serves and only the fields core uses for limits and capabilities, and writes
// snapshot.json. It never copies a model from another provider's section or invents an entry.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"
)

const defaultSourceURL = "https://models.dev/api.json"

var canonicalEfforts = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}

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

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "catalogue gen: %v\n", err)
	os.Exit(1)
}

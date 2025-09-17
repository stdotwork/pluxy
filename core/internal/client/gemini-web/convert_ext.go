package geminiwebapi

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	reGoogle   = regexp.MustCompile("(\\()?\\[`([^`]+?)`\\]\\(https://www\\.google\\.com/search\\?q=[^)]*\\)(\\))?")
	reColonNum = regexp.MustCompile(`([^:]+:\d+)`)
	reInline   = regexp.MustCompile("`(\\[[^\\]]+\\]\\([^\\)]+\\))`")
)

func unescapeGeminiText(s string) string {
	if s == "" {
		return s
	}
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "\\<", "<")
	s = strings.ReplaceAll(s, "\\_", "_")
	s = strings.ReplaceAll(s, "\\>", ">")
	return s
}

func postProcessModelText(text string) string {
	text = reGoogle.ReplaceAllStringFunc(text, func(m string) string {
		subs := reGoogle.FindStringSubmatch(m)
		if len(subs) < 4 {
			return m
		}
		outerOpen := subs[1]
		display := subs[2]
		target := display
		if loc := reColonNum.FindString(display); loc != "" {
			target = loc
		}
		newSeg := "[`" + display + "`](" + target + ")"
		if outerOpen != "" {
			return "(" + newSeg + ")"
		}
		return newSeg
	})
	text = reInline.ReplaceAllString(text, "$1")
	return text
}

func estimateTokens(s string) int {
	if s == "" {
		return 0
	}
	rc := float64(utf8.RuneCountInString(s))
	if rc <= 0 {
		return 0
	}
	est := int(math.Ceil(rc / 4.0))
	if est < 0 {
		return 0
	}
	return est
}

// ConvertOutputToGemini converts simplified ModelOutput to Gemini API-like JSON.
// promptText is used only to estimate usage tokens to populate usage fields.
func ConvertOutputToGemini(output *ModelOutput, modelName string, promptText string) ([]byte, error) {
	if output == nil || len(output.Candidates) == 0 {
		return nil, fmt.Errorf("empty output")
	}

	parts := make([]map[string]any, 0, 2)

	var thoughtsText string
	if output.Candidates[0].Thoughts != nil {
		if t := strings.TrimSpace(*output.Candidates[0].Thoughts); t != "" {
			thoughtsText = unescapeGeminiText(t)
			parts = append(parts, map[string]any{
				"text":    thoughtsText,
				"thought": true,
			})
		}
	}

	visible := unescapeGeminiText(output.Candidates[0].Text)
	finalText := postProcessModelText(visible)
	if finalText != "" {
		parts = append(parts, map[string]any{"text": finalText})
	}

	if imgs := output.Candidates[0].GeneratedImages; len(imgs) > 0 {
		for _, gi := range imgs {
			if mime, data, err := FetchGeneratedImageData(gi); err == nil && data != "" {
				parts = append(parts, map[string]any{
					"inlineData": map[string]any{
						"mimeType": mime,
						"data":     data,
					},
				})
			}
		}
	}

	promptTokens := estimateTokens(promptText)
	completionTokens := estimateTokens(finalText)
	thoughtsTokens := 0
	if thoughtsText != "" {
		thoughtsTokens = estimateTokens(thoughtsText)
	}
	totalTokens := promptTokens + completionTokens

	now := time.Now()
	resp := map[string]any{
		"candidates": []any{
			map[string]any{
				"content": map[string]any{
					"parts": parts,
					"role":  "model",
				},
				"finishReason": "stop",
				"index":        0,
			},
		},
		"createTime":   now.Format(time.RFC3339Nano),
		"responseId":   fmt.Sprintf("gemini-web-%d", now.UnixNano()),
		"modelVersion": modelName,
		"usageMetadata": map[string]any{
			"promptTokenCount":     promptTokens,
			"candidatesTokenCount": completionTokens,
			"thoughtsTokenCount":   thoughtsTokens,
			"totalTokenCount":      totalTokens,
		},
	}
	b, err := json.Marshal(resp)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal gemini response: %w", err)
	}
	return b, nil
}

package rawjson

import (
	"encoding/json"

	"github.com/stdotwork/pluxy/next/llm"
)

func CodeExecutionLosses(t llm.Tool, fallbackPath, target string) []llm.Loss {
	p := fallbackPath
	if t.Origin != nil && t.Origin.Path != "" {
		p = t.Origin.Path
	}
	detail := "no place for it in " + target + " code execution"
	var losses []llm.Loss
	lose := func(path string) {
		losses = append(losses, llm.Loss{Kind: llm.LossDropped, Path: path, Detail: detail})
	}
	if t.Origin == nil || len(t.Origin.Raw) == 0 {
		return nil
	}
	m := Object(t.Origin.Raw)
	if m == nil {
		return nil
	}
	switch t.Origin.Format {
	case llm.FormatOpenAIResponses:
		for _, k := range sortedKeys(m) {
			v := m[k]
			if isNullValue(v) {
				continue
			}
			switch k {
			case "type":
			case "container":
				cm := Object(v)
				if cm == nil {
					lose(Join(p, k))
					continue
				}
				cp := Join(p, k)
				for _, ck := range sortedKeys(cm) {
					cv := cm[ck]
					if isNullValue(cv) {
						continue
					}
					switch ck {
					case "type":
						var s string
						if json.Unmarshal(cv, &s) != nil || (s != "" && s != "auto") {
							lose(Join(cp, ck))
						}
					case "file_ids":
						if !isEmptyArray(cv) {
							lose(Join(cp, ck))
						}
					case "memory_limit":
						if !isEmptyString(cv) {
							lose(Join(cp, ck))
						}
					default:
						lose(Join(cp, ck))
					}
				}
			case "allowed_callers":
				if !isEmptyArray(v) {
					lose(Join(p, k))
				}
			default:
				lose(Join(p, k))
			}
		}
	case llm.FormatAnthropicMessages:
		for _, k := range sortedKeys(m) {
			v := m[k]
			if isNullValue(v) {
				continue
			}
			switch k {
			case "type", "name":
			case "allowed_callers":
				if !isEmptyArray(v) {
					lose(Join(p, k))
				}
			default:
				lose(Join(p, k))
			}
		}
	case llm.FormatGeminiGenerate:
		for _, k := range sortedKeys(m) {
			v := m[k]
			if isNullValue(v) {
				continue
			}
			if k == "codeExecution" {
				gm := Object(v)
				if gm == nil {
					lose(p)
					continue
				}
				for _, gk := range sortedKeys(gm) {
					if !isNullValue(gm[gk]) {
						lose(Join(p, gk))
					}
				}
				continue
			}
			lose(Join(p, k))
		}
	}
	return losses
}

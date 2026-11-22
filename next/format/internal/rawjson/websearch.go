package rawjson

import (
	"bytes"
	"encoding/json"
	"slices"

	"github.com/stdotwork/pluxy/next/llm"
)

// IsCountryCode reports whether s is two ASCII letters (the shape of an ISO 3166-1 alpha-2 code).
func IsCountryCode(s string) bool {
	return len(s) == 2 && isASCIILetter(s[0]) && isASCIILetter(s[1])
}

func isASCIILetter(b byte) bool {
	return (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')
}

// WebSearchCarried reports which canonical web search settings an encoder actually emitted
// in the target format's tool.
type WebSearchCarried struct {
	MaxUses           bool
	AllowedDomains    bool
	BlockedDomains    bool
	UserLocation      bool
	RawUserLocation   bool // true when user_location JSON was copied verbatim
	SearchContextSize bool
}

// WebSearchLosses returns a dropped loss for every field of a translated web search tool that
// the target did not carry, at that field's path in the client's original tool.
func WebSearchLosses(t llm.Tool, fallbackPath, target string, carried WebSearchCarried) []llm.Loss {
	p := fallbackPath
	if t.Origin != nil && t.Origin.Path != "" {
		p = t.Origin.Path
	}
	detail := "no place for it in " + target + " web search"
	var losses []llm.Loss
	lose := func(path string) {
		losses = append(losses, llm.Loss{Kind: llm.LossDropped, Path: path, Detail: detail})
	}

	if t.Origin != nil && len(t.Origin.Raw) > 0 {
		if m := Object(t.Origin.Raw); m != nil {
			switch t.Origin.Format {
			case llm.FormatAnthropicMessages:
				for _, k := range sortedKeys(m) {
					v := m[k]
					if isNullValue(v) {
						continue
					}
					switch k {
					case "type", "name":
					case "max_uses":
						if !carried.MaxUses {
							lose(Join(p, k))
						}
					case "allowed_domains":
						if !isEmptyArray(v) && !carried.AllowedDomains {
							lose(Join(p, k))
						}
					case "blocked_domains":
						if !isEmptyArray(v) && !carried.BlockedDomains {
							lose(Join(p, k))
						}
					case "user_location":
						if !carried.UserLocation {
							lose(Join(p, k))
						} else if !carried.RawUserLocation {
							losses = append(losses, userLocationExtraLosses(v, Join(p, k), detail, false)...)
						}
					default:
						lose(Join(p, k))
					}
				}
				return losses
			case llm.FormatOpenAIResponses:
				for _, k := range sortedKeys(m) {
					v := m[k]
					if isNullValue(v) {
						continue
					}
					switch k {
					case "type", "name":
					case "external_web_access":
						var b bool
						if json.Unmarshal(v, &b) != nil || !b {
							lose(Join(p, k))
						}
					case "max_uses":
						if !carried.MaxUses {
							lose(Join(p, k))
						}
					case "search_context_size":
						if !isEmptyString(v) && !carried.SearchContextSize {
							lose(Join(p, k))
						}
					case "filters":
						fm := Object(v)
						if fm == nil {
							lose(Join(p, k))
							continue
						}
						fp := Join(p, k)
						for _, fk := range sortedKeys(fm) {
							fv := fm[fk]
							if isNullValue(fv) {
								continue
							}
							switch fk {
							case "allowed_domains":
								if !isEmptyArray(fv) && !carried.AllowedDomains {
									lose(Join(fp, fk))
								}
							case "blocked_domains":
								if !isEmptyArray(fv) && !carried.BlockedDomains {
									lose(Join(fp, fk))
								}
							default:
								lose(Join(fp, fk))
							}
						}
					case "user_location":
						if !carried.UserLocation {
							lose(Join(p, k))
						} else if !carried.RawUserLocation {
							losses = append(losses, userLocationExtraLosses(v, Join(p, k), detail, false)...)
						}
					default:
						lose(Join(p, k))
					}
				}
				return losses
			case llm.FormatOpenAIChat:
				for _, k := range sortedKeys(m) {
					v := m[k]
					if isNullValue(v) {
						continue
					}
					switch k {
					case "search_context_size":
						if !isEmptyString(v) && !carried.SearchContextSize {
							lose(Join(p, k))
						}
					case "user_location":
						if !carried.UserLocation {
							lose(Join(p, k))
						} else if !carried.RawUserLocation {
							losses = append(losses, userLocationExtraLosses(v, Join(p, k), detail, true)...)
						}
					default:
						lose(Join(p, k))
					}
				}
				return losses
			case llm.FormatGeminiGenerate:
				for _, k := range sortedKeys(m) {
					v := m[k]
					if isNullValue(v) {
						continue
					}
					if k == "googleSearch" {
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
				return losses
			}
		}
	}

	if ws := t.WebSearch; ws != nil {
		if ws.MaxUses != nil && !carried.MaxUses {
			lose(Join(p, "max_uses"))
		}
		if len(ws.AllowedDomains) > 0 && !carried.AllowedDomains {
			lose(Join(p, "allowed_domains"))
		}
		if len(ws.BlockedDomains) > 0 && !carried.BlockedDomains {
			lose(Join(p, "blocked_domains"))
		}
		if ws.UserLocation != nil && !carried.UserLocation {
			lose(Join(p, "user_location"))
		} else if ws.UserLocation != nil && !carried.RawUserLocation {
			if ws.UserLocation.Type != "" && ws.UserLocation.Type != "approximate" {
				lose(Join(p, "user_location.type"))
			}
			if ws.UserLocation.Country != "" && !IsCountryCode(ws.UserLocation.Country) {
				lose(Join(p, "user_location.country"))
			}
		}
		if ws.SearchContextSize != "" && !carried.SearchContextSize {
			lose(Join(p, "search_context_size"))
		}
	}
	return losses
}

func userLocationExtraLosses(raw json.RawMessage, locPath, detail string, allowApprox bool) []llm.Loss {
	m := Object(raw)
	if m == nil {
		return nil
	}
	var losses []llm.Loss
	for _, k := range sortedKeys(m) {
		v := m[k]
		if isNullValue(v) {
			continue
		}
		switch k {
		case "type":
			var s string
			if json.Unmarshal(v, &s) != nil || (s != "" && s != "approximate") {
				losses = append(losses, llm.Loss{Kind: llm.LossDropped, Path: Join(locPath, k), Detail: detail})
			}
		case "country":
			var s string
			if json.Unmarshal(v, &s) != nil || (s != "" && !IsCountryCode(s)) {
				losses = append(losses, llm.Loss{Kind: llm.LossDropped, Path: Join(locPath, k), Detail: detail})
			}
		case "city", "region", "timezone":
		case "approximate":
			if !allowApprox {
				losses = append(losses, llm.Loss{Kind: llm.LossDropped, Path: Join(locPath, k), Detail: detail})
				continue
			}
			am := Object(v)
			if am == nil {
				losses = append(losses, llm.Loss{Kind: llm.LossDropped, Path: Join(locPath, k), Detail: detail})
				continue
			}
			ap := Join(locPath, k)
			for _, ak := range sortedKeys(am) {
				if isNullValue(am[ak]) {
					continue
				}
				switch ak {
				case "country":
					var s string
					if json.Unmarshal(am[ak], &s) != nil || (s != "" && !IsCountryCode(s)) {
						losses = append(losses, llm.Loss{Kind: llm.LossDropped, Path: Join(ap, ak), Detail: detail})
					}
				case "city", "region", "timezone":
				default:
					losses = append(losses, llm.Loss{Kind: llm.LossDropped, Path: Join(ap, ak), Detail: detail})
				}
			}
		default:
			losses = append(losses, llm.Loss{Kind: llm.LossDropped, Path: Join(locPath, k), Detail: detail})
		}
	}
	return losses
}

func sortedKeys(m map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func isNullValue(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) == 0 || bytes.Equal(t, []byte("null"))
}

func isEmptyArray(raw json.RawMessage) bool {
	var a []json.RawMessage
	return json.Unmarshal(raw, &a) == nil && len(a) == 0
}

func isEmptyString(raw json.RawMessage) bool {
	var s string
	return json.Unmarshal(raw, &s) == nil && s == ""
}

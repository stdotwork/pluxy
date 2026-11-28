package rawjson

import (
	"bytes"
	"encoding/json"
	"slices"

	"github.com/stdotwork/pluxy/next/llm"
)

var CountryCodes = map[string]struct{}{
	"AD": {}, "AE": {}, "AF": {}, "AG": {}, "AI": {}, "AL": {}, "AM": {}, "AO": {}, "AQ": {}, "AR": {},
	"AS": {}, "AT": {}, "AU": {}, "AW": {}, "AX": {}, "AZ": {}, "BA": {}, "BB": {}, "BD": {}, "BE": {},
	"BF": {}, "BG": {}, "BH": {}, "BI": {}, "BJ": {}, "BL": {}, "BM": {}, "BN": {}, "BO": {}, "BQ": {},
	"BR": {}, "BS": {}, "BT": {}, "BV": {}, "BW": {}, "BY": {}, "BZ": {}, "CA": {}, "CC": {}, "CD": {},
	"CF": {}, "CG": {}, "CH": {}, "CI": {}, "CK": {}, "CL": {}, "CM": {}, "CN": {}, "CO": {}, "CR": {},
	"CU": {}, "CV": {}, "CW": {}, "CX": {}, "CY": {}, "CZ": {}, "DE": {}, "DJ": {}, "DK": {}, "DM": {},
	"DO": {}, "DZ": {}, "EC": {}, "EE": {}, "EG": {}, "EH": {}, "ER": {}, "ES": {}, "ET": {}, "FI": {},
	"FJ": {}, "FK": {}, "FM": {}, "FO": {}, "FR": {}, "GA": {}, "GB": {}, "GD": {}, "GE": {}, "GF": {},
	"GG": {}, "GH": {}, "GI": {}, "GL": {}, "GM": {}, "GN": {}, "GP": {}, "GQ": {}, "GR": {}, "GS": {},
	"GT": {}, "GU": {}, "GW": {}, "GY": {}, "HK": {}, "HM": {}, "HN": {}, "HR": {}, "HT": {}, "HU": {},
	"ID": {}, "IE": {}, "IL": {}, "IM": {}, "IN": {}, "IO": {}, "IQ": {}, "IR": {}, "IS": {}, "IT": {},
	"JE": {}, "JM": {}, "JO": {}, "JP": {}, "KE": {}, "KG": {}, "KH": {}, "KI": {}, "KM": {}, "KN": {},
	"KP": {}, "KR": {}, "KW": {}, "KY": {}, "KZ": {}, "LA": {}, "LB": {}, "LC": {}, "LI": {}, "LK": {},
	"LR": {}, "LS": {}, "LT": {}, "LU": {}, "LV": {}, "LY": {}, "MA": {}, "MC": {}, "MD": {}, "ME": {},
	"MF": {}, "MG": {}, "MH": {}, "MK": {}, "ML": {}, "MM": {}, "MN": {}, "MO": {}, "MP": {}, "MQ": {},
	"MR": {}, "MS": {}, "MT": {}, "MU": {}, "MV": {}, "MW": {}, "MX": {}, "MY": {}, "MZ": {}, "NA": {},
	"NC": {}, "NE": {}, "NF": {}, "NG": {}, "NI": {}, "NL": {}, "NO": {}, "NP": {}, "NR": {}, "NU": {},
	"NZ": {}, "OM": {}, "PA": {}, "PE": {}, "PF": {}, "PG": {}, "PH": {}, "PK": {}, "PL": {}, "PM": {},
	"PN": {}, "PR": {}, "PS": {}, "PT": {}, "PW": {}, "PY": {}, "QA": {}, "RE": {}, "RO": {}, "RS": {},
	"RU": {}, "RW": {}, "SA": {}, "SB": {}, "SC": {}, "SD": {}, "SE": {}, "SG": {}, "SH": {}, "SI": {},
	"SJ": {}, "SK": {}, "SL": {}, "SM": {}, "SN": {}, "SO": {}, "SR": {}, "SS": {}, "ST": {}, "SV": {},
	"SX": {}, "SY": {}, "SZ": {}, "TC": {}, "TD": {}, "TF": {}, "TG": {}, "TH": {}, "TJ": {}, "TK": {},
	"TL": {}, "TM": {}, "TN": {}, "TO": {}, "TR": {}, "TT": {}, "TV": {}, "TW": {}, "TZ": {}, "UA": {},
	"UG": {}, "UM": {}, "US": {}, "UY": {}, "UZ": {}, "VA": {}, "VC": {}, "VE": {}, "VG": {}, "VI": {},
	"VN": {}, "VU": {}, "WF": {}, "WS": {}, "YE": {}, "YT": {}, "ZA": {}, "ZM": {}, "ZW": {},
}

func IsCountryCode(s string) bool {
	_, ok := CountryCodes[s]
	return ok
}

type WebSearchCarried struct {
	MaxUses           bool
	AllowedDomains    bool
	BlockedDomains    bool
	UserLocation      bool
	RawUserLocation   bool
	SearchContextSize bool
}

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

package conversation

import (
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
)

var (
	aliasOnce sync.Once
	aliasMap  map[string]string
)

// EnsureGeminiWebAliasMap populates the alias map once.
func EnsureGeminiWebAliasMap() {
	aliasOnce.Do(func() {
		aliasMap = make(map[string]string)
		for _, m := range registry.GetGeminiModels() {
			if m.ID == "gemini-2.5-flash-lite" {
				continue
			}
			if m.ID == "gemini-2.5-flash" {
				aliasMap["gemini-2.5-flash-image-preview"] = "gemini-2.5-flash"
			}
			alias := AliasFromModelID(m.ID)
			aliasMap[strings.ToLower(alias)] = strings.ToLower(m.ID)
		}
	})
}

// MapAliasToUnderlying normalizes a model alias to its underlying identifier.
func MapAliasToUnderlying(name string) string {
	EnsureGeminiWebAliasMap()
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" {
		return n
	}
	if u, ok := aliasMap[n]; ok {
		return u
	}
	const suffix = "-web"
	if strings.HasSuffix(n, suffix) {
		return strings.TrimSuffix(n, suffix)
	}
	return n
}

// AliasFromModelID mirrors the original helper for deriving alias IDs.
func AliasFromModelID(modelID string) string {
	return modelID + "-web"
}

// NormalizeModel returns the canonical identifier used for hashing.
func NormalizeModel(model string) string {
	return MapAliasToUnderlying(model)
}

// GetGeminiWebAliasedModels returns alias metadata for registry exposure.
func GetGeminiWebAliasedModels() []*registry.ModelInfo {
	EnsureGeminiWebAliasMap()
	aliased := make([]*registry.ModelInfo, 0)
	for _, m := range registry.GetGeminiModels() {
		if m.ID == "gemini-2.5-flash-lite" {
			continue
		} else if m.ID == "gemini-2.5-flash" {
			cpy := *m
			cpy.ID = "gemini-2.5-flash-image-preview"
			cpy.Name = "gemini-2.5-flash-image-preview"
			cpy.DisplayName = "Nano Banana"
			cpy.Description = "Gemini 2.5 Flash Preview Image"
			aliased = append(aliased, &cpy)
		}
		cpy := *m
		cpy.ID = AliasFromModelID(m.ID)
		cpy.Name = cpy.ID
		aliased = append(aliased, &cpy)
	}
	return aliased
}

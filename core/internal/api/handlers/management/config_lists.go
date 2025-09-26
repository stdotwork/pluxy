package management

import (
	"encoding/json"
	"fmt"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	sdkConfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

// Generic helpers for list[string]
func (h *Handler) putStringList(c *gin.Context, set func([]string), after func()) {
	data, err := c.GetRawData()
	if err != nil {
		c.JSON(400, gin.H{"error": "failed to read body"})
		return
	}
	var arr []string
	if err = json.Unmarshal(data, &arr); err != nil {
		var obj struct {
			Items []string `json:"items"`
		}
		if err2 := json.Unmarshal(data, &obj); err2 != nil || len(obj.Items) == 0 {
			c.JSON(400, gin.H{"error": "invalid body"})
			return
		}
		arr = obj.Items
	}
	set(arr)
	if after != nil {
		after()
	}
	h.persist(c)
}

func (h *Handler) patchStringList(c *gin.Context, target *[]string, after func()) {
	var body struct {
		Old   *string `json:"old"`
		New   *string `json:"new"`
		Index *int    `json:"index"`
		Value *string `json:"value"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(400, gin.H{"error": "invalid body"})
		return
	}
	if body.Index != nil && body.Value != nil && *body.Index >= 0 && *body.Index < len(*target) {
		(*target)[*body.Index] = *body.Value
		if after != nil {
			after()
		}
		h.persist(c)
		return
	}
	if body.Old != nil && body.New != nil {
		for i := range *target {
			if (*target)[i] == *body.Old {
				(*target)[i] = *body.New
				if after != nil {
					after()
				}
				h.persist(c)
				return
			}
		}
		*target = append(*target, *body.New)
		if after != nil {
			after()
		}
		h.persist(c)
		return
	}
	c.JSON(400, gin.H{"error": "missing fields"})
}

func (h *Handler) deleteFromStringList(c *gin.Context, target *[]string, after func()) {
	if idxStr := c.Query("index"); idxStr != "" {
		var idx int
		_, err := fmt.Sscanf(idxStr, "%d", &idx)
		if err == nil && idx >= 0 && idx < len(*target) {
			*target = append((*target)[:idx], (*target)[idx+1:]...)
			if after != nil {
				after()
			}
			h.persist(c)
			return
		}
	}
	if val := c.Query("value"); val != "" {
		out := make([]string, 0, len(*target))
		for _, v := range *target {
			if v != val {
				out = append(out, v)
			}
		}
		*target = out
		if after != nil {
			after()
		}
		h.persist(c)
		return
	}
	c.JSON(400, gin.H{"error": "missing index or value"})
}

// api-keys
func (h *Handler) GetAPIKeys(c *gin.Context) { c.JSON(200, gin.H{"api-keys": h.cfg.APIKeys}) }
func (h *Handler) PutAPIKeys(c *gin.Context) {
	h.putStringList(c, func(v []string) { sdkConfig.SyncInlineAPIKeys(&h.cfg.SDKConfig, v) }, nil)
}
func (h *Handler) PatchAPIKeys(c *gin.Context) {
	h.patchStringList(c, &h.cfg.APIKeys, func() { sdkConfig.SyncInlineAPIKeys(&h.cfg.SDKConfig, h.cfg.APIKeys) })
}
func (h *Handler) DeleteAPIKeys(c *gin.Context) {
	h.deleteFromStringList(c, &h.cfg.APIKeys, func() { sdkConfig.SyncInlineAPIKeys(&h.cfg.SDKConfig, h.cfg.APIKeys) })
}

// generative-language-api-key
func (h *Handler) GetGlKeys(c *gin.Context) {
	c.JSON(200, gin.H{"generative-language-api-key": h.cfg.GlAPIKey})
}
func (h *Handler) PutGlKeys(c *gin.Context) {
	h.putStringList(c, func(v []string) { h.cfg.GlAPIKey = v }, nil)
}
func (h *Handler) PatchGlKeys(c *gin.Context)  { h.patchStringList(c, &h.cfg.GlAPIKey, nil) }
func (h *Handler) DeleteGlKeys(c *gin.Context) { h.deleteFromStringList(c, &h.cfg.GlAPIKey, nil) }

// claude-api-key: []ClaudeKey
func (h *Handler) GetClaudeKeys(c *gin.Context) {
	c.JSON(200, gin.H{"claude-api-key": h.cfg.ClaudeKey})
}
func (h *Handler) PutClaudeKeys(c *gin.Context) {
	data, err := c.GetRawData()
	if err != nil {
		c.JSON(400, gin.H{"error": "failed to read body"})
		return
	}
	var arr []config.ClaudeKey
	if err = json.Unmarshal(data, &arr); err != nil {
		var obj struct {
			Items []config.ClaudeKey `json:"items"`
		}
		if err2 := json.Unmarshal(data, &obj); err2 != nil || len(obj.Items) == 0 {
			c.JSON(400, gin.H{"error": "invalid body"})
			return
		}
		arr = obj.Items
	}
	h.cfg.ClaudeKey = arr
	h.persist(c)
}
func (h *Handler) PatchClaudeKey(c *gin.Context) {
	var body struct {
		Index *int              `json:"index"`
		Match *string           `json:"match"`
		Value *config.ClaudeKey `json:"value"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Value == nil {
		c.JSON(400, gin.H{"error": "invalid body"})
		return
	}
	if body.Index != nil && *body.Index >= 0 && *body.Index < len(h.cfg.ClaudeKey) {
		h.cfg.ClaudeKey[*body.Index] = *body.Value
		h.persist(c)
		return
	}
	if body.Match != nil {
		for i := range h.cfg.ClaudeKey {
			if h.cfg.ClaudeKey[i].APIKey == *body.Match {
				h.cfg.ClaudeKey[i] = *body.Value
				h.persist(c)
				return
			}
		}
	}
	c.JSON(404, gin.H{"error": "item not found"})
}
func (h *Handler) DeleteClaudeKey(c *gin.Context) {
	if val := c.Query("api-key"); val != "" {
		out := make([]config.ClaudeKey, 0, len(h.cfg.ClaudeKey))
		for _, v := range h.cfg.ClaudeKey {
			if v.APIKey != val {
				out = append(out, v)
			}
		}
		h.cfg.ClaudeKey = out
		h.persist(c)
		return
	}
	if idxStr := c.Query("index"); idxStr != "" {
		var idx int
		_, err := fmt.Sscanf(idxStr, "%d", &idx)
		if err == nil && idx >= 0 && idx < len(h.cfg.ClaudeKey) {
			h.cfg.ClaudeKey = append(h.cfg.ClaudeKey[:idx], h.cfg.ClaudeKey[idx+1:]...)
			h.persist(c)
			return
		}
	}
	c.JSON(400, gin.H{"error": "missing api-key or index"})
}

// openai-compatibility: []OpenAICompatibility
func (h *Handler) GetOpenAICompat(c *gin.Context) {
	c.JSON(200, gin.H{"openai-compatibility": h.cfg.OpenAICompatibility})
}
func (h *Handler) PutOpenAICompat(c *gin.Context) {
	data, err := c.GetRawData()
	if err != nil {
		c.JSON(400, gin.H{"error": "failed to read body"})
		return
	}
	var arr []config.OpenAICompatibility
	if err = json.Unmarshal(data, &arr); err != nil {
		var obj struct {
			Items []config.OpenAICompatibility `json:"items"`
		}
		if err2 := json.Unmarshal(data, &obj); err2 != nil || len(obj.Items) == 0 {
			c.JSON(400, gin.H{"error": "invalid body"})
			return
		}
		arr = obj.Items
	}
	h.cfg.OpenAICompatibility = arr
	h.persist(c)
}
func (h *Handler) PatchOpenAICompat(c *gin.Context) {
	var body struct {
		Name  *string                     `json:"name"`
		Index *int                        `json:"index"`
		Value *config.OpenAICompatibility `json:"value"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Value == nil {
		c.JSON(400, gin.H{"error": "invalid body"})
		return
	}
	if body.Index != nil && *body.Index >= 0 && *body.Index < len(h.cfg.OpenAICompatibility) {
		h.cfg.OpenAICompatibility[*body.Index] = *body.Value
		h.persist(c)
		return
	}
	if body.Name != nil {
		for i := range h.cfg.OpenAICompatibility {
			if h.cfg.OpenAICompatibility[i].Name == *body.Name {
				h.cfg.OpenAICompatibility[i] = *body.Value
				h.persist(c)
				return
			}
		}
	}
	c.JSON(404, gin.H{"error": "item not found"})
}
func (h *Handler) DeleteOpenAICompat(c *gin.Context) {
	if name := c.Query("name"); name != "" {
		out := make([]config.OpenAICompatibility, 0, len(h.cfg.OpenAICompatibility))
		for _, v := range h.cfg.OpenAICompatibility {
			if v.Name != name {
				out = append(out, v)
			}
		}
		h.cfg.OpenAICompatibility = out
		h.persist(c)
		return
	}
	if idxStr := c.Query("index"); idxStr != "" {
		var idx int
		_, err := fmt.Sscanf(idxStr, "%d", &idx)
		if err == nil && idx >= 0 && idx < len(h.cfg.OpenAICompatibility) {
			h.cfg.OpenAICompatibility = append(h.cfg.OpenAICompatibility[:idx], h.cfg.OpenAICompatibility[idx+1:]...)
			h.persist(c)
			return
		}
	}
	c.JSON(400, gin.H{"error": "missing name or index"})
}

// codex-api-key: []CodexKey
func (h *Handler) GetCodexKeys(c *gin.Context) {
	c.JSON(200, gin.H{"codex-api-key": h.cfg.CodexKey})
}
func (h *Handler) PutCodexKeys(c *gin.Context) {
	data, err := c.GetRawData()
	if err != nil {
		c.JSON(400, gin.H{"error": "failed to read body"})
		return
	}
	var arr []config.CodexKey
	if err = json.Unmarshal(data, &arr); err != nil {
		var obj struct {
			Items []config.CodexKey `json:"items"`
		}
		if err2 := json.Unmarshal(data, &obj); err2 != nil || len(obj.Items) == 0 {
			c.JSON(400, gin.H{"error": "invalid body"})
			return
		}
		arr = obj.Items
	}
	h.cfg.CodexKey = arr
	h.persist(c)
}
func (h *Handler) PatchCodexKey(c *gin.Context) {
	var body struct {
		Index *int             `json:"index"`
		Match *string          `json:"match"`
		Value *config.CodexKey `json:"value"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Value == nil {
		c.JSON(400, gin.H{"error": "invalid body"})
		return
	}
	if body.Index != nil && *body.Index >= 0 && *body.Index < len(h.cfg.CodexKey) {
		h.cfg.CodexKey[*body.Index] = *body.Value
		h.persist(c)
		return
	}
	if body.Match != nil {
		for i := range h.cfg.CodexKey {
			if h.cfg.CodexKey[i].APIKey == *body.Match {
				h.cfg.CodexKey[i] = *body.Value
				h.persist(c)
				return
			}
		}
	}
	c.JSON(404, gin.H{"error": "item not found"})
}
func (h *Handler) DeleteCodexKey(c *gin.Context) {
	if val := c.Query("api-key"); val != "" {
		out := make([]config.CodexKey, 0, len(h.cfg.CodexKey))
		for _, v := range h.cfg.CodexKey {
			if v.APIKey != val {
				out = append(out, v)
			}
		}
		h.cfg.CodexKey = out
		h.persist(c)
		return
	}
	if idxStr := c.Query("index"); idxStr != "" {
		var idx int
		_, err := fmt.Sscanf(idxStr, "%d", &idx)
		if err == nil && idx >= 0 && idx < len(h.cfg.CodexKey) {
			h.cfg.CodexKey = append(h.cfg.CodexKey[:idx], h.cfg.CodexKey[idx+1:]...)
			h.persist(c)
			return
		}
	}
	c.JSON(400, gin.H{"error": "missing api-key or index"})
}

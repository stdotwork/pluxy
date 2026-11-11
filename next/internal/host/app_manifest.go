package host

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// AppContribution describes the desktop part of a version-1 manifest. Its files and
// programs are never used by core. The contract is in app/app-host/README.md.
type AppContribution struct {
	Version      int               `json:"version"`
	Tier         string            `json:"tier"`
	HostVersions []int             `json:"host_versions"`
	UI           *UI               `json:"ui,omitempty"`
	Config       *ConfigSection    `json:"config,omitempty"`
	Resources    []AppResource     `json:"resources,omitempty"`
	Programs     map[string]string `json:"programs,omitempty"`
	Operations   []AppOperation    `json:"operations,omitempty"`
	State        *AppState         `json:"state,omitempty"`
}

type AppResource struct {
	ID         string   `json:"id"`
	Kind       string   `json:"kind"`
	Operations []string `json:"operations"`
}

type AppOperation struct {
	ID           string          `json:"id"`
	Description  string          `json:"description"`
	InputSchema  json.RawMessage `json:"input_schema"`
	OutputSchema json.RawMessage `json:"output_schema"`
}

type AppState struct {
	Version          int   `json:"version"`
	ReadableVersions []int `json:"readable_versions"`
}

func (a *AppContribution) validate() error {
	if a.Version != 0 {
		return fmt.Errorf("app.version %d is not supported", a.Version)
	}
	if a.Tier != "web" && a.Tier != "native" {
		return errors.New("app.tier must be web or native")
	}
	if len(a.HostVersions) == 0 {
		return errors.New("app.host_versions must list compatible host contracts")
	}
	for _, v := range a.HostVersions {
		if v < 0 {
			return errors.New("app.host_versions must be nonnegative")
		}
	}
	if a.UI != nil {
		if err := a.UI.validate(); err != nil {
			return fmt.Errorf("app: %w", err)
		}
	}
	if a.Config != nil {
		if err := a.Config.validate(); err != nil {
			return fmt.Errorf("app: %w", err)
		}
	}
	if a.Tier == "web" {
		if a.Programs != nil || a.Operations != nil || a.State != nil {
			return errors.New("web app cannot declare programs, operations or state")
		}
		seen := map[string]bool{}
		for _, r := range a.Resources {
			if !pageID.MatchString(r.ID) || seen[r.ID] || (r.Kind != "file" && r.Kind != "subtree") || len(r.Operations) == 0 {
				return errors.New("app.resources require unique IDs, file or subtree kind and operations")
			}
			for _, op := range r.Operations {
				if strings.TrimSpace(op) == "" {
					return errors.New("app.resources operations must not be empty")
				}
			}
			seen[r.ID] = true
		}
		return nil
	}
	if a.Resources != nil {
		return errors.New("native app cannot declare resources")
	}
	if len(a.Programs) == 0 {
		return errors.New("native app programs must list at least one platform")
	}
	for platform, prog := range a.Programs {
		if !filepath.IsLocal(filepath.FromSlash(prog)) || strings.Contains(prog, `\`) {
			return fmt.Errorf("app program for %s must be a path inside the package folder", platform)
		}
	}
	if len(a.Operations) == 0 {
		return errors.New("native app operations must list at least one operation")
	}
	seen := map[string]bool{}
	for _, op := range a.Operations {
		if !pageID.MatchString(op.ID) || seen[op.ID] || strings.TrimSpace(op.Description) == "" {
			return errors.New("app.operations require unique IDs and descriptions")
		}
		seen[op.ID] = true
		for _, schema := range []json.RawMessage{op.InputSchema, op.OutputSchema} {
			var obj map[string]json.RawMessage
			if err := json.Unmarshal(schema, &obj); err != nil || obj == nil {
				return errors.New("app.operations schemas must be JSON objects")
			}
		}
	}
	if a.State == nil || a.State.Version < 0 || len(a.State.ReadableVersions) == 0 {
		return errors.New("native app state requires version and readable_versions")
	}
	for _, v := range a.State.ReadableVersions {
		if v < 0 {
			return errors.New("app.state.readable_versions must be nonnegative")
		}
	}
	return nil
}

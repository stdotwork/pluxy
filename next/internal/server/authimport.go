package server

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/stdotwork/pluxy/next/internal/legacy"
	"github.com/stdotwork/pluxy/next/internal/route"
)

const maxAuthImportBody = 4 << 20

type authImported struct {
	ID        string   `json:"id"`
	Extension string   `json:"extension"`
	Ignored   []string `json:"ignored,omitempty"`
}

func (m *Management) importAuthFile(w http.ResponseWriter, r *http.Request) {
	b, ok := readBody(w, r, maxAuthImportBody)
	if !ok {
		return
	}
	var req struct {
		Name    string  `json:"name"`
		Content *string `json:"content"`
	}
	if json.Unmarshal(b, &req) != nil || req.Content == nil {
		writeManagementError(w, http.StatusBadRequest, "invalid_request", `send {"name": "<file name>.json", "content": "<the file's text>"}`)
		return
	}
	if !authFileName(req.Name) {
		writeManagementError(w, http.StatusBadRequest, "invalid_request", "name must be a .json file name, with no folder")
		return
	}
	if len(*req.Content) > legacy.MaxAuthFile {
		writeManagementError(w, http.StatusRequestEntityTooLarge, "invalid_request", "the file is "+legacy.TooLarge)
		return
	}
	cfg := m.cfg()
	files, unread := legacy.ReadAuthFile(req.Name, []byte(*req.Content), cfg.Import, time.Now())
	imported, taken, err := legacy.Import(m.creds, files, func(id string) bool {
		return slices.ContainsFunc(cfg.Accounts(), func(a route.Account) bool { return strings.EqualFold(a.ID, id) })
	})
	if err != nil && len(imported) == 0 {
		writeCredError(w, err)
		return
	}
	reasons := []string{}
	for _, s := range append(unread, taken...) {
		reasons = append(reasons, s.Reason)
	}
	if err != nil {
		reasons = append(reasons, err.Error())
	}
	switch {
	case len(imported) == 0 && len(taken) > 0:
		writeManagementError(w, http.StatusConflict, "conflict", strings.Join(reasons, "; "))
	case len(imported) == 0:
		writeManagementError(w, http.StatusBadRequest, "invalid_request", strings.Join(reasons, "; "))
	default:
		out := []authImported{}
		for _, f := range imported {
			out = append(out, authImported{ID: f.Record.ID, Extension: f.Record.Extension, Ignored: f.Ignored})
		}
		writeJSON(w, http.StatusOK, map[string]any{"imported": out, "skipped": reasons})
	}
}

func authFileName(name string) bool {
	return len(name) > len(".json") && len(name) <= 255 && strings.HasSuffix(strings.ToLower(name), ".json") &&
		!strings.ContainsAny(name, "/\\\x00")
}

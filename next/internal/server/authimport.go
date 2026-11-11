package server

import (
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/stdotwork/pluxy/next/internal/legacy"
	"github.com/stdotwork/pluxy/next/internal/route"
)

// maxAuthImportBody limits the body of POST /accounts/import: room for a file of
// legacy.MaxAuthFile written as a JSON string, where a quote, backslash or line break takes two
// characters.
const maxAuthImportBody = 4 << 20

// authImported is an account POST /accounts/import added.
type authImported struct {
	ID        string   `json:"id"`
	Extension string   `json:"extension"`
	Ignored   []string `json:"ignored,omitempty"`
}

// importAuthFile is POST /accounts/import: {"name": "<file>.json", "content": "<its text>"}, one
// credential file of today's core's auth folder, read and added as -import-auth reads and adds
// each file of the folder (legacy.ReadAuthFile, legacy.Import), into the running core's store,
// where its accounts serve at once. Core reads no file of its own machine for it. Nothing of the
// file is logged or sent back: the reply names the accounts added (ID, extension, the file's
// settings not supported yet) and why the others were skipped.
func (m *Management) importAuthFile(w http.ResponseWriter, r *http.Request) {
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxAuthImportBody))
	if err != nil {
		writeManagementError(w, http.StatusRequestEntityTooLarge, "invalid_request", "request body is too large")
		return
	}
	var req struct {
		Name    string  `json:"name"`
		Content *string `json:"content"`
	}
	// The decoding error is not sent back: it can quote the file.
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
	// As -import-auth: an ID the config uses, whatever its case, is not taken.
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

// authFileName reports whether name names a credential file as today's core reads them from its
// auth folder: a .json file, with no folder in its name.
func authFileName(name string) bool {
	return len(name) > len(".json") && len(name) <= 255 && strings.HasSuffix(strings.ToLower(name), ".json") &&
		!strings.ContainsAny(name, "/\\\x00")
}

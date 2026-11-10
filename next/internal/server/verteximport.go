package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/stdotwork/pluxy/next/internal/cred"
	"github.com/stdotwork/pluxy/next/internal/legacy"
	"github.com/stdotwork/pluxy/next/internal/route"
	"github.com/stdotwork/pluxy/next/llm"
)

// vertexImportWait is how long POST /vertex/import waits for the login: the Gemini extension's
// token fetch has a 30 second timeout of its own.
const vertexImportWait = 45 * time.Second

// defaultVertexLocation is the location an import without one uses, as in today's core.
const defaultVertexLocation = "us-central1"

// vertexImportLoginMethod is the Gemini extension's login method that takes a service account key.
const vertexImportLoginMethod = "service_account"

// Codes of the import's own errors: today's `error` words, as codes.
const (
	codeFileRequired       = "file_required"
	codeInvalidJSON        = "invalid_json"
	codeInvalidServiceAcct = "invalid_service_account"
	codeProjectIDMissing   = "project_id_missing"
)

// importVertex is POST /vertex/import: the multipart field file holds a Google service account
// key, and location (a form field or a query parameter) the Vertex location. It adds the account
// vertex-<project_id> by the Gemini extension's service_account login, or signs in to it again
// when a login added it before (so the key replaces the old one only if it is the same service
// account), waits for the login to end and replies once.
func (m *Management) importVertex(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxManagementBody)
	if err := r.ParseMultipartForm(maxManagementBody); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeManagementError(w, http.StatusRequestEntityTooLarge, "invalid_request", "request body is too large")
			return
		}
		writeManagementError(w, http.StatusBadRequest, codeFileRequired, "file required: send multipart/form-data with the service account key in the field file")
		return
	}
	defer r.MultipartForm.RemoveAll()
	if len(r.MultipartForm.File["file"]) > 1 {
		writeManagementError(w, http.StatusBadRequest, "invalid_request", "send one service account key in the field file, not several")
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		writeManagementError(w, http.StatusBadRequest, codeFileRequired, "file required")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		writeManagementError(w, http.StatusBadRequest, "invalid_request", "failed to read file: "+err.Error())
		return
	}

	var key map[string]any
	if err := json.Unmarshal(data, &key); err != nil {
		writeManagementError(w, http.StatusBadRequest, codeInvalidJSON, "invalid json: "+err.Error())
		return
	}
	if pk, _ := key["private_key"].(string); strings.TrimSpace(pk) == "" {
		writeManagementError(w, http.StatusBadRequest, codeInvalidServiceAcct, "invalid service account: service account missing private_key")
		return
	}
	project := strings.TrimSpace(stringField(key, "project_id"))
	if project == "" {
		writeManagementError(w, http.StatusBadRequest, codeProjectIDMissing, "project_id missing")
		return
	}
	email := strings.TrimSpace(stringField(key, "client_email"))
	location := strings.TrimSpace(r.PostFormValue("location"))
	if location == "" {
		location = strings.TrimSpace(r.URL.Query().Get("location"))
	}
	asked := location != ""
	if !asked {
		location = defaultVertexLocation
	}

	id := vertexAccountID(project)
	if _, ok := m.configured(id); ok {
		writeManagementError(w, http.StatusConflict, "conflict", fmt.Sprintf("account %q is set in core's config", id))
		return
	}
	// An account a login added before gets a new login: its settings stay as they are, so the
	// location it has is the one that counts.
	var existing *cred.Record
	for _, rec := range m.creds.Records() {
		if rec.ID == id {
			existing = &rec
			break
		}
	}
	if existing != nil {
		// Only the same project's Vertex service account is signed in to again: another account
		// that happens to have this ID (an API key, another project whose ID sanitises the same)
		// must not get this key with its own settings.
		if existing.Extension != legacy.Gemini || existing.Credential.Kind != vertexImportLoginMethod || vertexProject(existing.Account.Settings) != project {
			writeManagementError(w, http.StatusConflict, "conflict", fmt.Sprintf(
				"account %q exists and is not project %q's Vertex service account; remove it or import into another account", id, project))
			return
		}
		have := vertexLocation(existing.Account.Settings)
		if asked && location != have {
			writeManagementError(w, http.StatusConflict, "conflict", fmt.Sprintf(
				"account %q uses location %q; to change it, remove the account and import the key again", id, have))
			return
		}
		location = have
	}
	settings, _ := json.Marshal(map[string]any{"vertex": map[string]string{"project": project, "location": location}})

	ctx, cancel := context.WithTimeout(r.Context(), vertexImportWait)
	defer cancel()
	l, err := m.creds.StartLogin(ctx, cred.LoginRequest{
		Account:   route.Account{ID: id, Settings: settings},
		Extension: legacy.Gemini,
		Method:    vertexImportLoginMethod,
		Input:     map[string]string{"service_account": string(data)},
		Label:     vertexLabel(project, email),
		Reauth:    existing != nil,
	})
	if err != nil {
		writeCredError(w, err)
		return
	}
	if l.State == cred.LoginPending {
		// The login ended nothing in time (or waits for input a key import never gets), or the
		// client went away. Cancel it and wait, briefly, for it to end, so an import tried again
		// at once is not refused as a login already running.
		_ = m.creds.CancelLogin(l.ID)
		for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
			if v, ok := m.creds.Login(l.ID); !ok || v.State != cred.LoginPending {
				break
			}
		}
		writeManagementError(w, http.StatusGatewayTimeout, llm.CodeUnavailable, "the service account login did not finish in time")
		return
	}
	if l.State != cred.LoginDone {
		writeVertexImportFailure(w, l)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "account": id, "project_id": project, "email": email, "location": location})
}

// writeVertexImportFailure answers a login that did not end done: a key Google or the extension
// refused is 400, a token endpoint that could not be reached or answered badly 502.
func writeVertexImportFailure(w http.ResponseWriter, l cred.LoginView) {
	e := l.Error
	switch {
	case e == nil:
		writeManagementError(w, http.StatusInternalServerError, llm.CodeInternal, fmt.Sprintf("the service account login ended %s", l.State))
	case e.Code == llm.CodeAuth:
		writeManagementError(w, http.StatusBadRequest, codeInvalidServiceAcct, "invalid service account: "+e.Message)
	case e.Code == llm.CodeInvalidRequest:
		writeManagementError(w, http.StatusBadRequest, "invalid_request", e.Message)
	case e.Code == llm.CodeInternal:
		writeManagementError(w, http.StatusInternalServerError, "internal", e.Message)
	default:
		writeManagementError(w, http.StatusBadGateway, e.Code, e.Message)
	}
}

func stringField(m map[string]any, name string) string {
	s, _ := m[name].(string)
	return s
}

// vertexAccountID returns the ID of the account an imported key for project gets: vertex- and
// the project ID as today's core names its auth file (a path separator or colon becomes _, a
// space -).
func vertexAccountID(project string) string {
	return "vertex-" + strings.NewReplacer("/", "_", `\`, "_", ":", "_", " ", "-").Replace(project)
}

// vertexLabel is the label of an imported account, as today's core makes it.
func vertexLabel(project, email string) string {
	if email == "" {
		return project
	}
	return project + " (" + email + ")"
}

// vertexProject returns the vertex.project in an account's settings, or "".
func vertexProject(settings json.RawMessage) string {
	var s struct {
		Vertex struct {
			Project string `json:"project"`
		} `json:"vertex"`
	}
	_ = json.Unmarshal(settings, &s)
	return s.Vertex.Project
}

// vertexLocation returns the vertex.location in an account's settings; "global" (the Gemini
// extension's default) when it sets none.
func vertexLocation(settings json.RawMessage) string {
	var s struct {
		Vertex struct {
			Location string `json:"location"`
		} `json:"vertex"`
	}
	_ = json.Unmarshal(settings, &s)
	if s.Vertex.Location == "" {
		return "global"
	}
	return s.Vertex.Location
}

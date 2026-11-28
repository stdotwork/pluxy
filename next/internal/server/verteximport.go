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

const vertexImportWait = 45 * time.Second

const defaultVertexLocation = "us-central1"

const vertexImportLoginMethod = "service_account"

const (
	codeFileRequired       = "file_required"
	codeInvalidJSON        = "invalid_json"
	codeInvalidServiceAcct = "invalid_service_account"
	codeProjectIDMissing   = "project_id_missing"
)

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
	var existing *cred.Record
	for _, rec := range m.creds.Records() {
		if rec.ID == id {
			existing = &rec
			break
		}
	}
	if existing != nil {
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
	req := cred.LoginRequest{
		Account:   route.Account{ID: id, Settings: settings},
		Extension: legacy.Gemini,
		Method:    vertexImportLoginMethod,
		Input:     map[string]string{"service_account": string(data)},
		Label:     vertexLabel(project, email),
		Reauth:    existing != nil,
	}
	if existing != nil {
		req.Registration = existing.Registration
	}
	l, err := m.creds.StartLogin(ctx, req)
	if err != nil {
		writeCredError(w, err)
		return
	}
	if l.State == cred.LoginPending {
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

func vertexAccountID(project string) string {
	return "vertex-" + strings.NewReplacer("/", "_", `\`, "_", ":", "_", " ", "-").Replace(project)
}

func vertexLabel(project, email string) string {
	if email == "" {
		return project
	}
	return project + " (" + email + ")"
}

func vertexProject(settings json.RawMessage) string {
	var s struct {
		Vertex struct {
			Project string `json:"project"`
		} `json:"vertex"`
	}
	_ = json.Unmarshal(settings, &s)
	return s.Vertex.Project
}

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

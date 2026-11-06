package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/stdotwork/pluxy/next/internal/cred"
	"github.com/stdotwork/pluxy/next/internal/route"
	"github.com/stdotwork/pluxy/next/llm"
)

// ManagementPrefix is where the management API is served. The 0 is its version.
const ManagementPrefix = "/v0/management"

// maxManagementBody limits a management request body.
const maxManagementBody = 1 << 20

// AccountView is an account as the management API lists it. It never holds a secret.
type AccountView struct {
	ID string `json:"id"`
	// Source is "config" for an account set in core's config, "login" for one added by login.
	Source    string `json:"source"`
	Extension string `json:"extension,omitempty"`
	// Kind is the credential's kind, for example api_key or oauth.
	Kind     string `json:"kind,omitempty"`
	State    string `json:"state"`
	Disabled bool   `json:"disabled,omitempty"`
	// Reason says why the account needs a new login.
	Reason      string `json:"reason,omitempty"`
	Label       string `json:"label,omitempty"`
	Version     uint64 `json:"version,omitempty"`
	ExpiresAt   int64  `json:"expires_at,omitempty"`
	Refreshable bool   `json:"refreshable"`
	// RenewalError is why the last automatic renewal failed, while core waits before trying
	// again.
	RenewalError *llm.Error `json:"renewal_error,omitempty"`
	CreatedAt    *time.Time `json:"created_at,omitempty"`
	UpdatedAt    *time.Time `json:"updated_at,omitempty"`
}

type management struct {
	cfg   *Config
	creds *cred.Manager
}

// addManagement serves the management API on mux, when the config sets a management key.
func addManagement(mux *http.ServeMux, cfg *Config, creds *cred.Manager) {
	if cfg.ManagementKey == "" || creds == nil {
		return
	}
	m := &management{cfg: cfg, creds: creds}
	handle := func(pattern string, h func(w http.ResponseWriter, r *http.Request)) {
		method, path, _ := strings.Cut(pattern, " ")
		mux.HandleFunc(method+" "+ManagementPrefix+path, func(w http.ResponseWriter, r *http.Request) {
			if !m.authorized(r) {
				writeManagementError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid management key")
				return
			}
			w.Header().Set("Cache-Control", "no-store")
			h(w, r)
		})
	}
	handle("GET /accounts", m.list)
	handle("POST /accounts", m.add)
	handle("PATCH /accounts/{id}", m.patch)
	handle("DELETE /accounts/{id}", m.remove)
	handle("POST /accounts/{id}/refresh", m.refresh)
	handle("POST /accounts/{id}/login", m.reauth)
	handle("GET /logins/{id}", m.login)
	handle("POST /logins/{id}/input", m.input)
	handle("DELETE /logins/{id}", m.cancel)
}

func (m *management) authorized(r *http.Request) bool {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return ok && token != "" && subtle.ConstantTimeCompare([]byte(token), []byte(m.cfg.ManagementKey)) == 1
}

func (m *management) configured(id string) (route.Account, bool) {
	i := slices.IndexFunc(m.cfg.Extension.Accounts, func(a route.Account) bool { return a.ID == id })
	if i < 0 {
		return route.Account{}, false
	}
	return m.cfg.Extension.Accounts[i], true
}

func (m *management) view(r cred.Record) AccountView {
	v := AccountView{ID: r.ID, Source: "login", Extension: r.Extension, Kind: r.Credential.Kind, State: r.State,
		Disabled: r.Disabled, Reason: r.Reason, Label: r.Label, Version: r.Version, ExpiresAt: r.Credential.ExpiresAt,
		Refreshable: m.creds.Refreshable(r), RenewalError: m.creds.LastRenewalError(r.ID)}
	created, updated := r.CreatedAt, r.UpdatedAt
	v.CreatedAt, v.UpdatedAt = &created, &updated
	return v
}

func (m *management) list(w http.ResponseWriter, r *http.Request) {
	views := []AccountView{}
	for _, a := range m.cfg.Extension.Accounts {
		v := AccountView{ID: a.ID, Source: "config", State: cred.StateActive}
		if a.Credential != nil {
			v.Kind = a.Credential.Kind
		}
		views = append(views, v)
	}
	for _, rec := range m.creds.Records() {
		if _, shadowed := m.configured(rec.ID); !shadowed {
			views = append(views, m.view(rec))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"accounts": views})
}

// addRequest is the body of POST /accounts: the account's fields (as in the config, without a
// credential) and how to log in.
type addRequest struct {
	route.Account
	Method string            `json:"method"`
	Input  map[string]string `json:"input,omitempty"`
}

func (m *management) add(w http.ResponseWriter, r *http.Request) {
	var req addRequest
	if !readJSON(w, r, &req) {
		return
	}
	if req.Credential != nil {
		writeManagementError(w, http.StatusBadRequest, "invalid_request", "an added account's credential comes from its login; send method and input instead")
		return
	}
	if req.MaxConcurrency < 0 {
		writeManagementError(w, http.StatusBadRequest, "invalid_request", "max_concurrency must not be negative")
		return
	}
	m.startLogin(w, r, cred.LoginRequest{Account: req.Account, Method: req.Method, Input: req.Input})
}

func (m *management) reauth(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Method string            `json:"method"`
		Input  map[string]string `json:"input,omitempty"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	m.startLogin(w, r, cred.LoginRequest{Account: route.Account{ID: id}, Method: req.Method, Input: req.Input, Reauth: true})
}

func (m *management) startLogin(w http.ResponseWriter, r *http.Request, req cred.LoginRequest) {
	if _, ok := m.configured(req.Account.ID); ok {
		writeManagementError(w, http.StatusConflict, "conflict", fmt.Sprintf("account %q is set in core's config", req.Account.ID))
		return
	}
	// Wait a little for the provider's first prompt, so the reply usually carries it; the
	// login goes on either way.
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	l, err := m.creds.StartLogin(ctx, req)
	if err != nil {
		writeCredError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"login": l})
}

func (m *management) login(w http.ResponseWriter, r *http.Request) {
	l, ok := m.creds.Login(r.PathValue("id"))
	if !ok {
		writeCredError(w, cred.ErrNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"login": l})
}

func (m *management) input(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Input map[string]string `json:"input"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if err := m.creds.LoginInput(r.PathValue("id"), req.Input); err != nil {
		writeCredError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (m *management) cancel(w http.ResponseWriter, r *http.Request) {
	if err := m.creds.CancelLogin(r.PathValue("id")); err != nil {
		writeCredError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// stored refuses changes to config accounts, which only the config changes.
func (m *management) stored(w http.ResponseWriter, id string) bool {
	if _, ok := m.configured(id); ok {
		writeManagementError(w, http.StatusConflict, "conflict", fmt.Sprintf("account %q is set in core's config; change it there", id))
		return false
	}
	return true
}

func (m *management) patch(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Disabled *bool `json:"disabled"`
	}
	if !readJSON(w, r, &req) || !m.stored(w, id) {
		return
	}
	if req.Disabled == nil {
		writeManagementError(w, http.StatusBadRequest, "invalid_request", "nothing to change: send disabled")
		return
	}
	rec, err := m.creds.SetDisabled(id, *req.Disabled)
	if err != nil {
		writeCredError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"account": m.view(rec)})
}

func (m *management) remove(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !m.stored(w, id) {
		return
	}
	if err := m.creds.Remove(id); err != nil {
		writeCredError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (m *management) refresh(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !m.stored(w, id) {
		return
	}
	rec, err := m.creds.Refresh(r.Context(), id)
	if err != nil {
		writeCredError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"account": m.view(rec)})
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxManagementBody))
	if err != nil {
		writeManagementError(w, http.StatusRequestEntityTooLarge, "invalid_request", "request body is too large")
		return false
	}
	if err := json.Unmarshal(b, v); err != nil {
		writeManagementError(w, http.StatusBadRequest, "invalid_request", "request body is not valid JSON: "+err.Error())
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeManagementError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": msg}})
}

// writeCredError writes an error from the credential manager. A provider's failure is a 502
// carrying the provider's error code.
func writeCredError(w http.ResponseWriter, err error) {
	var le *llm.Error
	switch {
	case errors.As(err, &le):
		writeManagementError(w, http.StatusBadGateway, le.Code, le.Message)
	case errors.Is(err, cred.ErrNotFound):
		writeManagementError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, cred.ErrExists), errors.Is(err, cred.ErrConflict), errors.Is(err, cred.ErrStale):
		writeManagementError(w, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, cred.ErrInvalid):
		writeManagementError(w, http.StatusBadRequest, "invalid_request", err.Error())
	default:
		writeManagementError(w, http.StatusInternalServerError, "internal", err.Error())
	}
}

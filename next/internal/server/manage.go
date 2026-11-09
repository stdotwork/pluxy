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
	// Check is the result of core's last check of the account; absent while core has none.
	Check *AccountCheck `json:"check,omitempty"`
}

// Account check states (AccountCheck.State).
const (
	CheckRunning    = "checking"
	CheckOK         = "ok"
	CheckFailed     = "failed"
	CheckNotChecked = "not_checked"
)

// AccountCheck is the result of core's check of an account: the extension's check call, which
// asks the upstream something that costs the account nothing. Core checks an account when it
// starts, and when a reload or a login adds or changes one.
type AccountCheck struct {
	// State is checking, ok, failed, or not_checked when the extension cannot check the account.
	State string `json:"state"`
	// Detail says what was checked (ok) or why the account was not checked (not_checked).
	Detail string `json:"detail,omitempty"`
	// Error is why the check failed (failed).
	Error *llm.Error `json:"error,omitempty"`
	// At is when the check ended.
	At *time.Time `json:"at,omitempty"`
}

// Management is the management API, version 0. Every call needs the management key of the
// config active when it arrives, as a bearer token; while that config sets none, the API is not
// served.
type Management struct {
	mux   *http.ServeMux
	cfg   func() *Config
	creds *cred.Manager
	// Check, when set, returns the result of core's check of account id, or nil.
	Check func(id string) *AccountCheck
}

// NewManagement returns the management API with its account and login calls. cfg returns the
// active config.
func NewManagement(cfg func() *Config, creds *cred.Manager) *Management {
	m := &Management{mux: http.NewServeMux(), cfg: cfg, creds: creds}
	m.Handle("GET /accounts", m.list)
	m.Handle("POST /accounts", m.add)
	m.Handle("PATCH /accounts/{id}", m.patch)
	m.Handle("DELETE /accounts/{id}", m.remove)
	m.Handle("POST /accounts/{id}/refresh", m.refresh)
	m.Handle("POST /accounts/{id}/login", m.reauth)
	m.Handle("GET /logins/{id}", m.login)
	m.Handle("POST /logins/{id}/input", m.input)
	m.Handle("DELETE /logins/{id}", m.cancel)
	return m
}

// Handle adds a call: pattern is a method and a path under ManagementPrefix, such as
// "GET /accounts".
func (m *Management) Handle(pattern string, h http.HandlerFunc) {
	method, path, _ := strings.Cut(pattern, " ")
	m.mux.HandleFunc(method+" "+ManagementPrefix+path, h)
}

func (m *Management) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key := m.cfg().ManagementKey
	if key == "" {
		http.NotFound(w, r)
		return
	}
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(key)) != 1 {
		writeManagementError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid management key")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	m.mux.ServeHTTP(w, r)
}

func (m *Management) configured(id string) (route.Account, bool) {
	return configured(m.cfg(), id)
}

func configured(cfg *Config, id string) (route.Account, bool) {
	accounts := cfg.Accounts()
	i := slices.IndexFunc(accounts, func(a route.Account) bool { return a.ID == id })
	if i < 0 {
		return route.Account{}, false
	}
	return accounts[i], true
}

func (m *Management) view(r cred.Record) AccountView {
	v := AccountView{ID: r.ID, Source: "login", Extension: r.Extension, Kind: r.Credential.Kind, State: r.State,
		Disabled: r.Disabled, Reason: r.Reason, Label: r.Label, Version: r.Version, ExpiresAt: r.Credential.ExpiresAt,
		Refreshable: m.creds.Refreshable(r), RenewalError: m.creds.LastRenewalError(r)}
	created, updated := r.CreatedAt, r.UpdatedAt
	v.CreatedAt, v.UpdatedAt = &created, &updated
	return v
}

func (m *Management) list(w http.ResponseWriter, r *http.Request) {
	cfg := m.cfg()
	views := []AccountView{}
	for _, a := range cfg.Accounts() {
		v := AccountView{ID: a.ID, Source: "config", Extension: a.Extension, State: cred.StateActive}
		if a.Credential != nil {
			v.Kind = a.Credential.Kind
		}
		views = append(views, v)
	}
	for _, rec := range m.creds.Records() {
		if _, shadowed := configured(cfg, rec.ID); !shadowed {
			views = append(views, m.view(rec))
		}
	}
	if m.Check != nil {
		for i := range views {
			views[i].Check = m.Check(views[i].ID)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"accounts": views})
}

// addRequest is the body of POST /accounts: the account's fields (as in the config, without a
// credential) and how to log in.
type addRequest struct {
	route.Account
	// Extension names the extension that signs in; empty while only one runs.
	Extension string            `json:"extension"`
	Method    string            `json:"method"`
	Input     map[string]string `json:"input,omitempty"`
}

func (m *Management) add(w http.ResponseWriter, r *http.Request) {
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
	m.startLogin(w, r, cred.LoginRequest{Account: req.Account, Extension: req.Extension, Method: req.Method, Input: req.Input})
}

func (m *Management) reauth(w http.ResponseWriter, r *http.Request) {
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

func (m *Management) startLogin(w http.ResponseWriter, r *http.Request, req cred.LoginRequest) {
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

func (m *Management) login(w http.ResponseWriter, r *http.Request) {
	l, ok := m.creds.Login(r.PathValue("id"))
	if !ok {
		writeCredError(w, cred.ErrNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"login": l})
}

func (m *Management) input(w http.ResponseWriter, r *http.Request) {
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

func (m *Management) cancel(w http.ResponseWriter, r *http.Request) {
	if err := m.creds.CancelLogin(r.PathValue("id")); err != nil {
		writeCredError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// stored refuses changes to config accounts, which only the config changes.
func (m *Management) stored(w http.ResponseWriter, id string) bool {
	if _, ok := m.configured(id); ok {
		writeManagementError(w, http.StatusConflict, "conflict", fmt.Sprintf("account %q is set in core's config; change it there", id))
		return false
	}
	return true
}

func (m *Management) patch(w http.ResponseWriter, r *http.Request) {
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

func (m *Management) remove(w http.ResponseWriter, r *http.Request) {
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

func (m *Management) refresh(w http.ResponseWriter, r *http.Request) {
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

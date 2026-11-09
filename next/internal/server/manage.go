package server

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"net/http"
	"slices"
	"strconv"
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
	// The account's routing fields, as its config entry or its login set them (route.Account).
	Priority       int                `json:"priority,omitempty"`
	Weight         *int               `json:"weight,omitempty"`
	DisableCooling *bool              `json:"disable_cooling,omitempty"`
	MaxConcurrency int                `json:"max_concurrency,omitempty"`
	Prefix         string             `json:"prefix,omitempty"`
	Models         []route.ModelEntry `json:"models,omitempty"`
	ExcludedModels []string           `json:"excluded_models,omitempty"`
	ModelAlias     []route.ModelAlias `json:"model_alias,omitempty"`
	Endpoint       string             `json:"endpoint,omitempty"`

	// What routing knows about the account now (route.Runtime). An account that receives no
	// requests (disabled, or waiting for a new login) has none of it.
	CooldownUntil  *time.Time       `json:"cooldown_until,omitempty"`
	Cooldowns      []route.Cooldown `json:"cooldowns,omitempty"`
	LastError      *llm.Error       `json:"last_error,omitempty"`
	LastErrorAt    *time.Time       `json:"last_error_at,omitempty"`
	ActiveRequests int              `json:"active_requests,omitempty"`

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

// fields sets v's routing fields from a and its runtime state from state.
func (v *AccountView) fields(a route.Account, state *route.State) {
	v.Priority, v.Weight, v.MaxConcurrency, v.Prefix = a.Priority, a.Weight, a.MaxConcurrency, a.Prefix
	v.DisableCooling = a.DisableCooling
	v.Models, v.ExcludedModels, v.ModelAlias, v.Endpoint = a.Models, a.ExcludedModels, a.ModelAlias, a.Endpoint
	if state == nil {
		return
	}
	rt := state.Runtime(a, time.Now())
	if !rt.CooldownUntil.IsZero() {
		v.CooldownUntil = &rt.CooldownUntil
	}
	v.Cooldowns, v.LastError, v.ActiveRequests = rt.Cooldowns, rt.LastError, rt.Active
	if rt.LastError != nil {
		v.LastErrorAt = &rt.LastErrorAt
	}
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
	state *route.State
	// lockout blocks a client that keeps sending a wrong key.
	lockout *lockout
}

// NewManagement returns the management API with its account and login calls. cfg returns the
// active config; state is the routing state accounts' cooldowns are read from (nil: none).
func NewManagement(cfg func() *Config, creds *cred.Manager, state *route.State) *Management {
	m := &Management{mux: http.NewServeMux(), cfg: cfg, creds: creds, state: state, lockout: newLockout()}
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
	client := ClientIP(r)
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	right := ok && token != "" && sameKey(token, key)
	left, blockedNow := m.lockout.attempt(client, right)
	if left > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int((left+time.Second-1)/time.Second)))
		writeManagementError(w, http.StatusForbidden, "blocked",
			fmt.Sprintf("too many failed management keys from this address; try again in %s", left.Round(time.Second)))
		return
	}
	if !right {
		if blockedNow {
			log.Printf("management: %d failed management keys in a row from %s; refusing it for %s", lockoutFailures, client, lockoutFor)
		}
		writeManagementError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid management key")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	m.mux.ServeHTTP(w, r)
}

// sameKey compares a sent key with the management key in constant time, length included: it
// compares their SHA-256 digests.
func sameKey(sent, key string) bool {
	a, b := sha256.Sum256([]byte(sent)), sha256.Sum256([]byte(key))
	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
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
	a := r.Account
	a.ID = r.ID
	v.fields(a, m.state)
	v.Check = m.check(r.ID)
	return v
}

// check returns the result of core's check of account id, or nil.
func (m *Management) check(id string) *AccountCheck {
	if m.Check == nil {
		return nil
	}
	return m.Check(id)
}

// configView returns config account a as the management API lists it.
func (m *Management) configView(a route.Account) AccountView {
	v := AccountView{ID: a.ID, Source: "config", Extension: a.Extension, State: cred.StateActive}
	if a.Credential != nil {
		v.Kind = a.Credential.Kind
	}
	v.fields(a, m.state)
	v.Check = m.check(a.ID)
	return v
}

// View returns account id as the management API lists it: the config's account of that ID,
// else the one added by login.
func (m *Management) View(id string) (AccountView, bool) {
	if a, ok := m.configured(id); ok {
		return m.configView(a), true
	}
	for _, rec := range m.creds.Records() {
		if rec.ID == id {
			return m.view(rec), true
		}
	}
	return AccountView{}, false
}

func (m *Management) list(w http.ResponseWriter, r *http.Request) {
	cfg := m.cfg()
	views := []AccountView{}
	for _, a := range cfg.Accounts() {
		views = append(views, m.configView(a))
	}
	for _, rec := range m.creds.Records() {
		if _, shadowed := configured(cfg, rec.ID); !shadowed {
			views = append(views, m.view(rec))
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

// patchFields are the fields PATCH /accounts/{id} changes besides disabled: an account's routing
// fields (route.Account), by their JSON names. null sets a field back to its default.
var patchFields = map[string]func(a *route.Account, raw json.RawMessage) error{
	"priority": func(a *route.Account, raw json.RawMessage) error {
		a.Priority = 0
		return unmarshalSet(raw, &a.Priority)
	},
	"weight": func(a *route.Account, raw json.RawMessage) error { a.Weight = nil; return unmarshalSet(raw, &a.Weight) },
	"disable_cooling": func(a *route.Account, raw json.RawMessage) error {
		a.DisableCooling = nil
		return unmarshalSet(raw, &a.DisableCooling)
	},
	"max_concurrency": func(a *route.Account, raw json.RawMessage) error {
		a.MaxConcurrency = 0
		return unmarshalSet(raw, &a.MaxConcurrency)
	},
	"prefix": func(a *route.Account, raw json.RawMessage) error { a.Prefix = ""; return unmarshalSet(raw, &a.Prefix) },
	"models": func(a *route.Account, raw json.RawMessage) error { a.Models = nil; return unmarshalSet(raw, &a.Models) },
	"excluded_models": func(a *route.Account, raw json.RawMessage) error {
		a.ExcludedModels = nil
		return unmarshalSet(raw, &a.ExcludedModels)
	},
	"model_alias": func(a *route.Account, raw json.RawMessage) error {
		a.ModelAlias = nil
		return unmarshalSet(raw, &a.ModelAlias)
	},
}

// unmarshalSet decodes raw into v, leaving v as it is for null.
func unmarshalSet(raw json.RawMessage, v any) error {
	if string(raw) == "null" {
		return nil
	}
	return json.Unmarshal(raw, v)
}

func (m *Management) patch(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req map[string]json.RawMessage
	if !readJSON(w, r, &req) || !m.stored(w, id) {
		return
	}
	var disabled *bool
	var changes []func(*route.Account) error
	for name, raw := range req {
		if name == "disabled" {
			if err := json.Unmarshal(raw, &disabled); err != nil || disabled == nil {
				writeManagementError(w, http.StatusBadRequest, "invalid_request", "disabled must be true or false")
				return
			}
			continue
		}
		set, ok := patchFields[name]
		if !ok {
			writeManagementError(w, http.StatusBadRequest, "invalid_request", fmt.Sprintf("%q cannot be changed here: send disabled or any of %s",
				name, strings.Join(slices.Sorted(maps.Keys(patchFields)), ", ")))
			return
		}
		changes = append(changes, func(a *route.Account) error {
			if err := set(a, raw); err != nil {
				return fmt.Errorf("%s: %v", name, err)
			}
			return nil
		})
	}
	if disabled == nil && len(changes) == 0 {
		writeManagementError(w, http.StatusBadRequest, "invalid_request", "nothing to change: send disabled or a routing field")
		return
	}
	// Decode every field before changing anything, so a bad one changes nothing.
	var probe route.Account
	for _, c := range changes {
		if err := c(&probe); err != nil {
			writeManagementError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
	}
	var change func(*route.Account)
	if len(changes) > 0 {
		change = func(a *route.Account) {
			for _, c := range changes {
				_ = c(a) // each decoded above
			}
		}
	}
	rec, err := m.creds.SetAccount(id, change, disabled)
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

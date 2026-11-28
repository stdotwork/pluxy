package server

import (
	"bytes"
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
	"unicode"
	"unicode/utf8"

	"github.com/stdotwork/pluxy/next/internal/cred"
	"github.com/stdotwork/pluxy/next/internal/proxyurl"
	"github.com/stdotwork/pluxy/next/internal/route"
	"github.com/stdotwork/pluxy/next/internal/upheader"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/management"
	"github.com/stdotwork/pluxy/next/protocol"
)

const ManagementPrefix = management.Prefix

const maxManagementBody = 1 << 20

type AccountView = management.Account

const (
	CheckRunning    = management.CheckRunning
	CheckOK         = management.CheckOK
	CheckFailed     = management.CheckFailed
	CheckNotChecked = management.CheckNotChecked
)

type AccountCheck = management.AccountCheck

func setFields(v *AccountView, a route.Account, state *route.State, hold route.Hold, red AccountRedactor) {
	v.Priority, v.Weight, v.MaxConcurrency, v.Prefix = a.Priority, a.Weight, a.MaxConcurrency, a.Prefix
	v.DisableCooling, v.RequestRetry = a.DisableCooling, a.RequestRetry
	if state != nil {
		if q, ok := state.LastQuota(a.ID); ok {
			v.Quota = &management.QuotaReply{FetchedAt: q.At.UTC(), Quota: q.Quota}
		}
	}
	v.Models, v.ExcludedModels, v.ModelAlias, v.Endpoint = a.Models, a.ExcludedModels, a.ModelAlias, a.Endpoint
	v.Timeouts = a.Timeouts
	v.ProxyURL = proxyurl.Redact(a.ProxyURL)
	if red != nil {
		v.Headers = red.Headers(a.Headers)
	} else if len(a.Headers) > 0 {
		v.Headers = map[string]string{}
		for k, val := range a.Headers {
			if upheader.Secret(k) {
				val = protocol.Redacted
			}
			v.Headers[k] = val
		}
	}
	now := time.Now()
	if state == nil {
		state = route.NewState()
		v.Health, v.PublicID = state.Health(a, now, hold), state.PublicID(a.ID)
		return
	}
	v.PublicID = state.PublicID(a.ID)
	v.Health = state.Health(a, now, hold)
	rt := state.Runtime(a, now)
	if !rt.CooldownUntil.IsZero() {
		v.CooldownUntil = &rt.CooldownUntil
	}
	v.Cooldowns, v.LastError, v.ActiveRequests = rt.Cooldowns, rt.LastError, rt.Active
	if rt.LastError != nil {
		v.LastErrorAt = &rt.LastErrorAt
	}
}

type Management struct {
	mux      *http.ServeMux
	cfg      func() *Config
	creds    *cred.Manager
	Check    func(id string) *AccountCheck
	Ignition func(id string) *management.AccountIgnition
	state    *route.State
	lockout  *lockout
	routes   []string
	Redactor AccountRedactor
}

type AccountRedactor interface {
	Headers(h map[string]string) map[string]string
	RestoreHeaders(sent, cur map[string]string) (map[string]string, error)
	RedactSettings(settings []byte) ([]byte, error)
	RestoreSettings(sent, current []byte) ([]byte, error)
}

var ErrStaleSecret = errors.New("a redacted value matches no secret the account's settings hold now")

var ErrSecretCopy = errors.New("settings")

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
	m.Handle("POST /vertex/import", m.importVertex)
	m.Handle("POST /accounts/import", m.importAuthFile)
	return m
}

func (m *Management) Routes() []string { return slices.Clone(m.routes) }

func (m *Management) Handle(pattern string, h http.HandlerFunc) {
	m.routes = append(m.routes, pattern)
	method, path, _ := strings.Cut(pattern, " ")
	m.mux.HandleFunc(method+" "+ManagementPrefix+path, h)
}

func (m *Management) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	cfg := m.cfg()
	if !cfg.ManagementAllowRemote && !FromThisMachine(r) {
		writeManagementError(w, http.StatusForbidden, "remote_disabled",
			"remote management is off: the management API answers only this machine (management_allow_remote turns on other machines)")
		return
	}
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || token == "" {
		writeManagementError(w, http.StatusUnauthorized, "unauthorized", "missing management key")
		return
	}
	right := sameKey(token, cfg.managementKeyDigest())
	if FromThisMachine(r) {
		if !right {
			writeManagementError(w, http.StatusUnauthorized, "unauthorized", "invalid management key")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		m.mux.ServeHTTP(w, r)
		return
	}
	client := ClientIP(r)
	left, blockedNow := m.lockout.attempt(client, right)
	if left > 0 {
		wait := llm.WholeSeconds(left)
		w.Header().Set("Retry-After", strconv.FormatInt(int64(wait/time.Second), 10))
		writeManagementError(w, http.StatusForbidden, "blocked",
			fmt.Sprintf("too many failed management keys from this address; try again in %s", wait))
		return
	}
	if !right {
		if blockedNow {
			log.Printf("management: %d failed management keys in a row from %s; refusing it for %s", lockoutFailures, client, lockoutFor)
		}
		writeManagementError(w, http.StatusUnauthorized, "unauthorized", "invalid management key")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	m.mux.ServeHTTP(w, r)
}

func sameKey(sent string, key [sha256.Size]byte) bool {
	a := sha256.Sum256([]byte(sent))
	return subtle.ConstantTimeCompare(a[:], key[:]) == 1
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
	setFields(&v, a, m.state, route.Hold{Disabled: r.Disabled, NeedsLogin: r.State == cred.StateReauth, Why: r.Reason}, m.Redactor)
	if len(a.Settings) > 0 && m.Redactor != nil {
		s, err := m.Redactor.RedactSettings(a.Settings)
		if err != nil {
			log.Printf("management: account %q: its settings cannot be shown with their secrets hidden", r.ID)
		} else {
			v.Settings, v.SettingsInexact = s, inexact(s)
		}
	}
	v.Check = m.check(r.ID)
	v.Ignition = m.ignition(r.ID)
	return v
}

func (m *Management) brokenView(b cred.Broken) AccountView {
	v := AccountView{ID: b.ID, Source: "login", Health: route.Health{State: route.HealthBroken, Why: b.Why()}}
	if m.state != nil {
		v.PublicID = m.state.PublicID(b.ID)
	}
	return v
}

func inexact(b []byte) bool {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	for {
		t, err := d.Token()
		if err != nil {
			return false
		}
		n, ok := t.(json.Number)
		if !ok {
			continue
		}
		sent := decimalOf(n.String())
		if sent.digits == "" {
			continue
		}
		mag := sent.exp + int64(len(sent.digits))
		if mag > 400 || mag < -400 {
			return true
		}
		sign := ""
		if sent.neg {
			sign = "-"
		}
		f, err := strconv.ParseFloat(sign+"0."+sent.digits+"e"+strconv.FormatInt(mag, 10), 64)
		if err != nil || decimalOf(strconv.FormatFloat(f, 'g', -1, 64)) != sent {
			return true
		}
	}
}

type decimal struct {
	neg    bool
	digits string
	exp    int64
}

func decimalOf(s string) decimal {
	var d decimal
	s, d.neg = strings.CutPrefix(s, "-")
	mantissa, e, _ := strings.Cut(strings.ToLower(s), "e")
	whole, frac, _ := strings.Cut(mantissa, ".")
	if e != "" {
		eneg := false
		if e[0] == '+' || e[0] == '-' {
			eneg, e = e[0] == '-', e[1:]
		}
		for i := 0; i < len(e); i++ {
			d.exp = min(d.exp*10+int64(e[i]-'0'), 1<<40)
		}
		if eneg {
			d.exp = -d.exp
		}
	}
	digits := strings.TrimLeft(whole+frac, "0")
	trimmed := strings.TrimRight(digits, "0")
	d.exp += int64(len(digits)-len(trimmed)) - int64(len(frac))
	d.digits = trimmed
	if d.digits == "" {
		return decimal{}
	}
	return d
}

func (m *Management) check(id string) *AccountCheck {
	if m.Check == nil {
		return nil
	}
	return m.Check(id)
}

func (m *Management) ignition(id string) *management.AccountIgnition {
	if m.Ignition == nil {
		return nil
	}
	return m.Ignition(id)
}

func (m *Management) configView(a route.Account) AccountView {
	v := AccountView{ID: a.ID, Source: "config", Extension: a.Extension, State: cred.StateActive}
	if a.Credential != nil {
		v.Kind = a.Credential.Kind
	}
	setFields(&v, a, m.state, route.Hold{}, m.Redactor)
	v.Check = m.check(a.ID)
	v.Ignition = m.ignition(a.ID)
	return v
}

func (m *Management) View(id string) (AccountView, bool) {
	if a, ok := m.configured(id); ok {
		return m.configView(a), true
	}
	for _, rec := range m.creds.Records() {
		if rec.ID == id {
			return m.view(rec), true
		}
	}
	for _, b := range m.creds.Broken() {
		if b.ID == id {
			return m.brokenView(b), true
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
	for _, b := range m.creds.Broken() {
		if _, shadowed := configured(cfg, b.ID); !shadowed {
			views = append(views, m.brokenView(b))
		}
	}
	writeJSON(w, http.StatusOK, management.AccountsReply{Accounts: views})
}

type addRequest struct {
	route.Account
	Label     string            `json:"label"`
	Extension string            `json:"extension"`
	Method    string            `json:"method"`
	Input     map[string]string `json:"input,omitempty"`
}

func (m *Management) add(w http.ResponseWriter, r *http.Request) {
	var req addRequest
	if !readJSON(w, r, &req) {
		return
	}
	if len(req.Settings) > 0 {
		var settings map[string]json.RawMessage
		if json.Unmarshal(req.Settings, &settings) != nil {
			writeManagementError(w, http.StatusBadRequest, "invalid_request", "account settings must be an object")
			return
		}
	}
	if req.Credential != nil {
		writeManagementError(w, http.StatusBadRequest, "invalid_request", "an added account's credential comes from its login; send method and input instead")
		return
	}
	if req.MaxConcurrency < 0 {
		writeManagementError(w, http.StatusBadRequest, "invalid_request", "max_concurrency must not be negative")
		return
	}
	if req.RequestRetry != nil && *req.RequestRetry < 0 {
		writeManagementError(w, http.StatusBadRequest, "invalid_request", "request_retry must not be negative")
		return
	}
	if err := proxyurl.Check("proxy_url", req.ProxyURL); err != nil {
		writeManagementError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := upheader.Check("headers", req.Headers); err != nil {
		writeManagementError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := route.ValidateTimeouts("timeouts", req.Timeouts); err != nil {
		writeManagementError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	label, err := CheckLabel(req.Label)
	if err != nil {
		writeManagementError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	m.startLogin(w, r, cred.LoginRequest{Account: req.Account, Extension: req.Extension, Method: req.Method, Input: req.Input, Label: label})
}

const maxLabel = 64

func CheckLabel(label string) (string, error) {
	label = strings.TrimSpace(label)
	notOneLine := strings.ContainsFunc(label, func(r rune) bool { return unicode.IsControl(r) || r == '\u2028' || r == '\u2029' })
	if notOneLine || utf8.RuneCountInString(label) > maxLabel {
		return "", fmt.Errorf("label must be one line of at most %d characters", maxLabel)
	}
	return label, nil
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

func (m *Management) stored(w http.ResponseWriter, id string) bool {
	if _, ok := m.configured(id); ok {
		writeManagementError(w, http.StatusConflict, "conflict", fmt.Sprintf("account %q is set in core's config; change it there", id))
		return false
	}
	return true
}

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
	"request_retry": func(a *route.Account, raw json.RawMessage) error {
		a.RequestRetry = nil
		return unmarshalSet(raw, &a.RequestRetry)
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
	"proxy_url": func(a *route.Account, raw json.RawMessage) error {
		cur := a.ProxyURL
		a.ProxyURL = ""
		if err := unmarshalSet(raw, &a.ProxyURL); err != nil {
			return err
		}
		if len(proxyurl.Password(cur)) > 0 && strings.TrimSpace(a.ProxyURL) == proxyurl.Redact(cur) {
			a.ProxyURL = cur
		}
		return proxyurl.Check("value", a.ProxyURL)
	},
	"headers": nil,
	"timeouts": func(a *route.Account, raw json.RawMessage) error {
		a.Timeouts = nil
		if err := unmarshalSet(raw, &a.Timeouts); err != nil {
			return err
		}
		return route.ValidateTimeouts("value", a.Timeouts)
	},
}

func (m *Management) patchHeaders(a *route.Account, raw json.RawMessage) error {
	var sent map[string]string
	if err := unmarshalSet(raw, &sent); err != nil {
		return err
	}
	if m.Redactor == nil {
		return errors.New("headers cannot be changed here")
	}
	h, err := m.Redactor.RestoreHeaders(sent, a.Headers)
	if err != nil {
		return err
	}
	if err := upheader.Check("value", h); err != nil {
		return err
	}
	a.Headers = h
	return nil
}

func unmarshalSet(raw json.RawMessage, v any) error {
	if string(raw) == "null" {
		return nil
	}
	return json.Unmarshal(raw, v)
}

func (m *Management) patch(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req map[string]json.RawMessage
	if !readJSON(w, r, &req) {
		return
	}
	if cfgAcct, ok := m.configured(id); ok {
		var disabled *bool
		if len(req) == 1 {
			if raw, has := req["disabled"]; has && json.Unmarshal(raw, &disabled) == nil && disabled != nil && !*disabled {
				if m.state != nil {
					m.state.Unban(id)
				}
				writeJSON(w, http.StatusOK, management.AccountReply{Account: m.configView(cfgAcct)})
				return
			}
		}
		m.stored(w, id)
		return
	}
	var disabled *bool
	var label *string
	var settings json.RawMessage
	var changes []func(*route.Account) error
	for name, raw := range req {
		if name == "label" {
			var sent *string
			if json.Unmarshal(raw, &sent) != nil {
				writeManagementError(w, http.StatusBadRequest, "invalid_request", "label must be a string, or null for none")
				return
			}
			l := ""
			if sent != nil {
				var err error
				if l, err = CheckLabel(*sent); err != nil {
					writeManagementError(w, http.StatusBadRequest, "invalid_request", err.Error())
					return
				}
			}
			label = &l
			continue
		}
		if name == "disabled" {
			if err := json.Unmarshal(raw, &disabled); err != nil || disabled == nil {
				writeManagementError(w, http.StatusBadRequest, "invalid_request", "disabled must be true or false")
				return
			}
			continue
		}
		if name == "settings" {
			if m.Redactor == nil {
				writeManagementError(w, http.StatusInternalServerError, "internal", "settings cannot be changed: core set no redactor for them")
				return
			}
			var obj map[string]json.RawMessage
			if string(raw) != "null" && (json.Unmarshal(raw, &obj) != nil || obj == nil) {
				writeManagementError(w, http.StatusBadRequest, "invalid_request", "settings must be an object, or null for none")
				return
			}
			settings = raw
			continue
		}
		set, ok := patchFields[name]
		if ok && set == nil {
			set = m.patchHeaders
		}
		if !ok {
			writeManagementError(w, http.StatusBadRequest, "invalid_request", fmt.Sprintf("%q cannot be changed here: send disabled, label, settings or any of %s",
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
	if disabled == nil && label == nil && settings == nil && len(changes) == 0 {
		writeManagementError(w, http.StatusBadRequest, "invalid_request", "nothing to change: send disabled, label, settings or a routing field")
		return
	}
	var probe route.Account
	recs := m.creds.Records()
	if i := slices.IndexFunc(recs, func(r cred.Record) bool { return r.ID == id }); i >= 0 {
		probe = recs[i].Account
	}
	for _, c := range changes {
		if err := c(&probe); err != nil {
			writeManagementError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
	}
	var change func(*route.Account) error
	if len(changes) > 0 || settings != nil {
		change = func(a *route.Account) error {
			for _, c := range changes {
				_ = c(a)
			}
			if settings == nil {
				return nil
			}
			return m.restoreSettings(a, settings)
		}
	}
	rec, err := m.creds.SetAccount(id, change, disabled, label)
	if err != nil {
		writeCredError(w, err)
		return
	}
	if disabled != nil && !*disabled && m.state != nil {
		m.state.Unban(id)
	}
	writeJSON(w, http.StatusOK, management.AccountReply{Account: m.view(rec)})
}

func (m *Management) restoreSettings(a *route.Account, sent json.RawMessage) error {
	if string(sent) == "null" {
		a.Settings = nil
		return nil
	}
	b, err := m.Redactor.RestoreSettings(sent, a.Settings)
	switch {
	case errors.Is(err, ErrStaleSecret):
		return fmt.Errorf("%w: settings: a redacted value matches no secret the account's settings hold now; read the account again", cred.ErrConflict)
	case errors.Is(err, ErrSecretCopy):
		return fmt.Errorf("%w: %v", cred.ErrInvalid, err)
	case err != nil:
		return fmt.Errorf("%w: settings: a redacted value is where no secret goes; write the value itself there", cred.ErrInvalid)
	}
	a.Settings = b
	return nil
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
	writeJSON(w, http.StatusOK, management.AccountReply{Account: m.view(rec)})
}

func readBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, bool) {
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err == nil {
		return b, true
	}
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		writeManagementError(w, http.StatusRequestEntityTooLarge, "invalid_request", "request body is too large")
		return nil, false
	}
	writeManagementError(w, http.StatusBadRequest, "invalid_request", "reading the request body: "+err.Error())
	return nil, false
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	b, ok := readBody(w, r, maxManagementBody)
	if !ok {
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
	writeJSON(w, status, management.ErrorBody{Error: management.ErrorDetail{Code: code, Message: msg}})
}

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

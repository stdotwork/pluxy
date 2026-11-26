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

// ManagementPrefix is where the management API is served. The 0 is its version.
const ManagementPrefix = management.Prefix

// maxManagementBody limits a management request body.
const maxManagementBody = 1 << 20

// AccountView is an account as the management API lists it (management.Account).
type AccountView = management.Account

// Account check states (AccountCheck.State).
const (
	CheckRunning    = management.CheckRunning
	CheckOK         = management.CheckOK
	CheckFailed     = management.CheckFailed
	CheckNotChecked = management.CheckNotChecked
)

// AccountCheck is the result of core's check of an account (management.AccountCheck).
type AccountCheck = management.AccountCheck

// setFields sets v's routing fields from a, with its secret header values replaced by red's
// placeholders, and its runtime state and health from state and hold.
func setFields(v *AccountView, a route.Account, state *route.State, hold route.Hold, red AccountRedactor) {
	v.Priority, v.Weight, v.MaxConcurrency, v.Prefix = a.Priority, a.Weight, a.MaxConcurrency, a.Prefix
	v.DisableCooling, v.RequestRetry = a.DisableCooling, a.RequestRetry
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

// Management is the management API, version 0. Every call needs the management key of the
// config active when it arrives, as a bearer token.
type Management struct {
	mux   *http.ServeMux
	cfg   func() *Config
	creds *cred.Manager
	// Check, when set, returns the result of core's check of account id, or nil.
	Check func(id string) *AccountCheck
	// Ignition, when set, returns account id's window ignition (routing.ignition), or nil when
	// ignition does not run for it.
	Ignition func(id string) *management.AccountIgnition
	state    *route.State
	// lockout blocks a client that keeps sending a wrong key.
	lockout *lockout
	// routes are the patterns added with Handle, in order.
	routes []string
	// Redactor hides the secrets in accounts' headers and settings and puts them back from the
	// placeholders a PATCH sends; core sets the one its config reads use, so an account shows
	// the same placeholders in both. Nil: secret header values show as protocol.Redacted,
	// settings are not shown, and PATCH changes neither.
	Redactor AccountRedactor
}

// AccountRedactor hides the secrets in accounts' headers and settings and puts them back: core's
// config redactor (configdoc.Redactor), which this package cannot import.
type AccountRedactor interface {
	// Headers returns h with a placeholder in place of each secret value.
	Headers(h map[string]string) map[string]string
	// RestoreHeaders returns sent with each placeholder Headers showed for a value of cur put
	// back; a placeholder that stands for none is an error.
	RestoreHeaders(sent, cur map[string]string) (map[string]string, error)
	// RedactSettings returns settings, a JSON object, with a placeholder in place of each secret.
	RedactSettings(settings []byte) ([]byte, error)
	// RestoreSettings returns sent with each placeholder replaced by the secret it stands for in
	// current. A placeholder that matches no secret in current is an ErrStaleSecret; a copy of
	// a secret's placeholder whose secret sent no longer holds as it was is an ErrSecretCopy; any
	// other error means a placeholder is where no secret goes.
	RestoreSettings(sent, current []byte) ([]byte, error)
}

// ErrStaleSecret marks a placeholder that matches no secret the account's settings hold now.
var ErrStaleSecret = errors.New("a redacted value matches no secret the account's settings hold now")

// ErrSecretCopy marks settings holding a copy of a secret's placeholder whose secret they no
// longer hold as it was; the error's text after it names the copy's line and says what to do.
var ErrSecretCopy = errors.New("settings")

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
	m.Handle("POST /vertex/import", m.importVertex)
	m.Handle("POST /accounts/import", m.importAuthFile)
	return m
}

// Routes returns the calls added with Handle, as their patterns, such as "GET /accounts".
func (m *Management) Routes() []string { return slices.Clone(m.routes) }

// Handle adds a call: pattern is a method and a path under ManagementPrefix, such as
// "GET /accounts".
func (m *Management) Handle(pattern string, h http.HandlerFunc) {
	m.routes = append(m.routes, pattern)
	method, path, _ := strings.Cut(pattern, " ")
	m.mux.HandleFunc(method+" "+ManagementPrefix+path, h)
}

func (m *Management) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	cfg := m.cfg()
	// The gate comes first, so calls it refuses are not counted for the lockout.
	if !cfg.ManagementAllowRemote && !FromThisMachine(r) {
		writeManagementError(w, http.StatusForbidden, "remote_disabled",
			"remote management is off: the management API answers only this machine (management_allow_remote turns on other machines)")
		return
	}
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || token == "" {
		// No key at all is not a guess, so it does not count toward the lockout: a proxy or a
		// page that forgets the key must not lock out everyone behind its address.
		writeManagementError(w, http.StatusUnauthorized, "unauthorized", "missing management key")
		return
	}
	right := sameKey(token, cfg.managementKeyDigest())
	// A caller on this machine is never blocked, so the person at the keyboard can always get
	// back in; anyone who can call from here can read the config file anyway.
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

// sameKey compares a sent key with the management key's digest (Config.managementKeyDigest) in
// constant time, length included: it compares their SHA-256 digests, the key's made once, when
// the config was loaded, so the time of the check does not grow with the key's length.
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
			// Never shown unredacted; the settings are then left out of the view.
			log.Printf("management: account %q: its settings cannot be shown with their secrets hidden", r.ID)
		} else {
			v.Settings, v.SettingsInexact = s, inexact(s)
		}
	}
	v.Check = m.check(r.ID)
	v.Ignition = m.ignition(r.ID)
	return v
}

// brokenView returns the account a record file the store could not read stands for: its ID is
// the file's name, its health says broken with the file and the error, and nothing core cannot
// know is filled in.
func (m *Management) brokenView(b cred.Broken) AccountView {
	v := AccountView{ID: b.ID, Source: "login", Health: route.Health{State: route.HealthBroken, Why: b.Why()}}
	if m.state != nil {
		v.PublicID = m.state.PublicID(b.ID)
	}
	return v
}

// inexact reports whether a JSON text holds a number that a client reading it as a float64 and
// writing it back as the shortest decimal that reads as the same float64, as a browser does,
// would change. Values are compared, not text: 1.0 and 2e3 come back as 1 and 2000, unchanged.
// It takes time in proportion to the text's length, however long a number is.
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
			continue // zero, which a browser writes back as 0
		}
		// The order of magnitude: the value is 0.digits × 10^mag. Past these bounds a float64
		// holds only infinity or zero, which changes the value.
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

// decimal is a number's value as its sign, its significant digits (no leading or trailing
// zeros; empty for zero, whose sign is dropped) and an exponent: digits × 10^exp.
type decimal struct {
	neg    bool
	digits string
	exp    int64
}

// decimalOf returns a JSON number's value as a decimal, in time in proportion to its length. An
// exponent too large to matter is cut to ±2^40, so its value still reads as out of range.
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

// check returns the result of core's check of account id, or nil.
func (m *Management) check(id string) *AccountCheck {
	if m.Check == nil {
		return nil
	}
	return m.Check(id)
}

// ignition returns account id's window ignition, or nil.
func (m *Management) ignition(id string) *management.AccountIgnition {
	if m.Ignition == nil {
		return nil
	}
	return m.Ignition(id)
}

// configView returns config account a as the management API lists it.
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

// addRequest is the body of POST /accounts: the account's fields (as in the config, without a
// credential; without an ID, core makes one), a label and how to log in.
type addRequest struct {
	route.Account
	// Label is the label the person gives the account (checkLabel); it wins over the provider's.
	Label string `json:"label"`
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
	label, err := checkLabel(req.Label)
	if err != nil {
		writeManagementError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	m.startLogin(w, r, cred.LoginRequest{Account: req.Account, Extension: req.Extension, Method: req.Method, Input: req.Input, Label: label})
}

// maxLabel is the most characters a label a person gives an account may have.
const maxLabel = 64

// checkLabel returns a label a person gives an account without its surrounding spaces, or an
// error when it is not one line (it holds a control character or a line or paragraph separator)
// or is longer than maxLabel characters. "" is no label.
func checkLabel(label string) (string, error) {
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
		// The proxy as GET /accounts shows it, sent back unchanged, keeps the password it hides.
		if len(proxyurl.Password(cur)) > 0 && strings.TrimSpace(a.ProxyURL) == proxyurl.Redact(cur) {
			a.ProxyURL = cur
		}
		return proxyurl.Check("value", a.ProxyURL)
	},
	// headers is set by Management.patchHeaders, which needs the management API's redactor.
	"headers": nil,
	"timeouts": func(a *route.Account, raw json.RawMessage) error {
		a.Timeouts = nil
		if err := unmarshalSet(raw, &a.Timeouts); err != nil {
			return err
		}
		return route.ValidateTimeouts("value", a.Timeouts)
	},
}

// patchHeaders sets a.Headers to raw. A placeholder GET /accounts shows for a secret value,
// sent back, keeps that value; one that stands for no value the account has is refused.
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
				if l, err = checkLabel(*sent); err != nil {
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
	// Decode every field before changing anything, so a bad one changes nothing. The probe is
	// the account as it is, so a field that keeps a stored value (a redacted header sent back)
	// is checked against it.
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
				_ = c(a) // each decoded above
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

// restoreSettings sets a's settings to sent, with each placeholder replaced by the secret it
// stands for in a's settings as they are now; null removes them.
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

// readBody reads a management request body, up to limit bytes. It writes the error reply and
// returns false when it cannot: 413 invalid_request for a body larger than core reads, 400
// invalid_request for one it cannot read otherwise.
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

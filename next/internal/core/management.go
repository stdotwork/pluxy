package core

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/stdotwork/pluxy/next/internal/packages"
	"github.com/stdotwork/pluxy/next/internal/server"
)

// ManagementPrefix is where the management API is served.
const ManagementPrefix = server.ManagementPrefix + "/"

// maxManagementBody limits a management request body.
const maxManagementBody = 1 << 20

// Management returns the management API handler, version 0: the account and login calls (see
// server.NewManagement) and
//
//	GET    /v0/management/status    core's live state: desired and active config revision,
//	                                each extension generation's state, and core's build
//	POST   /v0/management/reload    reload the config file now; 200 with the status when the
//	                                new revision is active, 422 with the status (desired.error
//	                                says why) when it was refused and the active revision stays
//	GET    /v0/management/packages  installed packages
//	GET    /v0/management/registries  what each registry offers
//	POST   /v0/management/packages  install a package (InstallRequest)
//	POST   /v0/management/packages/{publisher}/{name}/enable, .../disable
//	DELETE /v0/management/packages/{publisher}/{name}  remove a package
//	GET    /v0/management/ui        the extensions' pages and where they are served
//	GET    /v0/management/extensions/{publisher}/{name}/config  an extension's settings
//	PATCH  /v0/management/extensions/{publisher}/{name}/config  change them
//	GET    /v0/management/config    the config file, with its secrets replaced by placeholders
//	POST   /v0/management/config/validate  check a config document without applying it
//	PUT    /v0/management/config    replace the config file and apply it (WriteConfig)
//	GET    /v0/management/client-keys  the client keys, masked
//	POST   /v0/management/client-keys  add a new random client key; the reply shows it once
//	DELETE /v0/management/client-keys/{id}  remove a client key
//	POST   /v0/management/management-key/rotate  replace the management key with a new one
//	GET    /v0/management/models    the models served, as GET /v1/models lists them, each with its
//	                                extension, upstream name and the IDs of the accounts that serve it
//	GET    /v0/management/settings  the basic and routing settings, by today's core's names
//	PATCH  /v0/management/settings  change them
//	GET    /v0/management/login-methods  each extension's login methods (handleAccounts)
//	POST   /v0/management/accounts/{id}/cooldown/reset  clear an account's cooldowns
//	GET    /v0/management/accounts/{id}/models  the models an account serves
//	POST   /v0/management/accounts/{id}/test  send an account one small request
//	GET    /v0/management/usage     usage totals, breakdowns and a time series (handleUsage)
//	GET    /v0/management/usage/events  the recorded requests, a page at a time
//	GET    /v0/management/logs      lines of core's log files (handleLogs)
//	DELETE /v0/management/logs      empty main.log and delete its rotated files
//	GET    /v0/management/request-error-logs  the error logs; .../{name} one of them
//	GET    /v0/management/request-log-by-id/{id}  the request log of a trace ID
//
// Every call needs the active config's management_key as a bearer token. Without a key in the
// config the API is not served.
func (c *Core) Management() http.Handler {
	m := server.NewManagement(c.Config, c.creds, c.state)
	m.Check = c.checks.result
	m.Handle("GET /status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, c.Status())
	})
	m.Handle("POST /reload", func(w http.ResponseWriter, r *http.Request) {
		code := http.StatusOK
		if err := c.Reload("management API"); err != nil {
			code = http.StatusUnprocessableEntity
		}
		writeJSON(w, code, c.Status())
	})
	m.Handle("GET /packages", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"packages": c.Packages()})
	})
	m.Handle("GET /registries", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"registries": c.Registries(r.Context())})
	})
	m.Handle("POST /packages", func(w http.ResponseWriter, r *http.Request) {
		var req InstallRequest
		if !readJSON(w, r, &req) {
			return
		}
		v, err := c.Install(r.Context(), req)
		writePackage(w, v, err)
	})
	for verb, enabled := range map[string]bool{"enable": true, "disable": false} {
		m.Handle("POST /packages/{publisher}/{name}/"+verb, func(w http.ResponseWriter, r *http.Request) {
			v, err := c.SetEnabled(packageID(r), enabled)
			writePackage(w, v, err)
		})
	}
	m.Handle("DELETE /packages/{publisher}/{name}", func(w http.ResponseWriter, r *http.Request) {
		if err := c.RemovePackage(packageID(r)); err != nil {
			writePackage(w, PackageView{}, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	m.Handle("GET /models", func(w http.ResponseWriter, r *http.Request) {
		s, release := c.acquire()
		defer release()
		writeJSON(w, http.StatusOK, map[string]any{"models": s.Router.ServedModels(r.Context())})
	})
	c.handleUI(m)
	c.handleAccounts(m)
	c.handleConfig(m)
	c.handleUsage(m)
	c.handleLogs(m)
	return m
}

func packageID(r *http.Request) string { return r.PathValue("publisher") + "/" + r.PathValue("name") }

// writePackage writes the reply of a package call: the package, or the error with the package
// when it is installed all the same.
func writePackage(w http.ResponseWriter, v PackageView, err error) {
	if err == nil {
		writeJSON(w, http.StatusOK, map[string]any{"package": v})
		return
	}
	code, kind := http.StatusInternalServerError, "internal"
	switch {
	case errors.Is(err, packages.ErrNotFound):
		code, kind = http.StatusNotFound, "not_found"
	case errors.Is(err, packages.ErrRefused), errors.Is(err, ErrNotStarted):
		code, kind = http.StatusUnprocessableEntity, "refused"
	case errors.Is(err, packages.ErrConflict):
		code, kind = http.StatusConflict, "conflict"
	case errors.Is(err, packages.ErrFetch):
		code, kind = http.StatusBadGateway, "fetch_failed"
	}
	body := map[string]any{"error": map[string]string{"code": kind, "message": err.Error()}}
	if v.ID != "" {
		body["package"] = v
	}
	writeJSON(w, code, body)
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxManagementBody))
	if err == nil {
		err = json.Unmarshal(b, v)
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]string{"code": "invalid_request", "message": "request body is not valid JSON: " + err.Error()}})
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

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
//	                                and each extension generation's state
//	POST   /v0/management/reload    reload the config file now; 200 with the status when the
//	                                new revision is active, 422 with the status (desired.error
//	                                says why) when it was refused and the active revision stays
//	GET    /v0/management/packages  installed packages
//	GET    /v0/management/registries  what each registry offers
//	POST   /v0/management/packages  install a package (InstallRequest)
//	POST   /v0/management/packages/{publisher}/{name}/enable, .../disable
//	DELETE /v0/management/packages/{publisher}/{name}  remove a package
//
// Every call needs the active config's management_key as a bearer token. Without a key in the
// config the API is not served.
func (c *Core) Management() http.Handler {
	m := server.NewManagement(c.Config, c.creds)
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

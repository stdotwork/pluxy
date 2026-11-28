package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/stdotwork/pluxy/next/internal/configdoc"
	"github.com/stdotwork/pluxy/next/internal/packages"
	"github.com/stdotwork/pluxy/next/internal/server"
	"github.com/stdotwork/pluxy/next/management"
)

const ManagementPrefix = server.ManagementPrefix + "/"

const maxManagementBody = 1 << 20

func (c *Core) Management() *server.Management {
	m := server.NewManagement(c.Config, c.creds, c.state)
	m.Check = c.checks.result
	m.Ignition = c.ignition.View
	m.Redactor = accountRedactor{c.redactor}
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
		writeJSON(w, http.StatusOK, management.PackagesReply{Packages: c.Packages()})
	})
	m.Handle("GET /registries", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, management.RegistriesReply{Registries: c.Registries(r.Context())})
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
		writeJSON(w, http.StatusOK, management.ModelsReply{Models: s.Router.ServedModels(r.Context())})
	})
	c.handleSetup(m)
	c.handleConnect(m)
	c.handleSetupImport(m)
	m.Handle("POST /models/refresh", func(w http.ResponseWriter, r *http.Request) {
		var req management.RefreshModelsRequest
		if !readOptionalJSON(w, r, &req) {
			return
		}
		s, release := c.acquire()
		defer release()
		if req.Account != "" {
			if _, ok := c.account(s, req.Account); !ok {
				writeError(w, http.StatusNotFound, "not_found", fmt.Sprintf("no account %q", req.Account))
				return
			}
		}
		var accounts []management.RefreshAccountResult
		if !c.opts.SafeMode {
			accounts = s.Router.RefreshModels(r.Context(), req.Account)
		}
		if accounts == nil {
			accounts = []management.RefreshAccountResult{}
		}
		writeJSON(w, http.StatusOK, management.RefreshModelsReply{
			Models:   s.Router.ServedModels(r.Context()),
			Accounts: accounts,
		})
	})
	c.handleUI(m)
	c.handleAccounts(m)
	c.handleRoute(m)
	c.handleChains(m)
	c.handleConfig(m)
	c.handleUsage(m)
	c.handleLogs(m)
	c.handleThemes(m)
	c.handleRequests(m)
	return m
}

func packageID(r *http.Request) string { return r.PathValue("publisher") + "/" + r.PathValue("name") }

func writePackage(w http.ResponseWriter, v PackageView, err error) {
	if err == nil {
		writeJSON(w, http.StatusOK, management.PackageReply{Package: v})
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

func readManagementBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxManagementBody))
	if err == nil {
		return b, true
	}
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		writeError(w, http.StatusRequestEntityTooLarge, "invalid_request", "request body is too large")
		return nil, false
	}
	writeError(w, http.StatusBadRequest, "invalid_request", "request body: "+err.Error())
	return nil, false
}

func readOptionalJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	b, ok := readManagementBody(w, r)
	if !ok {
		return false
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return true
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]string{"code": "invalid_request", "message": "request body is not valid JSON: " + err.Error()}})
		return false
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]string{"code": "invalid_request", "message": "request body must be one JSON object"}})
		return false
	}
	return true
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	b, ok := readManagementBody(w, r)
	if !ok {
		return false
	}
	if err := json.Unmarshal(b, v); err != nil {
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

type accountRedactor struct{ *configdoc.Redactor }

func (r accountRedactor) RestoreSettings(sent, current []byte) ([]byte, error) {
	b, err := r.Redactor.RestoreSettings(sent, current)
	switch {
	case errors.Is(err, configdoc.ErrStale):
		return nil, server.ErrStaleSecret
	case errors.Is(err, configdoc.ErrCopy):
		return nil, fmt.Errorf("%w: %s", server.ErrSecretCopy, strings.TrimPrefix(err.Error(), configdoc.ErrInvalid.Error()+": "))
	}
	return b, err
}

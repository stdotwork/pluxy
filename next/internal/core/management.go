package core

import (
	"encoding/json"
	"net/http"

	"github.com/stdotwork/pluxy/next/internal/server"
)

// ManagementPrefix is where the management API is served.
const ManagementPrefix = server.ManagementPrefix + "/"

// Management returns the management API handler, version 0: the account and login calls (see
// server.NewManagement) and
//
//	GET  /v0/management/status  core's live state: desired and active config revision, and
//	                            each extension generation's state
//	POST /v0/management/reload  reload the config file now; 200 with the status when the new
//	                            revision is active, 422 with the status (desired.error says
//	                            why) when it was refused and the active revision stays
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
	return m
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

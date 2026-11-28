package core

import (
	"log"
	"net/http"

	"github.com/stdotwork/pluxy/next/internal/server"
	"github.com/stdotwork/pluxy/next/management"
)

func (c *Core) handleRequests(m *server.Management) {
	m.Handle("GET /requests", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, management.RequestsReply{Requests: c.requests.List()})
	})
	m.Handle("POST /requests/{trace}/cancel", func(w http.ResponseWriter, r *http.Request) {
		v, ok := c.requests.Cancel(r.PathValue("trace"))
		if !ok {
			writeError(w, http.StatusNotFound, "not_found", "no request with this trace ID is being served")
			return
		}
		log.Printf("management: request %s cancelled by an operator through the management API, from %s", v.ID, server.ClientIP(r))
		writeJSON(w, http.StatusOK, management.RequestReply{Request: v})
	})
}

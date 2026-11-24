package core

import (
	"log"
	"net/http"

	"github.com/stdotwork/pluxy/next/internal/server"
	"github.com/stdotwork/pluxy/next/management"
)

// handleRequests adds the calls for the client requests core is serving now.
//
//	GET  /v0/management/requests  the requests being served, oldest first
//	POST /v0/management/requests/{trace}/cancel  cancel the request with a trace ID
//
// A request is listed by what the other calls already show of it: its trace ID, the client key's
// ID (never the key), the client's address, the model asked for and the account's ID. Cancelling
// one cancels its context, as its client going away does: the call to the extension is closed, the
// client gets the cancelled error, and the usage record has it as cancelled. A trace ID no
// request being served has is a 404: the request ended, or never was.
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
		// The request's own lines do not log a cancel, so this line is what says who ended it.
		log.Printf("management: request %s cancelled by an operator through the management API, from %s", v.ID, server.ClientIP(r))
		writeJSON(w, http.StatusOK, management.RequestReply{Request: v})
	})
}

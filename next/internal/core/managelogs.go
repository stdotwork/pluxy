package core

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"

	"github.com/stdotwork/pluxy/next/internal/logfile"
	"github.com/stdotwork/pluxy/next/internal/server"
)

// Limits of GET /logs.
const (
	defaultLogLines = 1000
	maxLogLines     = 10000
)

// handleLogs adds the log file calls, which answer 400 while logging to file is off.
//
//	GET    /v0/management/logs  lines of core's log files
//	DELETE /v0/management/logs  empty main.log and delete its rotated files
func (c *Core) handleLogs(m *server.Management) {
	m.Handle("GET /logs", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		var after float64
		if v := q.Get("after"); v != "" {
			var err error
			if after, err = strconv.ParseFloat(v, 64); err != nil || after < 0 || math.IsInf(after, 0) || math.IsNaN(after) {
				writeLogsError(w, fmt.Errorf("after %q must be a time in Unix seconds, such as a reply's latest-timestamp", v))
				return
			}
		}
		limit := defaultLogLines
		if v := q.Get("limit"); v != "" {
			var err error
			if limit, err = strconv.Atoi(v); err != nil || limit < 1 || limit > maxLogLines {
				writeLogsError(w, fmt.Errorf("limit %q must be a number from 1 to %d", v, maxLogLines))
				return
			}
		}
		lines, err := c.logs.Read(after, limit)
		if err != nil {
			writeLogsError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, lines)
	})
	m.Handle("DELETE /logs", func(w http.ResponseWriter, r *http.Request) {
		removed, err := c.logs.Clear()
		if err != nil {
			if errors.Is(err, logfile.ErrOff) {
				writeLogsError(w, err)
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": map[string]string{"code": "internal", "message": err.Error()}})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"removed": removed})
	})
}

func writeLogsError(w http.ResponseWriter, err error) {
	writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]string{"code": "invalid_request", "message": err.Error()}})
}

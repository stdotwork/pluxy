package core

import (
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"github.com/stdotwork/pluxy/next/internal/logfile"
	"github.com/stdotwork/pluxy/next/internal/server"
)

// Limits of GET /logs.
const (
	defaultLogLines = 1000
	maxLogLines     = 10000
	maxJSONLogBytes = 2 << 20
)

// handleLogs adds the log file calls, which answer 400 while logging to file is off.
//
//	GET    /v0/management/logs  lines of core's log files
//	DELETE /v0/management/logs  empty main.log and delete its rotated files
//	GET    /v0/management/request-error-logs  the error logs, newest first
//	GET    /v0/management/request-error-logs/{name}  one error log's file
//	GET    /v0/management/request-log-by-id/{id}  the request log of the request with a trace ID
func (c *Core) handleLogs(m *server.Management) {
	m.Handle("GET /request-error-logs", func(w http.ResponseWriter, r *http.Request) {
		files, err := server.ErrorLogs(c.logs.Dir())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": map[string]string{"code": "internal", "message": err.Error()}})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"files": files})
	})
	m.Handle("GET /request-error-logs/{name}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if !server.IsErrorLogName(name) {
			writeLogsError(w, fmt.Errorf("%q is not an error log's name (error-<...>.log)", name))
			return
		}
		c.serveLogFile(w, r, name)
	})
	m.Handle("GET /request-log-by-id/{id}", func(w http.ResponseWriter, r *http.Request) {
		name := server.FindRequestLog(c.logs.Dir(), r.PathValue("id"))
		if name == "" {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": map[string]string{"code": "not_found", "message": "no request log has this trace ID"}})
			return
		}
		c.serveLogFile(w, r, name)
	})
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

// serveLogFile sends a download, or bounded JSON for hosts that only carry JSON replies.
func (c *Core) serveLogFile(w http.ResponseWriter, r *http.Request, name string) {
	path := filepath.Join(c.logs.Dir(), name)
	// Only a regular file in the folder: a link there could point anywhere. The file opened must
	// be the one checked, so one swapped in between is refused too.
	linfo, err := os.Lstat(path)
	if err != nil || !linfo.Mode().IsRegular() {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": map[string]string{"code": "not_found", "message": "no log file of that name"}})
		return
	}
	f, err := os.Open(path)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": map[string]string{"code": "not_found", "message": "no log file of that name"}})
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(linfo, info) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": map[string]string{"code": "not_found", "message": "no log file of that name"}})
		return
	}
	if r.URL.Query().Get("format") == "json" {
		// Check the size before allocating, and bound the read in case the file grows.
		if info.Size() > maxJSONLogBytes {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"error": map[string]string{"code": "log_too_large", "message": "JSON log content exceeds 2 MiB; use the plain download"}})
			return
		}
		content, err := io.ReadAll(io.LimitReader(f, maxJSONLogBytes+1))
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": map[string]string{"code": "internal", "message": "could not read log content"}})
			return
		}
		if len(content) > maxJSONLogBytes {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"error": map[string]string{"code": "log_too_large", "message": "JSON log content exceeds 2 MiB; use the plain download"}})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"name": name, "size": len(content), "content": string(content)})
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, "", info.ModTime(), f)
}

func writeLogsError(w http.ResponseWriter, err error) {
	writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]string{"code": "invalid_request", "message": err.Error()}})
}

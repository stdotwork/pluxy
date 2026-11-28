package core

import (
	"encoding/hex"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/stdotwork/pluxy/next/internal/route"
	"github.com/stdotwork/pluxy/next/internal/server"
	"github.com/stdotwork/pluxy/next/management"
)

const publicKeyFile = "account-key"

func (c *Core) loadPublicKey() {
	path := filepath.Join(StateDir(c.opts.ConfigPath), publicKeyFile)
	if b, err := os.ReadFile(path); err == nil {
		if key, err := hex.DecodeString(strings.TrimSpace(string(b))); err == nil && len(key) == route.PublicKeyLen {
			c.state.SetPublicKey(key)
			return
		}
		log.Printf("routing: %s is not a key core made: it is replaced, and accounts get new public IDs", path)
	}
	key := route.NewPublicKey()
	if err := writePrivate(path, []byte(hex.EncodeToString(key)+"\n")); err != nil {
		log.Printf("error: routing: saving %s failed, so accounts' public IDs change at the next start: %v", path, err)
	}
	c.state.SetPublicKey(key)
}

func (c *Core) handleRoute(m *server.Management) {
	m.Handle("GET /route", func(w http.ResponseWriter, r *http.Request) {
		model := strings.TrimSpace(r.URL.Query().Get("model"))
		if model == "" {
			writeError(w, http.StatusBadRequest, "invalid_request", "model is required")
			return
		}
		s, release := c.acquire()
		defer release()
		writeJSON(w, http.StatusOK, management.RouteReply(s.Router.Plan(r.Context(), model)))
	})
}

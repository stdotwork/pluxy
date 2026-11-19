package core

import (
	"encoding/hex"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/stdotwork/pluxy/next/internal/cred"
	"github.com/stdotwork/pluxy/next/internal/route"
	"github.com/stdotwork/pluxy/next/internal/server"
	"github.com/stdotwork/pluxy/next/management"
)

// publicKeyFile is the file in the state folder that holds the key public account IDs are made
// with (route.State.PublicID), in hex. It stays across restarts, so an account keeps its public
// ID.
const publicKeyFile = "account-key"

// loadPublicKey reads the public ID key from the state folder, or makes and saves one. When it
// cannot be saved the key lives until core stops, and public IDs change at the next start.
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
		log.Printf("routing: saving %s failed, so accounts' public IDs change at the next start: %v", path, err)
	}
	c.state.SetPublicKey(key)
}

// held lists the accounts the credential store holds that are not routed over: turned off by a
// person, or waiting for a new login. A request that leaves them out says so.
func (c *Core) held() []route.Held {
	s := c.cur.Load()
	if s == nil || c.creds == nil {
		return nil
	}
	var out []route.Held
	for _, r := range c.creds.Records() {
		state := route.HealthNeedsLogin
		switch {
		case r.Usable() || s.exts[r.Extension] == nil || c.reserved(r.ID):
			continue
		case r.Disabled:
			state = route.HealthDisabled
		case r.State != cred.StateReauth:
			continue
		}
		a := r.Account
		a.ID, a.Credential, a.Managed, a.Extension = r.ID, nil, true, r.Extension
		out = append(out, route.Held{Account: c.extensionConfig(s, r.Extension).LoginDefaults(a, r.Credential.Kind), State: state})
	}
	return out
}

// handleRoute adds GET /v0/management/route?model=<name>: the accounts a request for the model
// would try now, in order, and those it would leave out and why. It sends nothing.
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

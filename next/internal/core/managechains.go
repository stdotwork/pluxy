package core

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"slices"

	"github.com/stdotwork/pluxy/next/internal/configdoc"
	"github.com/stdotwork/pluxy/next/internal/host"
	"github.com/stdotwork/pluxy/next/internal/route"
	"github.com/stdotwork/pluxy/next/internal/server"
	"github.com/stdotwork/pluxy/next/management"
)

func (c *Core) handleChains(m *server.Management) {
	m.Handle("GET /chains", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, chainsReply(c.Config().Routing.Chains))
	})

	m.Handle("PUT /chains/{name}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		var req management.ChainRequest
		if !readStrictJSON(w, r, &req) {
			return
		}
		if c.isYAML() {
			writeError(w, http.StatusConflict, "conflict", "a config.yaml cannot hold fallback chains, since it uses CLIProxyAPI's key names; they are kept in pluxy's JSON config")
			return
		}
		chains := maps.Clone(c.Config().Routing.Chains)
		if chains == nil {
			chains = map[string][]route.Step{}
		}
		chains[name] = req.Steps
		for i, st := range req.Steps {
			if st.Provider != "" && !host.ValidID(st.Provider) {
				writeError(w, http.StatusUnprocessableEntity, "refused", fmt.Sprintf("chain %s: step %d names the provider %q; a provider is an extension's ID, publisher/name", name, i+1, st.Provider))
				return
			}
		}
		if err := route.ValidateChains(chains); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "refused", err.Error())
			return
		}
		s, release := c.acquire()
		err := s.Router.CheckSteps(r.Context(), req.Steps)
		release()
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, "refused", fmt.Sprintf("chain %s: %v", name, err))
			return
		}
		steps, err := json.Marshal(req.Steps)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", err.Error())
			return
		}
		err = c.WriteConfig("management API: chain "+name+" set", c.edit(func(d *configdoc.Doc) error { return d.SetChain(name, steps) }))
		if err != nil {
			c.writeChangeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, management.Chain{Name: name, Steps: req.Steps})
	})

	m.Handle("DELETE /chains/{name}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		err := c.WriteConfig("management API: chain "+name+" removed", c.edit(func(d *configdoc.Doc) error {
			if d.YAML || !d.RemoveChain(name) {
				return &editError{http.StatusNotFound, "not_found", fmt.Sprintf("there is no chain named %q", name)}
			}
			return nil
		}))
		if err != nil {
			c.writeChangeError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

func chainsReply(chains map[string][]route.Step) management.ChainsReply {
	out := management.ChainsReply{Chains: []management.Chain{}}
	for _, name := range slices.Sorted(maps.Keys(chains)) {
		out.Chains = append(out.Chains, management.Chain{Name: name, Steps: chains[name]})
	}
	return out
}

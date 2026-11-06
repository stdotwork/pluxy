package core

import (
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/stdotwork/pluxy/next/internal/extconfig"
	"github.com/stdotwork/pluxy/next/internal/host"
	"github.com/stdotwork/pluxy/next/internal/pages"
	"github.com/stdotwork/pluxy/next/internal/server"
)

// SetPagesPort records the port the pages listener got, for the addresses of extension pages.
func (c *Core) SetPagesPort(port int) { c.pagesPort.Store(int64(port)) }

// PagesFolder returns the ui folder of extension id when it runs now with fingerprint key fp;
// the pages site serves only those.
func (c *Core) PagesFolder(id, fp string) (pages.Folder, bool) {
	cur := c.cur.Load()
	if cur == nil {
		return pages.Folder{}, false
	}
	g := cur.exts[id]
	if g == nil || g.Manifest().UI == nil || pages.FingerprintKey(g.Fingerprint()) != fp {
		return pages.Folder{}, false
	}
	return pages.Folder{Dir: g.Manifest().UIDir()}, true
}

// UIView is what the panel can show of the extensions that run: their pages and whether they
// have settings.
type UIView struct {
	// PagesOrigin is the pages site's origin for a panel reached as the request was; empty, with
	// PagesError saying why, when there is none.
	PagesOrigin string        `json:"pages_origin,omitempty"`
	PagesError  string        `json:"pages_error,omitempty"`
	Extensions  []UIExtension `json:"extensions"`
}

// UIExtension is one running extension with pages or settings.
type UIExtension struct {
	ID          string   `json:"id"`
	Version     string   `json:"version"`
	Permissions []string `json:"permissions"`
	Pages       []UIPage `json:"pages"`
	Config      bool     `json:"config"`
}

// UIPage is one extension page. URL is empty when there is no pages origin.
type UIPage struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Short string `json:"short"`
	URL   string `json:"url"`
}

// PagesOrigin returns the pages site's origin for a panel reached at panelHost (the request's
// Host), or why there is none.
func (c *Core) PagesOrigin(panelHost string) (string, error) {
	return pages.Origin(c.Config().ExtensionPages.URL, panelHost, int(c.pagesPort.Load()))
}

// UI returns the UIView for a panel reached at panelHost (the request's Host).
func (c *Core) UI(panelHost string) UIView {
	v := UIView{Extensions: []UIExtension{}}
	origin, err := c.PagesOrigin(panelHost)
	if err != nil {
		v.PagesError = err.Error()
	} else {
		v.PagesOrigin = origin
	}
	cur := c.cur.Load()
	if cur == nil {
		return v
	}
	for _, id := range slices.Sorted(maps.Keys(cur.exts)) {
		g := cur.exts[id]
		m := g.Manifest()
		if m.UI == nil && m.Settings() == nil {
			continue
		}
		x := UIExtension{ID: id, Version: m.Version, Permissions: []string{}, Pages: []UIPage{}, Config: m.Settings() != nil}
		if m.UI != nil {
			x.Permissions = append(x.Permissions, m.UI.Permissions...)
			for _, p := range m.UI.Pages {
				page := UIPage{ID: p.ID, Title: p.Title, Short: p.Short}
				if v.PagesOrigin != "" {
					page.URL = v.PagesOrigin + pages.Path(id, g.Fingerprint(), p.Path)
				}
				x.Pages = append(x.Pages, page)
			}
		}
		v.Extensions = append(v.Extensions, x)
	}
	return v
}

// ErrNoSettings is asking for the settings of an extension that does not run or has none.
var ErrNoSettings = errors.New("the extension does not run or has no settings")

// SettingsView is an extension's schema and what may be shown of its settings.
type SettingsView struct {
	Schema json.RawMessage `json:"schema"`
	extconfig.View
}

func (c *Core) schema(id string) (*extconfig.Schema, error) {
	cur := c.cur.Load()
	var g *host.Managed
	if cur != nil {
		g = cur.exts[id]
	}
	if g == nil || g.Manifest().Settings() == nil {
		return nil, ErrNoSettings
	}
	return g.Manifest().Settings(), nil
}

// Settings returns extension id's settings, as core shows them.
func (c *Core) Settings(id string) (SettingsView, error) {
	s, err := c.schema(id)
	if err != nil {
		return SettingsView{}, err
	}
	stored, err := c.settings.Load(id)
	if err != nil {
		return SettingsView{}, err
	}
	return SettingsView{Schema: s.Raw(), View: s.View(stored)}, nil
}

// ChangeSettings applies change to extension id's settings (extconfig.Schema.Merge) and returns
// them. A change the schema refuses is an *extconfig.Invalid and changes nothing.
func (c *Core) ChangeSettings(id string, change extconfig.Values) (SettingsView, error) {
	s, err := c.schema(id)
	if err != nil {
		return SettingsView{}, err
	}
	next, err := c.settings.Change(id, s, change)
	if err != nil {
		return SettingsView{}, err
	}
	return SettingsView{Schema: s.Raw(), View: s.View(next)}, nil
}

// handleUI adds the extension pages calls to management m.
func (c *Core) handleUI(m *server.Management) {
	m.Handle("GET /ui", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, c.UI(r.Host))
	})
	m.Handle("GET /extensions/{publisher}/{name}/config", func(w http.ResponseWriter, r *http.Request) {
		v, err := c.Settings(packageID(r))
		writeSettings(w, v, err)
	})
	m.Handle("PATCH /extensions/{publisher}/{name}/config", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Values extconfig.Values `json:"values"`
		}
		if !readJSON(w, r, &req) {
			return
		}
		if req.Values == nil {
			writeError(w, http.StatusBadRequest, "invalid_request", `the body must be {"values": {...}}`)
			return
		}
		v, err := c.ChangeSettings(packageID(r), req.Values)
		writeSettings(w, v, err)
	})
}

func writeSettings(w http.ResponseWriter, v SettingsView, err error) {
	var invalid *extconfig.Invalid
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, v)
	case errors.Is(err, ErrNoSettings):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.As(err, &invalid):
		writeError(w, http.StatusUnprocessableEntity, "invalid_config", invalid.Error())
	default:
		writeError(w, http.StatusInternalServerError, "internal", strings.TrimSpace(err.Error()))
	}
}

func writeError(w http.ResponseWriter, code int, kind, msg string) {
	writeJSON(w, code, map[string]any{"error": map[string]string{"code": kind, "message": msg}})
}

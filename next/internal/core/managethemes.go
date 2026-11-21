package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/stdotwork/pluxy/next/internal/private"
	"github.com/stdotwork/pluxy/next/internal/server"
	"github.com/stdotwork/pluxy/next/management"
)

const (
	// maxThemeBytes is the maximum size of one stored theme file (256 KiB).
	maxThemeBytes = 256 << 10
	// maxStoredThemes is the maximum number of themes core will keep on disk.
	maxStoredThemes = 64
	// maxThemePartLen limits each publisher and name segment of a theme ID.
	maxThemePartLen = 64
	// themeCurrentFile is the file in themes/ holding the selected theme ID.
	themeCurrentFile = "current"
	// maxCurrentFileBytes bounds reading themes/current (longest ID is 64+1+64 bytes plus newline).
	maxCurrentFileBytes = 256
)

var (
	errNotRegular    = errors.New("not a regular file")
	errThemeTooLarge = errors.New("theme exceeds size limit")
	errThemeLimit    = fmt.Errorf("at most %d themes can be stored", maxStoredThemes)
)

// themeStore keeps user-added theme JSON files under <state>/themes/<publisher>/<name>.json
// and the selected theme ID in <state>/themes/current.
type themeStore struct {
	dir string
	mu  sync.Mutex
}

func newThemeStore(dir string) *themeStore { return &themeStore{dir: dir} }

func (c *Core) handleThemes(m *server.Management) {
	m.Handle("GET /themes", func(w http.ResponseWriter, r *http.Request) {
		reply, err := c.themes.list()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, reply)
	})

	m.Handle("GET /themes/{publisher}/{name}", func(w http.ResponseWriter, r *http.Request) {
		pub, name := r.PathValue("publisher"), r.PathValue("name")
		if _, err := parseThemeParts(pub, name); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		raw, err := c.themes.get(pub, name)
		switch {
		case errors.Is(err, fs.ErrNotExist), errors.Is(err, errNotRegular):
			writeError(w, http.StatusNotFound, "not_found", "no stored theme has this ID")
		case errors.Is(err, errThemeTooLarge):
			writeError(w, http.StatusRequestEntityTooLarge, "invalid_request", "stored theme exceeds 256 KiB")
		case err != nil:
			writeError(w, http.StatusInternalServerError, "internal", err.Error())
		default:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(raw)
		}
	})

	m.Handle("PUT /themes/{publisher}/{name}", func(w http.ResponseWriter, r *http.Request) {
		pub, name := r.PathValue("publisher"), r.PathValue("name")
		if _, err := parseThemeParts(pub, name); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxThemeBytes)
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				writeError(w, http.StatusRequestEntityTooLarge, "invalid_request", fmt.Sprintf("theme body exceeds %d KiB", maxThemeBytes>>10))
				return
			}
			writeError(w, http.StatusBadRequest, "invalid_request", "reading request body: "+err.Error())
			return
		}
		themeName, ok := parseThemeObject(raw)
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid_request", "theme body must be one valid UTF-8 JSON object")
			return
		}
		summary, err := c.themes.put(pub, name, themeName, raw)
		switch {
		case errors.Is(err, errThemeLimit):
			writeError(w, http.StatusUnprocessableEntity, "refused", err.Error())
		case err != nil:
			writeError(w, http.StatusInternalServerError, "internal", err.Error())
		default:
			writeJSON(w, http.StatusOK, summary)
		}
	})

	m.Handle("DELETE /themes/{publisher}/{name}", func(w http.ResponseWriter, r *http.Request) {
		pub, name := r.PathValue("publisher"), r.PathValue("name")
		if _, err := parseThemeParts(pub, name); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		err := c.themes.remove(pub, name)
		switch {
		case errors.Is(err, fs.ErrNotExist), errors.Is(err, errNotRegular):
			writeError(w, http.StatusNotFound, "not_found", "no stored theme has this ID")
		case err != nil:
			writeError(w, http.StatusInternalServerError, "internal", err.Error())
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	})

	m.Handle("PUT /theme", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID *string `json:"id"`
		}
		if !readStrictJSONComplete(w, r, &req) {
			return
		}
		if req.ID == nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "send the theme ID as id")
			return
		}
		if *req.ID != "" && !validThemeID(*req.ID) {
			writeError(w, http.StatusBadRequest, "invalid_request", fmt.Sprintf("theme ID %q is not valid", *req.ID))
			return
		}
		if err := c.themes.setCurrent(*req.ID); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, management.ThemeSelection{ID: *req.ID})
	})
}

func readStrictJSONComplete(w http.ResponseWriter, r *http.Request, v any) bool {
	b, ok := readManagementBody(w, r)
	if !ok {
		return false
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body: "+err.Error())
		return false
	}
	switch err := dec.Decode(new(any)); {
	case err == io.EOF:
		return true
	case err == nil:
		writeError(w, http.StatusBadRequest, "invalid_request", "request body: unexpected trailing JSON value")
	default:
		writeError(w, http.StatusBadRequest, "invalid_request", "request body: "+err.Error())
	}
	return false
}

func validThemePart(s string) bool {
	if len(s) == 0 || len(s) > maxThemePartLen {
		return false
	}
	for i := range len(s) {
		c := s[i]
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-') {
			return false
		}
	}
	return true
}

func parseThemeParts(pub, name string) (string, error) {
	if !validThemePart(pub) || !validThemePart(name) {
		return "", fmt.Errorf("theme ID parts must match ^[a-z0-9-]{1,%d}$", maxThemePartLen)
	}
	if pub == themeCurrentFile {
		return "", fmt.Errorf("publisher %q is reserved", themeCurrentFile)
	}
	return pub + "/" + name, nil
}

func validThemeID(id string) bool {
	pub, name, ok := strings.Cut(id, "/")
	if !ok {
		return false
	}
	_, err := parseThemeParts(pub, name)
	return err == nil
}

func parseThemeObject(b []byte) (name string, ok bool) {
	if !utf8.Valid(b) {
		return "", false
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	var obj map[string]json.RawMessage
	if err := dec.Decode(&obj); err != nil || obj == nil {
		return "", false
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return "", false
	}
	if raw, has := obj["name"]; has {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			name = s
		}
	}
	return name, true
}

func (s *themeStore) openTop(create bool) (*os.Root, error) {
	return private.Dir{Path: s.dir, Redo: "add the themes again"}.OpenRoot(create)
}

// readRegularInRoot reads name from root without following symlinks or opening non-regular files,
// confirming the opened descriptor is the file Lstat checked and owned by this user.
func readRegularInRoot(root *os.Root, name string, limit int64) ([]byte, error) {
	linfo, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !linfo.Mode().IsRegular() {
		return nil, errNotRegular
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(linfo, info) {
		return nil, errNotRegular
	}
	ok, owner, err := private.Owner(f)
	if err != nil {
		return nil, err
	}
	if !ok {
		log.Printf("themes: skipped %s: owned by %s, not by this user", name, owner)
		return nil, errNotRegular
	}
	if info.Size() > limit {
		return nil, errThemeTooLarge
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, errThemeTooLarge
	}
	return b, nil
}

func (s *themeStore) readCurrentLocked(top *os.Root) string {
	b, err := readRegularInRoot(top, themeCurrentFile, maxCurrentFileBytes)
	if err != nil {
		return ""
	}
	id := strings.TrimSpace(string(b))
	if !validThemeID(id) {
		return ""
	}
	return id
}

func (s *themeStore) listEntriesLocked(top *os.Root) ([]management.ThemeSummary, error) {
	d, err := top.Open(".")
	if err != nil {
		return nil, err
	}
	defer d.Close()
	pubDirs, err := d.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	themes := []management.ThemeSummary{}
	for _, pe := range pubDirs {
		pub := pe.Name()
		if pub == themeCurrentFile || pe.Type()&fs.ModeSymlink != 0 || !pe.IsDir() || !validThemePart(pub) {
			continue
		}
		pubRoot, err := top.OpenRoot(pub)
		if err != nil {
			continue
		}
		pd, err := pubRoot.Open(".")
		if err != nil {
			pubRoot.Close()
			continue
		}
		files, err := pd.ReadDir(-1)
		pd.Close()
		if err != nil {
			pubRoot.Close()
			continue
		}
		for _, fe := range files {
			fname := fe.Name()
			if fe.Type()&fs.ModeSymlink != 0 || !strings.HasSuffix(fname, ".json") {
				continue
			}
			name := strings.TrimSuffix(fname, ".json")
			if !validThemePart(name) {
				continue
			}
			raw, err := readRegularInRoot(pubRoot, fname, maxThemeBytes)
			if err != nil {
				continue
			}
			themeName, ok := parseThemeObject(raw)
			if !ok {
				continue
			}
			themes = append(themes, management.ThemeSummary{
				ID:    pub + "/" + name,
				Name:  themeName,
				Bytes: len(raw),
			})
		}
		pubRoot.Close()
	}
	slices.SortFunc(themes, func(a, b management.ThemeSummary) int { return strings.Compare(a.ID, b.ID) })
	return themes, nil
}

func (s *themeStore) list() (management.ThemesReply, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	top, err := s.openTop(false)
	if errors.Is(err, fs.ErrNotExist) {
		return management.ThemesReply{Themes: []management.ThemeSummary{}}, nil
	}
	if err != nil {
		return management.ThemesReply{}, err
	}
	defer top.Close()
	themes, err := s.listEntriesLocked(top)
	if err != nil {
		return management.ThemesReply{}, err
	}
	return management.ThemesReply{
		Current: s.readCurrentLocked(top),
		Themes:  themes,
	}, nil
}

func (s *themeStore) get(pub, name string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	top, err := s.openTop(false)
	if err != nil {
		return nil, err
	}
	defer top.Close()
	pubInfo, err := top.Lstat(pub)
	if err != nil {
		return nil, err
	}
	if pubInfo.Mode()&fs.ModeSymlink != 0 || !pubInfo.IsDir() {
		return nil, errNotRegular
	}
	pubRoot, err := top.OpenRoot(pub)
	if err != nil {
		return nil, err
	}
	defer pubRoot.Close()
	return readRegularInRoot(pubRoot, name+".json", maxThemeBytes)
}

func (s *themeStore) countStoredLocked(top *os.Root) (int, error) {
	d, err := top.Open(".")
	if err != nil {
		return 0, err
	}
	defer d.Close()
	pubDirs, err := d.ReadDir(-1)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, pe := range pubDirs {
		pub := pe.Name()
		if pub == themeCurrentFile || pe.Type()&fs.ModeSymlink != 0 || !pe.IsDir() || !validThemePart(pub) {
			continue
		}
		pubRoot, err := top.OpenRoot(pub)
		if err != nil {
			continue
		}
		pd, err := pubRoot.Open(".")
		if err != nil {
			pubRoot.Close()
			continue
		}
		files, err := pd.ReadDir(-1)
		pd.Close()
		if err != nil {
			pubRoot.Close()
			continue
		}
		for _, fe := range files {
			fname := fe.Name()
			if fe.Type()&fs.ModeSymlink != 0 || !strings.HasSuffix(fname, ".json") {
				continue
			}
			if !validThemePart(strings.TrimSuffix(fname, ".json")) {
				continue
			}
			linfo, err := pubRoot.Lstat(fname)
			if err != nil || !linfo.Mode().IsRegular() {
				continue
			}
			n++
		}
		pubRoot.Close()
	}
	return n, nil
}

func (s *themeStore) put(pub, name, themeName string, raw []byte) (management.ThemeSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	top, err := s.openTop(true)
	if err != nil {
		return management.ThemeSummary{}, err
	}
	defer top.Close()

	// Replacing an existing stored theme stays allowed even at the 64-theme cap.
	exists := false
	fname := name + ".json"
	if pubInfo, err := top.Lstat(pub); err == nil {
		if pubInfo.Mode()&fs.ModeSymlink != 0 || !pubInfo.IsDir() {
			return management.ThemeSummary{}, fmt.Errorf("%s is not a folder", pub)
		}
		if pubRoot, err := top.OpenRoot(pub); err == nil {
			if linfo, err := pubRoot.Lstat(fname); err == nil && linfo.Mode().IsRegular() {
				exists = true
			}
			pubRoot.Close()
		}
	}
	if !exists {
		count, err := s.countStoredLocked(top)
		if err != nil {
			return management.ThemeSummary{}, err
		}
		if count >= maxStoredThemes {
			return management.ThemeSummary{}, errThemeLimit
		}
	}

	if err := top.Mkdir(pub, private.DirMode); err != nil && !errors.Is(err, fs.ErrExist) {
		return management.ThemeSummary{}, err
	}
	pubRoot, err := top.OpenRoot(pub)
	if err != nil {
		return management.ThemeSummary{}, err
	}
	defer pubRoot.Close()
	if err := private.WriteFile(pubRoot, filepath.Join(s.dir, pub), ".theme-", fname, raw); err != nil {
		return management.ThemeSummary{}, err
	}
	return management.ThemeSummary{
		ID:    pub + "/" + name,
		Name:  themeName,
		Bytes: len(raw),
	}, nil
}

func (s *themeStore) remove(pub, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	top, err := s.openTop(false)
	if err != nil {
		return err
	}
	defer top.Close()
	pubInfo, err := top.Lstat(pub)
	if err != nil {
		return err
	}
	if pubInfo.Mode()&fs.ModeSymlink != 0 || !pubInfo.IsDir() {
		return errNotRegular
	}
	pubRoot, err := top.OpenRoot(pub)
	if err != nil {
		return err
	}
	fname := name + ".json"
	linfo, lerr := pubRoot.Lstat(fname)
	if lerr != nil {
		pubRoot.Close()
		return lerr
	}
	if !linfo.Mode().IsRegular() {
		pubRoot.Close()
		return errNotRegular
	}
	if err := pubRoot.Remove(fname); err != nil {
		pubRoot.Close()
		return err
	}
	if err := private.SyncDir(pubRoot); err != nil {
		pubRoot.Close()
		return err
	}
	pubRoot.Close()
	// Best-effort removal of now-empty publisher directory.
	if top.Remove(pub) == nil {
		_ = private.SyncDir(top)
	}
	if s.readCurrentLocked(top) == pub+"/"+name {
		if err := top.Remove(themeCurrentFile); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return private.SyncDir(top)
	}
	return nil
}

func (s *themeStore) setCurrent(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id == "" {
		top, err := s.openTop(false)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		defer top.Close()
		if err := top.Remove(themeCurrentFile); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return private.SyncDir(top)
	}
	top, err := s.openTop(true)
	if err != nil {
		return err
	}
	defer top.Close()
	return private.WriteFile(top, s.dir, ".current-", themeCurrentFile, []byte(id+"\n"))
}

package core

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/stdotwork/pluxy/next/internal/configdoc"
	"github.com/stdotwork/pluxy/next/internal/route"
	"github.com/stdotwork/pluxy/next/internal/server"
)

// The management calls that read and change core's config file: the document itself, the
// client keys, the management key, and the basic and routing settings. Every change goes
// through a reload transaction (WriteConfig): the new contents are checked and prepared like
// any reload, and the file is written, atomically, only when they are ready to serve.

// errWriteConfig marks a change that was ready but could not be written to the file.
var errWriteConfig = errors.New("writing the config file")

// editError is a change that does not apply to the config file: the reply's status and code.
type editError struct {
	status int
	code   string
	msg    string
}

func (e *editError) Error() string { return e.msg }

// WriteConfig changes the config file in a reload transaction: edit gets the file's contents
// and returns the new contents. They are checked and prepared as a reload prepares them, and
// only then written to the file (replacing it in one step) and published. When edit fails or
// the new contents are refused, the file and the active config stay as they were.
func (c *Core) WriteConfig(why string, edit func(cur []byte) ([]byte, error)) error {
	c.reloading.Lock()
	defer c.reloading.Unlock()
	return c.transact(true, why, nil, nil, edit)
}

// writeFileAtomic replaces the file at path (the file a symbolic link there points to) with b
// in one step, keeping its permissions: other readers see the old contents or the new, never
// part of them.
func writeFileAtomic(path string, b []byte) (err error) {
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	fi, err := os.Stat(target)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(tmp)
		}
	}()
	_, err = f.Write(b)
	if err == nil {
		err = f.Chmod(fi.Mode().Perm())
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(tmp, target); err != nil {
		return err
	}
	if dir, derr := os.Open(filepath.Dir(target)); derr == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}

func (c *Core) isYAML() bool { return server.IsYAML(c.opts.ConfigPath) }

func fileHash(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// configView is the config document as the management API shows it.
type configView struct {
	// Format is "yaml" for a config.yaml with today's core's key names, else "json".
	Format string `json:"format"`
	// Hash is the SHA-256 of the file's contents, as status shows it; a write may send it
	// back as base_hash.
	Hash string `json:"hash"`
	// Content is the file's contents with a placeholder in place of each secret.
	Content string `json:"content"`
}

func (c *Core) configView() (configView, error) {
	b, err := os.ReadFile(c.opts.ConfigPath)
	if err != nil {
		return configView{}, err
	}
	red, err := c.redactor.Redact(b, c.isYAML())
	if err != nil {
		return configView{}, err
	}
	v := configView{Format: "json", Hash: fileHash(b), Content: string(red)}
	if c.isYAML() {
		v.Format = "yaml"
	}
	return v, nil
}

// scrub returns msg with the secrets of the config file, of the active config and of the
// given config contents replaced by placeholders.
func (c *Core) scrub(msg string, docs ...[]byte) string {
	var secrets []string
	if b, err := os.ReadFile(c.opts.ConfigPath); err == nil {
		docs = append(docs, b)
	}
	for _, b := range docs {
		if d, err := configdoc.Parse(b, c.isYAML()); err == nil {
			secrets = append(secrets, d.Secrets()...)
		}
	}
	if cur := c.cur.Load(); cur != nil { // nil while core starts
		secrets = append(append(secrets, cur.Config.ManagementKey), cur.Config.ClientKeys...)
	}
	return c.redactor.Scrub(msg, secrets)
}

// ScrubConfigError returns err with the secrets of the config file at path taken out of its
// message, for a command that reads the config without starting core (-import-auth).
func ScrubConfigError(path string, err error) error {
	c := &Core{opts: Options{ConfigPath: path}, redactor: configdoc.NewRedactor()}
	return c.scrubbed(err)
}

// scrubbed returns err with the secrets scrub finds taken out of its message; errors.Is and
// errors.As still see err.
func (c *Core) scrubbed(err error, docs ...[]byte) error {
	msg := c.scrub(err.Error(), docs...)
	if msg == err.Error() {
		return err
	}
	return &scrubbedError{msg, err}
}

type scrubbedError struct {
	msg string
	err error
}

func (e *scrubbedError) Error() string { return e.msg }
func (e *scrubbedError) Unwrap() error { return e.err }

// writeChangeError writes why a change to the config file failed; docs are the config
// contents involved, whose secrets the message must not show.
func (c *Core) writeChangeError(w http.ResponseWriter, err error, docs ...[]byte) {
	var ee *editError
	switch {
	case errors.As(err, &ee):
		writeError(w, ee.status, ee.code, c.scrub(ee.msg, docs...))
	case errors.Is(err, errWriteConfig):
		writeError(w, http.StatusInternalServerError, "internal", c.scrub(err.Error(), docs...))
	default:
		writeError(w, http.StatusUnprocessableEntity, "refused", c.scrub(err.Error(), docs...))
	}
}

// edit returns an edit of the config file that parses it, applies change and writes it out.
func (c *Core) edit(change func(d *configdoc.Doc) error) func([]byte) ([]byte, error) {
	return func(cur []byte) ([]byte, error) {
		d, err := configdoc.Parse(cur, c.isYAML())
		if err != nil {
			return nil, err
		}
		if err := change(d); err != nil {
			return nil, err
		}
		return d.Bytes()
	}
}

// readStrictJSON reads a request body into v, refusing fields v does not have.
func readStrictJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxManagementBody))
	if err == nil {
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.DisallowUnknownFields()
		err = dec.Decode(v)
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body: "+err.Error())
		return false
	}
	return true
}

// newKey returns a new random key: 26 characters, 130 random bits.
func newKey() string { return rand.Text() }

// clientKeyView is a client key as the management API lists it.
type clientKeyView struct {
	// ID names the key in calls, without showing it. It changes when core restarts.
	ID string `json:"id"`
	// Masked is the key with all but its first and last two characters hidden, as today's
	// panel shows keys; a key shorter than 8 characters is hidden whole.
	Masked string `json:"masked"`
}

func (c *Core) keyView(k string) clientKeyView {
	return clientKeyView{ID: c.redactor.ID(k), Masked: maskKey(k)}
}

func maskKey(k string) string {
	r := []rune(k)
	if len(r) < 8 {
		return strings.Repeat("*", 10)
	}
	return string(r[:2]) + strings.Repeat("*", 6) + string(r[len(r)-2:])
}

// settingsView is the basic and routing settings, by today's core's names.
type settingsView struct {
	Routing       routingView       `json:"routing"`
	Observability observabilityView `json:"observability"`
}

type observabilityView struct {
	Logs  logsView  `json:"logs"`
	Usage usageView `json:"usage"`
}

type logsView struct {
	LoggingToFile      bool `json:"logging-to-file"`
	LogsMaxTotalSizeMB int  `json:"logs-max-total-size-mb"`
}

type usageView struct {
	UsageStatisticsEnabled bool `json:"usage-statistics-enabled"`
}

type routingView struct {
	Strategy           string       `json:"strategy"`
	SessionAffinity    bool         `json:"session-affinity"`
	SessionAffinityTTL string       `json:"session-affinity-ttl"`
	ForceModelPrefix   bool         `json:"force-model-prefix"`
	Retry              retryView    `json:"retry"`
	Cooldown           cooldownView `json:"cooldown"`
}

type retryView struct {
	// RequestRetry is null while the config leaves rounds to this core's own rules.
	RequestRetry        *int `json:"request-retry"`
	MaxRetryCredentials int  `json:"max-retry-credentials"`
	// MaxRetryInterval is in seconds.
	MaxRetryInterval float64 `json:"max-retry-interval"`
}

type cooldownView struct {
	DisableCooling bool `json:"disable-cooling"`
	// TransientErrorCooldownSeconds is 0 for this core's backoff, -1 for none.
	TransientErrorCooldownSeconds float64 `json:"transient-error-cooldown-seconds"`
}

// settings returns the active config's settings by today's core's names.
func settings(cfg *server.Config) settingsView {
	r := cfg.Routing
	v := routingView{Strategy: r.Strategy, ForceModelPrefix: r.ForceModelPrefix,
		Retry:    retryView{RequestRetry: r.RequestRetry, MaxRetryCredentials: r.MaxAccounts, MaxRetryInterval: float64(r.MaxWaitMS) / 1000},
		Cooldown: cooldownView{DisableCooling: r.DisableCooling, TransientErrorCooldownSeconds: float64(max(r.TransientErrorCooldownMS, -1000)) / 1000}}
	switch r.Strategy {
	case "":
		v.Strategy = route.StrategyRoundRobin
	case route.StrategyWeighted:
		v.Strategy = "weighted-round-robin"
	}
	ttl := time.Hour
	if a := r.SessionAffinity; a != nil {
		v.SessionAffinity = true
		if a.TTLMS > 0 {
			ttl = time.Duration(a.TTLMS) * time.Millisecond
		}
	}
	v.SessionAffinityTTL = shortDuration(ttl)
	return settingsView{Routing: v, Observability: observabilityView{
		Logs:  logsView{LoggingToFile: cfg.Logs.ToFile, LogsMaxTotalSizeMB: cfg.Logs.MaxTotalSizeMB},
		Usage: usageView{UsageStatisticsEnabled: cfg.Usage.On()}}}
}

// shortDuration writes d as today's config does, such as 1h or 1h30m rather than 1h0m0s.
func shortDuration(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

// handleConfig adds the config calls to the management API.
func (c *Core) handleConfig(m *server.Management) {
	m.Handle("GET /config", func(w http.ResponseWriter, r *http.Request) {
		v, err := c.configView()
		switch {
		case errors.Is(err, configdoc.ErrInvalid):
			writeError(w, http.StatusUnprocessableEntity, "invalid_config", "the config file does not parse, so it cannot be shown with its secrets hidden: "+err.Error())
		case err != nil:
			writeError(w, http.StatusInternalServerError, "internal", err.Error())
		default:
			writeJSON(w, http.StatusOK, v)
		}
	})

	m.Handle("POST /config/validate", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Content *string `json:"content"`
		}
		if !readStrictJSON(w, r, &req) {
			return
		}
		if req.Content == nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "send the document as content")
			return
		}
		cur, _ := os.ReadFile(c.opts.ConfigPath) // unreadable: placeholders match nothing
		b, err := c.redactor.Restore([]byte(*req.Content), cur, c.isYAML())
		if errors.Is(err, configdoc.ErrStale) {
			writeError(w, http.StatusConflict, "conflict", err.Error())
			return
		}
		var cfg *server.Config
		if err == nil {
			if cfg, err = server.ParseConfig(b, c.opts.ConfigPath); err == nil {
				err = c.fixed(cfg, c.cur.Load())
			}
		}
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"valid": false, "error": c.scrub(err.Error(), cur, b)})
			return
		}
		notices := []string{}
		for _, n := range cfg.Notices {
			notices = append(notices, c.scrub(n, cur, b))
		}
		writeJSON(w, http.StatusOK, map[string]any{"valid": true, "notices": notices})
	})

	m.Handle("PUT /config", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Content  *string `json:"content"`
			BaseHash string  `json:"base_hash"`
		}
		if !readStrictJSON(w, r, &req) {
			return
		}
		if req.Content == nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "send the document as content")
			return
		}
		var current, written []byte
		err := c.WriteConfig("management API: config document", func(cur []byte) ([]byte, error) {
			current = cur
			if req.BaseHash != "" && req.BaseHash != fileHash(cur) {
				return nil, &editError{http.StatusConflict, "conflict", "the config file changed since it was read; read it again"}
			}
			b, err := c.redactor.Restore([]byte(*req.Content), cur, c.isYAML())
			if errors.Is(err, configdoc.ErrStale) {
				return nil, &editError{http.StatusConflict, "conflict", err.Error()}
			}
			written = b
			return b, err
		})
		if err != nil {
			c.writeChangeError(w, err, current, written)
			return
		}
		v, err := c.configView()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, v)
	})

	m.Handle("GET /client-keys", func(w http.ResponseWriter, r *http.Request) {
		keys := []clientKeyView{}
		for _, k := range c.Config().ClientKeys {
			keys = append(keys, c.keyView(k))
		}
		writeJSON(w, http.StatusOK, map[string]any{"keys": keys})
	})

	m.Handle("POST /client-keys", func(w http.ResponseWriter, r *http.Request) {
		key := "sk-" + newKey()
		err := c.WriteConfig("management API: new client key", c.edit(func(d *configdoc.Doc) error { return d.AddClientKey(key) }))
		if err != nil {
			c.writeChangeError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"key": c.keyView(key), "value": key})
	})

	m.Handle("DELETE /client-keys/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		err := c.WriteConfig("management API: client key removed", c.edit(func(d *configdoc.Doc) error {
			found := false
			left, err := d.RemoveClientKeys(func(k string) bool {
				drop := c.redactor.ID(k) == id
				found = found || drop
				return drop
			})
			switch {
			case err != nil:
				return err
			case !found:
				return &editError{http.StatusNotFound, "not_found", "no client key in the config file has this ID"}
			case left == 0:
				return &editError{http.StatusConflict, "conflict", "this is the last client key, and without one anyone who reaches core can use it; add another key first"}
			}
			return nil
		}))
		if err != nil {
			c.writeChangeError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	m.Handle("POST /management-key/rotate", func(w http.ResponseWriter, r *http.Request) {
		key := newKey()
		err := c.WriteConfig("management API: management key rotated", c.edit(func(d *configdoc.Doc) error { return d.SetManagementKey(key) }))
		if err != nil {
			c.writeChangeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"value": key})
	})

	m.Handle("GET /settings", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, settings(c.Config()))
	})

	m.Handle("PATCH /settings", func(w http.ResponseWriter, r *http.Request) {
		var p configdoc.SettingsPatch
		if !readStrictJSON(w, r, &p) {
			return
		}
		err := c.WriteConfig("management API: settings", c.edit(func(d *configdoc.Doc) error {
			if err := d.ApplySettings(p); err != nil {
				return &editError{http.StatusBadRequest, "invalid_request", err.Error()}
			}
			return nil
		}))
		if err != nil {
			c.writeChangeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, settings(c.Config()))
	})
}

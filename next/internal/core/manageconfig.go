package core

import (
	"bytes"
	"cmp"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/stdotwork/pluxy/next/internal/configdoc"
	"github.com/stdotwork/pluxy/next/internal/private"
	"github.com/stdotwork/pluxy/next/internal/proxyurl"
	"github.com/stdotwork/pluxy/next/internal/route"
	"github.com/stdotwork/pluxy/next/internal/server"
	"github.com/stdotwork/pluxy/next/management"
)

var errWriteConfig = errors.New("writing the config file")

type editError struct {
	status int
	code   string
	msg    string
}

func (e *editError) Error() string { return e.msg }

func (c *Core) WriteConfig(why string, edit func(cur []byte) ([]byte, error)) error {
	c.reloading.Lock()
	defer c.reloading.Unlock()
	return c.transact(true, why, nil, nil, edit)
}

// On Windows, a mode sets only the read-only attribute. A temporary file made the usual way would
// take its folder's list, which may let in users the config's own list keeps out.
func writeFileAtomic(path string, b []byte) (err error) {
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	fi, err := os.Stat(target)
	if err != nil {
		return err
	}
	dir, name := filepath.Split(target)
	dir = filepath.Clean(dir)
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	f, tmp, err := private.CreateTemp(root, dir, "."+name+".")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = root.Remove(tmp)
		}
	}()
	_, err = f.Write(b)
	if err == nil {
		err = f.Chmod(fi.Mode().Perm())
	}
	if err == nil {
		err = private.CopyAccess(target, filepath.Join(dir, tmp))
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
	if err = root.Rename(tmp, name); err != nil {
		return err
	}
	_ = private.SyncDir(root)
	return nil
}

func (c *Core) isYAML() bool { return server.IsYAML(c.opts.ConfigPath) }

func fileHash(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

type configView = management.Config

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

func (c *Core) scrub(msg string, docs ...[]byte) string {
	var secrets []string
	if b, err := os.ReadFile(c.opts.ConfigPath); err == nil {
		docs = append(docs, b)
	}
	for _, b := range docs {
		if d, err := configdoc.Parse(b, c.isYAML()); err == nil {
			secrets = append(secrets, d.TextSecrets()...)
		}
	}
	if cur := c.cur.Load(); cur != nil {
		secrets = append(append(secrets, cur.Config.ManagementKey), cur.Config.ClientKeys...)
	}
	return c.redactor.Scrub(msg, secrets)
}

func ScrubConfigError(path string, err error) error {
	c := &Core{opts: Options{ConfigPath: path}, redactor: configdoc.NewRedactor()}
	return c.scrubbed(err)
}

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

func readStrictJSON(w http.ResponseWriter, r *http.Request, v any) bool {
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
	if dec.Decode(new(any)) != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body must be one JSON object")
		return false
	}
	return true
}

const stagedKeyFor = 10 * time.Minute

type stagedKey struct {
	mu      sync.Mutex
	value   string
	expires time.Time

	waiting, checked func()
}

func (s *stagedKey) lock() {
	if s.waiting == nil {
		s.mu.Lock()
		return
	}
	if !s.mu.TryLock() {
		s.waiting()
		s.mu.Lock()
	}
}

func (s *stagedKey) set(value string, expires time.Time) {
	s.value, s.expires = value, expires
}

func (s *stagedKey) holds(value string, now time.Time) bool {
	return s.value != "" && now.Before(s.expires) && sameSecret(value, s.value)
}

func (s *stagedKey) drop(value string) {
	if sameSecret(value, s.value) {
		s.value, s.expires = "", time.Time{}
	}
}

func sameSecret(a, b string) bool {
	x, y := sha256.Sum256([]byte(a)), sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(x[:], y[:]) == 1
}

var errKeyInUse = errors.New("the management key is already in use")

func newKey() string { return rand.Text() }

type clientKeyView = management.ClientKey

func (c *Core) keyView(k string) clientKeyView {
	id := c.keyID(k)
	return clientKeyView{ID: id, Masked: maskKey(k), Label: c.Config().ClientKeyLabels[id]}
}

func (c *Core) checkKeyLabel(w http.ResponseWriter, sent string) (string, bool) {
	label, err := server.CheckLabel(sent)
	if err == nil && c.Config().LabelIsKey(label) {
		err = server.ErrLabelIsKey
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return "", false
	}
	return label, true
}

func maskKey(k string) string {
	r := []rune(k)
	if len(r) < 8 {
		return strings.Repeat("*", 10)
	}
	return string(r[:2]) + strings.Repeat("*", 6) + string(r[len(r)-2:])
}

type settingsView = management.Settings

type observabilityView = management.ObservabilitySettings

type logsView = management.LogSettings

type usageView = management.UsageSettings

type routingView = management.RoutingSettings

type retryView = management.RetrySettings

type cooldownView = management.CooldownSettings

func settings(cfg *server.Config) settingsView {
	r := cfg.Routing
	v := routingView{Strategy: r.Strategy, ForceModelPrefix: r.ForceModelPrefix,
		Retry: retryView{RequestRetry: r.RequestRetry, MaxRetryCredentials: r.MaxAccounts, MaxRetryInterval: float64(r.MaxWaitMS) / 1000},
		Cooldown: cooldownView{DisableCooling: r.DisableCooling, TransientErrorCooldownSeconds: float64(max(r.TransientErrorCooldownMS, -1000)) / 1000,
			QuotaResponseMode: cmp.Or(r.QuotaResponseMode, route.QuotaRetryable)},
		Timeouts: r.Timeouts, Ignition: r.Ignition}
	switch r.Strategy {
	case "":
		v.Strategy = route.StrategyRoundRobin
	case route.StrategyWeighted:
		v.Strategy = "weighted-round-robin"
	}
	v.SessionAffinity = r.AffinityOn()
	v.SessionAffinityTTL = shortDuration(r.AffinityTTL())
	return settingsView{TokenCountEstimate: cfg.TokenCountEstimateOn(), ProxyURL: proxyurl.Redact(cfg.ProxyURL), Payload: cfg.Payload, ManagementAllowRemote: cfg.ManagementAllowRemote, Listen: cfg.Listen, Routing: v, Observability: observabilityView{
		Logs: logsView{LoggingToFile: cfg.Logs.ToFile, LogsMaxTotalSizeMB: cfg.Logs.MaxTotalSizeMB,
			RequestLog: cfg.Logs.RequestLog, ErrorLogsMaxFiles: cfg.Logs.ErrorLogs()},
		Usage: usageView{UsageStatisticsEnabled: cfg.Usage.On()}}}
}

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

func (c *Core) settingsFor(r *http.Request) settingsView {
	v := settings(c.Config())
	v.FromThisComputer = server.FromThisMachine(r)
	v.Listen, _ = c.setupCoreTarget()
	if b, err := os.ReadFile(c.opts.ConfigPath); err == nil {
		if file, err := server.ParseConfig(b, c.opts.ConfigPath); err == nil && file.Listen != c.Config().Listen && file.Listen != v.Listen {
			v.ListenAfterRestart = file.Listen
		}
	}
	return v
}

func checkListenAddress(listen string) error {
	host, _, err := configdoc.CheckListen(listen)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		return fmt.Errorf("%w: this computer cannot serve on the address %q, so pluxy could not start on %q; use an address this computer has, or 0.0.0.0 for all of them", configdoc.ErrInvalid, host, listen)
	}
	return ln.Close()
}

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
		cur, _ := os.ReadFile(c.opts.ConfigPath)
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
		if now, _ := c.setupCoreTarget(); cfg.Listen != c.Config().Listen && cfg.Listen != now {
			notices = append(notices, fmt.Sprintf("listen %q is used the next time pluxy starts; until then it keeps serving on %q", cfg.Listen, now))
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
		writeJSON(w, http.StatusOK, management.ClientKeysReply{Keys: keys})
	})

	m.Handle("POST /client-keys", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Label string `json:"label"`
		}
		if !readOptionalJSON(w, r, &req) {
			return
		}
		label, ok := c.checkKeyLabel(w, req.Label)
		if !ok {
			return
		}
		key := "sk-" + newKey()
		err := c.WriteConfig("management API: new client key", c.edit(func(d *configdoc.Doc) error {
			if err := d.AddClientKey(key); err != nil {
				return err
			}
			if label == "" {
				return nil
			}
			return d.SetClientKeyLabel(c.keyID(key), label)
		}))
		if err != nil {
			c.writeChangeError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, management.NewClientKeyReply{Key: c.keyView(key), Value: key})
	})

	m.Handle("PATCH /client-keys/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var req struct {
			Label json.RawMessage `json:"label"`
		}
		if !readStrictJSON(w, r, &req) {
			return
		}
		var sent *string
		if req.Label == nil || json.Unmarshal(req.Label, &sent) != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "send label as a string, or null for none")
			return
		}
		label := ""
		if sent != nil {
			var ok bool
			if label, ok = c.checkKeyLabel(w, *sent); !ok {
				return
			}
		}
		var masked string
		err := c.WriteConfig("management API: client key label", c.edit(func(d *configdoc.Doc) error {
			keys, err := d.ClientKeys()
			if err != nil {
				return err
			}
			i := slices.IndexFunc(keys, func(k string) bool { return c.keyID(k) == id })
			if i < 0 {
				return &editError{http.StatusNotFound, "not_found", "no client key in the config file has this ID"}
			}
			masked = maskKey(keys[i])
			return d.SetClientKeyLabel(id, label)
		}))
		if err != nil {
			c.writeChangeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, clientKeyView{ID: id, Masked: masked, Label: c.Config().ClientKeyLabels[id]})
	})

	m.Handle("DELETE /client-keys/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		err := c.WriteConfig("management API: client key removed", c.edit(func(d *configdoc.Doc) error {
			found := false
			left, err := d.RemoveClientKeys(func(k string) bool {
				drop := c.keyID(k) == id
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
			d.RemoveClientKeyLabel(id)
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

	m.Handle("POST /management-key/rotate/stage", func(w http.ResponseWriter, r *http.Request) {
		key := newKey()
		c.staged.lock()
		c.staged.set(key, c.keyNow().Add(stagedKeyFor))
		c.staged.mu.Unlock()
		config, err := filepath.Abs(c.opts.ConfigPath)
		if err != nil {
			config = c.opts.ConfigPath
		}
		writeJSON(w, http.StatusOK, map[string]any{"value": key, "config": config})
	})

	m.Handle("POST /management-key/rotate/commit", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Value string `json:"value"`
		}
		if !readStrictJSON(w, r, &req) {
			return
		}
		set := c.edit(func(d *configdoc.Doc) error { return d.SetManagementKey(req.Value) })
		err := func() error {
			c.staged.lock()
			defer c.staged.mu.Unlock()
			err := c.WriteConfig("management API: management key changed", func(cur []byte) ([]byte, error) {
				switch {
				case sameSecret(req.Value, c.Config().ManagementKey):
					return nil, errKeyInUse
				case !c.staged.holds(req.Value, c.keyNow()):
					return nil, &editError{http.StatusConflict, "conflict", "this is not the staged management key: none was staged, a later stage replaced it, it is more than 10 minutes old, or pluxy restarted since. Nothing changed"}
				}
				if c.staged.checked != nil {
					c.staged.checked()
				}
				return set(cur)
			})
			if err == nil {
				c.staged.drop(req.Value)
			}
			return err
		}()
		if err != nil && !errors.Is(err, errKeyInUse) {
			c.writeChangeError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	m.Handle("GET /settings", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, c.settingsFor(r))
	})

	m.Handle("PATCH /settings", func(w http.ResponseWriter, r *http.Request) {
		var p configdoc.SettingsPatch
		if !readStrictJSON(w, r, &p) {
			return
		}
		if cur := c.Config().ProxyURL; p.ProxyURL != nil && len(proxyurl.Password(cur)) > 0 && strings.TrimSpace(*p.ProxyURL) == proxyurl.Redact(cur) {
			p.ProxyURL = nil
		}
		if p.Listen != nil && strings.TrimSpace(*p.Listen) != c.Config().Listen {
			if err := checkListenAddress(*p.Listen); err != nil {
				writeError(w, http.StatusBadRequest, "invalid_request", strings.TrimPrefix(err.Error(), configdoc.ErrInvalid.Error()+": "))
				return
			}
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
		writeJSON(w, http.StatusOK, c.settingsFor(r))
	})
}

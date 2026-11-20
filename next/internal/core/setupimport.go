package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/stdotwork/pluxy/next/internal/configdoc"
	"github.com/stdotwork/pluxy/next/internal/cred"
	"github.com/stdotwork/pluxy/next/internal/legacy"
	"github.com/stdotwork/pluxy/next/internal/server"
	"github.com/stdotwork/pluxy/next/management"
)

// maxImportConfig bounds the config.yaml an import reads: today's example config is about 60 KiB.
const maxImportConfig = 4 << 20

// importPlan is a CLIProxyAPI setup read for an import, and what it changes in core.
type importPlan struct {
	res    *legacy.Result
	folder *legacy.AuthFolder // nil without an auth folder
	// skipped are the config's accounts core already has an account by the ID of, by ID.
	skipped map[string]bool
	// chainsKept are the chains of the file core already has one by the name of.
	chainsKept []string
}

// ImportCPA brings a CLIProxyAPI setup across (POST /setup/import): the config.yaml's settings
// and API-key accounts into core's config file, and the auth folder's accounts into its
// credential store. It returns the report, which names every key of the file and every entry of
// the folder; with req.DryRun nothing is written. The error is an *editError for the reply.
func (c *Core) ImportCPA(req management.SetupImportRequest) (management.ImportReport, error) {
	path, err := importPath(req.Config, "config")
	if err != nil {
		return management.ImportReport{}, err
	}
	b, err := readImportConfig(path)
	if err != nil {
		return management.ImportReport{}, &editError{http.StatusBadRequest, "invalid_request", err.Error()}
	}
	scrub := func(msg string) string {
		var secrets []string
		if d, err := configdoc.Parse(b, true); err == nil {
			secrets = d.TextSecrets()
		}
		return c.redactor.Scrub(c.scrub(msg), secrets)
	}
	res, err := legacy.ImportConfig(b, filepath.Dir(path))
	if err != nil {
		return management.ImportReport{}, &editError{http.StatusUnprocessableEntity, "refused", scrub(fmt.Sprintf("%s: %v", path, err))}
	}
	p := &importPlan{res: res, skipped: map[string]bool{}}

	// The auth folder: the one asked for, else the config's oauth.auth-dir, relative to its folder.
	dir, fromConfig := req.AuthDir, false
	if dir == "" && res.AuthDir != "" {
		dir, fromConfig = res.AuthDir, true
	}
	if dir != "" {
		if dir, err = server.ExpandHome(dir); err != nil {
			return management.ImportReport{}, &editError{http.StatusBadRequest, "invalid_request", err.Error()}
		}
		if !filepath.IsAbs(dir) {
			if !fromConfig {
				return management.ImportReport{}, &editError{http.StatusBadRequest, "invalid_request", "auth_dir must be an absolute path on core's machine"}
			}
			dir = filepath.Join(filepath.Dir(path), dir)
		}
		cfg := c.Config()
		if err := legacy.CheckImportFolders(dir, cfg.CredentialsDir); err != nil {
			return management.ImportReport{}, &editError{http.StatusUnprocessableEntity, "refused", err.Error()}
		}
		p.folder, err = legacy.ReadAuthFolder(dir, res.Import, time.Now())
		switch {
		case errors.Is(err, fs.ErrNotExist) && fromConfig:
			// The config names a folder that is not there: its row says so.
			p.folder = nil
			res.Ledger.Update(legacy.ConfigItem(authDirKey(res)), func(list *int, row *legacy.Row) {
				*list, row.Why = legacy.NotBrought, "the folder "+dir+" is not there"
			})
		case err != nil:
			return management.ImportReport{}, &editError{http.StatusBadRequest, "invalid_request", "auth folder: " + err.Error()}
		}
	}

	if !req.DryRun && c.isYAML() {
		return management.ImportReport{}, &editError{http.StatusConflict, "conflict",
			"core runs on a config.yaml, which it reads with today's core's names already; an import writes core's JSON config (pluxy init writes one)"}
	}

	// The config's part, applied to the file (or, in a dry run, to a copy that is only checked).
	cur, err := os.ReadFile(c.opts.ConfigPath)
	if err != nil {
		return management.ImportReport{}, &editError{http.StatusInternalServerError, "internal", err.Error()}
	}
	merge := func(cur []byte) ([]byte, error) {
		d, err := configdoc.Parse(cur, c.isYAML())
		if err != nil {
			return nil, err
		}
		*p = importPlan{res: p.res, folder: p.folder, skipped: map[string]bool{}}
		if err := c.mergeImport(d, p); err != nil {
			return nil, &editError{http.StatusUnprocessableEntity, "refused", scrub(err.Error())}
		}
		return d.Bytes()
	}
	if req.DryRun {
		next, err := merge(cur)
		if err != nil {
			return management.ImportReport{}, err
		}
		if !c.isYAML() {
			cfg, err := server.ParseConfig(next, c.opts.ConfigPath)
			if err != nil {
				return management.ImportReport{}, &editError{http.StatusUnprocessableEntity, "refused", scrub(err.Error())}
			}
			// The checks the write path makes, which a setting that cannot change while core
			// runs (TLS, a credentials folder) fails: a dry run reports them too.
			if err := c.fixed(cfg, c.cur.Load()); err != nil {
				return management.ImportReport{}, &editError{http.StatusUnprocessableEntity, "refused", scrub(err.Error())}
			}
		}
	} else {
		// Every item is accounted for before anything is written.
		if p.folder != nil {
			if err := p.folder.Ledger(p.folder.Files, nil).Check(); err != nil {
				return management.ImportReport{}, &editError{http.StatusInternalServerError, "internal", err.Error()}
			}
		}
		if err := c.WriteConfig("management API: CLIProxyAPI import", merge); err != nil {
			var ee *editError
			if errors.As(err, &ee) {
				return management.ImportReport{}, ee
			}
			return management.ImportReport{}, &editError{http.StatusUnprocessableEntity, "refused", scrub(err.Error())}
		}
	}

	// The auth folder's accounts: into the credential store, or in a dry run checked against it.
	led := &legacy.Ledger{}
	led.Merge(res.Ledger)
	var imported []legacy.AuthFile
	if p.folder != nil {
		reserved := map[string]bool{}
		for _, a := range c.Config().Accounts() {
			reserved[strings.ToLower(a.ID)] = true
		}
		var store legacy.Creator = c.creds
		if req.DryRun {
			for id, item := range res.Accounts {
				if !p.skipped[id] && item != "" {
					reserved[strings.ToLower(id)] = true
				}
			}
			store = &dryStore{have: c.creds.Records()}
		}
		var taken []legacy.Skipped
		imported, taken, err = legacy.Import(store, p.folder.Files, func(id string) bool { return reserved[strings.ToLower(id)] })
		if err != nil {
			// The accounts added so far stay; the rest are named with the reason.
			done := map[string]bool{}
			for _, f := range imported {
				done[f.Record.ID] = true
			}
			for _, f := range p.folder.Files {
				if !done[f.Record.ID] && !slices.ContainsFunc(taken, func(s legacy.Skipped) bool { return s.File == f.File }) {
					taken = append(taken, legacy.Skipped{File: f.File, Reason: err.Error()})
				}
			}
		}
		led.Merge(p.folder.Ledger(imported, taken))
	}

	c.importRows(led, p)
	if res.NewManagementKey != "" && !req.DryRun {
		for _, it := range []string{"management.secret-key", "remote-management.secret-key", "secret-key"} {
			led.Update(legacy.ConfigItem(it), func(_ *int, row *legacy.Row) { row.To = res.NewManagementKey })
		}
	}
	lists, err := led.Report()
	if err != nil {
		return management.ImportReport{}, &editError{http.StatusInternalServerError, "internal", err.Error()}
	}
	conv := func(rows []legacy.Row) []management.ImportRow {
		out := make([]management.ImportRow, len(rows))
		for i, r := range rows {
			out[i] = management.ImportRow(r)
		}
		return out
	}
	return management.ImportReport{DryRun: req.DryRun, Brought: conv(lists.Brought), Changed: conv(lists.Changed),
		NotBrought: conv(lists.NotBrought), Needs: c.importNeeds(p, imported)}, nil
}

// authDirKey is the path in the file of the auth folder's key: the one the translator read.
func authDirKey(res *legacy.Result) string {
	// The ledger names a key by its spelling in the file: the v8 one, or the older auth-dir. When
	// the file sets both, the v8 one is the key translator read (oauth.auth-dir); the older one
	// keeps the reason it is not brought.
	for _, k := range []string{"oauth.auth-dir", "auth-dir"} {
		if res.Ledger.Has(legacy.ConfigItem(k)) {
			return k
		}
	}
	return "oauth.auth-dir"
}

// importRows adds what core decided to the report: listen, which cannot change while core runs,
// the accounts core already has, and the chains it already has.
func (c *Core) importRows(led *legacy.Ledger, p *importPlan) {
	// mergeImport never writes listen (it cannot change while core runs): every listen row says
	// core keeps its own, with the file's reach in from and core's address in to.
	have := c.Config().Listen
	for _, it := range p.res.ListenKeys {
		led.Update(it, func(list *int, row *legacy.Row) {
			*list = legacy.Changed
			if row.From == "" {
				row.From = p.res.OldListen
			}
			row.To = have
			row.Why = "core keeps listening on " + have + ": listen cannot change while core runs"
			row.KeepWith = fmt.Sprintf(`"listen": %q in pluxy's config file, then restart core`, p.res.OldListen)
		})
	}
	byItem := map[string][]string{}
	for id, item := range p.res.Accounts {
		byItem[item] = append(byItem[item], id)
	}
	for item, ids := range byItem {
		var gone []string
		for _, id := range ids {
			if p.skipped[id] {
				gone = append(gone, id)
			}
		}
		if len(gone) == 0 {
			continue
		}
		slices.Sort(gone)
		led.Update(item, func(list *int, row *legacy.Row) {
			if len(gone) == len(ids) {
				*list, row.As = legacy.NotBrought, ""
			} else {
				*list = legacy.Changed
			}
			why := "core already has " + plural(len(gone), "an account", "accounts") + " " + strings.Join(gone, ", ") + ", which it keeps"
			row.Why = strings.Join(slices.DeleteFunc([]string{row.Why, why}, func(s string) bool { return s == "" }), "; ")
		})
	}
	for _, name := range p.chainsKept {
		led.Add(legacy.NotBrought, "", legacy.Row{What: "routing.chains." + name, Why: "core already has a chain by this name, which it keeps"})
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// importNeeds lists the provider packages the imported accounts need that core has not installed.
func (c *Core) importNeeds(p *importPlan, imported []legacy.AuthFile) []management.ImportNeed {
	count := map[string]int{}
	exts, _ := p.res.Config["extensions"].(map[string]any)
	for pkg, v := range exts {
		e, _ := v.(map[string]any)
		accounts, _ := e["accounts"].([]any)
		for _, a := range accounts {
			if id, _ := a.(map[string]any)["id"].(string); !p.skipped[id] {
				count[pkg]++
			}
		}
	}
	for _, f := range imported {
		count[f.Record.Extension]++
	}
	installed := c.cur.Load().pkgs
	out := []management.ImportNeed{}
	for _, pkg := range slices.Sorted(maps.Keys(count)) {
		if _, ok := installed[pkg]; ok || count[pkg] == 0 {
			continue
		}
		out = append(out, management.ImportNeed{Pick: pkg, Why: fmt.Sprintf("%d %s", count[pkg], plural(count[pkg], "account needs it", "accounts need it"))})
	}
	return out
}

// mergeImport writes the import's config into d, core's config: its settings replace core's,
// its client keys, accounts, aliases, excluded models and chains are added to core's, and listen
// stays (it cannot change while core runs). An account or chain core already has one by the ID
// or name of stays as it is, and p records it.
func (c *Core) mergeImport(d *configdoc.Doc, p *importPlan) error {
	have := map[string]bool{}
	for _, a := range c.Config().Accounts() {
		have[strings.ToLower(a.ID)] = true
	}
	for _, r := range c.creds.Records() {
		have[strings.ToLower(r.ID)] = true
	}
	cfg := p.res.Config
	for _, k := range slices.Sorted(maps.Keys(cfg)) {
		v := cfg[k]
		switch k {
		case "listen":
		case "client_keys":
			keys, err := d.ClientKeys()
			if err != nil {
				return err
			}
			for _, key := range v.([]string) {
				if !slices.Contains(keys, key) {
					if err := d.AddClientKey(key); err != nil {
						return err
					}
				}
			}
		case "management_key":
			if err := d.SetManagementKey(v.(string)); err != nil {
				return err
			}
		case "routing":
			for _, rk := range slices.Sorted(maps.Keys(v.(map[string]any))) {
				rv := v.(map[string]any)[rk]
				if rk != "chains" {
					if err := setFields(d, rv, "routing", rk); err != nil {
						return err
					}
					continue
				}
				b, _ := json.Marshal(rv)
				var chains map[string]json.RawMessage
				_ = json.Unmarshal(b, &chains)
				for _, name := range slices.Sorted(maps.Keys(chains)) {
					if d.Get("routing", "chains", name) != nil {
						p.chainsKept = append(p.chainsKept, name)
						continue
					}
					if err := d.SetChain(name, chains[name]); err != nil {
						return err
					}
				}
			}
		case "extensions":
			for _, pkg := range slices.Sorted(maps.Keys(v.(map[string]any))) {
				sec, _ := v.(map[string]any)[pkg].(map[string]any)
				for _, sk := range slices.Sorted(maps.Keys(sec)) {
					sv := sec[sk]
					switch sk {
					case "accounts":
						for _, a := range sv.([]any) {
							id, _ := a.(map[string]any)["id"].(string)
							if have[strings.ToLower(id)] {
								p.skipped[id] = true
								continue
							}
							have[strings.ToLower(id)] = true
							if err := appendValue(d, a, "extensions", pkg, "accounts"); err != nil {
								return err
							}
						}
					case "model_alias", "excluded_models":
						if err := addMissing(d, sv, "extensions", pkg, sk); err != nil {
							return err
						}
					default:
						if err := setFields(d, sv, "extensions", pkg, sk); err != nil {
							return err
						}
					}
				}
			}
		default:
			if err := setFields(d, v, k); err != nil {
				return err
			}
		}
	}
	return nil
}

// node is v as a node of a config document. JSON is YAML: its nodes carry each value's type,
// which the JSON writer needs.
func node(v any) (*yaml.Node, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var n yaml.Node
	if err := yaml.Unmarshal(b, &n); err != nil {
		return nil, err
	}
	return n.Content[0], nil
}

func setValue(d *configdoc.Doc, v any, path ...string) error {
	n, err := node(v)
	if err != nil {
		return err
	}
	return d.Set(n, path...)
}

// setFields writes v into d at path field by field: a mapping is each of its fields set on its
// own, so a field the file does not set keeps core's value, and anything else replaces the value
// at path.
func setFields(d *configdoc.Doc, v any, path ...string) error {
	m, ok := v.(map[string]any)
	if !ok {
		return setValue(d, v, path...)
	}
	for _, k := range slices.Sorted(maps.Keys(m)) {
		if err := setFields(d, m[k], append(slices.Clone(path), k)...); err != nil {
			return err
		}
	}
	return nil
}

// appendValue adds v to the list at path, making the list when there is none.
func appendValue(d *configdoc.Doc, v any, path ...string) error {
	n, err := node(v)
	if err != nil {
		return err
	}
	list := d.Get(path...)
	if list == nil || list.Tag == "!!null" {
		return d.Set(&yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: []*yaml.Node{n}}, path...)
	}
	if list.Kind != yaml.SequenceNode {
		return fmt.Errorf("%s is not a list", strings.Join(path, "."))
	}
	list.Content = append(list.Content, n)
	return d.Set(list, path...)
}

// addMissing adds the entries of the list v that the list at path does not hold yet: a model
// alias by its alias, whatever its case, an excluded-models pattern as it is.
func addMissing(d *configdoc.Doc, v any, path ...string) error {
	var have []any
	if n := d.Get(path...); n != nil {
		var cur any
		if err := n.Decode(&cur); err == nil {
			have, _ = cur.([]any)
		}
	}
	key := func(e any) string {
		if m, ok := e.(map[string]any); ok {
			s, _ := m["alias"].(string)
			return "alias:" + strings.ToLower(s)
		}
		return fmt.Sprint(e)
	}
	b, _ := json.Marshal(v)
	var add []any
	_ = json.Unmarshal(b, &add)
	for _, e := range add {
		if slices.ContainsFunc(have, func(h any) bool { return key(h) == key(e) }) {
			continue
		}
		have = append(have, e)
		if err := appendValue(d, e, path...); err != nil {
			return err
		}
	}
	return nil
}

// dryStore answers as the credential store would, without saving anything: an ID it already has,
// or one an earlier create in the same dry run took, is taken.
type dryStore struct{ have []cred.Record }

func (s *dryStore) Create(r cred.Record) (cred.Record, error) {
	if !cred.ValidID(r.ID) {
		return cred.Record{}, fmt.Errorf("account ID %q is not valid: use letters, digits, '.', '_' and '-'", r.ID)
	}
	for _, h := range s.have {
		// IDs name files, so IDs that differ only in case name the same file (see cred.Store).
		if strings.EqualFold(h.ID, r.ID) {
			return cred.Record{}, cred.ErrExists
		}
	}
	s.have = append(s.have, r)
	return r, nil
}

// importPath reads a path a management call names on core's machine: absolute, or starting
// with ~ for the home folder.
func importPath(p, name string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", &editError{http.StatusBadRequest, "invalid_request", name + " must name a file on core's machine"}
	}
	p, err := server.ExpandHome(p)
	if err != nil {
		return "", &editError{http.StatusBadRequest, "invalid_request", err.Error()}
	}
	if !filepath.IsAbs(p) {
		return "", &editError{http.StatusBadRequest, "invalid_request", name + " must be an absolute path on core's machine"}
	}
	return filepath.Clean(p), nil
}

func readImportConfig(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil {
		return nil, err
	} else if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a file", path)
	}
	b, err := io.ReadAll(io.LimitReader(f, maxImportConfig+1))
	if err == nil && len(b) > maxImportConfig {
		err = fmt.Errorf("%s is larger than %d MiB", path, maxImportConfig>>20)
	}
	return b, err
}

func (c *Core) handleSetupImport(m *server.Management) {
	m.Handle("POST /setup/import", func(w http.ResponseWriter, r *http.Request) {
		var req management.SetupImportRequest
		if !readStrictJSON(w, r, &req) {
			return
		}
		rep, err := c.ImportCPA(req)
		if err != nil {
			c.writeChangeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, rep)
	})
}

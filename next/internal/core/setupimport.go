package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
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

const maxImportConfig = 4 << 20

type importPlan struct {
	res        *legacy.Result
	folder     *legacy.AuthFolder
	skipped    map[string]bool
	chainsKept []string
	block      extBlock
}

type extBlock struct {
	added int
	kept  []string
	wrote bool
}

func (c *Core) ImportCPA(req management.SetupImportRequest) (management.ImportReport, error) {
	path, err := importPath(req.Config, "config")
	if err != nil {
		return management.ImportReport{}, err
	}
	b, err := readImportConfig(path)
	if errors.Is(err, fs.ErrNotExist) {
		return management.ImportReport{}, &editError{http.StatusBadRequest, "invalid_request", "there is no file at " + path}
	}
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
			p.folder = nil
			res.Ledger.Update(legacy.ConfigItem(authDirKey(res)), func(list *int, row *legacy.Row) {
				*list, row.Why = legacy.NotBrought, "the folder "+dir+" is not there"
			})
		case errors.Is(err, fs.ErrNotExist) && missing(dir):
			return management.ImportReport{}, &editError{http.StatusBadRequest, "invalid_request", "there is no auth folder at " + dir}
		case err != nil:
			return management.ImportReport{}, &editError{http.StatusBadRequest, "invalid_request", "auth folder: " + err.Error()}
		}
		if fromConfig && p.folder != nil {
			res.Ledger.Update(legacy.ConfigItem(authDirKey(res)), func(_ *int, row *legacy.Row) {
				row.Why = "the folder's accounts go into pluxy's credential store; pluxy keeps no auth folder"
			})
		}
	}

	if !req.DryRun && c.isYAML() {
		return management.ImportReport{}, &editError{http.StatusConflict, "conflict",
			"pluxy runs on a config.yaml, which it already reads with CLIProxyAPI's key names; an import writes pluxy's JSON config (pluxy init writes one)"}
	}

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
			if err := c.fixed(cfg, c.cur.Load()); err != nil {
				return management.ImportReport{}, &editError{http.StatusUnprocessableEntity, "refused", scrub(err.Error())}
			}
		}
	} else {
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
	rep := management.ImportReport{DryRun: req.DryRun, Brought: conv(lists.Brought), Changed: conv(lists.Changed),
		NotBrought: conv(lists.NotBrought), Needs: c.importNeeds(p, imported)}
	if !req.DryRun && c.setup != nil {
		rec := management.SetupLastImport{At: time.Now().UTC(), Brought: len(rep.Brought), Changed: len(rep.Changed), NotBrought: len(rep.NotBrought)}
		if err := c.setup.recordImport(path, rec); err != nil {
			log.Printf("error: setup: saving %s: %v", c.setup.statePath, err)
		}
	}
	return rep, nil
}

func authDirKey(res *legacy.Result) string {
	for _, k := range []string{"oauth.auth-dir", "auth-dir"} {
		if res.Ledger.Has(legacy.ConfigItem(k)) {
			return k
		}
	}
	return "oauth.auth-dir"
}

func (c *Core) importRows(led *legacy.Ledger, p *importPlan) {
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
	if len(p.block.kept) > 0 {
		slices.Sort(p.block.kept)
		kept := p.block.kept
		why := "core already has " + plural(len(kept), "an account", "accounts") + " " + strings.Join(kept, ", ") + ", which it keeps"
		led.Update(legacy.ConfigItem("extensions"), func(list *int, row *legacy.Row) {
			if p.block.added == 0 && !p.block.wrote {
				*list, row.As = legacy.NotBrought, ""
			} else {
				*list = legacy.Changed
			}
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

var notShipped = map[string]string{
	legacy.Antigravity: "pluxy does not ship it: build and install it by hand from next/archive/antigravity (its README says how)",
}

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
		why := fmt.Sprintf("%d %s", count[pkg], plural(count[pkg], "account needs it", "accounts need it"))
		if hint := notShipped[pkg]; hint != "" {
			why += ", and " + hint
		}
		out = append(out, management.ImportNeed{Pick: pkg, Why: why})
	}
	return out
}

func (c *Core) mergeImport(d *configdoc.Doc, p *importPlan) error {
	have := map[string]bool{}
	for _, a := range c.Config().Accounts() {
		have[strings.ToLower(a.ID)] = true
	}
	for _, r := range c.creds.Records() {
		have[strings.ToLower(r.ID)] = true
	}
	own := maps.Clone(have)
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
								if own[strings.ToLower(id)] {
									if _, provider := p.res.Accounts[id]; !provider {
										p.block.kept = append(p.block.kept, id)
									}
								}
								continue
							}
							have[strings.ToLower(id)] = true
							if _, provider := p.res.Accounts[id]; !provider {
								p.block.added++
							}
							if err := appendValue(d, a, "extensions", pkg, "accounts"); err != nil {
								return err
							}
						}
					case "model_alias", "excluded_models":
						p.block.wrote = true
						if err := addMissing(d, sv, "extensions", pkg, sk); err != nil {
							return err
						}
					default:
						p.block.wrote = true
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

type dryStore struct{ have []cred.Record }

func (s *dryStore) Create(r cred.Record) (cred.Record, error) {
	if !cred.ValidID(r.ID) {
		return cred.Record{}, fmt.Errorf("account ID %q is not valid: use letters, digits, '.', '_' and '-'", r.ID)
	}
	for _, h := range s.have {
		if strings.EqualFold(h.ID, r.ID) {
			return cred.Record{}, cred.ErrExists
		}
	}
	s.have = append(s.have, r)
	return r, nil
}

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

func missing(path string) bool {
	_, err := os.Stat(path)
	return errors.Is(err, fs.ErrNotExist)
}

func readImportConfig(path string) ([]byte, error) {
	f, err := openClientFile(path)
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

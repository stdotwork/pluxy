package legacy

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/stdotwork/pluxy/next/internal/cred"
	"github.com/stdotwork/pluxy/next/internal/proxyurl"
	"github.com/stdotwork/pluxy/next/internal/route"
	"github.com/stdotwork/pluxy/next/internal/upheader"
	"github.com/stdotwork/pluxy/next/protocol"
)

type AuthFile struct {
	File    string
	Record  cred.Record
	Ignored []string
}

type Skipped struct {
	File   string
	Reason string
}

const (
	codexRefreshLead       = 96 * time.Hour
	claudeRefreshLead      = time.Hour
	xaiRefreshLead         = 5 * time.Minute
	siwcRefreshLead        = 5 * time.Minute
	antigravityRefreshLead = 30 * time.Minute
)

const googleTokenURI = "https://oauth2.googleapis.com/token"

func AuthFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.Type().IsRegular() && strings.HasSuffix(strings.ToLower(e.Name()), ".json") {
			out = append(out, e.Name())
		}
	}
	slices.Sort(out)
	return out, nil
}

func AuthAccountID(file string) string {
	return AccountID(file[:len(file)-len(filepath.Ext(file))])
}

func ReadAuthDir(dir string, opts ImportOptions, now time.Time) ([]AuthFile, []Skipped, error) {
	files, err := AuthFiles(dir)
	if err != nil {
		return nil, nil, err
	}
	return readAuthFiles(files, func(name string) ([]byte, error) { return readLimited(filepath.Join(dir, name)) }, opts, now)
}

func ReadAuthFile(name string, b []byte, opts ImportOptions, now time.Time) ([]AuthFile, []Skipped) {
	files, skipped, _ := readAuthFiles([]string{name}, func(string) ([]byte, error) {
		if len(b) > MaxAuthFile {
			return nil, errTooLarge
		}
		return b, nil
	}, opts, now)
	return files, skipped
}

func readAuthFiles(files []string, read func(name string) ([]byte, error), opts ImportOptions, now time.Time) ([]AuthFile, []Skipped, error) {
	var out, siwcs []AuthFile
	var skipped []Skipped
	for _, name := range files {
		b, err := read(name)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if errors.Is(err, errTooLarge) {
			skipped = append(skipped, Skipped{File: name, Reason: TooLarge})
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		f, siwc, reason := readAuthFile(name, b, opts, now)
		if reason != "" {
			skipped = append(skipped, Skipped{File: name, Reason: reason})
		} else {
			out = append(out, f)
		}
		if siwc != nil {
			siwcs = append(siwcs, *siwc)
		}
	}
	used := map[string]bool{}
	for _, name := range files {
		used[strings.ToLower(AuthAccountID(name))] = true
	}
	for _, f := range siwcs {
		f.Record.ID = accountID(f.Record.ID, used)
		out = append(out, f)
	}
	return out, skipped, nil
}

func readAuthFile(name string, b []byte, opts ImportOptions, now time.Time) (f AuthFile, siwc *AuthFile, reason string) {
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return AuthFile{}, nil, "not a JSON object"
	}
	typ := strings.ToLower(strings.TrimSpace(text(m, "type")))
	f = AuthFile{File: name}
	r := cred.Record{ID: AuthAccountID(name), State: cred.StateActive}
	settings := map[string]any{}
	c := protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: text(m, "access_token"), RefreshToken: text(m, "refresh_token")}
	if t, err := time.Parse(time.RFC3339, text(m, "expired")); err == nil {
		c.ExpiresAt = t.Unix()
	}
	renewAhead := func(lead time.Duration) {
		if c.ExpiresAt > 0 {
			if at := time.Unix(c.ExpiresAt, 0).Add(-lead); at.After(now) {
				c.RefreshAt = at.Unix()
			}
		}
	}
	switch typ {
	case "codex":
		r.Extension = Codex
		renewAhead(codexRefreshLead)
		c.Data = object(map[string]any{"id_token": text(m, "id_token"), "account_id": text(m, "account_id")})
		if boolean(m["websockets"]) {
			settings["websockets"] = true
		}
		if !opts.CodexIdentity {
			settings["codex_identity"] = false
		}
		if !opts.ImageGenerationTool {
			settings["image_generation_tool"] = false
		}
		if !opts.Images {
			settings["images"] = false
		}
	case "claude":
		r.Extension = Claude
		renewAhead(claudeRefreshLead)
		if opts.ClaudeModelLevelCooling {
			settings["model_level_cooling"] = true
		}
	case "kimi", "kimi-ai":
		r.Extension = Kimi
		c.Data = object(map[string]any{"device_id": text(m, "device_id")})
		if d := kimiDomain(text(m, "domain"), text(m, "base_url"), typ); d != "" {
			settings["domain"] = d
		}
		if u := text(m, "base_url"); u != "" {
			settings["base_url"] = u
		}
	case "vertex":
		r.Extension = Gemini
		sa, _ := m["service_account"].(map[string]any)
		pk, _ := sa["private_key"].(string)
		key := map[string]any{"client_email": text(sa, "client_email"), "private_key": pk}
		if u := text(sa, "token_uri"); u != "" && u != googleTokenURI {
			key["token_uri"] = u
		}
		if key["client_email"] == "" || strings.TrimSpace(pk) == "" {
			return f, nil, "its service_account has no client_email or private_key"
		}
		c = protocol.Credential{Kind: "service_account", Data: object(key)}
		project := text(m, "project_id")
		if project == "" {
			project = text(sa, "project_id")
		}
		location := text(m, "location")
		if location == "" {
			location = "us-central1"
		}
		v := map[string]any{"location": location}
		if project != "" {
			v["project"] = project
		}
		settings["vertex"] = v
		interactions, _ := m["interactions"].(bool)
		native, _ := m["native_interactions"].(bool)
		if interactions || native {
			settings["interactions"] = true
		}
	case "antigravity":
		r.Extension = Antigravity
		renewAhead(antigravityRefreshLead)
		project := text(m, "project_id")
		c.Data = object(map[string]any{"project_id": project})
		if project == "" && c.RefreshToken != "" {
			c.RefreshAt = now.Unix()
		}
		if u := text(m, "base_url"); u != "" {
			settings["base_url"] = u
		}
	case "xai":
		r.Extension = XAI
		renewAhead(xaiRefreshLead)
		c.Data = object(map[string]any{"token_endpoint": text(m, "token_endpoint")})
		if u := text(m, "base_url"); u != "" {
			settings["base_url"] = u
		}
		if opts.InjectXSearch {
			settings["inject_x_search"] = true
		}
	case "meta":
		r.Extension = Meta
		key, dca := text(m, "api_key"), text(m, "dca_token")
		if t := text(m, "access_token"); strings.HasPrefix(t, "dca:") {
			if dca == "" {
				dca = t
			}
		} else if key == "" {
			key = t
		}
		if strings.HasPrefix(key, "dca:") {
			key = ""
		}
		c = protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: key, RefreshToken: dca,
			Data: object(map[string]any{"base_url": text(m, "base_url")})}
		if key == "" && dca != "" {
			c.RefreshAt = now.Unix()
		}
	case "devin":
		r.Extension = Devin
		tok := text(m, "session_token")
		if tok == "" {
			tok = text(m, "api_key")
		}
		if tok == "" {
			return f, nil, "it holds no token"
		}
		c = protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: tok}
		if u := text(m, "base_url"); u != "" {
			settings["base_url"] = u
		}
	case "":
		return f, nil, "it names no type (CLIProxyAPI does not load it either)"
	case "gemini":
		return f, nil, "type gemini is not supported (CLIProxyAPI does not load it either)"
	default:
		return f, nil, "its type is not one the import takes"
	}
	if c.Kind == protocol.CredentialOAuth && c.AccessToken == "" && c.RefreshToken == "" {
		if typ != "codex" {
			return f, nil, "it holds no token"
		}
		reason = "it holds no token"
	}
	r.Credential = c
	r.Disabled = boolean(m["disabled"])
	r.ProviderLabel = text(m, "email")
	if r.ProviderLabel == "" && typ == "devin" {
		r.ProviderLabel = text(m, "user_name")
	}
	r.Label, r.Named = text(m, "label"), true
	if r.Label == "" {
		r.Label, r.Named = r.ProviderLabel, false
	}
	r.Account = route.Account{Priority: number(m["priority"])}
	if _, ok := m["weight"]; ok {
		w := max(number(m["weight"]), 0)
		r.Account.Weight = &w
	}
	if len(settings) > 0 {
		r.Account.Settings, _ = json.Marshal(settings)
	}
	if p, ok := m["prefix"].(string); ok {
		r.Account.Prefix = modelPrefix(p)
	}
	for _, k := range []string{"disable_cooling", "disable-cooling"} {
		if on, ok := parseBool(m[k]); ok {
			r.Account.DisableCooling = &on
			break
		}
	}
	for _, k := range []string{"request_retry", "request-retry"} {
		if n, ok := wholeNumber(m[k]); ok {
			if n >= 0 {
				r.Account.RequestRetry = &n
			}
			break
		}
	}
	r.Account.ExcludedModels = authExcluded(m)
	r.Account.ModelAlias = authAliases(m)
	for _, k := range []string{"proxy_url", "proxy-url"} {
		if v, ok := m[k]; ok && !isZero(v) {
			p, isText := v.(string)
			if !isText {
				return f, nil, k + " is not text"
			}
			if err := proxyurl.Check(k, p); err != nil {
				return f, nil, err.Error()
			}
			r.Account.ProxyURL = strings.TrimSpace(p)
			break
		}
	}
	if raw, ok := m["headers"].(map[string]any); ok {
		text := map[string]string{}
		for k, v := range raw {
			if s, isText := v.(string); isText {
				text[k] = s
			}
		}
		h, left, err := upheader.Import(text)
		if err != nil {
			return f, nil, err.Error()
		}
		if err := upheader.Check("headers", h); err != nil {
			return f, nil, err.Error()
		}
		r.Account.Headers = h
		for _, l := range left {
			f.Ignored = append(f.Ignored, "headers."+l.Name+" ("+l.Why+")")
		}
	}
	f.Record = r
	if typ == "codex" {
		siwc = siwcAccount(m, f, opts, now)
	}
	return f, siwc, reason
}

func siwcAccount(m map[string]any, base AuthFile, opts ImportOptions, now time.Time) *AuthFile {
	s, _ := m["siwc"].(map[string]any)
	c := protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: text(s, "access_token"), RefreshToken: text(s, "refresh_token")}
	if c.AccessToken == "" {
		c.AccessToken = text(s, "access")
	}
	if c.AccessToken == "" && c.RefreshToken == "" {
		return nil
	}
	for _, k := range []string{"expires_at", "expired"} {
		if t, err := time.Parse(time.RFC3339, text(s, k)); err == nil {
			c.ExpiresAt = t.Unix()
			break
		}
	}
	if c.ExpiresAt > 0 {
		if at := time.Unix(c.ExpiresAt, 0).Add(-siwcRefreshLead); at.After(now) {
			c.RefreshAt = at.Unix()
		}
	}
	c.Data = object(map[string]any{"siwc": true, "client_id": text(s, "client_id"), "device_id": text(s, "device_id"),
		"scope": text(s, "scope"), "id_token": text(s, "id_token")})
	settings := map[string]any{}
	if boolean(m["websockets"]) {
		settings["websockets"] = true
	}
	if opts.SIWCIdentifier != "" {
		settings["signin_with_chatgpt_identifier"] = opts.SIWCIdentifier
	}
	r := base.Record
	r.ID = r.ID[:min(len(r.ID), 128-len("-siwc"))] + "-siwc"
	r.Credential = c
	r.Disabled = base.Record.Disabled || !boolean(m["use_siwc"])
	r.Account.Settings = nil
	if len(settings) > 0 {
		r.Account.Settings, _ = json.Marshal(settings)
	}
	return &AuthFile{File: base.File, Record: r, Ignored: base.Ignored}
}

func authExcluded(m map[string]any) []string {
	v, ok := m["excluded_models"]
	if !ok {
		v = m["excluded-models"]
	}
	var list []string
	switch v := v.(type) {
	case string:
		list = strings.Split(v, ",")
	case []any:
		for _, e := range v {
			if s, ok := e.(string); ok {
				list = append(list, s)
			}
		}
	}
	return patterns(list)
}

func authAliases(m map[string]any) []route.ModelAlias {
	var out []route.ModelAlias
	for _, k := range []string{"model_aliases", "model-aliases", "models"} {
		list, _ := m[k].([]any)
		for _, e := range list {
			o, _ := e.(map[string]any)
			name, alias := strings.TrimSpace(text(o, "name")), strings.TrimSpace(text(o, "alias"))
			if name == "" || alias == "" || strings.EqualFold(name, alias) ||
				slices.ContainsFunc(out, func(r route.ModelAlias) bool { return strings.EqualFold(r.Alias, alias) }) {
				continue
			}
			out = append(out, route.ModelAlias{Name: name, Alias: alias, Fork: boolean(o["fork"]),
				DisplayName: strings.TrimSpace(text(o, "display-name"))})
		}
	}
	return out
}

const MaxAuthFile = 1 << 20

var TooLarge = fmt.Sprintf("larger than %d KiB", MaxAuthFile>>10)

var errTooLarge = errors.New("file too large")

func readLimited(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, MaxAuthFile+1))
	if err == nil && len(b) > MaxAuthFile {
		err = errTooLarge
	}
	return b, err
}

func kimiDomain(domain, baseURL, typ string) string {
	site := func(d string) string {
		d = strings.ToLower(strings.TrimSpace(d))
		switch {
		case d == "kimi.ai" || d == "ai" || d == "kimi-ai" || strings.HasSuffix(d, ".kimi.ai"):
			return "kimi.ai"
		case d == "kimi.com" || d == "com" || d == "kimi" || strings.HasSuffix(d, ".kimi.com"):
			return "kimi.com"
		}
		return ""
	}
	if d := site(domain); d != "" {
		return d
	}
	if u, err := url.Parse(baseURL); err == nil {
		if h := strings.ToLower(u.Hostname()); h == "kimi.ai" || strings.HasSuffix(h, ".kimi.ai") {
			return "kimi.ai"
		} else if h == "kimi.com" || strings.HasSuffix(h, ".kimi.com") {
			return "kimi.com"
		}
	}
	return site(typ)
}

func CheckImportFolders(authDir, credDir string) error {
	real := func(p string) (string, error) {
		p, err := filepath.Abs(p)
		if err != nil {
			return "", err
		}
		rest := ""
		for {
			if r, err := filepath.EvalSymlinks(p); err == nil {
				return filepath.Join(r, rest), nil
			}
			parent := filepath.Dir(p)
			if parent == p {
				return filepath.Join(p, rest), nil
			}
			rest = filepath.Join(filepath.Base(p), rest)
			p = parent
		}
	}
	a, err := real(authDir)
	if err != nil {
		return err
	}
	c, err := real(credDir)
	if err != nil {
		return err
	}
	if rel, err := filepath.Rel(a, c); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("the credential store (%s) is CLIProxyAPI's auth folder or inside it; set credentials_dir to another folder", credDir)
	}
	return nil
}

func text(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return strings.TrimSpace(s)
}

func boolean(v any) bool {
	switch v := v.(type) {
	case bool:
		return v
	case string:
		b, _ := strconv.ParseBool(strings.TrimSpace(v))
		return b
	}
	return false
}

func parseBool(v any) (on, ok bool) {
	switch v := v.(type) {
	case bool:
		return v, true
	case string:
		b, err := strconv.ParseBool(strings.TrimSpace(v))
		return b, err == nil
	}
	return false, false
}

func wholeNumber(v any) (n int, ok bool) {
	switch v := v.(type) {
	case float64:
		return int(v), true
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		return n, err == nil
	}
	return 0, false
}

func number(v any) int {
	switch v := v.(type) {
	case float64:
		return int(v)
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(v))
		return n
	}
	return 0
}

func object(m map[string]any) json.RawMessage {
	for k, v := range m {
		if v == "" {
			delete(m, k)
		}
	}
	if len(m) == 0 {
		return nil
	}
	b, _ := json.Marshal(m)
	return b
}

type Creator interface {
	Create(r cred.Record) (cred.Record, error)
}

func Import(store Creator, files []AuthFile, reserved func(id string) bool) (imported []AuthFile, skipped []Skipped, err error) {
	for _, f := range files {
		if reserved(f.Record.ID) {
			skipped = append(skipped, Skipped{File: f.File, Reason: fmt.Sprintf("the config already has an account %q", f.Record.ID)})
			continue
		}
		switch _, err := store.Create(f.Record); {
		case errors.Is(err, cred.ErrExists):
			skipped = append(skipped, Skipped{File: f.File, Reason: fmt.Sprintf("the store already has an account %q", f.Record.ID)})
		case errors.Is(err, cred.ErrConflict):
			skipped = append(skipped, Skipped{File: f.File, Reason: err.Error()})
		case err != nil:
			return imported, skipped, err
		default:
			imported = append(imported, f)
		}
	}
	return imported, skipped, nil
}

type AuthFolder struct {
	Entries []string
	Files   []AuthFile
	Skipped []Skipped
	read    []string
}

func ReadAuthFolder(dir string, opts ImportOptions, now time.Time) (*AuthFolder, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	a := &AuthFolder{}
	for _, e := range entries {
		a.Entries = append(a.Entries, e.Name())
	}
	slices.Sort(a.Entries)
	if a.read, err = AuthFiles(dir); err != nil {
		return nil, err
	}
	a.Files, a.Skipped, err = ReadAuthDir(dir, opts, now)
	return a, err
}

func (a *AuthFolder) Ledger(imported []AuthFile, taken []Skipped) *Ledger {
	l := &Ledger{}
	for _, name := range a.Entries {
		item := AuthItem(name)
		l.Expect(item)
		row := Row{What: "auth file", Name: name}
		if !slices.Contains(a.read, name) {
			row.Why = "CLIProxyAPI reads only the .json files at the top of its auth folder"
			l.Add(NotBrought, item, row)
			continue
		}
		var made, why []string
		ext := ""
		for _, f := range imported {
			if f.File == name {
				made = append(made, f.Record.ID)
				ext = f.Record.Extension
				why = append(why, notBrought(f.Ignored)...)
			}
		}
		for _, s := range append(slices.Clone(a.Skipped), taken...) {
			if s.File == name {
				why = append(why, s.Reason)
			}
		}
		switch {
		case len(made) == 0:
			if len(why) == 0 {
				why = []string{"it could not be read"}
			}
			row.Why = joinWhy(why)
			l.Add(NotBrought, item, row)
		default:
			row.As = ext + " " + plural(len(made), "account", "accounts") + " " + strings.Join(made, ", ")
			row.Why = joinWhy(why)
			list := Brought
			if len(why) > 0 {
				list = Changed
			}
			l.Add(list, item, row)
		}
	}
	return l
}

func notBrought(ignored []string) []string {
	var out []string
	for _, s := range ignored {
		out = append(out, s+" not brought")
	}
	return out
}

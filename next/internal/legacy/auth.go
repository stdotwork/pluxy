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
	"github.com/stdotwork/pluxy/next/internal/route"
	"github.com/stdotwork/pluxy/next/protocol"
)

// AuthFile is one credential file of today's auth folder, read as an account of this core.
type AuthFile struct {
	// File is the file's name in the folder.
	File string
	// Record is the account, ready for the credential store.
	Record cred.Record
	// Ignored names the file's settings this core does not support yet.
	Ignored []string
}

// Skipped is a file of today's auth folder that does not become an account.
type Skipped struct {
	File   string
	Reason string
}

// How long before expiry the extensions renew what they log in: Codex 4 days ahead, Claude an
// hour ahead, Antigravity half an hour ahead and xAI five minutes ahead
// (next/extensions/*/login.go, next/extensions/claude/oauth.go). Imported accounts get the same;
// the others use core's default.
const (
	codexRefreshLead       = 96 * time.Hour
	claudeRefreshLead      = time.Hour
	antigravityRefreshLead = 30 * time.Minute
	xaiRefreshLead         = 5 * time.Minute
)

// googleTokenURI is the token endpoint a service account key names by default; the Gemini
// extension uses it when the credential names none.
const googleTokenURI = "https://oauth2.googleapis.com/token"

// AuthFiles lists the credential files today's core reads in dir: the .json files at its top
// level.
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

// AuthAccountID is the account ID a file of today's auth folder is imported as: its name less
// .json, made a valid account ID.
func AuthAccountID(file string) string {
	return AccountID(file[:len(file)-len(filepath.Ext(file))])
}

// ReadAuthDir reads every credential file in dir as an account, as next/README.md
// ("Importing today's auth folder") describes. now is the current time, for renewal times.
func ReadAuthDir(dir string, opts ImportOptions, now time.Time) ([]AuthFile, []Skipped, error) {
	files, err := AuthFiles(dir)
	if err != nil {
		return nil, nil, err
	}
	var out []AuthFile
	var skipped []Skipped
	for _, name := range files {
		b, err := readLimited(filepath.Join(dir, name))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if errors.Is(err, errTooLarge) {
			skipped = append(skipped, Skipped{File: name, Reason: fmt.Sprintf("larger than %d KiB", maxAuthFile>>10)})
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		f, reason := readAuthFile(name, b, opts, now)
		if reason != "" {
			skipped = append(skipped, Skipped{File: name, Reason: reason})
			continue
		}
		out = append(out, f)
	}
	return out, skipped, nil
}

// readAuthFile reads one file; a reason says why it is skipped.
func readAuthFile(name string, b []byte, opts ImportOptions, now time.Time) (AuthFile, string) {
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return AuthFile{}, "not a JSON object"
	}
	typ := strings.ToLower(strings.TrimSpace(text(m, "type")))
	f := AuthFile{File: name}
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
	case "claude":
		r.Extension = Claude
		renewAhead(claudeRefreshLead)
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
		pk, _ := sa["private_key"].(string) // as written: a PEM block ends in a newline
		key := map[string]any{"client_email": text(sa, "client_email"), "private_key": pk}
		if u := text(sa, "token_uri"); u != "" && u != googleTokenURI {
			key["token_uri"] = u
		}
		if key["client_email"] == "" || strings.TrimSpace(pk) == "" {
			return f, "its service_account has no client_email or private_key"
		}
		c = protocol.Credential{Kind: "service_account", Data: object(key)}
		project := text(m, "project_id")
		if project == "" {
			project = text(sa, "project_id")
		}
		location := text(m, "location")
		if location == "" {
			location = "us-central1" // today's core's default
		}
		v := map[string]any{"location": location}
		if project != "" {
			v["project"] = project
		}
		settings["vertex"] = v
	case "antigravity":
		// The file holds no OAuth client secret (today's core has it built in), so the account
		// serves requests until its token expires; renewing it needs a new login, which asks for
		// the secret.
		r.Extension = Antigravity
		renewAhead(antigravityRefreshLead)
		c.Data = object(map[string]any{"project_id": text(m, "project_id")})
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
	case "meta":
		// The minted API key is the access token and the device (dca:) token the refresh token,
		// which mints a new key. Today's core keeps the device token in dca_token, or as the
		// access token when no key was minted yet.
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
			c.RefreshAt = now.Unix() // no key yet: core mints one on the first request
		}
	case "devin":
		// A Devin session token is a permanent API key in the Devin extension.
		r.Extension = Devin
		tok := text(m, "session_token")
		if tok == "" {
			tok = text(m, "api_key")
		}
		if tok == "" {
			return f, "it holds no token"
		}
		c = protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: tok}
		if u := text(m, "base_url"); u != "" {
			settings["base_url"] = u
		}
	case "", "gemini":
		return f, "today's core does not load it either"
	default:
		return f, fmt.Sprintf("type %q is not known", typ)
	}
	if c.Kind == protocol.CredentialOAuth && c.AccessToken == "" && c.RefreshToken == "" {
		return f, "it holds no token"
	}
	r.Credential = c
	r.Disabled = boolean(m["disabled"])
	r.Label = text(m, "label")
	if r.Label == "" {
		r.Label = text(m, "email")
	}
	if r.Label == "" && typ == "devin" {
		r.Label = text(m, "user_name")
	}
	r.Account = route.Account{Priority: number(m["priority"])}
	if _, ok := m["weight"]; ok {
		w := max(number(m["weight"]), 0)
		r.Account.Weight = &w
	}
	if len(settings) > 0 {
		r.Account.Settings, _ = json.Marshal(settings)
	}
	for _, k := range []string{"prefix", "proxy_url", "proxy-url", "headers", "excluded_models", "excluded-models", "model_aliases", "model-aliases"} {
		if v, ok := m[k]; ok && !isZero(v) {
			f.Ignored = append(f.Ignored, strings.ReplaceAll(k, "-", "_"))
		}
	}
	f.Record = r
	return f, ""
}

// maxAuthFile bounds a credential file: today's are a few KiB, and a credential must fit in
// one extension protocol frame.
const maxAuthFile = 1 << 20

var errTooLarge = errors.New("file too large")

func readLimited(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxAuthFile+1))
	if err == nil && len(b) > maxAuthFile {
		err = errTooLarge
	}
	return b, err
}

// kimiDomain is the Kimi site ("kimi.com" or "kimi.ai") of an account, decided as today's core
// decides it (core/internal/auth/kimi/kimi.go): its domain, else its base_url's host, else its
// type. Empty: neither, so the Kimi extension's default (kimi.com).
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

// CheckImportFolders refuses an import whose credential store (credDir) is today's auth folder
// (authDir) or inside it: opening the store tidies its folder, and the import must never change
// today's auth folder.
func CheckImportFolders(authDir, credDir string) error {
	real := func(p string) (string, error) {
		p, err := filepath.Abs(p)
		if err != nil {
			return "", err
		}
		// The store's folder may not exist yet: resolve the part that does.
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
		return fmt.Errorf("the credential store (%s) is today's auth folder or inside it; set credentials_dir to another folder", credDir)
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

// number reads a whole number written as a number or as text, as today's core reads priority
// and weight; anything else is 0.
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

// object encodes the non-empty values of m as a JSON object, or nil when none is left.
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

// Import adds the accounts of files to store, skipping those whose ID store or the config
// (reserved) already uses. It returns the files it imported and those it skipped.
func Import(store *cred.Store, files []AuthFile, reserved func(id string) bool) (imported []AuthFile, skipped []Skipped, err error) {
	for _, f := range files {
		if reserved(f.Record.ID) {
			skipped = append(skipped, Skipped{File: f.File, Reason: fmt.Sprintf("the config already has an account %q", f.Record.ID)})
			continue
		}
		switch _, err := store.Create(f.Record); {
		case errors.Is(err, cred.ErrExists):
			skipped = append(skipped, Skipped{File: f.File, Reason: fmt.Sprintf("the store already has an account %q", f.Record.ID)})
		case err != nil:
			return imported, skipped, err
		default:
			imported = append(imported, f)
		}
	}
	return imported, skipped, nil
}

// Pending counts the files in dir that would import as accounts whose IDs has does not know.
func Pending(dir string, has func(id string) bool) (int, error) {
	files, _, err := ReadAuthDir(dir, DefaultImportOptions, time.Now())
	n := 0
	for _, f := range files {
		if !has(f.Record.ID) {
			n++
		}
	}
	return n, err
}

package e2e

import (
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/extensions/kimi/kimitest"
)

// authImportReply is the reply of POST /accounts/import.
type authImportReply struct {
	Imported []struct {
		ID        string   `json:"id"`
		Extension string   `json:"extension"`
		Ignored   []string `json:"ignored"`
	} `json:"imported"`
	Skipped []string `json:"skipped"`
	Error   *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// importAuthFile sends one auth file of today's core to POST /accounts/import with key, and
// returns the status, the raw reply and the reply decoded.
func importAuthFile(t *testing.T, base, key, name, content string) (int, string, authImportReply) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"name": name, "content": content})
	req, err := http.NewRequest(http.MethodPost, base+"/v0/management/accounts/import", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out authImportReply
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, string(raw), out
}

// An auth file of today's core sent to the running core becomes an account at once: listed, and
// serving a request through its extension and the file's proxy. A file that is not JSON, of a type the importer does
// not take, too large, or badly named adds nothing and says why; one whose ID the store or the
// config already uses is skipped; of several imports of one new file at once, one adds it. No
// reply and no line of core's log holds the file's tokens.
func TestImportAuthFile(t *testing.T) {
	kimi := kimitest.New(t)
	proxy := newHTTPProxy(t, false)
	access, refresh := kimi.Grant()
	base, logs := runCoreLogged(t, map[string]any{
		"listen": "127.0.0.1:0", "client_keys": []string{clientKey}, "management_key": managementKey,
		"credentials_dir": filepath.Join(t.TempDir(), "credentials"),
		"extension": map[string]any{"manifest": filepath.Join(binDir, "kimi", "pluxy-extension.json"), "accounts": []any{
			map[string]any{"id": "kimi-config", "settings": kimi.Settings(),
				"credential": map[string]any{"kind": "api_key", "api_key": "fake-kimi-config-key"}},
		}},
	})
	file, _ := json.Marshal(map[string]any{"type": "kimi", "access_token": access, "refresh_token": refresh,
		"device_id": "device-upload-e2e", "base_url": kimi.URL + "/coding", "email": "user@example.com",
		"expired": time.Now().Add(10 * 24 * time.Hour).UTC().Format(time.RFC3339), "proxy_url": proxy.URL("http")})
	secrets := []string{access, refresh, "device-upload-e2e"}
	noSecret := func(where, s string) {
		t.Helper()
		for _, x := range secrets {
			if strings.Contains(s, x) {
				t.Errorf("%s holds %q:\n%s", where, x, s)
			}
		}
	}

	code, raw, reply := importAuthFile(t, base, managementKey, "kimi-upload@example.com.json", string(file))
	if code != http.StatusOK || len(reply.Imported) != 1 || len(reply.Skipped) != 0 {
		t.Fatalf("import: %d %s, want 200 with one account", code, raw)
	}
	if a := reply.Imported[0]; a.ID != "kimi-upload-example.com" || a.Extension != "pluxy/kimi" || len(a.Ignored) != 0 {
		t.Errorf("imported %+v, want kimi-upload-example.com (pluxy/kimi) with nothing ignored", a)
	}
	noSecret("the import reply", raw)
	if a := getAccount(t, base, "kimi-upload-example.com"); a.Source != "login" || a.State != "active" || a.Disabled {
		t.Errorf("listed as %+v, want an active login account", a)
	}

	// It serves at once, with the file's token and device, through the file's proxy.
	req, _ := http.NewRequest(http.MethodPost, base+"/v1/chat/completions",
		strings.NewReader(`{"model":"kimi-k2.8","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+clientKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Pluxy-Account", "kimi-upload-example.com")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "Hello") {
		t.Fatalf("chat through the imported account: %d %s", resp.StatusCode, body)
	}
	reqs := kimi.Requests()
	if len(reqs) == 0 || reqs[len(reqs)-1].Header.Get("Authorization") != "Bearer "+access ||
		reqs[len(reqs)-1].Header.Get("X-Msh-Device-Id") != "device-upload-e2e" {
		t.Errorf("fake Kimi saw %d requests, the last not with the file's token and device", len(reqs))
	}
	if seen, _ := proxy.seen(); !slices.ContainsFunc(seen, func(r string) bool { return strings.Contains(r, "/coding/v1/chat/completions") }) {
		t.Errorf("the file's proxy saw %q, not the chat request", seen)
	}

	for _, c := range []struct {
		name, content string
		status        int
		code, reason  string
	}{
		{"kimi-upload@example.com.json", string(file), http.StatusConflict, "conflict", `the store already has an account "kimi-upload-example.com"`},
		{"KIMI-UPLOAD@example.com.json", string(file), http.StatusConflict, "conflict", `the store already has an account "KIMI-UPLOAD-example.com"`},
		{"kimi-config.json", string(file), http.StatusConflict, "conflict", `the config already has an account "kimi-config"`},
		{"broken.json", `{"type": "kimi", "access_token": "` + access, http.StatusBadRequest, "invalid_request", "not a JSON object"},
		{"gemini-old.json", `{"type": "gemini", "access_token": "fake-gemini-access"}`, http.StatusBadRequest, "invalid_request", "today's core does not load it either"},
		{"qwen-old.json", `{"type": "qwen", "access_token": "fake-qwen-access"}`, http.StatusBadRequest, "invalid_request", "its type is not one the import takes"},
		{"kimi-empty.json", `{"type": "kimi"}`, http.StatusBadRequest, "invalid_request", "it holds no token"},
		{"notes.txt", string(file), http.StatusBadRequest, "invalid_request", "a .json file name"},
		{"../kimi-up.json", string(file), http.StatusBadRequest, "invalid_request", "a .json file name"},
		{"big.json", `{"type": "kimi", "pad": "` + strings.Repeat("x", 1<<20) + `"}`, http.StatusRequestEntityTooLarge, "invalid_request", "larger than 1024 KiB"},
	} {
		code, raw, reply := importAuthFile(t, base, managementKey, c.name, c.content)
		if code != c.status || reply.Error == nil || reply.Error.Code != c.code || !strings.Contains(reply.Error.Message, c.reason) {
			t.Errorf("%s: %d %.300s, want %d %s with %q", c.name, code, raw, c.status, c.code, c.reason)
		}
		noSecret("the reply for "+c.name, raw)
	}

	// Several imports of one new file at once: one adds the account, the others are skipped.
	other, _ := json.Marshal(map[string]any{"type": "kimi", "access_token": access, "base_url": kimi.URL + "/coding"})
	var wg sync.WaitGroup
	codes := make([]int, 8)
	for i := range codes {
		wg.Go(func() { codes[i], _, _ = importAuthFile(t, base, managementKey, "kimi-race.json", string(other)) })
	}
	wg.Wait()
	if n200, n409 := countStatus(codes, http.StatusOK), countStatus(codes, http.StatusConflict); n200 != 1 || n409 != len(codes)-1 {
		t.Errorf("concurrent imports answered %v, want one 200 and the rest 409", codes)
	}

	// The refusals added nothing.
	if ids := listedIDs(t, base); !slices.Equal(ids, []string{"kimi-config", "kimi-race", "kimi-upload-example.com"}) {
		t.Errorf("accounts %v, want kimi-config, kimi-race and kimi-upload-example.com", ids)
	}
	// The management key is needed, as for every management call.
	if code, _, _ := importAuthFile(t, base, "wrong-key", "kimi-nokey.json", string(other)); code != http.StatusUnauthorized {
		t.Errorf("import with a wrong key: %d, want 401", code)
	}
	noSecret("core's log", logs.waitFor(t, ""))
}

func countStatus(codes []int, want int) int {
	n := 0
	for _, c := range codes {
		if c == want {
			n++
		}
	}
	return n
}

// An auth file whose routing fields core cannot route with is refused and adds nothing, so later
// accounts still route; a file's type is never sent back, as a misplaced token may be in it; an
// ID a sign-in still running will take, whatever its case, is refused while it runs.
func TestImportAuthFileRefusals(t *testing.T) {
	kimi := kimitest.New(t)
	access, refresh := kimi.Grant()
	base := runCore(t, map[string]any{
		"listen": "127.0.0.1:0", "client_keys": []string{clientKey}, "management_key": managementKey,
		"credentials_dir": filepath.Join(t.TempDir(), "credentials"),
		"extension":       map[string]any{"manifest": filepath.Join(binDir, "kimi", "pluxy-extension.json"), "accounts": []any{}},
	})
	kimiFile := func(extra map[string]any) string {
		m := map[string]any{"type": "kimi", "access_token": access, "refresh_token": refresh,
			"device_id": "device-refusals-e2e", "base_url": kimi.URL + "/coding"}
		maps.Copy(m, extra)
		b, _ := json.Marshal(m)
		return string(b)
	}

	// A prefix with spaces around it: core's routing refuses it.
	code, raw, reply := importAuthFile(t, base, managementKey, "kimi-bad-prefix.json", kimiFile(map[string]any{"prefix": "/ bad /"}))
	if code != http.StatusBadRequest || reply.Error == nil || reply.Error.Code != "invalid_request" || !strings.Contains(reply.Error.Message, "prefix") {
		t.Errorf("bad prefix: %d %s, want 400 invalid_request about the prefix", code, raw)
	}
	// A valid file imported next routes.
	if code, raw, _ := importAuthFile(t, base, managementKey, "kimi-after.json", kimiFile(nil)); code != http.StatusOK {
		t.Fatalf("import after the refused one: %d %s, want 200", code, raw)
	}
	req, _ := http.NewRequest(http.MethodPost, base+"/v1/chat/completions",
		strings.NewReader(`{"model":"kimi-k2.8","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+clientKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Pluxy-Account", "kimi-after")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "Hello") {
		t.Errorf("chat through the account imported after the refused one: %d %s", resp.StatusCode, body)
	}

	// A token where the type should be.
	const typ = "sk-fake-type-0123456789abcdef"
	code, raw, reply = importAuthFile(t, base, managementKey, "kimi-type.json", `{"type": "`+typ+`", "access_token": "x"}`)
	if code != http.StatusBadRequest || reply.Error == nil || reply.Error.Code != "invalid_request" {
		t.Errorf("unknown type: %d %s, want 400 invalid_request", code, raw)
	}
	if strings.Contains(strings.ToLower(raw), typ) {
		t.Errorf("the reply for an unknown type holds the type: %s", raw)
	}

	// A sign-in running for kimi-pending reserves its ID.
	var l loginReply
	if code := manage(t, base, "POST", "/accounts", map[string]any{"id": "kimi-pending", "method": "device", "settings": kimi.Settings()}, &l); code != http.StatusAccepted || l.Login.State != "pending" {
		t.Fatalf("starting the sign-in: %d %+v", code, l.Login)
	}
	code, raw, reply = importAuthFile(t, base, managementKey, "KIMI-Pending.json", kimiFile(nil))
	if code != http.StatusConflict || reply.Error == nil || reply.Error.Code != "conflict" {
		t.Errorf("import of an ID a sign-in will take: %d %s, want 409 conflict", code, raw)
	}
	if code := manage(t, base, "DELETE", "/logins/"+l.Login.ID, nil, nil); code != http.StatusNoContent {
		t.Errorf("cancelling the sign-in: %d", code)
	}

	if ids := listedIDs(t, base); !slices.Equal(ids, []string{"kimi-after"}) {
		t.Errorf("accounts %v, want only kimi-after", ids)
	}
}

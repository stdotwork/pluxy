package core

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/stdotwork/pluxy/next/management"
)

func TestChainsPutRefusesYAMLConfig(t *testing.T) {
	setTestHome(t)
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	writeTestFile(t, cfgPath, "server:\n  host: 127.0.0.1\n  port: 8317\naccess:\n  api-keys: [fake-client-key-1]\nmanagement:\n  secret-key: mgmt-key\n")
	c, err := Start(Options{ConfigPath: cfgPath, SafeMode: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Stop()

	rec := manageCall(t, c.Management(), http.MethodPut, "/chains/my-chain", `{"steps":[{"model":"m"}]}`)
	var reply management.ErrorBody
	const want = "a config.yaml cannot hold fallback chains, since it uses CLIProxyAPI's key names; they are kept in pluxy's JSON config"
	if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil || rec.Code != http.StatusConflict ||
		reply.Error.Code != "conflict" || reply.Error.Message != want {
		t.Errorf("PUT /chains/my-chain on yaml config: %d %s, want 409 conflict %q", rec.Code, rec.Body, want)
	}
}

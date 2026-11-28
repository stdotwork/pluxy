package core

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/internal/logfile"
	"github.com/stdotwork/pluxy/next/management"
)

func TestDeleteLogsReportsPartialFailure(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.json")
	cfg := `{"listen":"127.0.0.1:0","client_keys":["fake-client-key-1"],"management_key":"mgmt-key","logs":{"to_file":true}}`
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Start(Options{ConfigPath: cfgPath, SafeMode: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Stop)

	_, _ = c.logs.Write([]byte("seed\n"))

	logsDir := c.logs.Dir()
	realDir := logsDir + ".real"
	if err := os.Rename(logsDir, realDir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logsDir, []byte("block"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _ = c.logs.Write([]byte(strings.Repeat("x", logfile.MaxFileSize) + "\n"))

	if err := os.Remove(logsDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(realDir, logsDir); err != nil {
		t.Fatal(err)
	}

	rot := filepath.Join(logsDir, "main-2026-11-09T12-00-00.000.log")
	if err := os.WriteFile(rot, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mainPath := filepath.Join(logsDir, logfile.Main)
	if err := os.Remove(mainPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(mainPath, 0o700); err != nil {
		t.Fatal(err)
	}

	rec := manageCall(t, c.Management(), http.MethodDelete, "/logs", "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("DELETE /logs = %d (%s), want 500", rec.Code, rec.Body)
	}
	var body management.ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q: %v", rec.Body, err)
	}
	if body.Error.Code != "internal" {
		t.Errorf("error code = %q, want internal", body.Error.Code)
	}
	msg := body.Error.Message
	if !strings.Contains(msg, "1 rotated log file deleted") || !strings.Contains(msg, logfile.Main) {
		t.Errorf("message = %q, want count of deleted rotated files and %s", msg, logfile.Main)
	}
	if strings.Contains(msg, logsDir) {
		t.Errorf("message = %q leaks full path %s", msg, logsDir)
	}
	if _, err := os.Stat(rot); !os.IsNotExist(err) {
		t.Errorf("rotated file was not deleted: %v", err)
	}
}

func TestDeleteLogsOffStays400(t *testing.T) {
	h, _ := startManagementCore(t, managementTestConfig)
	rec := manageCall(t, h, http.MethodDelete, "/logs", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("DELETE /logs while off = %d (%s), want 400", rec.Code, rec.Body)
	}
	if code := errorCodeOf(t, rec); code != "invalid_request" {
		t.Errorf("code = %q, want invalid_request", code)
	}
}

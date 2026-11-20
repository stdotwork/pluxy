package server

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestManagementREADMEHasEveryErrorCode checks the error table in management/README.md against
// the status and code pairs the management handlers can send. A new pair needs this table and
// the README's error table updated together.
func TestManagementREADMEHasEveryErrorCode(t *testing.T) {
	// Codes an extension can return. writeCredError uses the code as sent with status 502;
	// writeAccountError does the same except for extension_unavailable (503) and unsupported
	// (422).
	extensionCodes := []string{
		"invalid_request", "auth", "permission", "account_closed", "not_found", "rate_limit",
		"quota", "unsupported", "upstream", "unavailable", "cancelled", "internal",
		"extension_crashed", "extension_protocol", "extension_unavailable",
	}
	want := map[int][]string{
		400: {"invalid_request", "file_required", "invalid_json", "invalid_service_account", "project_id_missing"},
		401: {"unauthorized"},
		403: {"remote_disabled", "blocked"},
		404: {"not_found"},
		409: {"conflict"},
		413: {"invalid_request", "log_too_large"},
		422: {"invalid_config", "invalid_request", "refused", "unsupported"},
		500: {"internal"},
		502: append([]string{"fetch_failed"}, extensionCodes...),
		503: {"extension_unavailable"},
		504: {"unavailable"},
	}
	got := readManagementErrorTable(t)

	wantPairs := errorPairs(want)
	gotPairs := errorPairs(got)
	for pair := range wantPairs {
		if !gotPairs[pair] {
			t.Errorf("management/README.md error table is missing %s", pair)
		}
	}
	for pair := range gotPairs {
		if !wantPairs[pair] {
			t.Errorf("management/README.md error table has %s, which no management handler sends", pair)
		}
	}
}

var backticked = regexp.MustCompile("`([^`]+)`")

// readManagementErrorTable returns the status and codes in management/README.md's error table.
func readManagementErrorTable(t *testing.T) map[int][]string {
	t.Helper()
	b, err := os.ReadFile("../../management/README.md")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(b), "\n")
	header := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == "| Status | Codes | Meaning |" {
			header = i
			break
		}
	}
	if header < 0 {
		t.Fatal("management/README.md has no | Status | Codes | Meaning | table")
	}
	out := map[int][]string{}
	for _, line := range lines[header+1:] {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "|") {
			break
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		if len(cells) != 3 {
			t.Fatalf("management/README.md error table row %q does not have three cells", line)
		}
		if strings.TrimSpace(cells[0]) == "---" {
			continue
		}
		status, err := strconv.Atoi(strings.TrimSpace(cells[0]))
		if err != nil {
			t.Fatalf("management/README.md error table status %q is not a number", cells[0])
		}
		codes := backticked.FindAllStringSubmatch(cells[1], -1)
		if len(codes) == 0 {
			t.Fatalf("management/README.md error table row %q names no code", line)
		}
		if strings.TrimSpace(cells[2]) == "" {
			t.Fatalf("management/README.md error table row %q has no meaning", line)
		}
		for _, code := range codes {
			out[status] = append(out[status], code[1])
		}
	}
	if len(out) == 0 {
		t.Fatal("management/README.md error table has no rows")
	}
	return out
}

func errorPairs(table map[int][]string) map[string]bool {
	out := map[string]bool{}
	for status, codes := range table {
		for _, code := range codes {
			out[fmt.Sprintf("%d %s", status, code)] = true
		}
	}
	return out
}

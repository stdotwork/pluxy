package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/internal/configdoc"
	"github.com/stdotwork/pluxy/next/internal/cred"
	"github.com/stdotwork/pluxy/next/internal/route"
)

type copyRefusingRedactor struct{ *configdoc.Redactor }

func (copyRefusingRedactor) RestoreSettings(_, _ []byte) ([]byte, error) {
	return nil, fmt.Errorf("%w: line 3: a redacted value is a copy of a secret", ErrSecretCopy)
}

// A PATCH whose settings hold a refused copy of a secret's placeholder is answered with the
// copy's line, not only that a placeholder is where no secret goes.
func TestPatchSettingsCopyRefusalNamesItsLine(t *testing.T) {
	m := &Management{Redactor: copyRefusingRedactor{configdoc.NewRedactor()}}
	a := route.Account{ID: "a", Settings: json.RawMessage(`{"api_key": "fake-settings-key-1"}`)}
	err := m.restoreSettings(&a, json.RawMessage(`{"note": "x"}`))
	if !errors.Is(err, cred.ErrInvalid) || !strings.Contains(err.Error(), "settings: line 3: a redacted value is a copy of a secret") {
		t.Errorf("restoreSettings = %v, want cred.ErrInvalid naming line 3", err)
	}
}

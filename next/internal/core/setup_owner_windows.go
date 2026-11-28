//go:build windows

package core

import (
	"os"

	"github.com/stdotwork/pluxy/next/internal/private"
)

// readNoWait is no flag on Windows, where opening a file does not wait for a writer.
const readNoWait = 0

// ownedByUser reports whether f belongs to the user pluxy runs as (or SYSTEM, or the token's
// default owner, as private.Owner accepts).
func ownedByUser(f *os.File) (bool, error) {
	ok, _, err := private.Owner(f)
	return ok, err
}

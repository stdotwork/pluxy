//go:build windows

package core

import (
	"os"

	"github.com/stdotwork/pluxy/next/internal/private"
)

// On Windows, opening a file does not wait for a writer.
const readNoWait = 0

func ownedByUser(f *os.File) (bool, error) {
	ok, _, err := private.Owner(f)
	return ok, err
}

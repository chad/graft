//go:build !unix

package evidence

import (
	"errors"
	"os"
)

func lockFile(*os.File) error {
	return errors.New("evidence: file locking is only implemented on unix")
}
func unlockFile(*os.File) error { return nil }

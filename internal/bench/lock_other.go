//go:build !unix

package bench

import (
	"errors"
	"os"
)

func lockFile(*os.File) error   { return errors.New("bench: requires a unix platform (flock)") }
func unlockFile(*os.File) error { return nil }

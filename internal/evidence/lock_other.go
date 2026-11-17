//go:build !unix

package evidence

import (
	"errors"
	"os"
)

type lockKind int

const (
	lockShared lockKind = iota
	lockExclusive
)

// graft's evidence log needs flock; non-unix platforms are unsupported (SPEC §7).
func lockFile(*os.File, lockKind) error {
	return errors.New("evidence: the evidence log requires a unix platform (flock)")
}
func unlockFile(*os.File) error { return nil }

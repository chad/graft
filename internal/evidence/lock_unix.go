//go:build unix

package evidence

import (
	"os"
	"syscall"
)

type lockKind int

const (
	lockShared lockKind = iota
	lockExclusive
)

func lockFile(f *os.File, how lockKind) error {
	op := syscall.LOCK_SH
	if how == lockExclusive {
		op = syscall.LOCK_EX
	}
	return syscall.Flock(int(f.Fd()), op)
}

func unlockFile(f *os.File) error { return syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }

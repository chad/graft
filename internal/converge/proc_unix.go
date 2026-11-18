//go:build unix

package converge

import (
	"os"
	"os/exec"
	"syscall"
)

// killGroupOnCancel runs the command in its own process group and kills the
// whole group when the context is cancelled, so children cannot outlive it.
func killGroupOnCancel(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

func lockFile(f *os.File) error   { return syscall.Flock(int(f.Fd()), syscall.LOCK_EX) }
func unlockFile(f *os.File) error { return syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }

// lockOpenFlags: the lock lives in a shared temp dir, so never follow a
// symlink planted at its name.
const lockOpenFlags = os.O_CREATE | os.O_RDWR | syscall.O_NOFOLLOW

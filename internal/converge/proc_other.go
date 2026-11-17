//go:build !unix

package converge

import (
	"errors"
	"os"
	"os/exec"
)

func killGroupOnCancel(*exec.Cmd) {}

func lockFile(*os.File) error   { return errors.New("converge: requires a unix platform (flock)") }
func unlockFile(*os.File) error { return nil }

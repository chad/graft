// Package sedumtest provisions the pinned sedum for tests that drive it. It is
// for tests only; the logic is the same as the TestMain of internal/recording
// and internal/pipeline.
package sedumtest

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// Version pins the sedum the tests run against: the release that added
// `sedum convergence`. Tests build exactly this tag, never a sedum already on
// PATH.
const Version = "v0.13.0"

// ProvisionInCI installs the pinned sedum and its generators when CI is set
// (see provisionSedum); locally it does nothing and tests that need sedum skip.
func ProvisionInCI() error {
	if os.Getenv("CI") == "" {
		return nil
	}
	return provisionSedum()
}

func provisionSedum() error {
	tmp, err := os.MkdirTemp("", "graft-sedum-*")
	if err != nil {
		return err
	}
	// Always build exactly the pinned commit in CI: a sedum already on PATH may
	// predate `sedum convergence`.
	bin := filepath.Join(tmp, "bin")
	cmd := exec.Command("go", "install", "github.com/livecodelife/sedum/cmd/sedum@"+Version)
	cmd.Env = append(os.Environ(), "GOBIN="+bin, "GOPROXY=direct")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("go install sedum@%s: %v\n%s", Version, err, out)
	}
	os.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if out, err := exec.Command(filepath.Join(bin, "sedum"), "convergence", "--help").CombinedOutput(); err != nil {
		return fmt.Errorf("the sedum built from %s has no `convergence` command: %v\n%s", Version, err, out)
	}
	if os.Getenv("SEDUM_GENERATORS") == "" {
		src := filepath.Join(tmp, "sedum")
		for _, args := range [][]string{
			{"init", "--quiet", src},
			{"-C", src, "fetch", "--quiet", "--depth", "1", "https://github.com/livecodelife/sedum", Version},
			{"-C", src, "checkout", "--quiet", "FETCH_HEAD"},
		} {
			if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
				return fmt.Errorf("fetch sedum %s for its generators: git %v: %v\n%s", Version, args, err, out)
			}
		}
		os.Setenv("SEDUM_GENERATORS", filepath.Join(src, "generators"))
	}
	return nil
}

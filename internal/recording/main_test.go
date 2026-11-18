package recording

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// sedumVersion pins the sedum the recording tests run against, so the
// recording format cannot drift underneath them.
const sedumVersion = "v0.12.2"

// TestMain provisions sedum in CI without a workflow change: when CI is set
// and sedum is not on PATH, it installs the pinned release with `go install`
// and fetches the matching generators (which the module zip does not carry)
// with a shallow git clone. Locally, tests that need sedum skip as before.
func TestMain(m *testing.M) {
	if os.Getenv("CI") != "" {
		if err := provisionSedum(); err != nil {
			fmt.Fprintln(os.Stderr, "recording tests: provisioning sedum:", err)
			os.Exit(1)
		}
	}
	os.Exit(m.Run())
}

func provisionSedum() error {
	tmp, err := os.MkdirTemp("", "graft-sedum-*")
	if err != nil {
		return err
	}
	if _, err := exec.LookPath("sedum"); err != nil {
		bin := filepath.Join(tmp, "bin")
		cmd := exec.Command("go", "install", "github.com/livecodelife/sedum/cmd/sedum@"+sedumVersion)
		cmd.Env = append(os.Environ(), "GOBIN="+bin)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("go install sedum@%s: %v\n%s", sedumVersion, err, out)
		}
		os.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	if os.Getenv("SEDUM_GENERATORS") == "" {
		src := filepath.Join(tmp, "sedum")
		cmd := exec.Command("git", "clone", "--quiet", "--depth", "1", "--branch", sedumVersion,
			"https://github.com/livecodelife/sedum", src)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("clone sedum %s for its generators: %v\n%s", sedumVersion, err, out)
		}
		os.Setenv("SEDUM_GENERATORS", filepath.Join(src, "generators"))
	}
	return nil
}

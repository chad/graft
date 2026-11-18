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
//
// This is a commit of the sedum PR that adds `sedum convergence`
// (livecodelife/sedum#68), pinned by hash because a Go proxy can serve a stale
// resolution of a branch name. Replace it with the release tag that carries it
// once there is one.
const sedumVersion = "efe5d4b726fd9f78fa54aa1c68395aa9e1b83442"

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
	// Always build exactly the pinned commit in CI: a sedum already on PATH may
	// predate `sedum convergence`.
	bin := filepath.Join(tmp, "bin")
	cmd := exec.Command("go", "install", "github.com/livecodelife/sedum/cmd/sedum@"+sedumVersion)
	cmd.Env = append(os.Environ(), "GOBIN="+bin, "GOPROXY=direct")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("go install sedum@%s: %v\n%s", sedumVersion, err, out)
	}
	os.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if out, err := exec.Command(filepath.Join(bin, "sedum"), "convergence", "--help").CombinedOutput(); err != nil {
		return fmt.Errorf("the sedum built from %s has no `convergence` command: %v\n%s", sedumVersion, err, out)
	}
	if os.Getenv("SEDUM_GENERATORS") == "" {
		src := filepath.Join(tmp, "sedum")
		for _, args := range [][]string{
			{"init", "--quiet", src},
			{"-C", src, "fetch", "--quiet", "--depth", "1", "https://github.com/livecodelife/sedum", sedumVersion},
			{"-C", src, "checkout", "--quiet", "FETCH_HEAD"},
		} {
			if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
				return fmt.Errorf("fetch sedum %s for its generators: git %v: %v\n%s", sedumVersion, args, err, out)
			}
		}
		os.Setenv("SEDUM_GENERATORS", filepath.Join(src, "generators"))
	}
	return nil
}

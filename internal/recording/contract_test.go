package recording

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/chad/graft/internal/contract"
)

// generatorsDir is sedum's generators directory: SEDUM_GENERATORS, or a sedum
// checkout at ~/sedum.
func generatorsDir() string {
	if g := os.Getenv("SEDUM_GENERATORS"); g != "" {
		return g
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "sedum", "generators")
}

// sedumContract is the real convergence contract of a generator package, from
// the sedum binary on PATH. It skips locally when sedum or its generators are
// missing or too old to have `sedum convergence`; CI must have them.
func sedumContract(t *testing.T, pkg string) contract.Contract {
	t.Helper()
	skip := func(format string, a ...any) {
		if os.Getenv("CI") != "" {
			t.Fatalf(format, a...)
		}
		t.Skipf(format, a...)
	}
	bin, err := exec.LookPath("sedum")
	if err != nil {
		skip("sedum not on PATH")
	}
	c := contract.Exec{Bin: bin, Generators: generatorsDir(), Package: pkg}
	if _, err := c.Info(); err != nil {
		skip("sedum convergence contract unavailable for %s: %v", pkg, err)
	}
	return c
}

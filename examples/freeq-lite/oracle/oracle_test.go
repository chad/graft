package main

import (
	"os/exec"
	"path/filepath"
	"testing"
)

// The oracle must score the hand-written reference 100%: if it does not,
// the oracle (not the reference) is what is wrong.
func TestOracleScoresReferenceFully(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	bin := filepath.Join(t.TempDir(), "reference")
	if out, err := exec.Command("go", "build", "-o", bin, "../reference").CombinedOutput(); err != nil {
		t.Fatalf("build reference: %v\n%s", err, out)
	}
	for _, c := range checks {
		t.Run(c.name, func(t *testing.T) {
			addr, stop, err := start(bin)
			if err != nil {
				t.Fatal(err)
			}
			defer stop()
			if err := c.run(addr); err != nil {
				t.Fatal(err)
			}
		})
	}
}

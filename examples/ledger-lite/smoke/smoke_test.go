package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The smoke suite must pass on the reference, or it is wrong.
func TestSmokePassesOnReference(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	bin := filepath.Join(t.TempDir(), "reference")
	if out, err := exec.Command("go", "build", "-o", bin, "../reference").CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	self, _ := filepath.Abs("smoke.go")
	out, err := exec.Command("go", "run", self, "-all", "-bin", bin).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "9/9 checks passed") {
		t.Fatalf("%v\n%s", err, out)
	}
}

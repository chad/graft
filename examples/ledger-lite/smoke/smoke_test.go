package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The smoke suite must pass on the reference, or it is wrong.
func TestSmokePassesOnReference(t *testing.T) {
	for _, ref := range []string{"reference", "reference-v2"} {
		t.Run(ref, func(t *testing.T) { smokePasses(t, ref) })
	}
}

// Both steps of the evolve case run the same smoke gate, so it must pass on
// the v2 reference too: a gate that fails the evolved behaviour would fail
// every correct change.
func smokePasses(t *testing.T, ref string) {
	if testing.Short() {
		t.Skip("short")
	}
	bin := filepath.Join(t.TempDir(), ref)
	if out, err := exec.Command("go", "build", "-o", bin, "../"+ref).CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	self, _ := filepath.Abs("smoke.go")
	out, err := exec.Command("go", "run", self, "-all", "-bin", bin).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "9/9 checks passed") {
		t.Fatalf("%v\n%s", err, out)
	}
}
